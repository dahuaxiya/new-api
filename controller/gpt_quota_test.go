package controller

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/gptquota"
	"github.com/QuantumNous/new-api/service/vm2api"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGPTQuotaSelfSessionGate exercises the actual session middleware, including
// logout revocation and invalid identity hints, before any panel lookup occurs.
func TestGPTQuotaSelfSessionGate(t *testing.T) {
	user, identity := setupSecurityEnrollmentTest(t)
	require.NoError(t, model.DB.AutoMigrate(&model.VM2APIEnrollment{}))
	token, _, err := service.IssueAccessToken(identity)
	require.NoError(t, err)
	svc, err := gptquota.New(gptquota.Config{PanelURL: "http://127.0.0.1:1/api/panel", Login: map[string]string{"username": "test", "password": "test"}, Users: map[string]gptquota.Binding{}})
	require.NoError(t, err)
	old := quotaService
	quotaService = func() (*gptquota.Service, error) { return svc, nil }
	t.Cleanup(func() { quotaService = old })
	router := gin.New()
	router.GET("/api/gpt-quota/self", middleware.DisableCache(), middleware.UserAuth(), GetGPTQuotaSelf)
	router.POST("/api/gpt-quota/self/refresh", middleware.DisableCache(), middleware.UserAuth(), RefreshGPTQuotaSelf)
	for _, tc := range []struct {
		name, path, token, hint string
		status                  int
	}{
		{"valid unbound", "/api/gpt-quota/self", token, "", 200},
		{"anonymous", "/api/gpt-quota/self", "", "", 401},
		{"forged", "/api/gpt-quota/self", "fake-token", "", 401},
		{"cross user hint", "/api/gpt-quota/self", token, "999", 401},
		{"slot query", "/api/gpt-quota/self?slot=other", token, "", 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			if tc.token != "" {
				req.Header.Set("Authorization", "Bearer "+tc.token)
			}
			if tc.hint != "" {
				req.Header.Set("New-Api-User", tc.hint)
			}
			out := httptest.NewRecorder()
			router.ServeHTTP(out, req)
			assert.Equal(t, tc.status, out.Code, out.Body.String())
			if tc.status == 200 {
				assert.Contains(t, out.Body.String(), `"bound":false`)
				assert.Contains(t, out.Header().Get("Cache-Control"), "no-store")
			}
		})
	}
	// The mutating route has the same session gate and rejects unbound identities.
	for _, tc := range []struct {
		token, suffix, hint string
		status              int
	}{
		{token, "", "", 403}, {"", "", "", 401}, {"fake-token", "", "", 401},
		{token, "?slot=other", "", 404}, {token, "", "999", 401},
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/gpt-quota/self/refresh"+tc.suffix, nil)
		if tc.token != "" {
			req.Header.Set("Authorization", "Bearer "+tc.token)
		}
		req.Header.Set("New-Api-User", tc.hint)
		out := httptest.NewRecorder()
		router.ServeHTTP(out, req)
		assert.Equal(t, tc.status, out.Code, out.Body.String())
	}
	revoked, err := model.RevokeUserSession(user.Id, identity.SessionID, "test logout")
	require.NoError(t, err)
	require.True(t, revoked)
	req := httptest.NewRequest(http.MethodGet, "/api/gpt-quota/self", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	out := httptest.NewRecorder()
	router.ServeHTTP(out, req)
	assert.Equal(t, http.StatusUnauthorized, out.Code)
	req = httptest.NewRequest(http.MethodPost, "/api/gpt-quota/self/refresh", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	out = httptest.NewRecorder()
	router.ServeHTTP(out, req)
	assert.Equal(t, http.StatusUnauthorized, out.Code)
	// A fresh session for a disabled user must also fail before reading cached data.
	bundle, err := service.CreateLoginSession(user.Id, "password", "127.0.0.1", "quota-test")
	require.NoError(t, err)
	require.NoError(t, model.DB.Model(user).Update("status", common.UserStatusDisabled).Error)
	require.NoError(t, model.PublishUserAuthCache(user.Id))
	req = httptest.NewRequest(http.MethodGet, "/api/gpt-quota/self", nil)
	req.Header.Set("Authorization", "Bearer "+bundle.AccessToken)
	out = httptest.NewRecorder()
	router.ServeHTTP(out, req)
	assert.Equal(t, http.StatusUnauthorized, out.Code)
}

// TestGPTQuotaAdminGate validates the real middleware and the target's persisted
// binding. Reading a user must never reveal credentials or trigger an official probe.
func TestGPTQuotaAdminGate(t *testing.T) {
	actor, identity := setupSecurityEnrollmentTest(t)
	require.NoError(t, model.DB.AutoMigrate(&model.VM2APIEnrollment{}))
	token, _, err := service.IssueAccessToken(identity)
	require.NoError(t, err)
	target := &model.User{Username: "quota-target", AffCode: "quota-target", Group: "target-group", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Password: "not-returned"}
	require.NoError(t, model.DB.Create(target).Error)
	deleted := &model.User{Username: "deleted-target", AffCode: "deleted-target", Group: "target-group"}
	require.NoError(t, model.DB.Create(deleted).Error)
	require.NoError(t, model.DB.Delete(deleted).Error)
	reads := 0
	panel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/panel/login" {
			fmt.Fprint(w, `{"ok":true,"token":"private-panel-token","role":"admin","expires_in":3600}`)
			return
		}
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/api/panel/vms/private-slot", r.URL.Path)
		reads++
		fmt.Fprintf(w, `{"ok":true,"data":{"vm":{"id":"private-slot","codex_usage":{"snapshot":{"updated_at":%q},"windows":[{"id":"7d","used_percent":39,"reset_at":"2099-01-01T00:00:00Z"}]}}}}`, time.Now().UTC().Format(time.RFC3339))
	}))
	defer panel.Close()
	svc, err := gptquota.New(gptquota.Config{PanelURL: panel.URL + "/api/panel", Login: map[string]string{"username": "test", "password": "private-password"}, Users: map[string]gptquota.Binding{strconv.Itoa(target.Id): {Username: target.Username, Group: target.Group, Slot: "private-slot", Label: "Target"}}})
	require.NoError(t, err)
	old := quotaService
	quotaService = func() (*gptquota.Service, error) { return svc, nil }
	t.Cleanup(func() { quotaService = old })
	router := gin.New()
	router.GET("/api/gpt-quota/users/:id", middleware.DisableCache(), middleware.AdminAuth(), GetGPTQuotaUser)
	// Use the existing session first as an ordinary user, then promote the persisted
	// role. This proves role checks do not trust a client claim or a stale JWT role.
	request := func(path, bearer, hint string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/gpt-quota/users/"+path, nil)
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		req.Header.Set("New-Api-User", hint)
		out := httptest.NewRecorder()
		router.ServeHTTP(out, req)
		return out
	}
	targetID := strconv.Itoa(target.Id)
	assert.Equal(t, 401, request(targetID, "", "").Code)
	assert.Equal(t, 401, request(targetID, "forged", "").Code)
	assert.Equal(t, 403, request(targetID, token, "").Code)
	assert.Zero(t, reads)
	require.NoError(t, model.DB.Model(actor).Update("role", common.RoleAdminUser).Error)
	require.NoError(t, model.PublishUserAuthCache(actor.Id))
	for _, tc := range []struct {
		path, hint string
		status     int
	}{
		{targetID, strconv.Itoa(actor.Id), 200},
		{strconv.Itoa(actor.Id), "", 200},
		{"999999", "", 404}, {"0", "", 404}, {"-1", "", 404}, {"01", "", 404},
		{strconv.Itoa(deleted.Id), "", 404},
		{targetID + "?slot=other", "", 404},
		{targetID, targetID, 401},
	} {
		out := request(tc.path, token, tc.hint)
		assert.Equal(t, tc.status, out.Code, out.Body.String())
		assert.NotContains(t, out.Body.String(), "private-")
		assert.NotContains(t, out.Body.String(), "not-returned")
		if tc.path == targetID && tc.status == 200 {
			assert.Contains(t, out.Body.String(), `"remaining_percent":61`)
			assert.Contains(t, out.Header().Get("Cache-Control"), "no-store")
		}
	}
	assert.Equal(t, 1, reads)
	// A renamed/moved target must lose its binding, even if a cached snapshot exists.
	require.NoError(t, model.DB.Model(target).Update("group", "different-group").Error)
	out := request(targetID, token, "")
	assert.Equal(t, 200, out.Code)
	assert.Contains(t, out.Body.String(), `"bound":false`)
	assert.Equal(t, 1, reads)
	revoked, err := model.RevokeUserSession(actor.Id, identity.SessionID, "test logout")
	require.NoError(t, err)
	require.True(t, revoked)
	assert.Equal(t, 401, request(targetID, token, "").Code)
	bundle, err := service.CreateLoginSession(actor.Id, "password", "127.0.0.1", "quota-admin-test")
	require.NoError(t, err)
	require.NoError(t, model.DB.Model(actor).Update("status", common.UserStatusDisabled).Error)
	require.NoError(t, model.PublishUserAuthCache(actor.Id))
	assert.Equal(t, 401, request(targetID, bundle.AccessToken, "").Code)
	assert.Equal(t, 1, reads)
}

