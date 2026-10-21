package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"

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
