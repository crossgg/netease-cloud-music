// Mlog API — 发送/删除 Mlog (仅支持图片)
// Ported from https://github.com/XiaoMengXinX/Music163Api-Go
// Endpoints:
//   - /api/mlog/publish/v1 (发送 Mlog, EAPI)
//   - /api/mlog/delete/v1 (删除 Mlog, EAPI)

package eapi

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/chaunsin/netease-cloud-music/api"
	"github.com/chaunsin/netease-cloud-music/api/types"
	"github.com/chaunsin/netease-cloud-music/pkg/utils"
)

// ===================== 发送 Mlog (MlogPublish) =====================

// MlogPublishReq 发送 Mlog 请求
type MlogPublishReq struct {
	// Type Mlog 类型, 固定为 "1"
	Type string `json:"type"`
	// Mlog Mlog 内容 JSON 字符串, 由 MlogBuildContent 生成
	Mlog string `json:"mlog"`
}

// mlogContent Mlog 内容结构 (序列化为 JSON 字符串后放入 MlogPublishReq.Mlog)
type mlogContent struct {
	Content struct {
		Image    []MlogPic `json:"image"`
		NeedAudio bool     `json:"needAudio"`
		Song     struct {
			EndTime  int    `json:"endTime"`
			Name     string `json:"name"`
			SongId   string `json:"songId"`
			StartTime int   `json:"startTime"`
		} `json:"song"`
		Text string `json:"text"`
	} `json:"content"`
	From int `json:"from"`
	Type int  `json:"type"`
}

// MlogPic Mlog 图片信息
type MlogPic struct {
	Height int    `json:"height"`
	More   bool   `json:"more"`
	NosKey string `json:"nosKey"`
	PicKey string `json:"picKey"`
	Width  int    `json:"width"`
}

// MlogPublishResp 发送 Mlog 响应
type MlogPublishResp struct {
	types.RespCommon[any]
	Data struct {
		Feed struct {
			MlogBaseData struct {
				Id          string `json:"id"`
				Type        int    `json:"type"`
				Text        string `json:"text"`
				Desc        string `json:"desc"`
				PubTime     int64  `json:"pubTime"`
				CoverUrl    string `json:"coverUrl"`
				CoverPicKey string `json:"coverPicKey"`
				CoverHeight int    `json:"coverHeight"`
				CoverWidth  int    `json:"coverWidth"`
				ThreadId    string `json:"threadId"`
				Duration    int    `json:"duration"`
			} `json:"mlogBaseData"`
			MlogExtVO struct {
				LikedCount   int `json:"likedCount"`
				CommentCount int `json:"commentCount"`
				PlayCount    int `json:"playCount"`
				ShareCount   int `json:"shareCount"`
			} `json:"mlogExtVO"`
			UserProfile struct {
				UserId    int    `json:"userId"`
				Nickname  string `json:"nickname"`
				AvatarUrl string `json:"avatarUrl"`
			} `json:"userProfile"`
			Status   int    `json:"status"`
			ShareUrl string `json:"shareUrl"`
		} `json:"feed"`
		Event struct {
			Id       int    `json:"id"`
			Type     int    `json:"type"`
			ThreadId string `json:"threadId"`
		} `json:"event"`
	} `json:"data"`
}

// MlogPublish 发送 Mlog (仅支持图片)
// 接口: /api/mlog/publish/v1
// 加密: EAPI
// 需要登录
//
// 使用示例:
//
//	// 1. 上传图片并构建 Mlog 内容
//	content, err := api.MlogBuildContent(ctx, "这是一条 Mlog", 12345,
//	    []string{"/path/to/img1.jpg", "/path/to/img2.jpg"}, 0)
//	// 2. 发送 Mlog
//	resp, err := api.MlogPublish(ctx, &eapi.MlogPublishReq{
//	    Type: "1",
//	    Mlog: content,
//	})
func (a *Api) MlogPublish(ctx context.Context, req *MlogPublishReq) (*MlogPublishResp, error) {
	if req.Type == "" {
		req.Type = "1"
	}

	var (
		url   = "https://music.163.com/eapi/mlog/publish/v1"
		reply MlogPublishResp
		opts  = api.NewOptions()
	)
	opts.CryptoMode = api.CryptoModeEAPI

	resp, err := a.client.Request(ctx, url, req, &reply, opts)
	if err != nil {
		return nil, fmt.Errorf("Request: %w", err)
	}
	_ = resp
	return &reply, nil
}

// ===================== 删除 Mlog (MlogDelete) =====================

// MlogDeleteReq 删除 Mlog 请求
type MlogDeleteReq struct {
	// Id Mlog ID
	Id string `json:"id"`
}

// MlogDeleteResp 删除 Mlog 响应
type MlogDeleteResp struct {
	types.RespCommon[any]
}

// MlogDelete 删除 Mlog
// 接口: /api/mlog/delete/v1
// 加密: EAPI
// 需要登录
func (a *Api) MlogDelete(ctx context.Context, req *MlogDeleteReq) (*MlogDeleteResp, error) {
	var (
		url   = "https://music.163.com/eapi/mlog/delete/v1"
		reply MlogDeleteResp
		opts  = api.NewOptions()
	)
	opts.CryptoMode = api.CryptoModeEAPI

	resp, err := a.client.Request(ctx, url, req, &reply, opts)
	if err != nil {
		return nil, fmt.Errorf("Request: %w", err)
	}
	_ = resp
	return &reply, nil
}

