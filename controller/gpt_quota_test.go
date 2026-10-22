package controller

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/gptquota"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGPTQuotaSelfSessionGate exercises the actual session middleware, including
// logout revocation and invalid identity hints, before any panel lookup occurs.
func TestGPTQuotaSelfSessionGate(t *testing.T) {
	user, identity := setupSecurityEnrollmentTest(t)
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
			fmt.Fprint(w, `{"token":"private-panel-token"}`)
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
