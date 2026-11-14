package controller

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
)

const (
	siteTaskHosting        = "hosting_maintenance"
	siteTaskHostingPayment = "hosting_payment_reconcile"
	siteTaskWalletPayment  = "wallet_payment_reconcile"
)

// siteTaskCheckpoint 只保存下一轮扫描起点，不保存凭据、账号内容或支付查单响应。
type siteTaskCheckpoint struct {
	Cursor string `json:"cursor"`
}

// siteTaskHandler 复用系统任务的数据库租约和历史；外部操作仍遵循原业务的幂等与归属检查。
type siteTaskHandler struct{ kind string }

// registerSiteTasks 按进程站点登记处理器，避免托管站领取钱包任务或中转站接触客户账号。
func registerSiteTasks() {
	if common.IsRetailSite() {
		service.RegisterSystemTaskHandler(siteTaskHandler{kind: siteTaskWalletPayment})
		return
	}
	service.RegisterSystemTaskHandler(siteTaskHandler{kind: siteTaskHosting})
	service.RegisterSystemTaskHandler(siteTaskHandler{kind: siteTaskHostingPayment})
}

// Type 返回固定任务种类，既是调度键也是跨进程互斥键。
func (h siteTaskHandler) Type() string { return h.kind }

// Enabled 在每次调度和执行时复核站点与私有配置；停售后仍补查已产生的订单。
func (h siteTaskHandler) Enabled() bool {
	if h.kind == siteTaskWalletPayment {
		if !common.IsRetailSite() {
			return false
		}
		client, err := walletAlipayClient()
		return err == nil && client != nil && client.Production()
	}
	if common.IsRetailSite() || os.Getenv("GPT_HOSTING_ENABLED") != "true" {
		return false
	}
	if h.kind == siteTaskHosting {
		return true
	}
	if h.kind == siteTaskHostingPayment {
		client, err := hostingAlipayClient()
		return err == nil && client != nil && client.Production()
	}
	return false
}

// Interval 保留原有扫描节奏；一次任务完成后至少等待此间隔再调度。
func (h siteTaskHandler) Interval() time.Duration {
	if h.kind == siteTaskWalletPayment {
		return time.Minute
	}
	return 30 * time.Second
}

// NewPayload 不接受外部参数；扫描范围由当前站点配置和数据库决定。
func (h siteTaskHandler) NewPayload() any { return nil }

// ResumeState 从上一任务恢复已落库的游标，包括失败或租约过期的任务；格式错误时停止调度。
func (h siteTaskHandler) ResumeState(previous *model.SystemTask) (any, error) {
	state := siteTaskCheckpoint{}
	if previous != nil {
		if err := previous.DecodeState(&state); err != nil {
			return nil, err
		}
	}
	if len(state.Cursor) > 128 {
		return nil, errors.New("invalid site task checkpoint")
	}
	return state, nil
}

// Run 执行有界的一页扫描，每条记录后保存进度；关闭进程或失去租约后停止下一次外部操作。
// 租约不是外部系统的全局事务：崩溃窗口可能重试同一条记录，入账和账号操作仍由原有幂等约束保护。
func (h siteTaskHandler) Run(parent context.Context, task *model.SystemTask, runnerID string) {
	ctx, cancel := context.WithTimeout(parent, time.Minute)
	defer cancel()
	state := siteTaskCheckpoint{}
	if !h.Enabled() || task.DecodeState(&state) != nil || len(state.Cursor) > 128 {
		finishSystemTaskHandler(task, runnerID, model.SystemTaskStatusFailed, nil, errors.New("site task configuration or checkpoint unavailable"))
		return
	}
	processed, failed := 0, 0
	// 同步检查租约再发起下一条外部操作，不能只依赖后台心跳最终发现锁丢失。
	process := func(cursor string, action func(context.Context) error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := model.UpdateSystemTaskState(task.TaskID, runnerID, state); err != nil {
			return err
		}
		// 每条记录最多占用 20 秒，避免一个持续超时的上游耗尽整页预算而永久卡住游标。
		itemContext, stopItem := context.WithTimeout(ctx, 20*time.Second)
		actionErr := action(itemContext)
		stopItem()
		if err := ctx.Err(); err != nil {
			return err
		}
		// 单个坏账号或查单失败不能永久卡住后续记录；下一次完整扫描会再次尝试。
		if actionErr != nil {
			failed++
		}
		processed++
		state.Cursor = cursor
		return model.UpdateSystemTaskState(task.TaskID, runnerID, state)
	}
	var runErr error
	var count int
	switch h.kind {
	case siteTaskHosting:
		var rows []model.GPTHostingBinding
		runErr = model.DB.WithContext(ctx).Where("state <> ? AND seat_id > ?", "unbound", state.Cursor).Order("seat_id").Limit(50).Find(&rows).Error
		count = len(rows)
		for _, binding := range rows {
			if runErr != nil {
				break
			}
			runErr = process(binding.SeatID, func(ctx context.Context) error {
				expired, err := model.ExpireHostingBinding(binding.UserID, time.Now().Unix(), binding.SeatID)
				if err != nil {
					return err
				}
				if expired || binding.State == "cleaning" {
					return cleanHosting(ctx, binding.UserID, binding.SeatID)
				}
				if binding.State == "active" {
					return syncHostingPolicy(ctx, binding.UserID, binding.SeatID)
				}
				return nil
			})
		}
	case siteTaskHostingPayment:
		client, _ := hostingAlipayClient()
		var orders []model.HostingOrder
		orders, runErr = model.PendingHostingPayments(client.Merchant(), state.Cursor, time.Now().Unix())
		count = len(orders)
		for i := range orders {
			if runErr != nil {
				break
			}
			runErr = process(orders[i].ID, func(ctx context.Context) error { return reconcileHostingAlipayOrder(ctx, client, &orders[i]) })
		}
	case siteTaskWalletPayment:
		client, _ := walletAlipayClient()
		var orders []model.WalletPayment
		orders, runErr = model.PendingWalletPayments(client.Merchant(), state.Cursor)
		count = len(orders)
		for i := range orders {
			if runErr != nil {
				break
			}
			runErr = process(orders[i].OrderID, func(ctx context.Context) error { return reconcileWalletPayment(ctx, client, &orders[i]) })
		}
	}
	if runErr == nil && count == 0 {
		state.Cursor = ""
		runErr = model.UpdateSystemTaskState(task.TaskID, runnerID, state)
	}
	status, message := model.SystemTaskStatusSucceeded, ""
	if runErr != nil || failed > 0 {
		// DB 与供应商错误不进入系统任务页面，那里只记录固定失败原因及处理数量。
		status, message = model.SystemTaskStatusFailed, "site task interrupted or some records require retry"
	}
	_ = model.FinishSystemTask(task.TaskID, runnerID, status, map[string]int{"processed": processed, "failed": failed}, message)
}
