// MIT License
//
// Copyright (c) 2024 chaunsin
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in all
// copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
// SOFTWARE.
//

package ncmctl

import (
	"context"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/chaunsin/netease-cloud-music/api"
	"github.com/chaunsin/netease-cloud-music/api/types"
	"github.com/chaunsin/netease-cloud-music/api/weapi"
	cookiejar "github.com/chaunsin/netease-cloud-music/pkg/cookie"
	"github.com/chaunsin/netease-cloud-music/pkg/database"
	"github.com/chaunsin/netease-cloud-music/pkg/log"
	"github.com/chaunsin/netease-cloud-music/pkg/utils"

	"github.com/cheggaaa/pb/v3"
	"github.com/spf13/cobra"
)

const (
	playIDsDefaultNum    int64 = 300
	playIDsDefaultGapMin int64 = 5
	playIDsDefaultGapMax int64 = 20
)

type PlayIDsOpts struct {
	IDs        string
	IDsFile    string
	Num        int64
	GapMin     int64
	GapMax     int64
	CookieFile string // 额外 cookie 文件路径（原始格式），加载后注入到 client
}

type PlayIDs struct {
	root *Root
	cmd  *cobra.Command
	opts PlayIDsOpts
	l    *log.Logger
	rng  *rand.Rand
}

type playSongMetadata struct {
	ID       int64
	Name     string
	AlbumID  int64
	Duration int64
}

type playSongQueue struct {
	ids      []int64
	round    []int64
	index    int
	roundNum int
	rng      *rand.Rand
}

func NewPlayIDs(root *Root, l *log.Logger) *PlayIDs {
	c := &PlayIDs{
		root: root,
		l:    l,
		rng:  rand.New(rand.NewSource(time.Now().UnixNano())),
		cmd: &cobra.Command{
			Use:     "playids",
			Short:   "[need login] Fully play specified songs by song IDs",
			Example: "  ncmctl playids --ids 2600804126,1984580503\n  ncmctl playids --ids-file ./song_ids.txt --num 50",
		},
	}
	c.addFlags()
	c.cmd.RunE = func(cmd *cobra.Command, args []string) error {
		return c.execute(cmd.Context())
	}
	return c
}

func (c *PlayIDs) addFlags() {
	c.cmd.PersistentFlags().StringVar(&c.opts.IDs, "ids", "", "comma-separated song ids")
	c.cmd.PersistentFlags().StringVar(&c.opts.IDsFile, "ids-file", "", "path to a file containing song ids")
	c.cmd.PersistentFlags().Int64VarP(&c.opts.Num, "num", "n", playIDsDefaultNum, "number of songs to fully play")
	c.cmd.PersistentFlags().Int64Var(&c.opts.GapMin, "gap-min", playIDsDefaultGapMin, "minimum random gap between songs in seconds")
	c.cmd.PersistentFlags().Int64Var(&c.opts.GapMax, "gap-max", playIDsDefaultGapMax, "maximum random gap between songs in seconds")
}

func (c *PlayIDs) validate() error {
	if c.opts.IDs == "" && c.opts.IDsFile == "" {
		return fmt.Errorf("ids or ids-file is required")
	}
	if c.opts.Num <= 0 || c.opts.Num > 300 {
		return fmt.Errorf("num <= 0 or > 300")
	}
	if c.opts.GapMin < 0 || c.opts.GapMax < 0 {
		return fmt.Errorf("gap-min and gap-max must be >= 0")
	}
	if c.opts.GapMin > c.opts.GapMax {
		return fmt.Errorf("gap-min > gap-max")
	}
	if _, err := parsePlaySongIDs(c.opts.IDs, c.opts.IDsFile); err != nil {
		return err
	}
	return nil
}

func (c *PlayIDs) Add(command ...*cobra.Command) {
	c.cmd.AddCommand(command...)
}

func (c *PlayIDs) Command() *cobra.Command {
	return c.cmd
}

