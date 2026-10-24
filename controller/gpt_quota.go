package controller

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/gptquota"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

var quotaService = sync.OnceValues(func() (*gptquota.Service, error) {
	cfg, err := configuredQuota()
	if err != nil {
		return nil, err
	}
	// Both features use the same private instance registry. A new account can
	// read its backend's quota without rewriting a per-user file.
	registry, registryErr := onboardingRegistry()
	if registryErr == nil {
		cfg.Instances = maps.Clone(cfg.Instances)
		if cfg.Instances == nil {
			cfg.Instances = make(map[string]gptquota.BackendConfig)
		}
		for _, instance := range registry.Instances() {
			client, _ := registry.Client(instance.ID)
			backend := client.Config()
			panel := strings.TrimRight(backend.BaseURL, "/") + "/api/panel"
			if previous, exists := cfg.Instances[instance.ID]; exists && strings.TrimRight(previous.PanelURL, "/") != panel {
				return nil, gptquota.ErrUnavailable
			}
			cfg.Instances[instance.ID] = gptquota.BackendConfig{PanelURL: panel, Login: map[string]string{"username": backend.Username, "password": backend.Password}}
		}
	}
	return gptquota.New(cfg)
})

// linkedGPTQuota reads a completed, server-owned binding on every request. The
// service also checks the current username/group so later administrative moves
// cannot expose the former account's cached subscription usage.
func linkedGPTQuota(ctx context.Context, svc *gptquota.Service, id int, username, group string, refresh bool) (gptquota.View, error) {
	var hosted *model.GPTHostingBinding
	hostedErr := gorm.ErrRecordNotFound
	if os.Getenv("GPT_HOSTING_ENABLED") == "true" {
		hosted, hostedErr = model.GetHostingBinding(id)
	}
	if hostedErr == nil {
		// 托管身份在中央解绑后立即停止读取旧槽位，防止空槽被再次分配后的额度泄露。
		if hosted.AccountHash == nil || hosted.InventoryID == nil || hosted.State == "cleaning" || hosted.State == "unbound" {
			return gptquota.View{}, gptquota.ErrUnbound
		}
		grant, err := model.GetHostingGrant(id)
		if err != nil {
			return gptquota.View{}, gptquota.ErrUnavailable
		}
		binding := gptquota.Binding{InstanceID: hosted.InstanceID, Username: username, Group: grant.Group, Slot: hosted.SlotID, Label: hosted.AccountLabel}
		if refresh {
			return svc.RefreshFor(ctx, id, username, group, binding)
		}
		return svc.SelfFor(ctx, id, username, group, binding)
	}
	// 未启用扩展的旧测试/安装可没有此表；启用后所有读取异常都必须失败关闭。
	if os.Getenv("GPT_HOSTING_ENABLED") == "true" && !errors.Is(hostedErr, gorm.ErrRecordNotFound) {
		return gptquota.View{}, gptquota.ErrUnavailable
	}
	job, err := model.GetVM2APIEnrollmentForUser(id)
	if err == nil {
		binding := gptquota.Binding{InstanceID: job.InstanceID, Username: job.Username, Group: job.Group, Slot: job.SlotID, Label: job.DisplayName}
		if refresh {
			return svc.RefreshFor(ctx, id, username, group, binding)
		}
		return svc.SelfFor(ctx, id, username, group, binding)
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return gptquota.View{}, gptquota.ErrUnavailable
	}
	if refresh {
		return svc.Refresh(ctx, id, username, group)
	}
	return svc.Self(ctx, id, username, group)
}

// GetGPTQuotaSelf uses the identity established by UserAuth on every request.
// Neither query parameters nor New-Api-User may select another account's slot.
func GetGPTQuotaSelf(c *gin.Context) { respondGPTQuota(c, false) }

// RefreshGPTQuotaSelf triggers an official probe only for the verified viewer.
func RefreshGPTQuotaSelf(c *gin.Context) { respondGPTQuota(c, true) }

// GetGPTQuotaUser exposes a saved quota reading to an authenticated administrator.
// The target's current database identity must match the configured binding; the
// browser cannot supply a username, group or slot to redirect the lookup.
func GetGPTQuotaUser(c *gin.Context) { respondGPTQuotaUser(c, false) }

// RefreshGPTQuotaUser 只允许会话管理员刷新其可管理的单个用户；与本人共享 30 秒冷却。
func RefreshGPTQuotaUser(c *gin.Context) { respondGPTQuotaUser(c, true) }

// respondGPTQuotaUser 统一读和刷新的目标权限，防止管理员操作同级或更高权限账号。
func respondGPTQuotaUser(c *gin.Context, refresh bool) {
	c.Header("Cache-Control", "no-store, private")
	actor := c.GetInt("id")
	if hint := c.GetHeader("New-Api-User"); actor <= 0 || (hint != "" && hint != strconv.Itoa(actor)) {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Authentication required"})
		return
	}
	// AdminAuth validates the live session and role; keep this guard as well so a
	// future route registration cannot accidentally expose other users' quotas.
	if c.GetInt("role") < common.RoleAdminUser {
		c.JSON(http.StatusForbidden, gin.H{"success": false, "message": "Access denied"})
		return
	}
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 || strconv.Itoa(id) != c.Param("id") || c.Request.URL.RawQuery != "" {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "Not found"})
		return
	}
	target, err := model.GetUserById(id, false)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "Not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "message": "Subscription quota temporarily unavailable"})
		return
	}
	if refresh && !canManageTargetRole(c.GetInt("role"), target.Role) {
		c.AbortWithStatus(403)
		return
	}
	svc, err := quotaService()
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "message": "Subscription quota temporarily unavailable"})
		return
	}
	// Read the shared snapshot only: opening a page must not trigger N official
	// probes or consume each account's manual-refresh cooldown.
	view, err := linkedGPTQuota(c.Request.Context(), svc, target.Id, target.Username, target.Group, refresh)
	if errors.Is(err, gptquota.ErrCooldown) {
		c.Header("Retry-After", "30")
		c.JSON(429, gin.H{"success": false, "message": "Please wait 30 seconds before refreshing quota again"})
		return
	}
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "message": "Subscription quota temporarily unavailable"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": view})
}

// respondGPTQuota keeps identity and query checks identical on read and refresh.
func respondGPTQuota(c *gin.Context, refresh bool) {
	c.Header("Cache-Control", "no-store, private")
	if c.Request.URL.RawQuery != "" {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "Not found"})
		return
	}
	id := c.GetInt("id")
	if hint := c.GetHeader("New-Api-User"); id <= 0 || (hint != "" && hint != strconv.Itoa(id)) {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Authentication required"})
		return
	}
	service, err := quotaService()
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "message": "Subscription quota temporarily unavailable"})
		return
	}
	view, err := linkedGPTQuota(c.Request.Context(), service, id, c.GetString("username"), c.GetString("group"), refresh)
	if errors.Is(err, gptquota.ErrCooldown) {
		c.Header("Retry-After", "30")
		c.JSON(http.StatusTooManyRequests, gin.H{"success": false, "message": "Please wait 30 seconds before refreshing quota again"})
		return
	}
	if errors.Is(err, gptquota.ErrUnbound) {
		c.JSON(http.StatusForbidden, gin.H{"success": false, "message": "No linked quota account"})
		return
	}
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "message": "Subscription quota temporarily unavailable"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": view})
}
