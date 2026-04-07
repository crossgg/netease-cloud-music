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
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/chaunsin/netease-cloud-music/api"
	"github.com/chaunsin/netease-cloud-music/api/types"
	"github.com/chaunsin/netease-cloud-music/api/weapi"
	"github.com/chaunsin/netease-cloud-music/pkg/database"
	"github.com/chaunsin/netease-cloud-music/pkg/log"
	"github.com/chaunsin/netease-cloud-music/pkg/utils"

	"github.com/cheggaaa/pb/v3"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// ScrobbleConfig 从 YAML 配置文件读取的循环播放配置
type ScrobbleConfig struct {
	// SongIds 普通歌曲ID列表
	SongIds []string `yaml:"songIds"`
	// CloudSongIds 云盘歌曲ID列表 (通过 ncmctl cloud 上传到云盘的歌曲)
	CloudSongIds []string `yaml:"cloudSongIds"`
	// RemoteSongListUrl 远程歌曲列表URL (从GitHub等地址获取songId)
	// 文件格式: 一行#开头的备注 + 一行songId，交替排列
	RemoteSongListUrl string `yaml:"remoteSongListUrl"`
	// Duration 每首歌的播放时长(秒), 0=使用歌曲实际时长
	Duration int64 `yaml:"duration"`
	// Count 循环播放总次数
	Count int64 `yaml:"count"`
}

type ScrobbleOpts struct {
	Num          int64
	SongIds      []string // 指定歌曲ID列表，如果设置则只播放这些歌曲
	PlayDuration int64    // 自定义每首歌的播放等待时长(秒)，0=使用歌曲实际时长，-1=不等待(快速刷歌)
	Loop         bool     // 是否循环播放
	LoopMinutes  int64    // 循环播放总时长(分钟)，0=无限循环直到达到Num
	ConfigFile   string   // scrobble 配置文件路径
}

type Scrobble struct {
	root *Root
	cmd  *cobra.Command
	opts ScrobbleOpts
	l    *log.Logger
}

func NewScrobble(root *Root, l *log.Logger) *Scrobble {
	c := &Scrobble{
		root: root,
		l:    l,
		cmd: &cobra.Command{
			Use:   "scrobble",
			Short: "[need login] Scrobble execute refresh 300 songs",
			Example: `  ncmctl scrobble
  ncmctl scrobble --scrobble-config ~/.ncmctl/scrobble.yaml
  ncmctl scrobble --songs 3366663042 --duration 0 --loop -n 300`,
		},
	}
	c.addFlags()
	c.cmd.RunE = func(cmd *cobra.Command, args []string) error {
		return c.execute(cmd.Context())
	}
	return c
}

func (c *Scrobble) addFlags() {
	c.cmd.PersistentFlags().Int64VarP(&c.opts.Num, "num", "n", 300, "num of songs")
	c.cmd.PersistentFlags().StringSliceVar(&c.opts.SongIds, "songs", nil, "specify song IDs to play, e.g. --songs 3366663042,xxx,yyy")
	c.cmd.PersistentFlags().Int64Var(&c.opts.PlayDuration, "duration", -1, "play duration per song in seconds, 0=use song actual duration, -1=no wait (fast mode)")
	c.cmd.PersistentFlags().BoolVar(&c.opts.Loop, "loop", false, "enable loop playback for specified songs")
	c.cmd.PersistentFlags().Int64Var(&c.opts.LoopMinutes, "loop-time", 0, "total loop duration in minutes, 0=loop until num is reached")
	c.cmd.PersistentFlags().StringVar(&c.opts.ConfigFile, "scrobble-config", "", "scrobble config file path (YAML)")
}

// loadConfigFile 加载 YAML 配置文件并覆盖命令行参数
// 优先级: --scrobble-config 命令行参数 > config.yaml 中的 scrobble.configFile
func (c *Scrobble) loadConfigFile() (*ScrobbleConfig, error) {
	configFile := c.opts.ConfigFile
	// 如果命令行没有指定，尝试从主配置文件读取
	if configFile == "" && c.root.Cfg.Scrobble != nil && c.root.Cfg.Scrobble.ConfigFile != "" {
		configFile = c.root.Cfg.Scrobble.ConfigFile
	}
	if configFile == "" {
		return nil, nil
	}
	data, err := os.ReadFile(configFile)
	if err != nil {
		return nil, fmt.Errorf("read config file: %w", err)
	}
	var cfg ScrobbleConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config file: %w", err)
	}
	return &cfg, nil
}

