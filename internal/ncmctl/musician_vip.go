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
	"math/rand"
	"net/http"
	neturl "net/url"
	"os"
	"strings"
	"time"

	"github.com/chaunsin/netease-cloud-music/api"
	"github.com/chaunsin/netease-cloud-music/api/eapi"
	"github.com/chaunsin/netease-cloud-music/config"
	"github.com/chaunsin/netease-cloud-music/pkg/log"

	"github.com/spf13/cobra"
)

type MusicianVip struct {
	root *Root
	cmd  *cobra.Command
	l    *log.Logger
	rng  *rand.Rand
}

func NewMusicianVip(root *Root, l *log.Logger) *MusicianVip {
	c := &MusicianVip{
		root: root,
		l:    l,
		rng:  rand.New(rand.NewSource(time.Now().UnixNano())),
		cmd: &cobra.Command{
			Use:     "musician-vip",
			Short:   "[need login] Auto-complete musician VIP tasks (publish notes + playids)",
			Example: "  ncmctl task --musician-vip\n  ncmctl musician-vip",
		},
	}
	c.cmd.RunE = func(cmd *cobra.Command, args []string) error {
		return c.execute(cmd.Context())
	}
	return c
}

func (c *MusicianVip) validate() error {
	if c.root.Cfg.MusicianVip == nil {
		return fmt.Errorf("musicianVip config is not set in config.yaml")
	}
	return nil
}

func (c *MusicianVip) Add(command ...*cobra.Command) {
	c.cmd.AddCommand(command...)
}

func (c *MusicianVip) Command() *cobra.Command {
	return c.cmd
}

func (c *MusicianVip) execute(ctx context.Context) error {
	if err := c.validate(); err != nil {
		return fmt.Errorf("validate: %w", err)
	}

	cli, err := api.NewClient(c.root.Cfg.Network, c.l)
	if err != nil {
		return fmt.Errorf("NewClient: %w", err)
	}
	defer cli.Close(ctx)

	// 设置cookie
	if err := loadCookies(cli, c.root.Cfg.Network); err != nil {
		log.Warn("[musician-vip] load cookies err: %s", err)
	}

	eapiCli := eapi.New(cli)

	// 获取音乐人黑胶会员任务状态
	c.cmd.Println("[musician-vip] 检查任务状态...")
	resp, err := eapiCli.MusicianVipTasks(ctx, &eapi.MusicianVipTasksReq{ER: false})
	if err != nil {
		return fmt.Errorf("MusicianVipTasks: %w", err)
	}
	if resp.Code != 200 {
		return fmt.Errorf("MusicianVipTasks error: code=%d msg=%s", resp.Code, resp.Message)
	}
	if !resp.Data.IsMusician {
		return fmt.Errorf("当前账号不是音乐人")
	}

	c.cmd.Printf("[musician-vip] ✅ 已认证音乐人 | 维持天数: %d 天 | 近30天播放: %d 次 | 解锁VIP权益: %v\n",
		resp.Data.MaintainDays, resp.Data.RecentPlayCount30, resp.Data.UnlockVipRight)

	if resp.Data.FurtherTask == nil {
		c.cmd.Println("[musician-vip] 没有进阶任务")
		return nil
	}

	c.cmd.Printf("[musician-vip] 进阶任务进度: %d/%d 完成\n",
		resp.Data.FurtherTask.ProgressRate, resp.Data.FurtherTask.TotalCompleteNum)

	// 遍历子任务，检查并执行
	for _, sub := range resp.Data.FurtherTask.Children {
		// 播放任务使用 recentPlayCount30 作为实际进度（服务端子任务 progressRate 可能不准确）
		progress := sub.ProgressRate
		if sub.MissionCode == "mission_code_recently_play_count" {
			progress = resp.Data.RecentPlayCount30
		}
		c.cmd.Printf("[musician-vip] 任务: %s — 状态: %d, 进度: %d/%d\\n",
			sub.Name, sub.MissionStatus, progress, sub.TotalCompleteNum)

		// 任务已完成则跳过
		if sub.MissionStatus == 100 {
			c.cmd.Printf("[musician-vip] ✅ 任务已完成: %s\\n", sub.Name)
			continue
		}

		// 根据任务类型执行
		switch sub.MissionCode {
		case "mission_code_musician_notebook_publish":
			// 发布图文笔记任务
			if err := c.handleNoteTask(ctx, cli, eapiCli, sub); err != nil {
				log.Error("[musician-vip] 笔记任务执行失败: %s", err)
				c.cmd.Printf("[musician-vip] ❌ 笔记任务失败: %s\\n", err)
			}

		case "mission_code_recently_play_count":
			// 播放任务：服务端子任务 progressRate 可能不准确，使用 recentPlayCount30 作为实际播放数
			if err := c.handlePlayTask(ctx, sub, resp.Data.RecentPlayCount30); err != nil {
				log.Error("[musician-vip] 播放任务执行失败: %s", err)
				c.cmd.Printf("[musician-vip] ❌ 播放任务失败: %s\\n", err)
			}

		default:
			c.cmd.Printf("[musician-vip] ⚠️ 未知任务类型: %s\\n", sub.MissionCode)
		}
	}

	return nil
}

