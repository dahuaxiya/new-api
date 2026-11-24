package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/require"
)

// TestRetailLoginStartWindow verifies that a full critical bucket cannot hide the
// password form and that rejected requests do not extend the one-minute window.
func TestRetailLoginStartWindow(t *testing.T) {
	t.Setenv("SITE_MODE", "retail")
	t.Setenv("SITE_SSO_ISSUER_URL", "https://issuer.example")
	db := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: db.Addr()})
	prev, enabled, critical := common.RDB, common.RedisEnabled, common.CriticalRateLimitEnable
	common.RDB, common.RedisEnabled, common.CriticalRateLimitEnable = client, true, true
	t.Cleanup(func() {
		common.RDB, common.RedisEnabled, common.CriticalRateLimitEnable = prev, enabled, critical
		_ = client.Close()
	})
	router := gin.New()
	router.POST("/oauth/state", OAuthStateRateLimit(), func(c *gin.Context) {
		value, ok := c.Get("retailOAuthStateRequest")
		require.True(t, ok)
		require.Equal(t, "site-sso", value.(oauthStateRequest).Provider)
		c.Status(http.StatusNoContent)
	})
	send := func(ip, intent string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/oauth/state", strings.NewReader(`{"provider":"site-sso","intent":"`+intent+`"}`))
		r.RemoteAddr = ip + ":4321"
		router.ServeHTTP(w, r)
		return w
	}
	const ip = "192.0.2.47"
	for range common.CriticalRateLimitNum {
		require.Equal(t, 204, send(ip, "verify").Code)
	}
	require.Equal(t, 429, send(ip, "verify").Code)
	for range 30 {
		require.Equal(t, 204, send(ip, "login").Code)
	}
	w := send(ip, "login")
	require.Equal(t, 429, w.Code)
	require.Equal(t, "60", w.Header().Get("Retry-After"))
	require.Equal(t, 204, send("192.0.2.48", "login").Code)
	db.FastForward(59 * time.Second)
	w = send(ip, "login")
	require.Equal(t, 429, w.Code)
	require.Equal(t, "1", w.Header().Get("Retry-After"))
	db.FastForward(time.Second)
	require.Equal(t, 204, send(ip, "login").Code)
}