func (c *Scrobble) validate() error {
	// 如果有配置文件，跳过命令行参数验证（配置文件优先）
	if c.opts.ConfigFile != "" {
		return nil
	}
	if len(c.opts.SongIds) > 0 && c.opts.Loop {
		if c.opts.Num <= 0 {
			c.opts.Num = 300
		}
	} else {
		if c.opts.Num <= 0 || c.opts.Num > 300 {
			return fmt.Errorf("num <= 0 or > 300")
		}
	}
	if c.opts.Loop && len(c.opts.SongIds) == 0 {
		return fmt.Errorf("loop mode requires --songs to be specified")
	}
	return nil
}

func (c *Scrobble) Add(command ...*cobra.Command) {
	c.cmd.AddCommand(command...)
}

func (c *Scrobble) Command() *cobra.Command {
	return c.cmd
}

func (c *Scrobble) execute(ctx context.Context) error {
	if err := c.validate(); err != nil {
		return fmt.Errorf("validate: %w", err)
	}

	cli, err := api.NewClient(c.root.Cfg.Network, c.l)
	if err != nil {
		return fmt.Errorf("NewClient: %w", err)
	}
	defer cli.Close(ctx)
	var request = weapi.New(cli)

	// 获取用户id
	user, err := request.GetUserInfo(ctx, &weapi.GetUserInfoReq{})
	if err != nil {
		return fmt.Errorf("UserInfo: %w", err)
	}
	if user.Code != 200 || user.Profile == nil || user.Account == nil {
		return fmt.Errorf("need login")
	}
	var uid = fmt.Sprintf("%v", user.Account.Id)

	// 判断是否满级，满级则不再执行。
	detail, err := request.GetUserInfoDetail(ctx, &weapi.GetUserInfoDetailReq{UserId: user.Account.Id})
	if err != nil {
		return fmt.Errorf("GetUserInfoDetail: %w", err)
	}
	if detail.Code != 200 {
		return fmt.Errorf("GetUserInfoDetail: %w", err)
	}
	if detail.Level >= 10 {
		c.cmd.Println("账号已满级")
		return nil
	}

	// 刷新token过期时间
	defer func() {
		refresh, err := request.TokenRefresh(ctx, &weapi.TokenRefreshReq{})
		if err != nil || refresh.Code != 200 {
			log.Warn("TokenRefresh resp:%+v err: %s", refresh, err)
		}
	}()

	// 初始化数据库如果文件不存在则直接创建
	db, err := database.New(c.root.Cfg.Database)
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	defer db.Close(ctx)

	// 判断今日刷歌数量
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
	if finish >= 300 {
		c.cmd.Println("today scrobble 300 completed")
		return nil
	}

	// 如果有配置文件，使用 HAR 精确模拟模式
	cfg, err := c.loadConfigFile()
	if err != nil {
		return fmt.Errorf("loadConfigFile: %w", err)
	}
	if cfg != nil {
		return c.executeHARSimulation(ctx, request, db, uid, finish, cfg)
	}

	// 如果指定了歌曲ID且开启循环模式，使用循环播放逻辑
	if len(c.opts.SongIds) > 0 && c.opts.Loop {
		return c.executeLoop(ctx, request, db, uid, finish)
	}

	var (
		left = 300 - finish
		num  = utils.Ternary(left > c.opts.Num, c.opts.Num, left)
		bar  = pb.Full.Start64(num)
	)

	// 获取歌曲列表
	var list []NeverHeardSongsList
	if len(c.opts.SongIds) > 0 {
		list, err = c.getSpecifiedSongs(ctx, request, c.opts.SongIds, "list")
		if err != nil {
			return fmt.Errorf("getSpecifiedSongs: %w", err)
		}
	} else {
		list, err = c.neverHeardSongs(ctx, request, db, uid, num)
		if err != nil {
			return fmt.Errorf("neverHeardSongs: %w", err)
		}
	}
	log.Debug("ready execute num(%d)", len(list))

	var total int
	defer func() {
		log.Debug("scrobble success: %d", total)
		bar.Finish()
	}()

	expire, err := utils.TimeUntilMidnight("Local")
	if err != nil {
		return fmt.Errorf("TimeUntilMidnight: %w", err)
	}

	// 执行刷歌（快速模式，原有逻辑）
	for _, v := range list {
		if int64(total) >= num {
			break
		}

		playTime := c.getPlayDuration(v.SongsTime)

		if err := c.sendPlayWebLog(ctx, request, v); err != nil {
			continue
		}

		if err := db.Set(ctx, scrobbleRecordKey(uid, v.SongsId), fmt.Sprintf("%v", time.Now().UnixMilli())); err != nil {
			log.Warn("[scrobble] set %v record err: %w", v.SongsId, err)
		}
		_, err := db.Increment(ctx, scrobbleTodayNumKey(uid), 1, expire)
		if err != nil {
			log.Warn("[scrobble] set %v record err: %w", v.SongsId, err)
		}
		total++
		bar.Increment()

		// 等待播放时长
		if playTime > 0 {
			c.cmd.Printf("正在播放歌曲 %s，等待 %d 秒...\n", v.SongsId, playTime)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(playTime) * time.Second):
			}
		} else {
			time.Sleep(time.Millisecond * 100)
		}
	}
	return nil
}