// TestGPTQuotaCompletedBindingsSeparateInstances 通过真实 UserAuth 验证开户绑定生效、同名槽位跨实例隔离与改组失效。
func TestGPTQuotaCompletedBindingsSeparateInstances(t *testing.T) {
	first, firstIdentity := setupSecurityEnrollmentTest(t)
	require.NoError(t, model.DB.AutoMigrate(&model.VM2APIEnrollment{}))
	second := &model.User{Username: "second-user", AffCode: "second-user", Group: "second-group", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, AuthVersion: 1}
	require.NoError(t, model.DB.Create(second).Error)
	require.NoError(t, model.PublishUserAuthCache(second.Id))
	firstToken, _, err := service.IssueAccessToken(firstIdentity)
	require.NoError(t, err)
	secondSession, err := service.CreateLoginSession(second.Id, "password", "127.0.0.1", "quota-second")
	require.NoError(t, err)
	var firstReads, secondReads atomic.Int64
	makePanel := func(used int, reads *atomic.Int64) *httptest.Server {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/panel/login" {
				fmt.Fprint(w, `{"token":"private-token"}`)
				return
			}
			assert.Equal(t, "/api/panel/vms/same-slot", r.URL.Path)
			assert.Equal(t, http.MethodGet, r.Method)
			reads.Add(1)
			fmt.Fprintf(w, `{"ok":true,"data":{"vm":{"id":"same-slot","codex_usage":{"snapshot":{"updated_at":%q},"windows":[{"id":"5h","used_percent":%d,"reset_at":"2099-01-01T00:00:00Z"}]}}}}`, time.Now().UTC().Format(time.RFC3339), used)
		}))
		t.Cleanup(server.Close)
		return server
	}
	firstPanel, secondPanel := makePanel(11, &firstReads), makePanel(72, &secondReads)
	svc, err := gptquota.New(gptquota.Config{Instances: map[string]gptquota.BackendConfig{
		"cc":  {PanelURL: firstPanel.URL + "/api/panel", Login: map[string]string{"username": "admin", "password": "private-cc-password"}},
		"bwh": {PanelURL: secondPanel.URL + "/api/panel", Login: map[string]string{"username": "admin", "password": "private-bwh-password"}},
	}})
	require.NoError(t, err)
	old := quotaService
	quotaService = func() (*gptquota.Service, error) { return svc, nil }
	t.Cleanup(func() { quotaService = old })
	firstJob := model.VM2APIEnrollment{RequestID: uuid.NewString(), ActorID: first.Id, Username: first.Username, DisplayName: "First", InstanceID: "cc", SlotID: "same-slot", Group: first.Group, Status: model.VM2APIEnrollmentPending, UserID: first.Id}
	secondJob := model.VM2APIEnrollment{RequestID: uuid.NewString(), ActorID: first.Id, Username: second.Username, DisplayName: "Second", InstanceID: "bwh", SlotID: "same-slot", Group: second.Group, Status: model.VM2APIEnrollmentCompleted, UserID: second.Id}
	require.NoError(t, model.DB.Create(&firstJob).Error)
	require.NoError(t, model.DB.Create(&secondJob).Error)
	router := gin.New()
	router.GET("/api/gpt-quota/self", middleware.DisableCache(), middleware.UserAuth(), GetGPTQuotaSelf)
	request := func(token, hint string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/gpt-quota/self", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("New-Api-User", hint)
		out := httptest.NewRecorder()
		router.ServeHTTP(out, req)
		return out
	}
	pending := request(firstToken, "")
	require.Equal(t, 200, pending.Code, pending.Body.String())
	assert.Contains(t, pending.Body.String(), `"bound":false`)
	assert.Zero(t, firstReads.Load())
	require.NoError(t, model.DB.Model(&firstJob).Update("status", model.VM2APIEnrollmentCompleted).Error)
	for _, tc := range []struct {
		token     string
		remaining int
	}{{firstToken, 89}, {secondSession.AccessToken, 28}, {firstToken, 89}} {
		out := request(tc.token, "")
		require.Equal(t, 200, out.Code, out.Body.String())
		assert.Contains(t, out.Body.String(), fmt.Sprintf(`"remaining_percent":%d`, tc.remaining))
		assert.NotContains(t, out.Body.String(), "private-")
		assert.NotContains(t, out.Body.String(), "same-slot")
	}
	assert.Equal(t, int64(1), firstReads.Load())
	assert.Equal(t, int64(1), secondReads.Load())
	assert.Equal(t, 401, request(firstToken, strconv.Itoa(second.Id)).Code)
	require.NoError(t, model.DB.Model(first).Update("group", "moved-group").Error)
	require.NoError(t, model.PublishUserAuthCache(first.Id))
	moved := request(firstToken, "")
	require.Equal(t, 200, moved.Code, moved.Body.String())
	assert.Contains(t, moved.Body.String(), `"bound":false`)
	assert.Equal(t, int64(1), firstReads.Load())
}

