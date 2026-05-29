// Musician API
// Ported from https://github.com/NeteaseCloudMusicApiEnhanced/api-enhanced

package weapi

import (
	"context"
	"fmt"

	"github.com/chaunsin/netease-cloud-music/api"
	"github.com/chaunsin/netease-cloud-music/api/types"
)

// MusicianSignReq 音乐人签到请求
type MusicianSignReq struct{}

// MusicianSignResp 音乐人签到响应
type MusicianSignResp struct {
	types.RespCommon[any]
}

// MusicianSign 音乐人签到（完成"登录音乐人中心"任务）
// url: /api/creator/user/access
func (a *Api) MusicianSign(ctx context.Context, req *MusicianSignReq) (*MusicianSignResp, error) {
	var (
		url   = "https://music.163.com/api/creator/user/access"
		reply MusicianSignResp
		opts  = api.NewOptions()
	)

	resp, err := a.client.Request(ctx, url, req, &reply, opts)
	if err != nil {
		return nil, fmt.Errorf("Request: %w", err)
	}
	_ = resp
	return &reply, nil
}

// MusicianTasksReq 获取音乐人任务列表请求
type MusicianTasksReq struct{}

// MusicianTasksResp 获取音乐人任务列表响应
type MusicianTasksResp struct {
	types.RespCommon[MusicianTasksRespData]
}

// MusicianTasksRespData 音乐人任务列表数据
type MusicianTasksRespData struct {
	TaskList []MusicianTask `json:"taskList"`
}

// MusicianTask 单个音乐人任务
type MusicianTask struct {
	UserMissionId int64  `json:"userMissionId"`
	MissionId     int64  `json:"missionId"`
	Period        int64  `json:"period"`
	Name          string `json:"name"`
	Description   string `json:"description"`
	Status        int64  `json:"status"` // 任务状态: 1=未完成, 2=已完成待领取, 3=已领取
	CurrentProgress int64 `json:"currentProgress"`
	TargetWorth   int64  `json:"targetWorth"`
	GrowthPoint   int64  `json:"growthPoint"`
	Action        string `json:"action"`
	ActionType    int64  `json:"actionType"`
	Type          int64  `json:"type"`
	UpdateTime    int64  `json:"updateTime"`
}

// MusicianTasks 获取音乐人任务列表
// url: /api/nmusician/workbench/mission/cycle/list
func (a *Api) MusicianTasks(ctx context.Context, req *MusicianTasksReq) (*MusicianTasksResp, error) {
	var (
		url   = "https://music.163.com/api/nmusician/workbench/mission/cycle/list"
		reply MusicianTasksResp
		opts  = api.NewOptions()
	)

	resp, err := a.client.Request(ctx, url, req, &reply, opts)
	if err != nil {
		return nil, fmt.Errorf("Request: %w", err)
	}
	_ = resp
	return &reply, nil
}

// MusicianCloudbeanObtainReq 领取云豆请求
type MusicianCloudbeanObtainReq struct {
	Id     string `json:"id"`     // 任务 id (userMissionId)
	Period string `json:"period"` // 任务周期
}

// MusicianCloudbeanObtainResp 领取云豆响应
type MusicianCloudbeanObtainResp struct {
	types.RespCommon[any]
}

// MusicianCloudbeanObtain 领取音乐人云豆奖励
// url: /api/nmusician/workbench/mission/reward/obtain/new
func (a *Api) MusicianCloudbeanObtain(ctx context.Context, req *MusicianCloudbeanObtainReq) (*MusicianCloudbeanObtainResp, error) {
	if req.Id == "" {
		return nil, fmt.Errorf("id is required")
	}
	if req.Period == "" {
		return nil, fmt.Errorf("period is required")
	}

	var (
		url   = "https://music.163.com/api/nmusician/workbench/mission/reward/obtain/new"
		reply MusicianCloudbeanObtainResp
		opts  = api.NewOptions()
	)

	resp, err := a.client.Request(ctx, url, req, &reply, opts)
	if err != nil {
		return nil, fmt.Errorf("Request: %w", err)
	}
	_ = resp
	return &reply, nil
}
