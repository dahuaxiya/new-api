package controller

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/gptquota"
	"github.com/QuantumNous/new-api/service/vm2api"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// TestGPTHostingSessionBoundary 验证真实登录会话隔离、跨站拒绝、名额停用以及敏感动作所需证明。
func TestGPTHostingSessionBoundary(t *testing.T) {
	user, identity := setupSecurityEnrollmentTest(t)
	t.Setenv("GPT_HOSTING_ENABLED", "true")
	require.NoError(t, model.DB.AutoMigrate(&model.GPTHostingGrant{}, &model.GPTHostingBinding{}, &model.GPTHostingInventory{}))
	now := time.Now().Unix()
	require.NoError(t, model.DB.Create(&model.GPTHostingGrant{UserID: user.Id, StartsAt: now - 1, EndsAt: model.HostingPermanentEnd, Enabled: true, Group: "acct_fixture"}).Error)
	require.NoError(t, model.DB.Create(&model.GPTHostingBinding{UserID: user.Id, BindingID: uuid.NewString(), State: "unbound"}).Error)
	token, _, e := service.IssueAccessToken(identity)
	require.NoError(t, e)
	r := gin.New()
	r.GET("/self", middleware.UserAuth(), GetGPTHostingSelf)
	r.GET("/admin", middleware.RootAuth(), GetGPTHostingAdmin)
	r.POST("/login", middleware.UserAuth(), middleware.SessionCookieOriginGuard(), StartGPTHostingLogin)
	r.POST("/unbind", middleware.UserAuth(), middleware.SessionCookieOriginGuard(), UnbindGPTHosting)
	request := func(method, path, auth, origin string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "http://portal.test"+path, bytes.NewBufferString(`{}`))
		req.Header.Set("Content-Type", "application/json")
		if auth != "" {
			req.Header.Set("Authorization", "Bearer "+auth)
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	own := request("GET", "/self?user_id=999", token, "")
	require.Equal(t, 200, own.Code, own.Body.String())
	require.Contains(t, own.Body.String(), `"binding_id"`)
	for _, private := range []string{"account_hash", "subject_hash", "slot_id", "instance_id", "operation_token", "key_id"} {
		require.NotContains(t, own.Body.String(), `"`+private+`"`)
	}
	require.Equal(t, 401, request("GET", "/self", "", "").Code)
	require.Equal(t, 403, request("GET", "/admin", token, "").Code)
	require.Equal(t, 403, request("POST", "/login", token, "https://untrusted.test").Code)
	for _, action := range []string{"/login", "/unbind"} {
		denied := request("POST", action, token, "http://portal.test")
		require.NotEqual(t, http.StatusOK, denied.Code, denied.Body.String())
		var binding model.GPTHostingBinding
		require.NoError(t, model.DB.First(&binding, "user_id = ?", user.Id).Error)
		require.Equal(t, "unbound", binding.State, "missing proof must not reserve or clear")
	}
	require.NoError(t, model.DB.Model(&model.GPTHostingGrant{}).Where("user_id = ?", user.Id).Update("ends_at", now-1).Error)
	denied := request("POST", "/login", token, "http://portal.test")
	require.Contains(t, denied.Body.String(), "hosting_expired")
	require.Equal(t, common.RoleCommonUser, user.Role)
}

// TestUnifiedHostingAdminBoundary 验证旧绑定立即可见、错误归属不会回退，以及普通用户与无证明写入均被拒绝。
func TestUnifiedHostingAdminBoundary(t *testing.T) {
	actor, identity := setupSecurityEnrollmentTest(t)
	t.Setenv("GPT_HOSTING_ENABLED", "true")
	require.NoError(t, model.DB.AutoMigrate(&model.GPTHostingGrant{}, &model.GPTHostingBinding{}, &model.GPTHostingInventory{}, &model.VM2APIEnrollment{}))
	target := model.User{Username: "legacy-owner", AffCode: "legacy-owner", Group: "old-group", Role: common.RoleCommonUser, Status: 1, Quota: 345}
	require.NoError(t, model.DB.Create(&target).Error)
	cfg := gptquota.Config{PanelURL: "https://fixture.test/api/panel", Users: map[string]gptquota.Binding{strconv.Itoa(target.Id): {Username: target.Username, Group: target.Group, Slot: "slot-old"}}}
	registry, err := vm2api.New(vm2api.Config{Instances: []vm2api.InstanceConfig{{ID: "cc", Name: "CloudCone", BaseURL: "https://fixture.test", Username: "fixture", Password: "fixture-password", Models: []string{"gpt-test"}}}})
	require.NoError(t, err)
	oldCfg, oldRegistry, oldSvc := configuredQuota, onboardingRegistry, quotaService
	configuredQuota = func() (gptquota.Config, error) { return cfg, nil }
	onboardingRegistry = func() (*vm2api.Registry, error) { return registry, nil }
	quotaService = func() (*gptquota.Service, error) { return nil, errors.New("offline") }
	t.Cleanup(func() { configuredQuota, onboardingRegistry, quotaService = oldCfg, oldRegistry, oldSvc })
	token, _, err := service.IssueAccessToken(identity)
	require.NoError(t, err)
	r := gin.New()
	r.GET("/users", middleware.AdminAuth(), GetGPTHostingUsers)
	r.POST("/wallet", middleware.AdminAuth(), middleware.SessionCookieOriginGuard(), AdjustHostingUserWallet)
	r.POST("/grants", middleware.RootAuth(), middleware.SessionCookieOriginGuard(), PutGPTHostingGrant)
	r.POST("/takeover", middleware.RootAuth(), middleware.SessionCookieOriginGuard(), TakeoverGPTHosting)
	request := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "http://portal.test"+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Origin", "http://portal.test")
		req.Header.Set("Content-Type", "application/json")
		out := httptest.NewRecorder()
		r.ServeHTTP(out, req)
		return out
	}
	path := "/users?ids=" + strconv.Itoa(target.Id)
	require.Equal(t, 403, request("GET", path, "").Code)
	require.NoError(t, model.DB.Model(actor).Update("role", common.RoleRootUser).Error)
	require.NoError(t, model.PublishUserAuthCache(actor.Id))
	out := request("GET", path, "")
	require.Equal(t, 200, out.Code)
	require.Contains(t, out.Body.String(), `"status":"undated"`)
	require.Contains(t, out.Body.String(), `"instance_id":"cc"`)
	require.Contains(t, out.Body.String(), `"quota_unavailable":true`)
	require.NotContains(t, out.Body.String(), "fixture-password")
	require.NotContains(t, out.Body.String(), "fixture.test")
	for _, tc := range []struct{ path, body string }{{"/wallet", fmt.Sprintf(`{"id":%d,"action":"add_quota","mode":"add","value":99,"reason":"fixture"}`, target.Id)}, {"/takeover", fmt.Sprintf(`{"user_id":%d,"starts_at":1,"ends_at":-1,"enabled":true}`, target.Id)}, {"/grants", fmt.Sprintf(`{"user_id":%d,"starts_at":1,"ends_at":-1,"enabled":true}`, target.Id)}} {
		require.NotEqual(t, 200, request("POST", tc.path, tc.body).Code, "missing proof must not write")
	}
	saved, err := model.GetUserById(target.Id, false)
	require.NoError(t, err)
	require.Equal(t, 345, saved.Quota)
	var count int64
	require.NoError(t, model.DB.Model(&model.GPTHostingGrant{}).Count(&count).Error)
	require.Zero(t, count)
	cfg.Users["999"] = cfg.Users[strconv.Itoa(target.Id)]
	require.Contains(t, request("GET", path, "").Body.String(), `"status":"conflict"`)
	delete(cfg.Users, "999")
	require.NoError(t, model.DB.Model(&target).Update("group", "moved").Error)
	require.Contains(t, request("GET", path, "").Body.String(), `"status":"conflict"`)
	require.Equal(t, 400, request("GET", "/users?ids=01", "").Code)
}

