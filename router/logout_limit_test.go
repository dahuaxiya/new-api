package router

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// TestLogoutSurvivesLoginLimit checks the real route chain without consuming any production quota.
func TestLogoutSurvivesLoginLimit(t *testing.T) {
	old, num, redis := common.CriticalRateLimitEnable, common.CriticalRateLimitNum, common.RedisEnabled
	apiNum, apiDuration := common.GlobalApiRateLimitNum, common.GlobalApiRateLimitDuration
	common.GlobalApiRateLimitNum, common.GlobalApiRateLimitDuration = 360, 180
	t.Cleanup(func() { common.GlobalApiRateLimitNum, common.GlobalApiRateLimitDuration = apiNum, apiDuration })
	common.CriticalRateLimitEnable, common.CriticalRateLimitNum, common.RedisEnabled = true, 1, false
	t.Cleanup(func() {
		common.CriticalRateLimitEnable, common.CriticalRateLimitNum, common.RedisEnabled = old, num, redis
	})
	r := gin.New()
	SetApiRouter(r)
	r.POST("/test/login-budget", middleware.CriticalRateLimit(), func(c *gin.Context) { c.Status(http.StatusNoContent) })
	post := func(path string) int {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, path, nil)
		req.RemoteAddr = "192.0.2.91:1111"
		req.Header.Set("Origin", "http://example.com")
		r.ServeHTTP(w, req)
		return w.Code
	}
	require.Equal(t, http.StatusNoContent, post("/test/login-budget"))
	require.Equal(t, http.StatusTooManyRequests, post("/test/login-budget"))
	require.Equal(t, http.StatusUnauthorized, post("/api/user/auth/refresh"))
	require.Equal(t, http.StatusOK, post("/api/user/auth/logout"))
}
