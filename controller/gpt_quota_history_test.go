package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
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

// TestGPTQuotaHistoryIsolation 通过真实会话和面板边界验证无页面采集、去重、
// 已过期窗口保留未知、查询范围限制，以及新用户/解绑后不能取得旧槽位历史。
func TestGPTQuotaHistoryIsolation(t *testing.T) {
	user, identity := setupSecurityEnrollmentTest(t)
	require.NoError(t, model.DB.AutoMigrate(&model.VM2APIEnrollment{}, &model.GPTQuotaHistory{}))
	now := time.Now().UTC()
	var reads atomic.Int32
	panel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/panel/login" {
			_, _ = w.Write([]byte(`{"token":"fixture"}`))
			return
		}
		reads.Add(1)
		body, _ := common.Marshal(gin.H{"ok": true, "data": gin.H{"vm": gin.H{"id": "slot-a", "org_uuid": "private-account", "email": "private@example.test", "codex_usage": gin.H{
			"snapshot": gin.H{"updated_at": now.Format(time.RFC3339Nano)},
			"windows":  []gin.H{{"id": "5h", "used_percent": 90, "reset_at": now.Add(-time.Hour).Format(time.RFC3339Nano)}, {"id": "7d", "used_percent": 32, "reset_at": now.Add(time.Hour).Format(time.RFC3339Nano)}},
		}}}})
		_, _ = w.Write(body)
	}))
	defer panel.Close()
	config := gptquota.Config{PanelURL: panel.URL + "/api/panel", Login: map[string]string{"username": "test", "password": "test"}, Users: map[string]gptquota.Binding{
		strconv.Itoa(user.Id): {Username: user.Username, Group: user.Group, Slot: "slot-a", Label: "Fixture"},
	}}
	svc, err := gptquota.New(config)
	require.NoError(t, err)
	previous := quotaService
	quotaService = func() (*gptquota.Service, error) { return svc, nil }
	t.Cleanup(func() { quotaService = previous })
	// 调用真正的后台批次而不是 HTTP 页面，证明采集不依赖用户访问。
	require.NoError(t, collectGPTQuotaHistory(context.Background(), svc))
	require.NoError(t, collectGPTQuotaHistory(context.Background(), svc))
	var count int64
	require.NoError(t, model.DB.Model(&model.GPTQuotaHistory{}).Count(&count).Error)
	require.EqualValues(t, 1, count)
	router := gin.New()
	router.GET("/history", middleware.DisableCache(), middleware.UserAuth(), GetGPTQuotaHistorySelf)
	token, _, err := service.IssueAccessToken(identity)
	require.NoError(t, err)
	for _, tc := range []struct {
		suffix, token, hint string
		status              int
	}{
		{"?hours=24", token, "", 200}, {"?hours=168", token, "", 200}, {"?hours=720", token, "", 200},
		{"?hours=24&user_id=99", token, "", 400}, {"?hours=24&hours=168", token, "", 400},
		{"?slot=slot-a", token, "", 400}, {"?hours=721", token, "", 400}, {"", token, "999", 401}, {"", "", "", 401},
	} {
		req := httptest.NewRequest("GET", "/history"+tc.suffix, nil)
		req.Header.Set("Authorization", "Bearer "+tc.token)
		req.Header.Set("New-Api-User", tc.hint)
		out := httptest.NewRecorder()
		router.ServeHTTP(out, req)
		require.Equal(t, tc.status, out.Code, out.Body.String())
		if tc.status == 200 {
			require.Contains(t, out.Body.String(), `"remaining_7d":68`)
			require.Contains(t, out.Body.String(), `"remaining_5h":null`)
			for _, secret := range []string{"slot-a", "private@example.test", "private-account", `"scope"`, `"user_id"`} {
				require.NotContains(t, out.Body.String(), secret)
			}
			require.Contains(t, out.Header().Get("Cache-Control"), "no-store")
		}
	}
	require.EqualValues(t, 2, reads.Load(), "history queries must not contact upstream")
	// 同一槽位的新用户从空历史开始，不能查询前一位用户的记录。
	other := model.User{Username: "history-other", AffCode: "history-other", Group: user.Group, Status: common.UserStatusEnabled}
	require.NoError(t, model.DB.Create(&other).Error)
	config.Users[strconv.Itoa(other.Id)] = gptquota.Binding{Username: other.Username, Group: other.Group, Slot: "slot-a"}
	newService, err := gptquota.New(config)
	require.NoError(t, err)
	_, scope, err := quotaHistoryScope(newService, &other)
	require.NoError(t, err)
	rows, err := model.GetGPTQuotaHistory(context.Background(), other.Id, scope, 720, time.Now())
	require.NoError(t, err)
	require.Empty(t, rows)
	// 换绑到另一槽位会换历史范围；当前窗口不会连接旧账号的曲线。
	config.Users[strconv.Itoa(user.Id)] = gptquota.Binding{Username: user.Username, Group: user.Group, Slot: "slot-b"}
	svc, err = gptquota.New(config)
	require.NoError(t, err)
	req := httptest.NewRequest("GET", "/history", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	out := httptest.NewRecorder()
	router.ServeHTTP(out, req)
	require.Equal(t, 200, out.Code)
	require.Contains(t, out.Body.String(), `"points":[]`)
	// 即使存储中存在记录，撤销会话仍须先于历史查询生效。
	_, err = model.RevokeUserSession(user.Id, identity.SessionID, "quota history test")
	require.NoError(t, err)
	out = httptest.NewRecorder()
	router.ServeHTTP(out, req)
	require.Equal(t, 401, out.Code)
	require.False(t, strings.Contains(out.Body.String(), "remaining_7d"))
}
