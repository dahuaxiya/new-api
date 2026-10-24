package controller

import (
	"context"
	"errors"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// checkHostingTakeover 验证中央路由、远端官方身份及所有实例的重复账号；失败不写入任何服务状态。
func checkHostingTakeover(ctx context.Context, userID int) (*model.HostingTakeover, error) {
	user, err := model.GetUserById(userID, false)
	if err != nil {
		return nil, err
	}
	source, err := resolveLegacyBinding(user)
	if err != nil {
		return nil, err
	}
	if source == nil {
		return nil, model.ErrHostingConflict
	}
	channel, snapshot, err := model.LegacyHostingChannel(userID)
	if err != nil {
		return nil, err
	}
	registry, err := onboardingRegistry()
	if err != nil {
		return nil, err
	}
	client, err := registry.Client(source.InstanceID)
	if err != nil {
		return nil, err
	}
	if strings.TrimRight(*channel.BaseURL, "/") != strings.TrimRight(client.Config().BaseURL, "/") {
		return nil, model.ErrHostingConflict
	}
	checked, err := client.HostingAction(ctx, source.Slot, "takeover-check", gin.H{"key": channel.Key})
	if err != nil {
		return nil, err
	}
	if checked.VMID != source.Slot || checked.State != "checked" {
		return nil, model.ErrHostingConflict
	}
	for _, instance := range registry.Instances() {
		other, e := registry.Client(instance.ID)
		if e != nil {
			return nil, e
		}
		slots, e := other.HostedSlots(ctx)
		if e != nil {
			return nil, e
		}
		for _, slot := range slots {
			if instance.ID == source.InstanceID && slot.ID == source.Slot {
				continue
			}
			if slot.Hosting != nil && (slot.Hosting.AccountHash == checked.AccountHash || slot.Hosting.SubjectHash == checked.SubjectHash) {
				return nil, model.ErrHostingConflict
			}
			for _, hash := range slot.LegacyAccountHashes {
				if hash == checked.AccountHash {
					return nil, model.ErrHostingConflict
				}
			}
			for _, hash := range slot.LegacySubjectHashes {
				if hash == checked.SubjectHash {
					return nil, model.ErrHostingConflict
				}
			}
		}
	}
	return &model.HostingTakeover{InstanceID: source.InstanceID, SlotID: source.Slot, ChannelID: channel.Id, Snapshot: snapshot, AccountHash: checked.AccountHash, SubjectHash: checked.SubjectHash, Label: checked.Label, Plan: checked.Plan, KeyID: checked.KeyID, PoolID: checked.PoolID}, nil
}

// CheckGPTHostingTakeover 返回脱敏预览；Key 和身份哈希只在固定服务端之间传输。
func CheckGPTHostingTakeover(c *gin.Context) {
	if _, ok := hostingActor(c, true); !ok {
		return
	}
	var input struct {
		UserID int `json:"user_id"`
	}
	if !onboardingBody(c, &input) {
		return
	}
	result, err := checkHostingTakeover(c.Request.Context(), input.UserID)
	if err != nil {
		hostingError(c, err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": gin.H{"account_label": result.Label, "plan": result.Plan, "instance_id": result.InstanceID, "slot_id": result.SlotID, "channel_id": result.ChannelID}})
}

// TakeoverGPTHosting 二次验证后重做预检再持久化；响应丢失沿用绑定重试，绝不恢复不受期限约束的旧路由。
func TakeoverGPTHosting(c *gin.Context) {
	actor, ok := hostingActor(c, true)
	if !ok {
		return
	}
	var input model.GPTHostingGrant
	if !onboardingBody(c, &input) {
		return
	}
	raw, _ := common.Marshal(service.AdminUserContext{UserID: input.UserID})
	if middleware.RequireSecurityProof(c, service.VerificationOperation{Scope: service.VerificationScopeAdminUserUpdate, Context: raw}) == nil {
		return
	}
	input.ActorID = actor
	existing, err := model.GetHostingBinding(input.UserID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		checked, e := checkHostingTakeover(c.Request.Context(), input.UserID)
		if e != nil {
			hostingError(c, e)
			return
		}
		if _, e = model.BeginHostingTakeover(input, *checked); e != nil {
			hostingError(c, e)
			return
		}
	} else if err != nil {
		hostingError(c, err)
		return
	} else if existing.LegacySnapshot == "" || existing.State != "takeover" {
		hostingError(c, model.ErrHostingConflict)
		return
	}
	b, lease, err := model.ClaimHostingOperation(input.UserID)
	if err != nil {
		hostingError(c, err)
		return
	}
	defer model.ReleaseHostingOperation(input.UserID, lease)
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
	// 渠道只在最终事务更新；未知结果下保留原 Key 以继续相同远端接管，不创建第二把。
	var channel model.Channel
	if err = model.DB.First(&channel, b.ChannelID).Error; err != nil {
		hostingError(c, err)
		return
	}
	state, err := client.HostingAction(c.Request.Context(), b.SlotID, "takeover", gin.H{"binding_id": b.BindingID, "generation": b.Generation, "account_hash": b.AccountHash, "subject_hash": b.SubjectHash, "key_id": b.KeyID, "key": channel.Key})
	if err != nil {
		hostingError(c, err)
		return
	}
	if b.AccountHash == nil || b.SubjectHash == nil || state.BindingID != b.BindingID || state.AccountHash != *b.AccountHash || state.SubjectHash != *b.SubjectHash {
		hostingError(c, model.ErrHostingConflict)
		return
	}
	grant, err := model.GetHostingGrant(input.UserID)
	if err != nil {
		hostingError(c, err)
		return
	}
	active, err := client.HostingAction(c.Request.Context(), b.SlotID, "activate", gin.H{"binding_id": b.BindingID, "generation": b.Generation, "account_hash": b.AccountHash, "subject_hash": b.SubjectHash, "starts_at": grant.StartsAt, "ends_at": grant.EndsAt, "enabled": grant.Enabled})
	if err != nil {
		hostingError(c, err)
		return
	}
	if active.BindingID != b.BindingID || active.AccountHash != *b.AccountHash || active.SubjectHash != *b.SubjectHash {
		hostingError(c, model.ErrHostingConflict)
		return
	}
	if err = model.CompleteHostingChannel(b.UserID, lease, client.Config().BaseURL, channel.Models, active.Key, active.KeyID, active.PoolID); err != nil {
		hostingError(c, err)
		return
	}
	model.InitChannelCache()
	recordHostingUserAudit(c, b.UserID, "hosting.takeover", map[string]any{"starts_at": grant.StartsAt, "ends_at": grant.EndsAt, "retention_days": grant.RetentionDays, "note": grant.Note})
	c.JSON(200, gin.H{"success": true})
}
