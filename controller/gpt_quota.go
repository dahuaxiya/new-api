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

// resolveGPTQuotaBinding 每次按已鉴权用户 ID 从数据库解析绑定；username、group 必须来自
// 当前会话或数据库用户，不能取自请求参数。epoch 区分解绑重绑，不能使用缓存授权。
func resolveGPTQuotaBinding(svc *gptquota.Service, id int, username, group string) (gptquota.Binding, string, error) {
	var hosted *model.GPTHostingBinding
	hostedErr := gorm.ErrRecordNotFound
	if os.Getenv("GPT_HOSTING_ENABLED") == "true" {
		hosted, hostedErr = model.GetHostingBinding(id)
	}
	if hostedErr == nil {
		if hosted.AccountHash == nil || hosted.InventoryID == nil || hosted.State == "cleaning" || hosted.State == "unbound" {
			return gptquota.Binding{}, "", gptquota.ErrUnbound
		}
		_, err := model.GetHostingGrant(id)
		if err != nil {
			return gptquota.Binding{}, "", gptquota.ErrUnavailable
		}
		// 托管专属组负责模型路由，旧用户和已有 Key 可以继续保留原用户组。
		// 额度服务的 Group 用于验证读取者身份，必须使用当前用户组；槽位仍只由
		// 上面的 user_id 查询及解绑状态确定，历史采集继续校验 AccountHash。
		binding := gptquota.Binding{InstanceID: hosted.InstanceID, Username: username, Group: group, Slot: hosted.SlotID, Label: hosted.AccountLabel, AccountHash: *hosted.AccountHash}
		return binding, "hosted:" + hosted.BindingID + ":" + *hosted.AccountHash, nil
	}
	// 启用托管后，数据库故障不能降级到旧绑定，避免解绑期间重新读到旧槽位。
	if os.Getenv("GPT_HOSTING_ENABLED") == "true" && !errors.Is(hostedErr, gorm.ErrRecordNotFound) {
		return gptquota.Binding{}, "", gptquota.ErrUnavailable
	}
	job, err := model.GetVM2APIEnrollmentForUser(id)
	if err == nil {
		return gptquota.Binding{InstanceID: job.InstanceID, Username: job.Username, Group: job.Group, Slot: job.SlotID, Label: job.DisplayName}, "enrollment:" + job.RequestID, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return gptquota.Binding{}, "", gptquota.ErrUnavailable
	}
	return svc.StaticBinding(id), "legacy", nil
}

// linkedGPTQuota 统一实时查询和刷新；绑定不匹配时保持旧接口的未绑定行为。
func linkedGPTQuota(ctx context.Context, svc *gptquota.Service, id int, username, group string, refresh bool) (gptquota.View, error) {
	binding, _, err := resolveGPTQuotaBinding(svc, id, username, group)
	if err != nil {
		return gptquota.View{}, err
	}
	if refresh {
		view, err := svc.RefreshFor(ctx, id, username, group, binding)
		// 手动刷新后读取刚持久化的快照；记录失败不把成功的官方查询变成失败。
		if err == nil {
			if user, userErr := model.GetUserById(id, false); userErr == nil {
				if historyErr := collectGPTQuotaUser(ctx, svc, user); historyErr != nil {
					common.SysError("GPT quota history refresh persistence unavailable")
				}
			}
		}
		return view, err
	}
	return svc.SelfFor(ctx, id, username, group, binding)
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