// executeHARSimulation 精确模拟 HAR 抓包的浏览器播放行为
// 每首歌的完整播放模拟流程:
//  1. POST /weapi/activity/p2p/flow/switch/get    → P2P心跳
//  2. POST /weapi/feedback/weblog                  → startplay 事件
//  3. POST /weapi/feedback/weblog                  → play 开始事件
//  4. POST /weapi/song/enhance/player/url/v1       → 获取播放URL
//  5. 每隔~60秒: POST /weapi/feedback/weblog + POST /weapi/pl/count
//  6. POST /weapi/feedback/weblog                  → play 完成事件(playend)
func (c *Scrobble) executeHARSimulation(ctx context.Context, request *weapi.Api, db database.Database, uid string, finish int64, cfg *ScrobbleConfig) error {
	// 合并普通歌曲和云盘歌曲
	var allSongIds []string
	var cloudSongIdSet = make(map[string]bool)

	for _, id := range cfg.SongIds {
		id = strings.TrimSpace(id)
		if id != "" {
			allSongIds = append(allSongIds, id)
		}
	}
	for _, id := range cfg.CloudSongIds {
		id = strings.TrimSpace(id)
		if id != "" {
			allSongIds = append(allSongIds, id)
			cloudSongIdSet[id] = true
		}
	}

	var remoteCount int
	// 从远程URL获取歌曲列表
	if cfg.RemoteSongListUrl != "" {
		c.cmd.Printf("📥 从远程地址获取歌曲列表: %s\n", cfg.RemoteSongListUrl)
		remoteIds, err := c.fetchRemoteSongList(cfg.RemoteSongListUrl)
		if err != nil {
			return fmt.Errorf("fetchRemoteSongList: %w", err)
		}
		c.cmd.Printf("   获取到 %d 首歌曲\n", len(remoteIds))
		allSongIds = append(allSongIds, remoteIds...)
		remoteCount = len(remoteIds)
	}

	if len(allSongIds) == 0 {
		return fmt.Errorf("no song IDs configured in scrobble config")
	}

	// 获取歌曲详情
	list, err := c.getSpecifiedSongsWithCloud(ctx, request, allSongIds, cloudSongIdSet)
	if err != nil {
		return fmt.Errorf("getSpecifiedSongsWithCloud: %w", err)
	}
	if len(list) == 0 {
		return fmt.Errorf("no songs found for the configured IDs")
	}

	var (
		left   = 300 - finish
		maxNum = cfg.Count
	)
	if maxNum <= 0 {
		maxNum = 300
	}
	if maxNum > left {
		maxNum = left
	}

	c.cmd.Printf("🎵 HAR精确模拟模式启动\n")
	c.cmd.Printf("   歌曲数量: %d (本地: %d, 远程: %d, 云盘: %d)\n", len(list), len(cfg.SongIds), remoteCount, len(cfg.CloudSongIds))
	c.cmd.Printf("   播放时长: %s\n", func() string {
		if cfg.Duration == 0 {
			return "歌曲实际时长"
		}
		return fmt.Sprintf("%d 秒", cfg.Duration)
	}())
	c.cmd.Printf("   循环次数: %d\n", maxNum)

	expire, err := utils.TimeUntilMidnight("Local")
	if err != nil {
		return fmt.Errorf("TimeUntilMidnight: %w", err)
	}

	var total int64
	for {
		for _, song := range list {
			if total >= maxNum {
				c.cmd.Printf("✅ 已完成 %d 首歌曲播放\n", total)
				return nil
			}

			select {
			case <-ctx.Done():
				c.cmd.Printf("⚠️ 任务被取消，共播放 %d 首\n", total)
				return ctx.Err()
			default:
			}

			// 计算本首歌曲的播放时长
			playTime := song.SongsTime
			if cfg.Duration > 0 {
				playTime = cfg.Duration
			}

			c.cmd.Printf("[%d/%d] 开始模拟播放: %s (时长: %ds)\n", total+1, maxNum, song.SongsId, playTime)

			// === HAR 模拟序列 ===

			// Step 1: P2P 心跳
			if _, err := request.P2PFlowSwitch(ctx, &weapi.P2PFlowSwitchReq{}); err != nil {
				log.Warn("[scrobble] P2PFlowSwitch err: %s", err)
			}

			// Step 2: WebLog - startplay 事件
			c.sendWebLogAction(ctx, request, "startplay", map[string]interface{}{
				"id":       song.SongsId,
				"type":     "song",
				"content":  fmt.Sprintf("id=%v", song.SourceId),
				"mainsite": "1",
			})

			// Step 3: WebLog - play 开始事件
			c.sendWebLogAction(ctx, request, "play", map[string]interface{}{
				"id":       song.SongsId,
				"type":     "song",
				"source":   song.Source,
				"sourceid": song.SourceId,
				"mainsite": "1",
				"content":  fmt.Sprintf("id=%v", song.SourceId),
			})

			// Step 4: 获取播放 URL (模拟浏览器获取播放地址)
			songIdInt, _ := strconv.ParseInt(song.SongsId, 10, 64)
			playerResp, err := request.SongPlayerV1(ctx, &weapi.SongPlayerV1Req{
				Ids:   types.IntsString{songIdInt},
				Level: types.LevelExhigh,
			})
			if err != nil {
				log.Warn("[scrobble] SongPlayerV1 err: %s", err)
			} else if len(playerResp.Data) > 0 && playerResp.Data[0].Url != "" {
				log.Debug("[scrobble] 获取播放URL成功: %s", playerResp.Data[0].Url[:50])
			}

			// Step 5: 播放过程中，每隔约60秒发送 WebLog + PlCount
			var elapsed int64
			heartbeatInterval := int64(60)
			for elapsed < playTime {
				remaining := playTime - elapsed
				sleepTime := heartbeatInterval
				if remaining < sleepTime {
					sleepTime = remaining
				}

				// 等待
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(time.Duration(sleepTime) * time.Second):
				}
				elapsed += sleepTime

				// 发送心跳 WebLog (播放中)
				c.sendWebLogAction(ctx, request, "play", map[string]interface{}{
					"type":     "song",
					"wifi":     0,
					"download": 0,
					"id":       song.SongsId,
					"time":     elapsed,
					"end":      "ui",
					"source":   song.Source,
					"sourceId": song.SourceId,
					"mainsite": "1",
					"content":  fmt.Sprintf("id=%v", song.SourceId),
				})

				// 发送 PlCount
				if _, err := request.PlCount(ctx, &weapi.PlCountReq{}); err != nil {
					log.Warn("[scrobble] PlCount err: %s", err)
				}

				c.cmd.Printf("  ⏱ 播放进度: %ds/%ds\n", elapsed, playTime)
			}

			// Step 6: WebLog - play 完成事件 (playend)
			c.sendWebLogAction(ctx, request, "play", map[string]interface{}{
				"type":     "song",
				"wifi":     0,
				"download": 0,
				"id":       song.SongsId,
				"time":     playTime,
				"end":      "playend",
				"source":   song.Source,
				"sourceId": song.SourceId,
				"mainsite": "1",
				"content":  fmt.Sprintf("id=%v", song.SourceId),
			})

			// 更新计数
			_, err = db.Increment(ctx, scrobbleTodayNumKey(uid), 1, expire)
			if err != nil {
				log.Warn("[scrobble] increment today num err: %s", err)
			}
			total++
			c.cmd.Printf("  ✅ 歌曲 %s 第 %d 次播放完成\n", song.SongsId, total)
		}
	}
}