// TestVM2APIOnboardingRootGate 使用真实 RootAuth 检查角色、会话和身份提示，普通用户不能访问开户入口。
func TestVM2APIOnboardingRootGate(t *testing.T) {
	actor, identity := setupSecurityEnrollmentTest(t)
	require.NoError(t, model.DB.AutoMigrate(&model.VM2APIEnrollment{}))
	token, _, err := service.IssueAccessToken(identity)
	require.NoError(t, err)
	registry, err := vm2api.New(vm2api.Config{})
	require.NoError(t, err)
	old := onboardingRegistry
	onboardingRegistry = func() (*vm2api.Registry, error) { return registry, nil }
	t.Cleanup(func() { onboardingRegistry = old })
	own := model.VM2APIEnrollment{RequestID: uuid.NewString(), ActorID: actor.Id, Username: "own-user", DisplayName: "Own", InstanceID: "cc", SlotID: "own-slot", Group: "own-group", Status: model.VM2APIEnrollmentPending, PasswordHash: "never-show-hash", LeaseToken: "never-show-lease"}
	other := model.VM2APIEnrollment{RequestID: uuid.NewString(), ActorID: actor.Id + 1, Username: "other-user", DisplayName: "Other", InstanceID: "cc", SlotID: "other-slot", Group: "other-group", Status: model.VM2APIEnrollmentPending}
	require.NoError(t, model.DB.Create(&own).Error)
	require.NoError(t, model.DB.Create(&other).Error)
	router := gin.New()
	router.GET("/api/vm2api-onboarding/options", middleware.DisableCache(), middleware.RootAuth(), GetVM2APIOnboardingOptions)
	router.GET("/api/vm2api-onboarding/accounts/:request_id", middleware.DisableCache(), middleware.RootAuth(), GetVM2APIAccount)
	request := func(path, bearer, hint string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		req.Header.Set("New-Api-User", hint)
		out := httptest.NewRecorder()
		router.ServeHTTP(out, req)
		return out
	}
	paths := []string{"/api/vm2api-onboarding/options", "/api/vm2api-onboarding/accounts/" + own.RequestID}
	for _, path := range paths {
		assert.Equal(t, 401, request(path, "", "").Code)
		assert.Equal(t, 401, request(path, "forged", "").Code)
		assert.Equal(t, 403, request(path, token, "").Code)
	}
	require.NoError(t, model.DB.Model(actor).Update("role", common.RoleAdminUser).Error)
	require.NoError(t, model.PublishUserAuthCache(actor.Id))
	assert.Equal(t, 403, request(paths[0], token, "").Code)
	require.NoError(t, model.DB.Model(actor).Update("role", common.RoleRootUser).Error)
	require.NoError(t, model.PublishUserAuthCache(actor.Id))
	for _, path := range paths {
		assert.Equal(t, 401, request(path, token, strconv.Itoa(actor.Id+1)).Code)
		out := request(path, token, "")
		assert.Equal(t, 200, out.Code, out.Body.String())
		assert.Contains(t, out.Header().Get("Cache-Control"), "no-store")
		assert.NotContains(t, out.Body.String(), "never-show")
	}
	assert.Equal(t, 404, request("/api/vm2api-onboarding/accounts/"+other.RequestID, token, "").Code)
	revoked, err := model.RevokeUserSession(actor.Id, identity.SessionID, "test logout")
	require.NoError(t, err)
	require.True(t, revoked)
	assert.Equal(t, 401, request(paths[0], token, "").Code)
}

