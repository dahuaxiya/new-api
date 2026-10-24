package controller

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/vm2api"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// hostingActor 限制为真实门户会话，管理员能力仍由 RootAuth 和本地角色校验共同保护。
func hostingActor(c *gin.Context, admin bool) (int, bool) {
	c.Header("Cache-Control", "no-store, private")
	id := c.GetInt("id")
	if os.Getenv("GPT_HOSTING_ENABLED") != "true" {
		c.JSON(503, gin.H{"success": false, "message": "GPT hosting is not enabled"})
		return 0, false
	}
	if id <= 0 || c.GetString("session_id") == "" || (c.GetHeader("New-Api-User") != "" && c.GetHeader("New-Api-User") != strconv.Itoa(id)) {
		c.JSON(401, gin.H{"success": false, "message": "Authentication required"})
		return 0, false
	}
	if admin && c.GetInt("role") != common.RoleRootUser {
		c.JSON(403, gin.H{"success": false, "message": "Access denied"})
		return 0, false
	}
	return id, true
}

// hostingError 仅输出固定诊断码，OAuth 和数据库异常的原始正文不能进入用户页面。
func hostingError(c *gin.Context, err error) {
	code, status := "hosting_unavailable", 503
	if errors.Is(err, model.ErrHostingConflict) {
		code, status = "hosting_conflict", 409
	}
	if errors.Is(err, model.ErrHostingUnavailable) {
		code, status = "hosting_expired", 403
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		code, status = "hosting_not_found", 404
	}
	var upstream *vm2api.Error
	if errors.As(err, &upstream) {
		switch upstream.Code {
		case "oauth_session_expired", "oauth_session_mismatch", "invalid_oauth_callback", "slot_not_empty", "slot_unavailable", "exit_unavailable", "account_changed", "personal_account_required", "identity_invalid", "identity_unavailable", "hosting_busy", "cleanup_pending", "hosting_conflict":
			code, status = upstream.Code, 409
		}
	}
	c.JSON(status, gin.H{"success": false, "code": code, "message": "GPT hosting operation could not be completed"})
}

// hostingProof 复用门户的密码/MFA/Passkey 二次验证，绑定动作与当前绑定编号并一次性消费。
func hostingProof(c *gin.Context, action, binding string) bool {
	raw, _ := common.Marshal(map[string]string{"action": action, "binding_id": binding})
	return middleware.RequireSecurityProof(c, service.VerificationOperation{Scope: service.VerificationScopeGPTHosting, Context: raw}) != nil
}

