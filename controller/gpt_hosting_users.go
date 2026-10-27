package controller

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/gptquota"
	"github.com/QuantumNous/new-api/service/vm2api"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// hostingUserSummary 统一展示来源，但不包含 Key、官方身份哈希和面板地址。
type hostingUserSummary struct {
	Seats            []hostingSeatView        `json:"seats"`
	SeatCount        int                      `json:"seat_count"`
	ActiveSeats      int                      `json:"active_seats"`
	BoundSeats       int                      `json:"bound_seats"`
	UserID           int                      `json:"user_id"`
	Status           string                   `json:"status"`
	Source           string                   `json:"source"`
	InstanceID       string                   `json:"instance_id,omitempty"`
	InstanceName     string                   `json:"instance_name,omitempty"`
	SlotID           string                   `json:"slot_id,omitempty"`
	Connection       *vm2api.SlotConnection   `json:"connection,omitempty"`
	Grant            *model.GPTHostingGrant   `json:"grant"`
	Binding          *model.GPTHostingBinding `json:"binding"`
	Quota            *gptquota.View           `json:"quota"`
	QuotaUnavailable bool                     `json:"quota_unavailable"`
}

// hostingSeatView 绑定与期限按名额组织，用户看不到机器凭据和官方身份哈希。
type hostingSeatView struct {
	Status       string                   `json:"status"`
	InstanceID   string                   `json:"instance_id,omitempty"`
	InstanceName string                   `json:"instance_name,omitempty"`
	SlotID       string                   `json:"slot_id,omitempty"`
	Connection   *vm2api.SlotConnection   `json:"connection,omitempty"`
	Grant        *model.GPTHostingGrant   `json:"grant"`
	Binding      *model.GPTHostingBinding `json:"binding"`
	BindingID    string                   `json:"binding_id"`
	Valid        bool                     `json:"valid"`
}