// sendWebLogAction 发送指定 action 的 WebLog
func (c *Scrobble) sendWebLogAction(ctx context.Context, request *weapi.Api, action string, jsonData map[string]interface{}) {
	req := &weapi.WebLogReq{
		CsrfToken: "",
		Logs: []map[string]interface{}{
			{
				"action": action,
				"json":   jsonData,
			},
		},
	}
	resp, err := request.WebLog(ctx, req)
	if err != nil {
		log.Warn("[scrobble] WebLog(%s) err: %s", action, err)
		return
	}
	if resp.Code != 200 {
		log.Warn("[scrobble] WebLog(%s) code: %d", action, resp.Code)
	}
}

// fetchRemoteSongList 从远程URL获取歌曲ID列表
// 文件格式: 以#开头的行为备注，非空非#开头的行为songId
// 示例:
//
//	# 这是一首周杰伦的歌
//	3366663042
//	# 另一首歌
//	1234567890
func (c *Scrobble) fetchRemoteSongList(url string) ([]string, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("HTTP GET %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP GET %s returned status: %d", url, resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}

	var songIds []string
	scanner := bufio.NewScanner(strings.NewReader(string(body)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		// 跳过空行和注释行
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		songIds = append(songIds, line)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan response: %w", err)
	}

	return songIds, nil
}

// executeLoop 循环播放指定歌曲（简单模式，仅发送 playend）
func (c *Scrobble) executeLoop(ctx context.Context, request *weapi.Api, db database.Database, uid string, finish int64) error {
	list, err := c.getSpecifiedSongs(ctx, request, c.opts.SongIds, "list")
	if err != nil {
		return fmt.Errorf("getSpecifiedSongs: %w", err)
	}
	if len(list) == 0 {
		return fmt.Errorf("no songs found for the specified IDs")
	}

	var (
		left        = 300 - finish
		maxNum      = utils.Ternary(left > c.opts.Num, c.opts.Num, left)
		total       int64
		hasDeadline = c.opts.LoopMinutes > 0
		deadline    time.Time
	)
	if hasDeadline {
		deadline = time.Now().Add(time.Duration(c.opts.LoopMinutes) * time.Minute)
	}

	c.cmd.Printf("开始循环播放 %d 首歌曲", len(list))
	if hasDeadline {
		c.cmd.Printf("，总时长 %d 分钟", c.opts.LoopMinutes)
	}
	c.cmd.Printf("，最多刷 %d 首\n", maxNum)

	expire, err := utils.TimeUntilMidnight("Local")
	if err != nil {
		return fmt.Errorf("TimeUntilMidnight: %w", err)
	}

	for {
		for _, v := range list {
			if total >= maxNum {
				c.cmd.Printf("已完成 %d 首歌曲播放\n", total)
				return nil
			}
			if hasDeadline && time.Now().After(deadline) {
				c.cmd.Printf("循环时间已到，共播放 %d 首\n", total)
				return nil
			}
			select {
			case <-ctx.Done():
				c.cmd.Printf("任务被取消，共播放 %d 首\n", total)
				return ctx.Err()
			default:
			}

			playTime := c.getPlayDuration(v.SongsTime)

			if err := c.sendPlayWebLog(ctx, request, v); err != nil {
				continue
			}

			_, err := db.Increment(ctx, scrobbleTodayNumKey(uid), 1, expire)
			if err != nil {
				log.Warn("[scrobble] increment today num err: %w", err)
			}
			total++
			c.cmd.Printf("[%d/%d] 歌曲 %s 播放完成\n", total, maxNum, v.SongsId)

			if playTime > 0 {
				c.cmd.Printf("等待 %d 秒（模拟完整播放）...\n", playTime)
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(time.Duration(playTime) * time.Second):
				}
			} else {
				time.Sleep(time.Millisecond * 100)
			}
		}
	}
}