func (c *PlayIDs) execute(ctx context.Context) error {
	if err := c.validate(); err != nil {
		return fmt.Errorf("validate: %w", err)
	}

	songIDs, err := parsePlaySongIDs(c.opts.IDs, c.opts.IDsFile)
	if err != nil {
		return fmt.Errorf("parsePlaySongIDs: %w", err)
	}

	// 创建 client（如果指定了 CookieFile，使用独立的 cookie 文件路径避免覆盖主 cookie）
	networkCfg := c.root.Cfg.Network
	if c.opts.CookieFile != "" {
		absPath, err := filepath.Abs(c.opts.CookieFile)
		if err != nil {
			return fmt.Errorf("解析cookie文件路径失败: %w", err)
		}
		// 复制配置，修改 cookie filepath 指向指定文件
		networkCfgCopy := *networkCfg
		networkCfgCopy.Cookie = cookiejar.Config{
			Filepath: absPath,
			Interval: networkCfg.Cookie.Interval,
		}
		networkCfg = &networkCfgCopy
		log.Info("[playids] 使用独立cookie文件: %s", absPath)
		c.cmd.Printf("[musician-vip] 使用指定cookie文件: %s\\n", absPath)
	}
	cli, err := api.NewClient(networkCfg, c.l)
	if err != nil {
		return fmt.Errorf("NewClient: %w", err)
	}
	defer cli.Close(ctx)

	request := weapi.New(cli)

	if request.NeedLogin(ctx) {
		return fmt.Errorf("need login")
	}

	user, err := request.GetUserInfo(ctx, &weapi.GetUserInfoReq{})
	if err != nil {
		return fmt.Errorf("GetUserInfo: %w", err)
	}
	if user.Code != 200 || user.Account == nil {
		return fmt.Errorf("GetUserInfo: %+v", user)
	}
	uid := fmt.Sprintf("%v", user.Account.Id)

	defer func() {
		refresh, err := request.TokenRefresh(ctx, &weapi.TokenRefreshReq{})
		if err != nil || refresh.Code != 200 {
			log.Warn("TokenRefresh resp:%+v err: %s", refresh, err)
		}
	}()

	db, err := database.New(c.root.Cfg.Database)
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	defer db.Close(ctx)

	record, err := db.Get(ctx, scrobbleTodayNumKey(uid))
	if err != nil {
		if strings.Contains(err.Error(), "Key not found") {
			record = "0"
		} else {
			return fmt.Errorf("get scrobble today num: %w", err)
		}
	}
	finish, err := strconv.ParseInt(record, 10, 64)
	if err != nil {
		return fmt.Errorf("ParseInt(%v): %w", record, err)
	}
	if finish >= playIDsDefaultNum {
		c.cmd.Println("today play limit 300 completed")
		return nil
	}

	left := playIDsDefaultNum - finish
	target := utils.Ternary(left > c.opts.Num, c.opts.Num, left)
	metadata, err := c.fetchSongMetadata(ctx, request, songIDs)
	if err != nil {
		return fmt.Errorf("fetchSongMetadata: %w", err)
	}
	if len(metadata) == 0 {
		return fmt.Errorf("input resource is empty or unavailable")
	}

	var validSongIDs = make([]int64, 0, len(songIDs))
	for _, id := range songIDs {
		if _, ok := metadata[id]; ok {
			validSongIDs = append(validSongIDs, id)
		}
	}
	if len(validSongIDs) == 0 {
		return fmt.Errorf("input resource is empty or unavailable")
	}

	nickname := ""
	if user.Profile.Nickname != "" {
		nickname = user.Profile.Nickname
	}
	c.cmd.Printf("[playids] 当前账号: uid=%s 昵称=%q\n", uid, nickname)
	c.cmd.Printf("[playids] 任务开始: 歌曲池=%d首, 目标播放=%d首, 今日已完成=%d首, 今日剩余=%d首, 间隔=%ds-%ds\n",
		len(validSongIDs), target, finish, left, c.opts.GapMin, c.opts.GapMax)
	printPlaySongList(c.cmd, metadata, validSongIDs)

	bar := pb.Full.Start64(target)
	defer bar.Finish()

	expire, err := utils.TimeUntilMidnight("Local")
	if err != nil {
		return fmt.Errorf("TimeUntilMidnight: %w", err)
	}

	queue := newPlaySongQueue(validSongIDs, c.rng)
	var success int64
	var failed int64
	for success < target {
		var roundSuccess int64
		for i := 0; i < len(validSongIDs) && success < target; i++ {
			songID, round, index, newRound := queue.Next()
			info := metadata[songID]
			if newRound && round > 1 {
				c.cmd.Printf("[playids] 开始第%d轮随机打乱\n", round)
				printPlaySongList(c.cmd, metadata, queue.CurrentRound())
			}
			attempt := success + failed + 1
			c.cmd.Printf("[playids] 正在播放: 第%d/%d首, 第%d轮第%d首, songId=%d, 歌名=%q, 时长=%s\n",
				attempt, target, round, index, songID, info.Name, formatPlayDuration(time.Duration(info.Duration)*time.Millisecond))
			if err := c.playSong(ctx, cli, request, info); err != nil {
				failed++
				c.cmd.Printf("[playids] 本首结果: 第%d/%d首, 失败, songId=%d, 歌名=%q, 原因=%q\n",
					attempt, target, songID, info.Name, err.Error())
				log.Warn("[playids] play %d (%s) err: %v", songID, info.Name, err)
				continue
			}

			if _, err := db.Increment(ctx, scrobbleTodayNumKey(uid), 1, expire); err != nil {
				log.Warn("[playids] set %v record err: %v", songID, err)
			}

			success++
			roundSuccess++
			c.cmd.Printf("[playids] 本首结果: 第%d/%d首, 成功, songId=%d, 歌名=%q\n",
				attempt, target, songID, info.Name)
			bar.Increment()

			if success >= target {
				break
			}
			if gap := randomGap(c.rng, c.opts.GapMin, c.opts.GapMax); gap > 0 {
				c.cmd.Printf("[playids] 播放间隔: 下一首等待 %s\n", formatPlayDuration(gap))
				if err := sleepWithContext(ctx, gap); err != nil {
					return err
				}
			}
		}
		if roundSuccess == 0 {
			if success == 0 {
				return fmt.Errorf("all songs failed to play")
			}
			log.Warn("[playids] all songs in current round failed, stop early")
			break
		}
	}

	c.cmd.Printf("[playids] 执行完成: 目标=%d首, 成功=%d首, 失败=%d首\n", target, success, failed)
	c.cmd.Printf("[playids] 今日统计: 执行前=%d首, 执行后=%d首\n", finish, finish+success)
	c.cmd.Printf("report total: %d success: %d failed: %d\n", target, success, failed)
	return nil
}

