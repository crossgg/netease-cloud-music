package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/chaunsin/netease-cloud-music/api"
	"github.com/chaunsin/netease-cloud-music/api/eapi"
	"github.com/chaunsin/netease-cloud-music/pkg/cookie"
	"github.com/chaunsin/netease-cloud-music/pkg/log"
)

func main() {
	log.Default = log.New(&log.Config{
		Level:  "info",
		Stdout: true,
	})

	ctx := context.Background()

	// 创建客户端
	cfg := api.Config{
		Debug:   true,
		Timeout: 120 * time.Second,
		Cookie: cookie.Config{},
	}
	cli := api.New(&cfg)
	defer cli.Close(ctx)

	// 读取并设置 cookie
	cookieStr, err := os.ReadFile("/tmp/ncm_cookie.txt")
	if err != nil {
		fmt.Printf("❌ 读取 cookie 文件失败: %v\n", err)
		os.Exit(1)
	}

	u, _ := url.Parse("https://music.163.com")
	var cookies []*http.Cookie
	for _, part := range strings.Split(strings.TrimSpace(string(cookieStr)), ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		kv := strings.SplitN(part, "=", 2)
		if len(kv) == 2 {
			cookies = append(cookies, &http.Cookie{
				Name:  strings.TrimSpace(kv[0]),
				Value: strings.TrimSpace(kv[1]),
			})
		}
	}
	cli.SetCookies(u, cookies)
	fmt.Printf("✅ 已设置 %d 个 cookie\n\n", len(cookies))

	eapiCli := eapi.New(cli)

	// ==================== 测试1: 黑胶会员任务 ====================
	fmt.Println("=" + strings.Repeat("=", 50))
	fmt.Println("📋 测试1: 音乐人黑胶会员任务")
	fmt.Println("=" + strings.Repeat("=", 50))

	vipResp, err := eapiCli.MusicianVipTasks(ctx, &eapi.MusicianVipTasksReq{ER: false})
	if err != nil {
		fmt.Printf("❌ MusicianVipTasks 失败: %v\n", err)
	} else if vipResp.Code != 200 {
		fmt.Printf("❌ API 返回错误: code=%d, msg=%s\n", vipResp.Code, vipResp.Message)
	} else {
		fmt.Printf("✅ 已认证音乐人: %v | 维持天数: %d 天 | 近30天播放: %d 次 | 解锁VIP权益: %v\n",
			vipResp.Data.IsMusician, vipResp.Data.MaintainDays,
			vipResp.Data.RecentPlayCount30, vipResp.Data.UnlockVipRight)

		if vipResp.Data.FurtherTask != nil {
			fmt.Printf("\n进阶任务 (%d/%d 完成):\n",
				vipResp.Data.FurtherTask.ProgressRate, vipResp.Data.FurtherTask.TotalCompleteNum)
			for i, sub := range vipResp.Data.FurtherTask.Children {
				status := "⏳ 进行中"
				if sub.MissionStatus == 100 {
					status = "✅ 已完成"
				} else if sub.MissionStatus == 50 {
					status = "⏳ 进行中"
				}
				fmt.Printf("  %d. %s — %s (进度: %d/%d)\n",
					i+1, sub.Name, status, sub.ProgressRate, sub.TotalCompleteNum)
			}
		}
	}

	// ==================== 测试2: 发送带图片的动态 ====================
	fmt.Println("\n" + strings.Repeat("=", 51))
	fmt.Println("📝 测试2: 发送带图片的动态")
	fmt.Println(strings.Repeat("=", 51))

	// 2.1 下载一张图片
	imgPath := "/tmp/ncm_test_image.jpg"
	fmt.Println("📷 下载测试图片...")
	if err := downloadImage(imgPath); err != nil {
		fmt.Printf("❌ 下载图片失败: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("✅ 图片已保存: %s\n", imgPath)

	// 2.2 上传图片
	fmt.Println("📤 上传图片...")
	pics, err := eapiCli.EventUploadImage(ctx, imgPath)
	if err != nil {
		fmt.Printf("❌ 上传图片失败: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("✅ 图片上传成功, pics=%s\n", pics)

	// 2.3 发送动态
	poem := "春眠不觉晓，处处闻啼鸟。\n夜来风雨声，花落知多少。\n—— 孟浩然《春晓》"

	fmt.Printf("💬 发送动态: %s\n", poem)
	resp, err := eapiCli.EventPublish(ctx, &eapi.EventPublishReq{
		Msg:  poem,
		Type: "noresource",
		Pics: pics,
	})
	if err != nil {
		fmt.Printf("❌ 发送动态失败: %v\n", err)
	} else if resp.Code != 200 {
		fmt.Printf("❌ API 返回错误: code=%d, msg=%s\n", resp.Code, resp.Message)
	} else {
		fmt.Printf("✅ 动态发送成功! 动态ID: %d\n", resp.Id)
		pretty, _ := json.MarshalIndent(resp, "", "  ")
		fmt.Printf("响应详情:\n%s\n", string(pretty))
	}
}

// downloadImage 从网上下载一张测试图片
func downloadImage(path string) error {
	// 使用一个公开的测试图片 URL (Lorem Picsum)
	resp, err := http.Get("https://picsum.photos/800/600")
	if err != nil {
		return fmt.Errorf("http get: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read body: %w", err)
	}

	return os.WriteFile(path, data, 0644)
}