// handleNoteTask 处理发布图文笔记任务
func (c *MusicianVip) handleNoteTask(ctx context.Context, cli *api.Client, eapiCli *eapi.Api, sub eapi.MusicianVipSubTask) error {
	c.cmd.Println("[musician-vip] 处理笔记任务...")

	cfg := c.root.Cfg.MusicianVip.Note

	// 检查是否需要发布（进度 < 目标）
	if sub.ProgressRate >= sub.TotalCompleteNum {
		c.cmd.Println("[musician-vip] 笔记任务已完成，无需发布")
		return nil
	}

	// 获取笔记内容
	msg := c.getRandomMessage(cfg.Messages)
	if msg == "" {
		msg = "分享一首好听的歌~"
	}

	// 获取图片URL
	imageURL := c.getRandomImageURL(cfg.ImageURLs)
	if imageURL == "" {
		return fmt.Errorf("没有配置图片URL，请在 config.yaml 的 musicianVip.note.imageUrls 中配置")
	}

	c.cmd.Printf("[musician-vip] 发布笔记: 内容=%q, 图片=%s\n", msg, imageURL)

	// 下载图片到临时文件
	tmpFile, err := downloadImageToTemp(ctx, imageURL)
	if err != nil {
		return fmt.Errorf("下载图片失败: %w", err)
	}
	defer os.Remove(tmpFile)

	// 上传图片
	c.cmd.Println("[musician-vip] 上传图片...")
	pics, err := eapiCli.EventUploadImage(ctx, tmpFile)
	if err != nil {
		return fmt.Errorf("上传图片失败: %w", err)
	}
	c.cmd.Printf("[musician-vip] 图片上传成功: %s\n", pics)

	// 发布动态
	c.cmd.Println("[musician-vip] 发布动态...")
	dynamicType := cfg.Type
	if dynamicType == 0 {
		dynamicType = 35 // 默认普通动态
	}

	resp, err := eapiCli.EventPublish(ctx, &eapi.EventPublishReq{
		Msg:  msg,
		Type: fmt.Sprintf("%d", dynamicType),
		Pics: pics,
	})
	if err != nil {
		return fmt.Errorf("发布动态失败: %w", err)
	}
	if resp.Code != 200 {
		return fmt.Errorf("发布动态失败: code=%d", resp.Code)
	}

	c.cmd.Printf("[musician-vip] ✅ 笔记发布成功! 动态ID: %d\n", resp.Id)
	return nil
}

// handlePlayTask 处理播放任务
func (c *MusicianVip) handlePlayTask(ctx context.Context, sub eapi.MusicianVipSubTask, recentPlayCount30 int) error {
	c.cmd.Println("[musician-vip] 处理播放任务...")

	cfg := c.root.Cfg.MusicianVip.Play

	// 使用 recentPlayCount30 作为实际播放数（服务端子任务 progressRate 可能不准确）
	c.cmd.Printf("[musician-vip] 当前播放进度: %d/%d\\n", recentPlayCount30, sub.TotalCompleteNum)
	if recentPlayCount30 >= sub.TotalCompleteNum {
		c.cmd.Println("[musician-vip] 播放任务已完成，无需播放")
		return nil
	}

	// 检查是否配置了歌曲ID
	if cfg.IDs == "" && cfg.IDsFile == "" {
		return fmt.Errorf("没有配置歌曲ID，请在 config.yaml 的 musicianVip.play.ids 或 idsFile 中配置")
	}

	// 构建playids命令参数
	args := []string{}
	if cfg.IDs != "" {
		args = append(args, "--ids", cfg.IDs)
	}
	if cfg.IDsFile != "" {
		args = append(args, "--ids-file", cfg.IDsFile)
	}
	if cfg.Num > 0 {
		args = append(args, "--num", fmt.Sprintf("%d", cfg.Num))
	} else {
		args = append(args, "--num", "300")
	}
	if cfg.GapMin > 0 {
		args = append(args, "--gap-min", fmt.Sprintf("%d", cfg.GapMin))
	}
	if cfg.GapMax > 0 {
		args = append(args, "--gap-max", fmt.Sprintf("%d", cfg.GapMax))
	}

	// 如果指定了非默认cookie文件，临时切换cookie路径（PlayIDs会通过 c.root.Cfg.Network 读取）
	if cfg.CookieFile != "" {
		c.cmd.Printf("[musician-vip] 使用指定cookie文件: %s\n", cfg.CookieFile)
		origPath := c.root.Cfg.Network.Cookie.Filepath
		c.root.Cfg.Network.Cookie.Filepath = cfg.CookieFile
		defer func() { c.root.Cfg.Network.Cookie.Filepath = origPath }()
	}

	c.cmd.Printf("[musician-vip] 启动playids: %v\n", args)

	// 创建playids命令并执行
	p := NewPlayIDs(c.root, c.l)
	p.cmd.DisableFlagParsing = true
	p.opts = PlayIDsOpts{
		IDs:     cfg.IDs,
		IDsFile: cfg.IDsFile,
		Num: func() int64 {
			if cfg.Num > 0 {
				return cfg.Num
			}
			return 300
		}(),
		GapMin: func() int64 {
			if cfg.GapMin > 0 {
				return cfg.GapMin
			}
			return 5
		}(),
		GapMax: func() int64 {
			if cfg.GapMax > 0 {
				return cfg.GapMax
			}
			return 20
		}(),
	}

	if err := p.validate(); err != nil {
		return fmt.Errorf("playids validate: %w", err)
	}

	return p.Command().ExecuteContext(ctx)
}