// sendPlayWebLog 发送简单的播放完成日志（快速刷歌模式）
func (c *Scrobble) sendPlayWebLog(ctx context.Context, request *weapi.Api, v NeverHeardSongsList) error {
	var req = &weapi.WebLogReq{CsrfToken: "", Logs: []map[string]interface{}{
		{
			"action": "play",
			"json": map[string]interface{}{
				"type":     "song",
				"wifi":     0,
				"download": 0,
				"id":       v.SongsId,
				"time":     v.SongsTime,
				"end":      "playend",
				"source":   v.Source,
				"sourceId": v.SourceId,
				"mainsite": "1",
				"content":  fmt.Sprintf("id=%v", v.SourceId),
			},
		},
	}}

	resp, err := request.WebLog(ctx, req)
	if err != nil {
		log.Error("[scrobble] WebLog: %w", err)
		return err
	}
	if resp.Code != 200 {
		log.Error("[scrobble] WebLog err: %+v\n", resp)
		time.Sleep(time.Second)
		return fmt.Errorf("WebLog code: %d", resp.Code)
	}
	return nil
}

// getPlayDuration 获取播放等待时长
func (c *Scrobble) getPlayDuration(songTime int64) int64 {
	switch {
	case c.opts.PlayDuration > 0:
		return c.opts.PlayDuration
	case c.opts.PlayDuration == 0:
		return songTime // 使用歌曲实际时长
	default:
		return 0 // -1: 不等待（快速模式）
	}
}