// GetGPTHostingSelf 只返回本人权益和脱敏账号；不暴露实例、上游 Key 或官方身份哈希。
func GetGPTHostingSelf(c *gin.Context) {
	id, ok := hostingActor(c, false)
	if !ok {
		return
	}
	g, err := model.GetHostingGrant(id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(200, gin.H{"success": true, "data": gin.H{"grant": nil, "binding": nil}})
		return
	}
	if err != nil {
		hostingError(c, err)
		return
	}
	b, err := model.GetHostingBinding(id)
	if err != nil {
		hostingError(c, err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": gin.H{"grant": g, "valid": g.ValidAt(time.Now().Unix()), "binding": b, "binding_id": b.BindingID}})
}

// GetGPTHostingAdmin 组合中央占用与远端空槽视图；单实例故障单独展示，不能把错误结果视为空槽。
func GetGPTHostingAdmin(c *gin.Context) {
	_, ok := hostingActor(c, true)
	if !ok {
		return
	}
	registry, err := onboardingRegistry()
	if err != nil {
		hostingError(c, err)
		return
	}
	inventory, err := model.ListHostingInventory()
	if err != nil {
		hostingError(c, err)
		return
	}
	var grants []model.GPTHostingGrant
	if err = model.DB.Order("user_id").Limit(500).Find(&grants).Error; err != nil {
		hostingError(c, err)
		return
	}
	instances := []gin.H{}
	for _, instance := range registry.Instances() {
		client, _ := registry.Client(instance.ID)
		ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
		slots, e := client.HostedSlots(ctx)
		cancel()
		rows := []gin.H{}
		for _, slot := range slots {
			state := "unmanaged"
			if slot.Hosting != nil {
				state = slot.Hosting.State
			}
			row := gin.H{"id": slot.ID, "name": slot.Name, "empty": slot.Empty, "exit_ready": slot.ExitReady, "managed": slot.Managed, "state": state, "enabled": false}
			for _, inv := range inventory {
				if inv.InstanceID == instance.ID && inv.SlotID == slot.ID {
					row["inventory_id"] = inv.ID
					row["enabled"] = inv.Enabled
					break
				}
			}
			rows = append(rows, row)
		}
		instances = append(instances, gin.H{"id": instance.ID, "name": instance.Name, "available": e == nil, "slots": rows})
	}
	c.JSON(200, gin.H{"success": true, "data": gin.H{"grants": grants, "instances": instances}})
}

// PutGPTHostingGrant 只向已有普通用户发放权益；旧静态账号不自动转换，以免撤销工作中的客户端。
func PutGPTHostingGrant(c *gin.Context) {
	actor, ok := hostingActor(c, true)
	if !ok {
		return
	}
	var input model.GPTHostingGrant
	if !onboardingBody(c, &input) {
		return
	}
	input.ActorID = actor
	raw, _ := common.Marshal(service.AdminUserContext{UserID: input.UserID})
	if middleware.RequireSecurityProof(c, service.VerificationOperation{Scope: service.VerificationScopeAdminUserUpdate, Context: raw}) == nil {
		return
	}
	cfg, err := configuredQuota()
	if err != nil {
		hostingError(c, err)
		return
	}
	_, grantErr := model.GetHostingGrant(input.UserID)
	if _, legacy := cfg.Users[strconv.Itoa(input.UserID)]; legacy && grantErr != nil {
		hostingError(c, model.ErrHostingConflict)
		return
	}
	g, err := model.PutHostingGrant(input)
	if err != nil {
		hostingError(c, err)
		return
	}
	// 中央请求校验立即生效；远端策略失败时保留待同步状态并明确反馈，后台会重试。
	syncErr := syncHostingPolicy(c.Request.Context(), g.UserID)
	recordHostingUserAudit(c, g.UserID, "hosting.grant", map[string]any{"starts_at": g.StartsAt, "ends_at": g.EndsAt, "enabled": g.Enabled, "retention_days": g.RetentionDays, "note": g.Note, "revision": g.Revision})
	c.JSON(200, gin.H{"success": true, "data": g, "sync_pending": syncErr != nil})
}

// PutGPTHostingInventory 先隔离远端空槽再发布可分配清单，失败不会将未隔离槽位暴露给用户。
func PutGPTHostingInventory(c *gin.Context) {
	_, ok := hostingActor(c, true)
	if !ok {
		return
	}
	var input struct {
		InstanceID string `json:"instance_id"`
		SlotID     string `json:"slot_id"`
		Enabled    bool   `json:"enabled"`
	}
	if !onboardingBody(c, &input) {
		return
	}
	registry, err := onboardingRegistry()
	if err != nil {
		hostingError(c, err)
		return
	}
	client, err := registry.Client(input.InstanceID)
	if err != nil {
		hostingError(c, err)
		return
	}
	legacy, err := staticSlotAssigned(input.InstanceID, input.SlotID, client)
	if err != nil || legacy {
		hostingError(c, model.ErrHostingConflict)
		return
	}
	assigned, err := model.IsVM2APISlotAssigned(input.InstanceID, input.SlotID)
	if err != nil || assigned {
		hostingError(c, model.ErrHostingConflict)
		return
	}
	if input.Enabled {
		if _, err = client.HostingAction(c.Request.Context(), input.SlotID, "enroll", gin.H{}); err != nil {
			hostingError(c, err)
			return
		}
	}
	row, err := model.PutHostingInventory(input.InstanceID, input.SlotID, input.Enabled)
	if err != nil {
		hostingError(c, err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": row})
}

// reserveHosting 为一个有效名额挑选预置空槽；独占写入成功后才触发任何远端授权动作。
func reserveHosting(ctx context.Context, userID int) (*model.GPTHostingBinding, error) {
	b, err := model.GetHostingBinding(userID)
	if err != nil {
		return nil, err
	}
	if b.State != "unbound" {
		return b, nil
	}
	inventory, err := model.ListHostingInventory()
	if err != nil {
		return nil, err
	}
	registry, err := onboardingRegistry()
	if err != nil {
		return nil, err
	}
	for _, inv := range inventory {
		if !inv.Enabled {
			continue
		}
		client, e := registry.Client(inv.InstanceID)
		if e != nil {
			continue
		}
		slots, e := client.HostedSlots(ctx)
		if e != nil {
			continue
		}
		for _, slot := range slots {
			if slot.ID != inv.SlotID || !slot.Empty || !slot.ExitReady || slot.Hosting == nil || slot.Hosting.State != "inventory" {
				continue
			}
			reserved, e := model.ReserveHostingSlot(userID, inv.ID)
			if e == nil {
				return reserved, nil
			}
			current, readErr := model.GetHostingBinding(userID)
			if readErr == nil && current.State != "unbound" {
				return current, nil
			}
		}
	}
	return nil, &vm2api.Error{Code: "slot_unavailable", HTTPStatus: 409}
}

// StartGPTHostingLogin 自助连接或同账号重登；客户端不能指定实例、槽位或其他用户。
func StartGPTHostingLogin(c *gin.Context) {
	id, ok := hostingActor(c, false)
	if !ok {
		return
	}
	var input struct{}
	if !onboardingBody(c, &input) {
		return
	}
	g, err := model.GetHostingGrant(id)
	if err != nil {
		hostingError(c, err)
		return
	}
	if !g.ValidAt(time.Now().Unix()) {
		hostingError(c, model.ErrHostingUnavailable)
		return
	}
	before, err := model.GetHostingBinding(id)
	if err != nil {
		hostingError(c, err)
		return
	}
	if before.State == "takeover" {
		hostingError(c, model.ErrHostingConflict)
		return
	}
	if !hostingProof(c, "login", before.BindingID) {
		return
	}
	if _, err = reserveHosting(c.Request.Context(), id); err != nil {
		hostingError(c, err)
		return
	}
	b, lease, err := model.ClaimHostingOperation(id)
	if err != nil {
		hostingError(c, err)
		return
	}
	defer model.ReleaseHostingOperation(id, lease)
	if b.State == "cleaning" {
		hostingError(c, model.ErrHostingConflict)
		return
	}
	registry, err := onboardingRegistry()
	if err != nil {
		hostingError(c, err)
		return
	}
	client, err := registry.Client(b.InstanceID)
	if err != nil {
		hostingError(c, err)
		return
	}
	if _, err = client.HostingAction(c.Request.Context(), b.SlotID, "reserve", gin.H{"binding_id": b.BindingID, "generation": b.Generation}); err != nil {
		hostingError(c, err)
		return
	}
	// 先禁止门户调用再替换授权；任一步失败仍保持暂停状态供用户重试或解绑。
	if err = model.UpdateHostingOperation(id, lease, map[string]any{"state": "authorizing"}); err != nil {
		hostingError(c, err)
		return
	}
	auth, err := registry.StartHostedOAuth(c.Request.Context(), id, c.GetString("session_id"), b.InstanceID, b.SlotID, b.BindingID, b.Generation)
	if err != nil {
		hostingError(c, err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": auth})
}

// finishHosting 根据远端暂存的已验证身份抢占中央唯一键，成功后才激活并创建用户专属渠道。
func finishHosting(ctx context.Context, b *model.GPTHostingBinding, lease string, prepared vm2api.HostedState) error {
	// root 沿用的历史标识不能被登录重试接口当成新 OAuth 证明。
	if prepared.IdentitySource != "official" || prepared.BindingID != b.BindingID || prepared.VMID != b.SlotID || prepared.State != "prepared" {
		return model.ErrHostingConflict
	}
	// 对历史账号只读去重：不能把旧客户使用中的账号再次交给自助用户。
	registryForIdentity, e := onboardingRegistry()
	if e != nil {
		return e
	}
	for _, instance := range registryForIdentity.Instances() {
		client, e := registryForIdentity.Client(instance.ID)
		if e != nil {
			return e
		}
		slots, e := client.HostedSlots(ctx)
		if e != nil {
			return e
		} // 实例不可达时无法确认唯一性，暂不激活。
		for _, slot := range slots {
			if slot.Managed {
				continue
			}
			for _, hash := range slot.LegacyAccountHashes {
				if hash == prepared.AccountHash {
					return model.ErrHostingConflict
				}
			}
			for _, hash := range slot.LegacySubjectHashes {
				if hash == prepared.SubjectHash {
					return model.ErrHostingConflict
				}
			}
		}
	}
	if err := model.ClaimHostingIdentity(b.UserID, lease, prepared.AccountHash, prepared.SubjectHash, prepared.Label, prepared.Plan); err != nil {
		return err
	}
	g, err := model.GetHostingGrant(b.UserID)
	if err != nil {
		return err
	}
	if !g.ValidAt(time.Now().Unix()) {
		return model.ErrHostingUnavailable
	}
	registry, err := onboardingRegistry()
	if err != nil {
		return err
	}
	client, err := registry.Client(b.InstanceID)
	if err != nil {
		return err
	}
	active, err := client.HostingAction(ctx, b.SlotID, "activate", gin.H{"binding_id": b.BindingID, "generation": b.Generation, "account_hash": prepared.AccountHash, "subject_hash": prepared.SubjectHash, "starts_at": g.StartsAt, "ends_at": g.EndsAt})
	if err != nil {
		return err
	}
	if active.BindingID != b.BindingID || active.AccountHash != prepared.AccountHash || active.SubjectHash != prepared.SubjectHash {
		return model.ErrHostingConflict
	}
	err = model.CompleteHostingChannel(b.UserID, lease, client.Config().BaseURL, strings.Join(client.Config().Models, ","), active.Key, active.KeyID, active.PoolID)
	if err == nil {
		model.InitChannelCache()
	}
	return err
}

// FinishGPTHostingLogin 消费当前会话的回调；网络响应丢失时 Retry 接口通过持久化远端状态恢复。
func FinishGPTHostingLogin(c *gin.Context) {
	id, ok := hostingActor(c, false)
	if !ok {
		return
	}
	var input struct {
		SessionID   string `json:"session_id"`
		CallbackURL string `json:"callback_url"`
	}
	if !onboardingBody(c, &input) {
		return
	}
	g, err := model.GetHostingGrant(id)
	if err != nil {
		hostingError(c, err)
		return
	}
	if !g.ValidAt(time.Now().Unix()) {
		hostingError(c, model.ErrHostingUnavailable)
		return
	}
	b, lease, err := model.ClaimHostingOperation(id)
	if err != nil {
		hostingError(c, err)
		return
	}
	defer model.ReleaseHostingOperation(id, lease)
	if b.State != "authorizing" {
		hostingError(c, model.ErrHostingConflict)
		return
	}
	registry, err := onboardingRegistry()
	if err != nil {
		hostingError(c, err)
		return
	}
	prepared, err := registry.PrepareHostedOAuth(c.Request.Context(), id, c.GetString("session_id"), b.InstanceID, b.SlotID, b.BindingID, input.SessionID, input.CallbackURL, b.Generation)
	if err == nil {
		err = finishHosting(c.Request.Context(), b, lease, prepared)
	}
	if err != nil {
		hostingError(c, err)
		return
	}
	GetGPTHostingSelf(c)
}

// RetryGPTHostingLogin 不重复兑换授权码；只接续同一绑定的暂存身份或已激活远端，处理响应丢失。
func RetryGPTHostingLogin(c *gin.Context) {
	id, ok := hostingActor(c, false)
	if !ok {
		return
	}
	var input struct{}
	if !onboardingBody(c, &input) {
		return
	}
	b, lease, err := model.ClaimHostingOperation(id)
	if err != nil {
		hostingError(c, err)
		return
	}
	defer model.ReleaseHostingOperation(id, lease)
	if b.State == "takeover" {
		hostingError(c, model.ErrHostingConflict)
		return
	}
	if !hostingProof(c, "login", b.BindingID) {
		return
	}
	registry, err := onboardingRegistry()
	if err != nil {
		hostingError(c, err)
		return
	}
	client, err := registry.Client(b.InstanceID)
	if err != nil {
		hostingError(c, err)
		return
	}
	slots, err := client.HostedSlots(c.Request.Context())
	if err != nil {
		hostingError(c, err)
		return
	}
	for _, slot := range slots {
		if slot.ID == b.SlotID && slot.Hosting != nil {
			state := *slot.Hosting
			// 远端激活成功但门户提交失败：重新取得同一上游 Key，不创建第二把。
			if state.State == "active" && b.State == "prepared" {
				state.State = "prepared"
			}
			if err = finishHosting(c.Request.Context(), b, lease, state); err != nil {
				hostingError(c, err)
				return
			}
			GetGPTHostingSelf(c)
			return
		}
	}
	hostingError(c, model.ErrHostingConflict)
}

// syncHostingPolicy 用最新权益更新远端；策略短暂不同步时门户仍实时拒绝过期请求。
func syncHostingPolicy(ctx context.Context, userID int) error {
	b, lease, err := model.ClaimHostingOperation(userID)
	if err != nil {
		return err
	}
	defer model.ReleaseHostingOperation(userID, lease)
	if b.State != "active" {
		return nil
	}
	g, err := model.GetHostingGrant(userID)
	if err != nil {
		return err
	}
	registry, err := onboardingRegistry()
	if err != nil {
		return err
	}
	client, err := registry.Client(b.InstanceID)
	if err != nil {
		return err
	}
	_, err = client.HostingAction(ctx, b.SlotID, "policy", gin.H{"binding_id": b.BindingID, "generation": b.Generation, "enabled": g.ValidAt(time.Now().Unix()) && !b.Paused, "starts_at": g.StartsAt, "ends_at": g.EndsAt})
	return err
}

// SetGPTHostingPaused 只修改当前用户的暂停状态，到期或停用的名额不能恢复。
func SetGPTHostingPaused(c *gin.Context) {
	id, ok := hostingActor(c, false)
	if !ok {
		return
	}
	var input struct {
		Paused bool `json:"paused"`
	}
	if !onboardingBody(c, &input) {
		return
	}
	b, lease, err := model.ClaimHostingOperation(id)
	if err != nil {
		hostingError(c, err)
		return
	}
	if b.State != "active" {
		model.ReleaseHostingOperation(id, lease)
		hostingError(c, model.ErrHostingConflict)
		return
	}
	g, e := model.GetHostingGrant(id)
	if e != nil || (!input.Paused && !g.ValidAt(time.Now().Unix())) {
		model.ReleaseHostingOperation(id, lease)
		hostingError(c, model.ErrHostingUnavailable)
		return
	}
	err = model.UpdateHostingOperation(id, lease, map[string]any{"paused": input.Paused})
	model.ReleaseHostingOperation(id, lease)
	if err == nil {
		err = syncHostingPolicy(c.Request.Context(), id)
	}
	if err != nil {
		hostingError(c, err)
		return
	}
	GetGPTHostingSelf(c)
}

// cleanHosting 幂等清理已获用户授权的绑定；必须远端确认后才释放中央身份与库存。
func cleanHosting(ctx context.Context, userID int) error {
	b, lease, err := model.ClaimHostingOperation(userID)
	if err != nil {
		return err
	}
	defer model.ReleaseHostingOperation(userID, lease)
	if b.State != "cleaning" {
		return model.ErrHostingConflict
	}
	registry, err := onboardingRegistry()
	if err != nil {
		return err
	}
	client, err := registry.Client(b.InstanceID)
	if err != nil {
		return err
	}
	remote, err := client.HostingAction(ctx, b.SlotID, "release", gin.H{"binding_id": b.BindingID, "generation": b.Generation})
	if err != nil {
		return err
	}
	if remote.State != "inventory" || remote.BindingID != "" {
		return model.ErrHostingConflict
	}
	if err := model.ClearHostingBinding(userID, lease); err != nil {
		return err
	}
	model.InitChannelCache()
	return nil
}

// UnbindGPTHosting 要求二次身份确认，先禁止新调用，失败时保留 cleaning 并由后台重试。
func UnbindGPTHosting(c *gin.Context) {
	id, ok := hostingActor(c, false)
	if !ok {
		return
	}
	var input struct{}
	if !onboardingBody(c, &input) {
		return
	}
	b, lease, err := model.ClaimHostingOperation(id)
	if err != nil {
		hostingError(c, err)
		return
	}
	defer model.ReleaseHostingOperation(id, lease)
	if b.State == "takeover" {
		hostingError(c, model.ErrHostingConflict)
		return
	}
	if !hostingProof(c, "unbind", b.BindingID) {
		return
	}
	if b.State == "unbound" {
		GetGPTHostingSelf(c)
		return
	}
	if err = model.UpdateHostingOperation(id, lease, map[string]any{"state": "cleaning"}); err != nil {
		hostingError(c, err)
		return
	}
	model.ReleaseHostingOperation(id, lease)
	err = cleanHosting(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusAccepted, gin.H{"success": true, "data": gin.H{"state": "cleaning"}})
		return
	}
	GetGPTHostingSelf(c)
}

// StartGPTHostingMaintenance 在后台重试已授权清理与权益同步，不自动创建槽位或释放未知状态账号。
func StartGPTHostingMaintenance() {
	if os.Getenv("GPT_HOSTING_ENABLED") != "true" {
		return
	}
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			var rows []model.GPTHostingBinding
			if model.DB.Where("state <> ?", "unbound").Limit(500).Find(&rows).Error != nil {
				continue
			}
			for _, b := range rows {
				if expired, err := model.ExpireHostingBinding(b.UserID, time.Now().Unix()); err == nil && expired {
					b.State = "cleaning"
				}
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				if b.State == "cleaning" {
					_ = cleanHosting(ctx, b.UserID)
				} else if b.State == "active" {
					_ = syncHostingPolicy(ctx, b.UserID)
				}
				cancel()
			}
		}
	}()
}