// getRandomMessage 随机获取一条消息
func (c *MusicianVip) getRandomMessage(messages []string) string {
	if len(messages) == 0 {
		return ""
	}
	return messages[c.rng.Intn(len(messages))]
}

// getRandomImageURL 随机获取一个图片URL
func (c *MusicianVip) getRandomImageURL(urls []string) string {
	if len(urls) == 0 {
		return ""
	}
	return urls[c.rng.Intn(len(urls))]
}

// loadCookies 从配置加载cookie
func loadCookies(cli *api.Client, cfg *api.Config) error {
	// 如果有cookie文件，尝试加载
	if cfg.Cookie.Filepath != "" {
		data, err := os.ReadFile(cfg.Cookie.Filepath)
		if err != nil {
			return fmt.Errorf("read cookie file: %w", err)
		}
		cookies := parseCookieString(string(data))
		if len(cookies) > 0 {
			// 设置到music.163.com域名
			url := &neturl.URL{
				Scheme: "https",
				Host:   "music.163.com",
			}
			cli.SetCookies(url, cookies)
		}
	}
	return nil
}

// parseCookieString 解析cookie字符串
func parseCookieString(s string) []*http.Cookie {
	var cookies []*http.Cookie
	parts := strings.Split(s, ";")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			continue
		}
		cookies = append(cookies, &http.Cookie{
			Name:  strings.TrimSpace(kv[0]),
			Value: strings.TrimSpace(kv[1]),
		})
	}
	return cookies
}

// downloadImageToTemp 下载图片到临时文件
func downloadImageToTemp(ctx context.Context, url string) (string, error) {
	// 如果是本地文件路径，直接返回
	if strings.HasPrefix(url, "/") || strings.HasPrefix(url, "./") {
		return url, nil
	}

	// 下载图片
	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download failed: status=%d", resp.StatusCode)
	}

	// 创建临时文件
	tmpFile, err := os.CreateTemp("", "ncm-img-*.jpg")
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}
	defer tmpFile.Close()

	// 写入文件
	if _, err := tmpFile.ReadFrom(resp.Body); err != nil {
		os.Remove(tmpFile.Name())
		return "", fmt.Errorf("write file: %w", err)
	}

	return tmpFile.Name(), nil
}

// MusicianVipSubTask 子任务信息（用于传递给handle函数）
type MusicianVipSubTask struct {
	Name             string
	MissionCode      string
	MissionStatus    int
	ProgressRate     int
	TotalCompleteNum int
}

// toSubTask 转换子任务结构
func toSubTask(sub eapi.MusicianVipSubTask) MusicianVipSubTask {
	return MusicianVipSubTask{
		Name:             sub.Name,
		MissionCode:      sub.MissionCode,
		MissionStatus:    sub.MissionStatus,
		ProgressRate:     sub.ProgressRate,
		TotalCompleteNum: sub.TotalCompleteNum,
	}
}

// MusicianVipConf 音乐人黑胶会员任务配置（用于测试）
type MusicianVipConf = config.MusicianVipConf

// MusicianVipNoteConf 笔记发布任务配置（用于测试）
type MusicianVipNoteConf = config.MusicianVipNoteConf

// MusicianVipPlayConf 播放任务配置（用于测试）
type MusicianVipPlayConf = config.MusicianVipPlayConf
