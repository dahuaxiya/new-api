package controller

import (
	"errors"
	"net/http"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/vm2api"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// QueryHostingResetCredits 只查询当前会话所选托管名额的官方重置次数，不消费次数。
// @param c 包含认证会话及可选名额编号的请求上下文。
func QueryHostingResetCredits(c *gin.Context) { handleHostingResetCredits(c, false) }

// UseHostingResetCredit 在一次性身份确认后使用当前绑定的一次官方重置次数。
// @param c 包含认证会话、名额编号及一次性验证证明的请求上下文。
func UseHostingResetCredit(c *gin.Context) { handleHostingResetCredits(c, true) }

// handleHostingResetCredits 在门户和远端共同校验归属；网络异常时保留不确定状态，不自动重放。
// @param c 当前用户的请求上下文；响应只包含受限的次数和状态。
// @param use 为 true 时需要二次验证并执行不可撤销的消费。
func handleHostingResetCredits(c *gin.Context, use bool) {
	userID, ok := hostingActor(c, false)
	if !ok {
		return
	}
	query := c.Request.URL.Query()
	if len(query) > 1 || (len(query) == 1 && (len(query["seat_id"]) != 1 || len(query.Get("seat_id")) > 36)) {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Invalid hosting seat"})
		return
	}
	var input struct{}
	if !onboardingBody(c, &input) {
		return
	}
	seat := query.Get("seat_id")
	before, err := model.GetHostingBinding(userID, seat)
	if err != nil {
		hostingError(c, err)
		return
	}
	if use && !hostingProof(c, "reset", before.BindingID) {
		return
	}
	binding, lease, err := model.ClaimHostingOperation(userID, seat)
	if err != nil {
		hostingError(c, err)
		return
	}
	defer model.ReleaseHostingOperation(userID, lease, binding.SeatID)
	grant, err := model.GetHostingGrant(userID, binding.SeatID)
	if err != nil || !grant.ValidAt(time.Now().Unix()) || binding.BindingID != before.BindingID || binding.State != "active" || binding.Paused || binding.InventoryID == nil || binding.AccountHash == nil || binding.SubjectHash == nil || binding.MigrationState != "" {
		hostingError(c, model.ErrHostingUnavailable)
		return
	}
	registry, err := onboardingRegistry()
	if err != nil {
		hostingError(c, err)
		return
	}
	client, err := registry.Client(binding.InstanceID)
	if err != nil {
		hostingError(c, err)
		return
	}
	blockedUntil, err := model.HostingResetBlockedUntil(*binding.AccountHash, time.Now())
	if err != nil {
		hostingError(c, err)
		return
	}
	action, operationID := "reset-query", ""
	if use {
		if blockedUntil > 0 {
			c.JSON(http.StatusConflict, gin.H{"success": false, "code": "reset_unconfirmed", "message": "The previous reset could not be confirmed", "blocked_until": blockedUntil})
			return
		}
		action, operationID = "reset-use", uuid.NewString()
		if err = model.BeginHostingResetAttempt(*binding.AccountHash, operationID, time.Now()); err != nil {
			hostingError(c, err)
			return
		}
	}
	result, err := client.HostedResetCredits(c.Request.Context(), binding.SlotID, binding.BindingID, *binding.AccountHash, *binding.SubjectHash, binding.Platform, action, operationID, binding.Generation)
	if use && (err != nil || result.Outcome == "unknown") {
		// 远端可能已经消费，连接中断和非明确结果都保留保护，页面只能展示未确认。
		until, _ := model.HostingResetBlockedUntil(*binding.AccountHash, time.Now())
		c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"outcome": "unknown", "blocked_until": until}})
		return
	}
	if err != nil {
		var upstream *vm2api.Error
		if errors.As(err, &upstream) {
			switch upstream.Code {
			case "reset_unsupported":
				c.JSON(http.StatusConflict, gin.H{"success": false, "code": "reset_unsupported", "message": "This account cannot use reset credits"})
				return
			case "reset_unavailable":
				c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "code": "reset_unavailable", "message": "Reset credits are temporarily unavailable"})
				return
			}
		}
		hostingError(c, err)
		return
	}
	if use {
		if err = model.FinishHostingResetAttempt(*binding.AccountHash, operationID, result.Outcome); err != nil {
			// 不能确认结果已持久化时仍告知用户未知，避免再次消耗。
			c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"outcome": "unknown"}})
			return
		}
	} else {
		result.BlockedUntil = blockedUntil
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": result})
}