func (c *PlayIDs) fetchSongMetadata(ctx context.Context, request *weapi.Api, ids []int64) (map[int64]playSongMetadata, error) {
	pages, err := utils.SplitSlice(ids, 500)
	if err != nil {
		return nil, fmt.Errorf("SplitSlice: %w", err)
	}

	metadata := make(map[int64]playSongMetadata, len(ids))
	for _, page := range pages {
		req := make([]weapi.SongDetailReqList, 0, len(page))
		for _, id := range page {
			req = append(req, weapi.SongDetailReqList{Id: fmt.Sprintf("%d", id), V: 0})
		}

		resp, err := request.SongDetail(ctx, &weapi.SongDetailReq{C: req})
		if err != nil {
			return nil, fmt.Errorf("SongDetail: %w", err)
		}
		if resp.Code != 200 {
			return nil, fmt.Errorf("SongDetail err: %+v", resp)
		}
		for _, song := range resp.Songs {
			metadata[song.Id] = playSongMetadata{
				ID:       song.Id,
				Name:     song.Name,
				AlbumID:  song.Al.Id,
				Duration: song.Dt,
			}
		}
	}
	return metadata, nil
}

func (c *PlayIDs) playSong(ctx context.Context, cli *api.Client, request *weapi.Api, song playSongMetadata) error {
	playResp, err := request.SongPlayerV1(ctx, &weapi.SongPlayerV1Req{
		Ids:   types.IntsString{song.ID},
		Level: types.LevelStandard,
	})
	if err != nil {
		return fmt.Errorf("SongPlayerV1(%d): %w", song.ID, err)
	}
	if playResp.Code != 200 {
		return fmt.Errorf("SongPlayerV1(%d) err: %+v", song.ID, playResp)
	}
	if len(playResp.Data) <= 0 {
		return fmt.Errorf("SongPlayerV1(%d) data is empty", song.ID)
	}

	data := playResp.Data[0]
	if data.Code != 200 || data.Url == "" {
		return fmt.Errorf("SongPlayerV1(%d) unavailable: code=%d url=%q", song.ID, data.Code, data.Url)
	}

	duration := data.Time
	if duration <= 0 {
		duration = song.Duration
	}
	if duration <= 0 {
		return fmt.Errorf("song %d duration is invalid", song.ID)
	}

	expected := time.Duration(duration) * time.Millisecond

	elapsed, err := streamSongToDiscard(ctx, cli, data.Url)
	if err != nil {
		return fmt.Errorf("streamSongToDiscard(%d): %w", song.ID, err)
	}
	wait := expected - elapsed
	if wait < 0 {
		wait = 0
	}
	c.cmd.Printf("[playids] 拉流完成: songId=%d, 已耗时=%s, 补等待=%s\n",
		song.ID, formatPlayDuration(elapsed), formatPlayDuration(wait))
	if wait > 0 {
		if err := sleepWithContext(ctx, wait); err != nil {
			return err
		}
	}

	resp, err := request.WebLog(ctx, buildPlayWebLogRequest(song, millisecondsToSeconds(duration)))
	if err != nil {
		return fmt.Errorf("WebLog(%d): %w", song.ID, err)
	}
	if resp.Code != 200 {
		return fmt.Errorf("WebLog(%d) err: %+v", song.ID, resp)
	}
	c.cmd.Printf("[playids] 播放上报成功: songId=%d, 上报时长=%ds\n", song.ID, millisecondsToSeconds(duration))
	return nil
}

