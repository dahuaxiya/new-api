package controller

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/gptquota"
)

// quotaHistoryCheckpoint 中 SeatID 非空表示该用户还有名额尚未扫完；为空表示已经扫完整个用户。
// 同时记录用户与名额，防止一个拥有大量名额的用户每次耗尽预算而饿死后面的名额。
type quotaHistoryCheckpoint struct {
	UserID int    `json:"user_id"`
	SeatID string `json:"seat_id,omitempty"`
}

// quotaHistoryTaskHandler 将后台快照读取纳入租约；手动刷新仍走原有用户鉴权和历史去重流程。
type quotaHistoryTaskHandler struct{}

// Type 返回集群内共享的采集任务种类。
func (quotaHistoryTaskHandler) Type() string { return "gpt_quota_history" }

// Enabled 仅允许配置额度来源的托管站采集；不能因为中转站持有同类表就访问客户账号。
func (quotaHistoryTaskHandler) Enabled() bool {
	return !common.IsRetailSite() && (os.Getenv("GPT_QUOTA_CONFIG") != "" || os.Getenv("VM2API_INSTANCES_CONFIG") != "")
}

// Interval 保留每分钟采集节奏；每个名额仍由五分钟桶限制历史点数量。
func (quotaHistoryTaskHandler) Interval() time.Duration { return time.Minute }

// NewPayload 不接受浏览器传入的用户或实例范围，采集目标必须从本站库中解析。
func (quotaHistoryTaskHandler) NewPayload() any { return nil }

// ResumeState 恢复上次提交的扫描位置；损坏的游标不能静默归零以掩盖重复扫描。
func (quotaHistoryTaskHandler) ResumeState(previous *model.SystemTask) (any, error) {
	state := quotaHistoryCheckpoint{}
	if previous != nil {
		if err := previous.DecodeState(&state); err != nil {
			return nil, err
		}
	}
	if state.UserID < 0 || len(state.SeatID) > 36 || (state.UserID == 0 && state.SeatID != "") {
		return nil, errors.New("invalid quota history checkpoint")
	}
	return state, nil
}

// Run 执行有预算的快照采集；逐名额提交进度，单条失败留待下轮全扫描，关闭或失锁后停止发起新操作。
// 上游只读取既有快照。崩溃窗口可能重读最后一项，历史表已有的唯一桶和时间检查负责防重及防倒退。
func (h quotaHistoryTaskHandler) Run(parent context.Context, task *model.SystemTask, runnerID string) {
	ctx, cancel := context.WithTimeout(parent, 50*time.Second)
	defer cancel()
	saved, err := h.ResumeState(task)
	if !h.Enabled() || err != nil {
		finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusFailed, nil, errors.New("site task configuration or checkpoint unavailable"))
		return
	}
	svc, err := quotaService()
	if err != nil {
		finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusFailed, nil, errors.New("site task configuration or checkpoint unavailable"))
		return
	}
	state := saved.(quotaHistoryCheckpoint)
	processed, failed := 0, 0
	// 任务取消与租约丢失均需在下一次上游读取前确认，不能只等待心跳异步发现。
	persist := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return model.UpdateSystemTaskState(task.TaskID, runnerID, state)
	}
	process := func(user *model.User, seat string) error {
		if err := persist(); err != nil {
			return err
		}
		itemContext, stop := context.WithTimeout(ctx, 12*time.Second)
		actionErr := collectGPTQuotaUser(itemContext, svc, user, seat)
		stop()
		if err := ctx.Err(); err != nil {
			return err
		}
		// 单条超时也推进游标，否则同一个不可达账号会阻断整个扫描；父任务取消时则保留上一位置。
		if actionErr != nil && !errors.Is(actionErr, gptquota.ErrUnbound) {
			failed++
		}
		processed++
		state = quotaHistoryCheckpoint{UserID: user.Id, SeatID: seat}
		return persist()
	}
	err = persist()
	if err == nil {
		// 先清理一批旧点；即使后面的慢节点耗尽本轮预算，也不让历史保留期依赖扫描能否完成。
		err = model.PruneGPTQuotaHistory(ctx, time.Now())
	}
	var users []model.User
	if err == nil {
		query := model.DB.WithContext(ctx).Select("id", "username", "group", "created_at", "status").Where("status = ?", common.UserStatusEnabled)
		if state.SeatID == "" {
			query = query.Where("id > ?", state.UserID)
		} else {
			query = query.Where("id >= ?", state.UserID)
		}
		err = query.Order("id ASC").Limit(50).Find(&users).Error
	}
	for i := range users {
		if err != nil || processed >= 50 {
			break
		}
		user := &users[i]
		afterSeat := ""
		if user.Id == state.UserID {
			afterSeat = state.SeatID
		}
		var seats []model.GPTHostingGrant
		remaining := 50 - processed
		if os.Getenv("GPT_HOSTING_ENABLED") == "true" {
			err = model.DB.WithContext(ctx).Where("user_id = ? AND seat_id > ?", user.Id, afterSeat).Order("seat_id ASC").Limit(remaining).Find(&seats).Error
			if err != nil {
				break
			}
		}
		if len(seats) == 0 && afterSeat == "" {
			// 没有托管名额的用户仍兼容历史静态映射和管理员开户；已扫过名额后不能回退到默认名额再扫一次。
			err = process(user, "")
		} else {
			for _, seat := range seats {
				if err = process(user, seat.SeatID); err != nil {
					break
				}
			}
		}
		if err == nil && len(seats) < remaining {
			state = quotaHistoryCheckpoint{UserID: user.Id}
			err = persist()
		}
	}
	if err == nil && len(users) == 0 {
		state = quotaHistoryCheckpoint{}
		err = persist()
	}
	status, message := model.SystemTaskStatusSucceeded, ""
	if err != nil || failed > 0 {
		status, message = model.SystemTaskStatusFailed, "site task interrupted or some records require retry"
	}
	_ = model.FinishSystemTask(task.TaskID, runnerID, status, map[string]int{"processed": processed, "failed": failed}, message)
}