type NeverHeardSongsList struct {
	Source    string // 资源类型 (toplist, list, cloud)
	SourceId string // 歌单id / 专辑id
	SongsId  string // 歌曲id
	SongsTime int64 // 歌曲时长(秒)
}

// getSpecifiedSongs 根据指定的歌曲ID获取歌曲详情
func (c *Scrobble) getSpecifiedSongs(ctx context.Context, request *weapi.Api, songIds []string, source string) ([]NeverHeardSongsList, error) {
	req := make([]weapi.SongDetailReqList, 0, len(songIds))
	for _, id := range songIds {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		req = append(req, weapi.SongDetailReqList{Id: id, V: 0})
	}
	if len(req) == 0 {
		return nil, fmt.Errorf("no valid song IDs provided")
	}

	details, err := request.SongDetail(ctx, &weapi.SongDetailReq{C: req})
	if err != nil {
		return nil, fmt.Errorf("SongDetail: %w", err)
	}
	if details.Code != 200 {
		return nil, fmt.Errorf("SongDetail code: %d", details.Code)
	}

	var songs []NeverHeardSongsList
	for _, v := range details.Songs {
		songs = append(songs, NeverHeardSongsList{
			Source:    source,
			SourceId:  fmt.Sprintf("%v", v.Al.Id),
			SongsId:   fmt.Sprintf("%v", v.Id),
			SongsTime: v.Dt / 1000,
		})
		log.Info("[scrobble] 加载歌曲: %s - %s (时长: %ds)", v.Name, fmt.Sprintf("%v", v.Id), v.Dt/1000)
	}
	return songs, nil
}

