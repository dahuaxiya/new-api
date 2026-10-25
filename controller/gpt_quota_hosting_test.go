package controller

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/gptquota"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// TestGPTQuotaHostedLegacyGroup 验证保留旧用户组的托管账号仍可读取本人额度和采集历史，
// 同时保持会话身份、官方账号指纹和解绑状态的隔离，不通过修改用户分组规避问题。
func TestGPTQuotaHostedLegacyGroup(t *testing.T) {
	user, identity := setupSecurityEnrollmentTest(t)
	t.Setenv("GPT_HOSTING_ENABLED", "true")
	require.NoError(t, model.DB.AutoMigrate(&model.GPTHostingGrant{}, &model.GPTHostingBinding{}, &model.VM2APIEnrollment{}, &model.GPTQuotaHistory{}))
	now := time.Now().UTC()
	accountHash := fmt.Sprintf("%x", sha256.Sum256([]byte("openai-account\x00owned-account")))
	inventory := "owned-inventory"
	require.NoError(t, model.DB.Create(&model.GPTHostingGrant{UserID: user.Id, Group: "acct_new_group", Enabled: true, StartsAt: now.Unix() - 1, EndsAt: model.HostingPermanentEnd}).Error)
	require.NoError(t, model.DB.Create(&model.GPTHostingBinding{UserID: user.Id, BindingID: "owned-binding", InventoryID: &inventory, InstanceID: "cc", SlotID: "owned-slot", State: "active", AccountHash: &accountHash}).Error)
	var reads atomic.Int32
	panel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/panel/login" {
			_, _ = w.Write([]byte(`{"token":"fixture"}`))
			return
		}
		reads.Add(1)
		if r.URL.Path != "/api/panel/vms/owned-slot" {
			http.NotFound(w, r)
			return
		}
		body, _ := common.Marshal(gin.H{"ok": true, "data": gin.H{"vm": gin.H{"id": "owned-slot", "org_uuid": "owned-account", "codex_usage": gin.H{
			"snapshot": gin.H{"updated_at": now.Format(time.RFC3339Nano)},
			"windows":  []gin.H{{"id": "7d", "used_percent": 32, "reset_at": now.Add(time.Hour).Format(time.RFC3339Nano)}},
		}}}})
		_, _ = w.Write(body)
	}))
	defer panel.Close()
	svc, err := gptquota.New(gptquota.Config{Instances: map[string]gptquota.BackendConfig{"cc": {PanelURL: panel.URL + "/api/panel", Login: map[string]string{"username": "fixture", "password": "fixture"}}}})
	require.NoError(t, err)
	old := quotaService
	quotaService = func() (*gptquota.Service, error) { return svc, nil }
	t.Cleanup(func() { quotaService = old })
	router := gin.New()
	router.GET("/quota", middleware.UserAuth(), GetGPTQuotaSelf)
	token, _, err := service.IssueAccessToken(identity)
	require.NoError(t, err)
	req := httptest.NewRequest("GET", "/quota", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	out := httptest.NewRecorder()
	router.ServeHTTP(out, req)
	require.Equal(t, 200, out.Code)
	require.Contains(t, out.Body.String(), `"bound":true`)
	require.Contains(t, out.Body.String(), `"remaining_percent":68`)
	// 历史采集与实时卡片必须使用相同身份规则；只修卡片会继续丢失曲线读数。
	require.NoError(t, collectGPTQuotaUser(context.Background(), svc, user))
	_, scope, err := quotaHistoryScope(svc, user)
	require.NoError(t, err)
	require.NotEmpty(t, scope)
	rows, err := model.GetGPTQuotaHistory(context.Background(), user.Id, scope, 24, time.Now())
	require.NoError(t, err)
	require.Len(t, rows, 1)
	// 伪造另一个用户的请求头仍需在访问面板前拒绝，不能因兼容旧分组放宽身份边界。
	before := reads.Load()
	req.Header.Set("New-Api-User", "99999")
	out = httptest.NewRecorder()
	router.ServeHTTP(out, req)
	require.Equal(t, 401, out.Code)
	require.Equal(t, before, reads.Load())
	req.Header.Del("New-Api-User")
	// 解绑必须立即失去读取权限，不能回退到旧映射或复用已缓存的账号额度。
	require.NoError(t, model.DB.Model(&model.GPTHostingBinding{}).Where("user_id = ?", user.Id).Update("state", "unbound").Error)
	out = httptest.NewRecorder()
	router.ServeHTTP(out, req)
	require.Equal(t, 403, out.Code)
	require.Equal(t, before, reads.Load())
}
