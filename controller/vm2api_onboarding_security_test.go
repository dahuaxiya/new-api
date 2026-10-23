package controller

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestVM2APIOnboardingManagedDraftOAuthIsolation 使用两个真实 root 登录会话验证：
// 其他管理员不能启动或完成草稿槽位的授权，也不能使创建者已有的授权会话失效。
// 所有上游请求仅到本地 httptest 服务；合法创建者成功换票作为拒绝用例的对照。
func TestVM2APIOnboardingManagedDraftOAuthIsolation(t *testing.T) {
	owner, ownerIdentity := setupSecurityEnrollmentTest(t)
	require.NoError(t, model.DB.AutoMigrate(&model.VM2APIEnrollment{}, &model.VM2APISlotDraft{}))
	require.NoError(t, model.DB.Model(owner).Update("role", common.RoleRootUser).Error)
	require.NoError(t, model.PublishUserAuthCache(owner.Id))
	ownerToken, _, err := service.IssueAccessToken(ownerIdentity)
	require.NoError(t, err)
	other := &model.User{
		Username: "other-root", AffCode: "other-root", Password: owner.Password,
		Role: common.RoleRootUser, Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1,
	}
	require.NoError(t, model.DB.Create(other).Error)
	require.NoError(t, model.PublishUserAuthCache(other.Id))
	otherSession, err := service.CreateLoginSession(other.Id, "password", "127.0.0.1", "managed-draft-test")
	require.NoError(t, err)
	otherIdentity, err := service.ParseAccessToken(otherSession.AccessToken)
	require.NoError(t, err)
	require.Equal(t, other.Id, otherIdentity.UserID)
	require.NotEqual(t, ownerIdentity.SessionID, otherIdentity.SessionID)

	draft, err := model.ReserveVM2APISlotDraft(&model.VM2APISlotDraft{
		RequestID: uuid.NewString(), ActorID: owner.Id, InstanceID: "test-vps", Name: "Owner slot", ExitID: "test-egress",
	})
	require.NoError(t, err)
	state := strings.Repeat("s", 40)
	innerURL := "https://auth.openai.com/oauth/authorize?" + url.Values{
		"state": {state}, "redirect_uri": {"http://localhost:1455/auth/callback"},
		"response_type": {"code"}, "code_challenge_method": {"S256"}, "code_challenge": {strings.Repeat("c", 43)},
	}.Encode()
	authorizationURL := "https://chatgpt.com/codex/desktop-auth?authorize_url=" + url.QueryEscape(innerURL)
	callbackURL := "http://localhost:1455/auth/callback?code=synthetic-authorization-code&state=" + state
	var starts, exchanges atomic.Int32
	var loggedIn atomic.Bool
	panel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/panel/login" {
			fmt.Fprint(w, `{"ok":true,"token":"synthetic-panel-token","role":"admin","expires_in":3600}`)
			return
		}
		assert.Equal(t, "Bearer synthetic-panel-token", r.Header.Get("Authorization"))
		switch r.Method + " " + r.URL.Path {
		case "GET /api/panel/vms/" + draft.SlotID:
			fmt.Fprintf(w, `{"ok":true,"data":{"vm":{"id":%q,"name":"Owner slot","family":"codex","runtime_type":"docker","status":"running","has_token":%t,"schedulable":true}}}`, draft.SlotID, loggedIn.Load())
		case "POST /api/panel/vms/" + draft.SlotID + "/oauth/generate-auth-url":
			starts.Add(1)
			fmt.Fprintf(w, `{"ok":true,"data":{"auth_url":%q,"session_id":"synthetic-upstream-session","expires_at":%d}}`, authorizationURL, time.Now().Add(5*time.Minute).UnixMilli())
		case "POST /api/panel/vms/" + draft.SlotID + "/oauth/exchange-code":
			exchanges.Add(1)
			var body map[string]string
			if !assert.NoError(t, common.DecodeJson(r.Body, &body)) || !assert.Equal(t, "synthetic-upstream-session", body["session_id"]) || !assert.Equal(t, callbackURL, body["callback_url"]) {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			loggedIn.Store(true)
			fmt.Fprint(w, `{"ok":true}`)
		default:
			t.Errorf("unexpected upstream route: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer panel.Close()
	registry, err := vm2api.New(vm2api.Config{Instances: []vm2api.InstanceConfig{{
		ID: "test-vps", Name: "Test VPS", BaseURL: panel.URL,
		Username: "test-root", Password: "synthetic-panel-password", Models: []string{"gpt-test"},
	}}})
	require.NoError(t, err)
	previousRegistry, previousQuota := onboardingRegistry, configuredQuota
	previousSecure, previousOrigins := common.SessionCookieSecure, common.SessionCookieTrustedURLs
	onboardingRegistry = func() (*vm2api.Registry, error) { return registry, nil }
	configuredQuota = func() (gptquota.Config, error) { return gptquota.Config{}, nil }
	common.SessionCookieSecure, common.SessionCookieTrustedURLs = true, nil
	t.Cleanup(func() {
		onboardingRegistry, configuredQuota = previousRegistry, previousQuota
		common.SessionCookieSecure, common.SessionCookieTrustedURLs = previousSecure, previousOrigins
	})
	router := gin.New()
	group := router.Group("/api/vm2api/onboarding", middleware.RootAuth(), middleware.DisableCache())
	group.POST("/oauth/start", middleware.SessionCookieOriginGuard(), VM2APIOnboardingOAuth)
	group.POST("/oauth/finish", middleware.SessionCookieOriginGuard(), VM2APIOnboardingOAuth)
	request := func(action, token, sessionID string) *httptest.ResponseRecorder {
		body, err := common.Marshal(map[string]string{
			"instance_id": draft.InstanceID, "slot_id": draft.SlotID,
			"session_id": sessionID, "callback_url": callbackURL,
		})
		require.NoError(t, err)
		req := httptest.NewRequest(http.MethodPost, "http://portal.test/api/vm2api/onboarding/oauth/"+action, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Origin", "http://portal.test")
		out := httptest.NewRecorder()
		router.ServeHTTP(out, req)
		return out
	}

	// 409 和明确的槽位占用消息证明请求通过了真实 root 会话及同源校验，
	// 在草稿归属检查处被拒绝，而不是因伪造身份、无效回调或外部服务失败。
	deniedStart := request("start", otherSession.AccessToken, "")
	require.Equal(t, http.StatusConflict, deniedStart.Code, deniedStart.Body.String())
	assert.Contains(t, deniedStart.Body.String(), `"message":"Slot is already assigned"`)
	assert.Zero(t, starts.Load())
	assert.Zero(t, exchanges.Load())

	started := request("start", ownerToken, "")
	require.Equal(t, http.StatusOK, started.Code, started.Body.String())
	var response struct {
		Data vm2api.OAuthSession `json:"data"`
	}
	require.NoError(t, common.Unmarshal(started.Body.Bytes(), &response))
	require.NotEmpty(t, response.Data.SessionID)
	assert.Equal(t, int32(1), starts.Load())
	for _, action := range []string{"start", "finish"} {
		denied := request(action, otherSession.AccessToken, response.Data.SessionID)
		require.Equal(t, http.StatusConflict, denied.Code, denied.Body.String())
		assert.Contains(t, denied.Body.String(), `"message":"Slot is already assigned"`)
		assert.Contains(t, denied.Header().Get("Cache-Control"), "no-store")
		assert.Equal(t, int32(1), starts.Load())
		assert.Zero(t, exchanges.Load())
	}

	// 即使另一位 root 知道合法会话 ID 和完整回调，也不能消费授权；
	// 创建者随后仍能完成同一会话，说明拒绝没有破坏原来的操作。
	finished := request("finish", ownerToken, response.Data.SessionID)
	require.Equal(t, http.StatusOK, finished.Code, finished.Body.String())
	assert.Contains(t, finished.Body.String(), `"logged_in":true`)
	assert.Contains(t, finished.Body.String(), `"available":true`)
	assert.Equal(t, int32(1), starts.Load())
	assert.Equal(t, int32(1), exchanges.Load())
}