// getSpecifiedSongsWithCloud 获取歌曲详情，区分普通歌曲和云盘歌曲
func (c *Scrobble) getSpecifiedSongsWithCloud(ctx context.Context, request *weapi.Api, songIds []string, cloudSet map[string]bool) ([]NeverHeardSongsList, error) {
	req := make([]weapi.SongDetailReqList, 0, len(songIds))
	for _, id := range songIds {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		req = append(req, weapi.SongDetailReqList{Id: id, V: 0})
	}
	if len(req) == 0 {
		return nil, fmt.Errorf("no valid song IDs provided")
	}

	details, err := request.SongDetail(ctx, &weapi.SongDetailReq{C: req})
	if err != nil {
		return nil, fmt.Errorf("SongDetail: %w", err)
	}
	if details.Code != 200 {
		return nil, fmt.Errorf("SongDetail code: %d", details.Code)
	}

	var songs []NeverHeardSongsList
	for _, v := range details.Songs {
		idStr := fmt.Sprintf("%v", v.Id)
		source := "list"
		if cloudSet[idStr] {
			source = "cloud"
		}
		songs = append(songs, NeverHeardSongsList{
			Source:    source,
			SourceId:  fmt.Sprintf("%v", v.Al.Id),
			SongsId:   idStr,
			SongsTime: v.Dt / 1000,
		})
		sourceLabel := "普通"
		if source == "cloud" {
			sourceLabel = "云盘"
		}
		log.Info("[scrobble] 加载歌曲[%s]: %s - %s (时长: %ds)", sourceLabel, v.Name, idStr, v.Dt/1000)
	}
	return songs, nil
}

func (c *Scrobble) neverHeardSongs(ctx context.Context, request *weapi.Api, db database.Database, uid string, num int64) ([]NeverHeardSongsList, error) {
	// 获取top歌单列表
	tops, err := request.TopList(ctx, &weapi.TopListReq{})
	if err != nil {
		return nil, fmt.Errorf("TopList: %w", err)
	}
	if tops.Code != 200 {
		return nil, fmt.Errorf("TopList err: %+v\n", tops)
	}
	if len(tops.List) <= 0 {
		return nil, fmt.Errorf("TopList is empty")
	}

	// 根据歌单返回顺序顺次刷歌直到300首歌曲
	var (
		req = make([]weapi.SongDetailReqList, 0, num)
		set = make(map[int64]string) // k:歌曲id v:歌单id
	)
	for _, list := range tops.List {
		info, err := request.PlaylistDetail(ctx, &weapi.PlaylistDetailReq{Id: fmt.Sprintf("%v", list.Id)})
		if err != nil {
			return nil, fmt.Errorf("PlaylistDetail(%v): %w", list.Id, err)
		}
		if info.Code != 200 {
			return nil, fmt.Errorf("PlaylistDetail(%v) err: %+v\n", list.Id, info)
		}
		if len(info.Playlist.TrackIds) <= 0 {
			log.Warn("PlaylistDetail(%v) is empty", list.Id)
			continue
		}

		var sourceId = list.Id
		for _, v := range info.Playlist.TrackIds {
			if int64(len(req)) >= num {
				break
			}

			exist, err := db.Exists(ctx, scrobbleRecordKey(uid, fmt.Sprintf("%d", v.Id)))
			if err != nil || exist {
				continue
			}

			if _, ok := set[v.Id]; !ok {
				set[v.Id] = fmt.Sprintf("%d", sourceId)
				req = append(req, weapi.SongDetailReqList{Id: fmt.Sprintf("%d", v.Id), V: 0})
			}
		}
		if int64(len(req)) >= num {
			log.Debug("SongDetailReqList num(%d)", len(req))
			break
		}
	}

	var resp = make([]NeverHeardSongsList, 0, num)
	details, err := request.SongDetail(ctx, &weapi.SongDetailReq{C: req})
	if err != nil {
		return nil, fmt.Errorf("SongDetail: %w", err)
	}
	for _, v := range details.Songs {
		resp = append(resp, NeverHeardSongsList{
			Source:    "toplist",
			SourceId:  set[v.Id],
			SongsId:   fmt.Sprintf("%v", v.Id),
			SongsTime: v.Dt / 1000,
		})
	}

	return resp, nil
}

func scrobbleRecordKey(uid string, songId string) string {
	return fmt.Sprintf("scrobble:record:%v:%v", uid, songId)
}

func scrobbleTodayNumKey(uid string) string {
	return fmt.Sprintf("scrobble:today:%v", uid)
}