func parsePlaySongIDs(idsText, idsFile string) ([]int64, error) {
	var input []string
	if idsText != "" {
		input = append(input, idsText)
	}
	if idsFile != "" {
		data, err := os.ReadFile(idsFile)
		if err != nil {
			return nil, fmt.Errorf("ReadFile(%s): %w", idsFile, err)
		}
		input = append(input, string(data))
	}

	seen := make(map[int64]struct{})
	ids := make([]int64, 0)
	for _, item := range input {
		tokens := strings.FieldsFunc(item, func(r rune) bool {
			switch r {
			case ',', ';', '\n', '\r', '\t', ' ':
				return true
			default:
				return false
			}
		})
		for _, token := range tokens {
			id, err := strconv.ParseInt(strings.TrimSpace(token), 10, 64)
			if err != nil {
				return nil, fmt.Errorf("invalid song id %q: %w", token, err)
			}
			if id <= 0 {
				return nil, fmt.Errorf("invalid song id %q", token)
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("song ids is empty")
	}
	return ids, nil
}

func newPlaySongQueue(ids []int64, rng *rand.Rand) *playSongQueue {
	return &playSongQueue{
		ids: ids,
		rng: rng,
	}
}

func (q *playSongQueue) Next() (id int64, round int, index int, newRound bool) {
	if len(q.ids) == 0 {
		return 0, 0, 0, false
	}
	if q.index >= len(q.round) {
		q.round = shuffleSongIDs(q.ids, q.rng)
		q.index = 0
		q.roundNum++
		newRound = true
	}
	id = q.round[q.index]
	round = q.roundNum
	index = q.index + 1
	q.index++
	return id, round, index, newRound
}

func (q *playSongQueue) CurrentRound() []int64 {
	return append([]int64(nil), q.round...)
}

func shuffleSongIDs(ids []int64, rng *rand.Rand) []int64 {
	shuffled := append([]int64(nil), ids...)
	rng.Shuffle(len(shuffled), func(i, j int) {
		shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
	})
	return shuffled
}

func buildPlayWebLogRequest(song playSongMetadata, durationSeconds int64) *weapi.WebLogReq {
	payload := map[string]interface{}{
		"type":     "song",
		"wifi":     0,
		"download": 0,
		"id":       fmt.Sprintf("%d", song.ID),
		"time":     durationSeconds,
		"end":      "playend",
		"mainsite": "1",
	}
	if song.AlbumID > 0 {
		albumID := fmt.Sprintf("%d", song.AlbumID)
		payload["source"] = "album"
		payload["sourceId"] = albumID
		payload["content"] = fmt.Sprintf("id=%s", albumID)
	}

	return &weapi.WebLogReq{
		Logs: []map[string]interface{}{
			{
				"action": "play",
				"json":   payload,
			},
		},
	}
}

func streamSongToDiscard(ctx context.Context, cli *api.Client, targetURL string) (time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return 0, fmt.Errorf("NewRequestWithContext: %w", err)
	}
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Referer", "https://music.163.com")
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("Accept-Language", "zh-CN,zh-Hans;q=0.9")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) NeteaseMusicDesktop/2.3.17.1034")

	start := time.Now()
	resp, err := cli.GetClient().Do(req)
	if err != nil {
		return time.Since(start), err
	}
	defer resp.Body.Close()

	if resp.StatusCode/100 != 2 {
		return time.Since(start), fmt.Errorf("http status code: %d", resp.StatusCode)
	}
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		return time.Since(start), err
	}
	return time.Since(start), nil
}

func sleepWithContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func randomGap(rng *rand.Rand, minSeconds, maxSeconds int64) time.Duration {
	if maxSeconds <= 0 || maxSeconds < minSeconds {
		return 0
	}
	if minSeconds == maxSeconds {
		return time.Duration(minSeconds) * time.Second
	}
	value := minSeconds + rng.Int63n(maxSeconds-minSeconds+1)
	return time.Duration(value) * time.Second
}

func millisecondsToSeconds(ms int64) int64 {
	if ms <= 0 {
		return 0
	}
	return (ms + 999) / 1000
}

func formatPlayDuration(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	if d < time.Second {
		return d.Round(time.Millisecond).String()
	}
	return d.Round(time.Second).String()
}

func printPlaySongList(cmd *cobra.Command, metadata map[int64]playSongMetadata, ids []int64) {
	for i, id := range ids {
		info, ok := metadata[id]
		if !ok {
			continue
		}
		cmd.Printf("[playids] 歌曲池[%d]: songId=%d 歌名=%q 时长=%s\n",
			i+1, info.ID, info.Name, formatPlayDuration(time.Duration(info.Duration)*time.Millisecond))
	}
}