// TestVM2APIOnboardingRecoverableAccount exercises the real root/session gate,
// remote pool/key recovery, one local commit and per-user route ownership.
func TestVM2APIOnboardingRecoverableAccount(t *testing.T) {
	oldOptions, oldRatios := common.OptionMap, ratio_setting.GroupRatio2JSONString()
	common.OptionMap = map[string]string{}
	t.Cleanup(func() {
		common.OptionMap = oldOptions
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(oldRatios))
	})
	actor, identity := setupSecurityEnrollmentTest(t)
	require.NoError(t, model.DB.AutoMigrate(&model.VM2APIEnrollment{}, &model.VM2APISlotDraft{}, &model.Channel{}, &model.Ability{}))
	require.NoError(t, model.DB.Model(actor).Update("role", common.RoleRootUser).Error)
	require.NoError(t, model.PublishUserAuthCache(actor.Id))
	token, _, err := service.IssueAccessToken(identity)
	require.NoError(t, err)
	requestID := uuid.NewString()
	stableName := "na-" + requestID
	var poolCreated, keyCreated, upstreamUnavailable atomic.Bool
	var poolWrites, keyWrites atomic.Int32
	upstreamUnavailable.Store(true)
	panel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/panel/login" {
			fmt.Fprint(w, `{"ok":true,"token":"private-panel-token","role":"admin","expires_in":3600}`)
			return
		}
		assert.Equal(t, "Bearer private-panel-token", r.Header.Get("Authorization"))
		switch r.Method + " " + r.URL.Path {
		case "GET /api/panel/vms/free-slot":
			fmt.Fprint(w, `{"ok":true,"data":{"vm":{"id":"free-slot","name":"Free account","family":"codex","runtime_type":"docker","status":"running","has_token":true,"schedulable":true}}}`)
		case "GET /api/panel/vm-pools":
			if poolCreated.Load() {
				fmt.Fprintf(w, `{"ok":true,"data":{"pools":[{"id":"pool-new","name":%q,"enabled":true,"vm_ids":["free-slot"]}]}}`, stableName)
			} else {
				fmt.Fprint(w, `{"ok":true,"data":{"pools":[]}}`)
			}
		case "POST /api/panel/vm-pools":
			poolWrites.Add(1)
			poolCreated.Store(true)
			// The remote write succeeded but its response was lost. Recovery must
			// read by stable name, not allocate another pool.
			w.WriteHeader(http.StatusBadGateway)
			fmt.Fprint(w, `{"error":"private-upstream-body"}`)
		case "GET /api/panel/api-keys":
			if upstreamUnavailable.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
				fmt.Fprint(w, `{"error":"private-upstream-body"}`)
			} else if keyCreated.Load() {
				fmt.Fprintf(w, `{"ok":true,"keys":[{"id":"key-new","name":%q,"status":"active","vm_pool_id":"pool-new"}]}`, stableName)
			} else {
				fmt.Fprint(w, `{"ok":true,"keys":[]}`)
			}
		case "POST /api/panel/api-keys":
			keyWrites.Add(1)
			keyCreated.Store(true)
			fmt.Fprint(w, `{"ok":true}`)
		case "POST /api/panel/api-keys/key-new/reveal":
			fmt.Fprint(w, `{"ok":true,"id":"key-new","key":"private-user-upstream-key"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer panel.Close()
	registry, err := vm2api.New(vm2api.Config{Instances: []vm2api.InstanceConfig{{ID: "test-vps", Name: "Test VPS", BaseURL: panel.URL, Username: "root", Password: "private-panel-password", Models: []string{"gpt-test"}}}})
	require.NoError(t, err)
	previousRegistry, previousQuota := onboardingRegistry, configuredQuota
	onboardingRegistry = func() (*vm2api.Registry, error) { return registry, nil }
	configuredQuota = func() (gptquota.Config, error) { return gptquota.Config{}, nil }
	previousSecure, previousOrigins := common.SessionCookieSecure, common.SessionCookieTrustedURLs
	common.SessionCookieSecure, common.SessionCookieTrustedURLs = true, nil
	t.Cleanup(func() {
		onboardingRegistry, configuredQuota = previousRegistry, previousQuota
		common.SessionCookieSecure, common.SessionCookieTrustedURLs = previousSecure, previousOrigins
	})
	router := gin.New()
	group := router.Group("/api/vm2api/onboarding", middleware.RootAuth(), middleware.DisableCache())
	group.POST("", middleware.SessionCookieOriginGuard(), CreateVM2APIAccount)
	group.GET("/:request_id", GetVM2APIAccount)
	group.POST("/:request_id/retry", middleware.SessionCookieOriginGuard(), RetryVM2APIAccount)
	request := func(method, path string, body any, origin string) *httptest.ResponseRecorder {
		raw, err := common.Marshal(body)
		require.NoError(t, err)
		req := httptest.NewRequest(method, "http://portal.test/api/vm2api/onboarding"+path, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Origin", origin)
		out := httptest.NewRecorder()
		router.ServeHTTP(out, req)
		for _, secret := range []string{"private-user-upstream-key", "private-panel-password", "private-upstream-body", "correct customer password", "password_hash", "lease_token"} {
			assert.NotContains(t, out.Body.String(), secret)
		}
		return out
	}
	input := onboardingRequest{RequestID: requestID, Username: "new-customer", DisplayName: "New customer", Password: "correct customer password", InstanceID: "test-vps", SlotID: "free-slot", Quota: 1000000}
	assert.Equal(t, http.StatusForbidden, request("POST", "", input, "https://evil.test").Code)
	assert.Zero(t, poolWrites.Load())
	// A public dictionary entry must fail before a durable reservation exists;
	// otherwise the administrator could not correct the customer's password.
	weak := input
	weak.Password = "YfDbUfNjH10305070"
	assert.Equal(t, http.StatusBadRequest, request("POST", "", weak, "http://portal.test").Code)
	assert.Equal(t, http.StatusNotFound, request("GET", "/"+requestID, nil, "http://portal.test").Code)
	assert.Zero(t, poolWrites.Load())
	failed := request("POST", "", input, "http://portal.test")
	require.Equal(t, http.StatusServiceUnavailable, failed.Code, failed.Body.String())
	job, err := model.GetVM2APIEnrollment(requestID, actor.Id)
	require.NoError(t, err)
	assert.Equal(t, model.VM2APIEnrollmentFailed, job.Status)
	assert.True(t, common.ValidatePasswordAndHash(input.Password, job.PasswordHash))
	var count int64
	require.NoError(t, model.DB.Model(&model.User{}).Where("username = ?", input.Username).Count(&count).Error)
	assert.Zero(t, count)
	assert.Equal(t, int32(1), poolWrites.Load())
	assert.Equal(t, http.StatusOK, request("GET", "/"+requestID, nil, "http://portal.test").Code)
	// Replay cannot change the password or redirect this operation to another slot.
	changed := input
	changed.Password = "another customer password"
	assert.Equal(t, http.StatusConflict, request("POST", "", changed, "http://portal.test").Code)
	upstreamUnavailable.Store(false)
	completed := request("POST", "/"+requestID+"/retry", nil, "http://portal.test")
	require.Equal(t, http.StatusOK, completed.Code, completed.Body.String())
	job, err = model.GetVM2APIEnrollment(requestID, actor.Id)
	require.NoError(t, err)
	assert.Equal(t, model.VM2APIEnrollmentCompleted, job.Status)
	assert.Empty(t, job.PasswordHash)
	var user model.User
	require.NoError(t, model.DB.First(&user, job.UserID).Error)
	assert.Equal(t, common.RoleCommonUser, user.Role)
	assert.Equal(t, input.Quota, user.Quota)
	assert.True(t, common.ValidatePasswordAndHash(input.Password, user.Password))
	var channel model.Channel
	require.NoError(t, model.DB.First(&channel, job.ChannelID).Error)
	assert.Equal(t, user.Group, channel.Group)
	assert.Equal(t, panel.URL, *channel.BaseURL)
	assert.Equal(t, "private-user-upstream-key", channel.Key)
	var ability model.Ability
	require.NoError(t, model.DB.Where("channel_id = ?", channel.Id).First(&ability).Error)
	assert.Equal(t, user.Group, ability.Group)
	assert.Equal(t, "gpt-test", ability.Model)
	assert.True(t, ability.Enabled)
	assert.Equal(t, http.StatusOK, request("POST", "", input, "http://portal.test").Code)
	assert.Equal(t, http.StatusOK, request("POST", "/"+requestID+"/retry", nil, "http://portal.test").Code)
	assert.Equal(t, int32(1), poolWrites.Load())
	assert.Equal(t, int32(1), keyWrites.Load())
	changed = input
	changed.RequestID, changed.Username = uuid.NewString(), "second-customer"
	assert.Equal(t, http.StatusConflict, request("POST", "", changed, "http://portal.test").Code)
	require.NoError(t, model.DB.Model(&model.Channel{}).Count(&count).Error)
	assert.Equal(t, int64(1), count)
}