// attachHostingConnections 仅供已鉴权管理员摘要使用，按实例批量读出口，不向个人托管接口补充运维信息。
// summaries 已确定账号归属；每台实例最多查询一次，失败保留明确未知状态，不能串用其他实例的同名槽位。
func attachHostingConnections(ctx context.Context, summaries []hostingUserSummary) {
	type instanceConnections struct {
		id, name  string
		slots     map[string]vm2api.SlotConnection
		available bool
	}
	registry, err := onboardingRegistry()
	instances := map[string]*instanceConnections{}
	if err == nil {
		for _, instance := range registry.Instances() {
			instances[instance.ID] = &instanceConnections{id: instance.ID, name: instance.Name}
		}
	}
	wanted := map[string]bool{}
	for _, summary := range summaries {
		if summary.SlotID != "" {
			wanted[summary.InstanceID] = true
		}
		for _, seat := range summary.Seats {
			if seat.SlotID != "" {
				wanted[seat.InstanceID] = true
			}
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	work := make(chan *instanceConnections, len(wanted))
	for id := range wanted {
		if instance := instances[id]; instance != nil {
			work <- instance
		}
	}
	close(work)
	var workers sync.WaitGroup
	for range min(4, len(wanted)) {
		workers.Go(func() {
			for instance := range work {
				client, e := registry.Client(instance.id)
				if e == nil {
					instance.slots, e = client.SlotConnections(ctx)
					instance.available = e == nil
				}
			}
		})
	}
	workers.Wait()
	// 所有远端读取结束后再写摘要，避免多个工作器修改同一用户的不同名额产生数据竞争。
	connectionFor := func(instanceID, slotID string) *vm2api.SlotConnection {
		if slotID == "" {
			return nil
		}
		value := vm2api.SlotConnection{Status: "unavailable"}
		if instance := instances[instanceID]; instance != nil && instance.available {
			if connection, exists := instance.slots[slotID]; exists {
				value = connection
			} else {
				value.Status = "missing"
			}
		}
		return &value
	}
	for i := range summaries {
		summary := &summaries[i]
		summary.Connection = connectionFor(summary.InstanceID, summary.SlotID)
		for j := range summary.Seats {
			seat := &summary.Seats[j]
			seat.Connection = connectionFor(seat.InstanceID, seat.SlotID)
			if instance := instances[seat.InstanceID]; instance != nil {
				seat.InstanceName = instance.name
			}
		}
	}
}

// hostingSeatViews 从鉴权后的用户 ID 读取全部名额，缺失绑定视为数据错误而非空名额。
func hostingSeatViews(userID int) ([]hostingSeatView, error) {
	grants, err := model.ListHostingSeats(userID)
	if err != nil {
		return nil, err
	}
	rows := []hostingSeatView{}
	for i := range grants {
		b, e := model.GetHostingBinding(userID, grants[i].SeatID)
		if e != nil {
			return nil, e
		}
		rows = append(rows, hostingSeatView{Grant: &grants[i], Binding: b, BindingID: b.BindingID, Valid: grants[i].ValidAt(time.Now().Unix()), Status: hostingSeatStatus(&grants[i], b)})
	}
	return rows, nil
}

// hostingSeatStatus 同一套状态用于用户摘要和每个名额，避免有效期限被误显示成账号已经就绪。
func hostingSeatStatus(grant *model.GPTHostingGrant, binding *model.GPTHostingBinding) string {
	now := time.Now().Unix()
	switch {
	case binding.State == "takeover":
		return "takeover"
	case !grant.Enabled || binding.Paused:
		return "disabled"
	case grant.EndsAt != model.HostingPermanentEnd && now >= grant.EndsAt:
		return "expired"
	case now < grant.StartsAt:
		return "scheduled"
	case binding.State != "active":
		return "awaiting_login"
	case grant.EndsAt != model.HostingPermanentEnd && grant.EndsAt-now <= 7*86400:
		return "expiring"
	default:
		return "active"
	}
}

// resolveLegacyBinding 按明确 ID 或完全一致的面板 URL 归一化；多来源冲突不得任选一个。
func resolveLegacyBinding(user *model.User) (*gptquota.Binding, error) {
	cfg, err := configuredQuota()
	if err != nil {
		return nil, err
	}
	registry, err := onboardingRegistry()
	if err != nil {
		return nil, err
	}
	var candidates []gptquota.Binding
	if b, ok := cfg.Users[strconv.Itoa(user.Id)]; ok {
		candidates = append(candidates, b)
	}
	job, err := model.GetVM2APIEnrollmentForUser(user.Id)
	if err == nil {
		candidates = append(candidates, gptquota.Binding{InstanceID: job.InstanceID, Username: job.Username, Group: job.Group, Slot: job.SlotID, Label: job.DisplayName})
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	var result *gptquota.Binding
	for _, b := range candidates {
		if b.Username != user.Username || b.Group != user.Group {
			return nil, model.ErrHostingConflict
		}
		b.InstanceID, err = resolveHostingInstance(cfg, registry, b)
		if err != nil {
			return nil, err
		}
		if result != nil && (result.InstanceID != b.InstanceID || result.Slot != b.Slot) {
			return nil, model.ErrHostingConflict
		}
		result = &b
	}
	if result == nil {
		return nil, nil
	}
	for id, b := range cfg.Users {
		if id == strconv.Itoa(user.Id) {
			continue
		}
		resolved, e := resolveHostingInstance(cfg, registry, b)
		if e != nil {
			return nil, e
		}
		if resolved == result.InstanceID && b.Slot == result.Slot {
			return nil, model.ErrHostingConflict
		}
	}
	var other int64
	if err := model.DB.Model(&model.VM2APIEnrollment{}).Where("instance_id = ? AND slot_id = ? AND user_id <> ?", result.InstanceID, result.Slot, user.Id).Count(&other).Error; err != nil {
		return nil, err
	}
	if other > 0 {
		return nil, model.ErrHostingConflict
	}
	return result, nil
}

// resolveHostingInstance 不把缺省实例猜成搬瓦工或 CloudCone；别名只能由部署配置证明。
func resolveHostingInstance(cfg gptquota.Config, registry *vm2api.Registry, b gptquota.Binding) (string, error) {
	id := b.InstanceID
	if id == "" {
		id = gptquota.DefaultInstanceID
	}
	panel := cfg.Instances[id].PanelURL
	if id == gptquota.DefaultInstanceID && cfg.PanelURL != "" {
		panel = cfg.PanelURL
	}
	result := ""
	for _, instance := range registry.Instances() {
		expected := strings.TrimRight(instance.BaseURL, "/") + "/api/panel"
		if id == instance.ID || strings.TrimRight(panel, "/") == expected {
			if result != "" || (id == instance.ID && panel != "" && strings.TrimRight(panel, "/") != expected) {
				return "", model.ErrHostingConflict
			}
			result = instance.ID
		}
	}
	if result == "" {
		return "", model.ErrHostingConflict
	}
	return result, nil
}

// hostingSummary 查询统一归属；已有托管记录始终优先，解绑、失败或到期不能回落到旧配置。
func hostingSummary(user *model.User) hostingUserSummary {
	view := hostingUserSummary{UserID: user.Id, Status: "unopened", Source: "none"}
	seats, seatErr := hostingSeatViews(user.Id)
	if seatErr != nil {
		view.Status = "unavailable"
		return view
	}
	view.Seats, view.SeatCount = seats, len(seats)
	for i := range seats {
		seat := &seats[i]
		// 实例与槽位仅加在管理员摘要，用户自助列表不公开运维拓扑。
		seat.InstanceID, seat.SlotID = seat.Binding.InstanceID, seat.Binding.SlotID
		if seat.Valid {
			view.ActiveSeats++
		}
		if seat.Binding.AccountLabel != "" {
			view.BoundSeats++
		}
	}
	if user.Role != common.RoleCommonUser {
		view.Status = "restricted"
		return view
	}
	grant, err := model.GetHostingGrant(user.Id)
	if err == nil {
		view.Grant, view.Source = grant, "hosting"
		b, e := model.GetHostingBinding(user.Id)
		if e != nil {
			view.Status = "conflict"
			return view
		}
		view.Binding, view.InstanceID, view.SlotID = b, b.InstanceID, b.SlotID
		view.Status = hostingSeatStatus(grant, b)
	} else if errors.Is(err, gorm.ErrRecordNotFound) {
		b, e := resolveLegacyBinding(user)
		if e != nil {
			view.Status = "conflict"
			return view
		}
		if b != nil {
			view.Source, view.Status, view.InstanceID, view.SlotID = "legacy", "undated", b.InstanceID, b.Slot
		}
	} else {
		view.Status = "unavailable"
	}
	if registry, e := onboardingRegistry(); e == nil {
		for _, instance := range registry.Instances() {
			if instance.ID == view.InstanceID {
				view.InstanceName = instance.Name
			}
		}
	}
	return view
}

// GetGPTHostingUsers 为当前分页汇总账号、缓存额度和配置连接；最多 100 个用户、4 个工作器，额度与连接各限 3 秒。
// 这里只读 vm2api 保存的额度，不触发官方探测；错误保留为未知，不能当成未绑定或 0%。
func GetGPTHostingUsers(c *gin.Context) {
	if _, ok := hostingActor(c, false); !ok {
		return
	}
	if c.GetInt("role") < common.RoleAdminUser {
		c.AbortWithStatus(403)
		return
	}
	parts := strings.Split(c.Query("ids"), ",")
	if len(parts) > 100 || len(parts) == 0 {
		c.AbortWithStatus(400)
		return
	}
	ids := []int{}
	for _, part := range parts {
		id, err := strconv.Atoi(part)
		if err != nil || id <= 0 || strconv.Itoa(id) != part {
			c.AbortWithStatus(400)
			return
		}
		ids = append(ids, id)
	}
	var users []model.User
	if err := model.DB.Where("id IN ?", ids).Find(&users).Error; err != nil {
		hostingError(c, err)
		return
	}
	result := make([]hostingUserSummary, len(users))
	svc, svcErr := quotaService()
	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	work := make(chan int, len(users))
	for i := range users {
		work <- i
	}
	close(work)
	for range min(4, len(users)) {
		wg.Go(func() {
			for i := range work {
				u := &users[i]

				v := hostingSummary(u)
				if v.Source != "none" && v.Status != "conflict" && v.SlotID != "" {
					if svcErr == nil {
						quota, e := linkedGPTQuota(ctx, svc, u.Id, u.Username, u.Group, false)
						if e == nil {
							v.Quota = &quota
						} else {
							v.QuotaUnavailable = true
						}
					} else {
						v.QuotaUnavailable = true
					}
				}
				result[i] = v
			}
		})
	}
	wg.Wait()
	attachHostingConnections(c.Request.Context(), result)
	c.JSON(200, gin.H{"success": true, "data": result})
}

// AdjustHostingUserWallet 对统一详情中的余额变更要求原因与当前会话的二次验证，复用已有账务事务。
func AdjustHostingUserWallet(c *gin.Context) {
	if _, ok := hostingActor(c, false); !ok {
		return
	}
	if c.GetInt("role") < common.RoleAdminUser {
		c.AbortWithStatus(403)
		return
	}
	var input ManageRequest
	if !onboardingBody(c, &input) {
		return
	}
	input.Reason = strings.TrimSpace(input.Reason)
	if input.Action != "add_quota" || input.Reason == "" || len([]rune(input.Reason)) > 255 {
		common.ApiErrorT(c, "Invalid parameters")
		return
	}
	raw, _ := common.Marshal(service.AdminUserContext{UserID: input.Id})
	if middleware.RequireSecurityProof(c, service.VerificationOperation{Scope: service.VerificationScopeAdminUserUpdate, Context: raw}) == nil {
		return
	}
	manageUserQuota(c, input)
}

// recordHostingUserAudit 同时记录操作者审计和目标用户视图；后者仍保留真实操作者角色，沿用日志读取权限。
func recordHostingUserAudit(c *gin.Context, userID int, action string, params map[string]any) {
	recordManageAuditFor(c, userID, action, params)
	operation := model.AuditOperation{Action: action, Params: params}
	model.RecordLogWithAdminInfo(userID, model.LogTypeManage, action, auditOperatorInfo(c), &operation, c)
}