// ===================== Mlog 图片上传 & 内容构建 =====================

// mlogNosTokenResp Mlog Nos Token 分配响应 (whalealloc)
type mlogNosTokenResp struct {
	types.RespCommon[any]
	Data struct {
		Bucket     string `json:"bucket"`
		Token      string `json:"token"`
		OuterUrl   string `json:"outerUrl"`
		DocId      string `json:"docId"`
		ObjectKey  string `json:"objectKey"`
		ResourceId int    `json:"resourceId"`
	} `json:"data"`
}

// MlogBuildContent 上传 Mlog 图片并构建 Mlog 内容 JSON 字符串
// 参数:
//   - text: Mlog 文本内容
//   - songId: 关联歌曲 ID (可传 0 表示无关联歌曲)
//   - filePaths: 图片文件路径列表 (至少1张)
//   - startTime: 歌曲播放起始时间 (毫秒, 无歌曲时传 0)
//
// 返回值为 MlogPublishReq.Mlog 所需的 JSON 字符串
func (a *Api) MlogBuildContent(ctx context.Context, text string, songId int64, filePaths []string, startTime int) (string, error) {
	if len(filePaths) == 0 {
		return "", fmt.Errorf("at least one image is required")
	}

	var pics []MlogPic
	for _, fp := range filePaths {
		data, err := os.ReadFile(fp)
		if err != nil {
			return "", fmt.Errorf("read file %s: %w", fp, err)
		}

		md5, err := utils.MD5Hex(data)
		if err != nil {
			return "", fmt.Errorf("MD5Hex: %w", err)
		}

		ext := strings.TrimPrefix(filepath.Ext(fp), ".")
		filename := filepath.Base(fp)
		fileSize := int64(len(data))

		// 获取图片尺寸
		width, height := imageDimensions(data)

		// Step 1: 获取 Nos Token (whalealloc)
		// 生成 8 位随机 hex 作为 bizKey
		bizKey := fmt.Sprintf("%08x", randomUint32())

		var (
			tokenURL = "https://music.163.com/eapi/nos/token/whalealloc"
			tokenReq = struct {
				BizKey   string `json:"bizKey"`
				Filename string `json:"filename"`
				Bucket   string `json:"bucket"`
				Md5      string `json:"md5"`
				Type     string `json:"type"`
				FileSize int64  `json:"fileSize"`
			}{
				BizKey:   bizKey,
				Filename: filename,
				Bucket:   "yyimgs",
				Md5:      md5,
				Type:     "image",
				FileSize: fileSize,
			}
			tokenReply mlogNosTokenResp
			tokenOpts  = api.NewOptions()
		)
		tokenOpts.CryptoMode = api.CryptoModeEAPI

		if _, err := a.client.Request(ctx, tokenURL, tokenReq, &tokenReply, tokenOpts); err != nil {
			return "", fmt.Errorf("get mlog nos token for %s: %w", fp, err)
		}
		if tokenReply.Code != 200 {
			return "", fmt.Errorf("get mlog nos token failed for %s: code=%d", fp, tokenReply.Code)
		}

		// Step 2: 上传文件到 NOS
		uploadNode, err := a.getUploadNode(ctx, tokenReply.Data.Bucket)
		if err != nil {
			return "", fmt.Errorf("get upload node: %w", err)
		}

		contentType := utils.DetectContentType(data, ext)
		uploadURL := fmt.Sprintf("%s/%s/%s?version=1.0&offset=0&complete=true",
			uploadNode, tokenReply.Data.Bucket, tokenReply.Data.ObjectKey)

		headers := map[string]string{
			"X-Nos-Token":  tokenReply.Data.Token,
			"Content-Type": contentType,
		}

		if err := a.rawUpload(ctx, uploadURL, headers, data); err != nil {
			return "", fmt.Errorf("upload %s: %w", fp, err)
		}

		// 构建 MlogPic
		pics = append(pics, MlogPic{
			Height: height,
			More:   false,
			NosKey: fmt.Sprintf("%s/%s", tokenReply.Data.Bucket, tokenReply.Data.ObjectKey),
			PicKey: strconv.Itoa(tokenReply.Data.ResourceId),
			Width:  width,
		})
	}

	// 构建 mlog 内容
	var content mlogContent
	content.Content.Image = pics
	content.Content.NeedAudio = false
	content.Content.Song.EndTime = 0
	content.Content.Song.StartTime = startTime
	content.Content.Song.SongId = strconv.FormatInt(songId, 10)
	content.Content.Text = text
	content.From = 0
	content.Type = 1

	// 如果没有歌曲信息，清空 song 字段
	if songId == 0 {
		content.Content.Song = struct {
			EndTime   int    `json:"endTime"`
			Name      string `json:"name"`
			SongId    string `json:"songId"`
			StartTime int    `json:"startTime"`
		}{}
	}

	contentBytes, err := json.Marshal(content)
	if err != nil {
		return "", fmt.Errorf("marshal mlog content: %w", err)
	}
	return string(contentBytes), nil
}

// imageDimensions 从图片字节数据中解码宽高
func imageDimensions(data []byte) (width, height int) {
	cfg, _, err := image.DecodeConfig(strings.NewReader(string(data)))
	if err != nil {
		return 0, 0
	}
	return cfg.Width, cfg.Height
}

// randomUint32 生成随机 uint32
func randomUint32() uint32 {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0
	}
	return binary.BigEndian.Uint32(b[:])
}