// TestHostingLoginRejectsLegacyReference 验证 root 保留的历史归属不能经登录恢复接口伪装成已验证的新登录。
func TestHostingLoginRejectsLegacyReference(t *testing.T) {
	binding := &model.GPTHostingBinding{BindingID: "binding", SlotID: "slot", State: "prepared"}
	state := vm2api.HostedState{BindingID: "binding", VMID: "slot", State: "prepared", IdentitySource: "legacy_binding"}
	require.ErrorIs(t, finishHosting(context.Background(), binding, "lease", state), model.ErrHostingConflict)
}

// TestPermanentHostingSummary 确保永久名额仍按开始时间、暂停和绑定阶段展示，不误报到期。
func TestPermanentHostingSummary(t *testing.T) {
	user, _ := setupSecurityEnrollmentTest(t)
	require.NoError(t, model.DB.AutoMigrate(&model.GPTHostingGrant{}, &model.GPTHostingBinding{}))
	now := time.Now().Unix()
	grant := model.GPTHostingGrant{UserID: user.Id, StartsAt: now - 1, EndsAt: model.HostingPermanentEnd, Enabled: true, Group: "acct_permanent"}
	binding := model.GPTHostingBinding{UserID: user.Id, BindingID: uuid.NewString(), State: "active"}
	require.NoError(t, model.DB.Create(&grant).Error)
	require.NoError(t, model.DB.Create(&binding).Error)
	for _, status := range []string{"active", "disabled", "scheduled", "awaiting_login"} {
		grant.StartsAt, binding.Paused, binding.State = now-1, status == "disabled", "active"
		if status == "scheduled" {
			grant.StartsAt = now + 3600
		}
		if status == "awaiting_login" {
			binding.State = "unbound"
		}
		require.NoError(t, model.DB.Save(&grant).Error)
		require.NoError(t, model.DB.Save(&binding).Error)
		require.Equal(t, status, hostingSummary(user).Status)
	}
}
