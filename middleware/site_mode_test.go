package middleware

import (
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// TestSiteBoundary 验证跨站业务在鉴权前关闭，回调、管理员和动态参数路由均不能执行处理器。
func TestSiteBoundary(t *testing.T) {
	routes := []struct{ method, path, request, site string }{
		{"GET", "/api/gpt-hosting/self", "/api/gpt-hosting/self", "hosting"},
		{"POST", "/api/gpt-hosting/payment/alipay/notify", "/api/gpt-hosting/payment/alipay/notify", "hosting"},
		{"GET", "/api/gpt-hosting/admin", "/api/gpt-hosting/admin", "hosting"},
		{"GET", "/api/gpt-quota/users/:id", "/api/gpt-quota/users/1", "hosting"},
		{"POST", "/api/vm2api/onboarding", "/api/vm2api/onboarding", "hosting"},
		{"GET", "/api/retail/admin/accounting", "/api/retail/admin/accounting", "retail"},
		{"POST", "/api/retail/admin/charges/:id/refund", "/api/retail/admin/charges/abc/refund", "retail"},
		{"POST", "/api/wallet/alipay/notify", "/api/wallet/alipay/notify", "retail"},
		{"POST", "/api/user/alipay/query", "/api/user/alipay/query", "retail"},
		{"POST", "/api/user/wallet", "/api/user/wallet", ""},
	}
	for _, mode := range []string{"hosting", "retail"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("SITE_MODE", mode)
			r := gin.New()
			r.Use(SiteBoundary())
			for _, route := range routes {
				r.Handle(route.method, route.path, func(c *gin.Context) { c.Status(204) })
			}
			for _, route := range routes {
				w := httptest.NewRecorder()
				r.ServeHTTP(w, httptest.NewRequest(route.method, route.request, nil))
				expected := 204
				if route.site != "" && route.site != mode {
					expected = 404
				}
				require.Equal(t, expected, w.Code, route.request)
			}
		})
	}
}

// TestSiteConfiguration 确认旧部署默认托管，新中转部署不能误挂托管凭据和授权配置。
func TestSiteConfiguration(t *testing.T) {
	keys := []string{"SITE_MODE", "GPT_HOSTING_ENABLED", "GPT_QUOTA_CONFIG", "VM2API_INSTANCES_CONFIG", "ALIPAY_HOSTING_CONFIG", "RETAIL_RELAY_CONFIG", "ALIPAY_WALLET_CONFIG"}
	for _, key := range keys {
		t.Setenv(key, "")
	}
	require.NoError(t, common.ValidateSiteMode())
	require.Equal(t, "hosting", common.SiteMode())
	t.Setenv("SITE_MODE", "unknown")
	require.Error(t, common.ValidateSiteMode())
	for _, mode := range []string{"hosting", "retail"} {
		t.Setenv("SITE_MODE", mode)
		require.NoError(t, common.ValidateSiteMode())
		invalid := []string{"RETAIL_RELAY_CONFIG", "ALIPAY_WALLET_CONFIG"}
		if mode == "retail" {
			invalid = []string{"GPT_HOSTING_ENABLED", "GPT_QUOTA_CONFIG", "VM2API_INSTANCES_CONFIG", "ALIPAY_HOSTING_CONFIG"}
		}
		for _, key := range invalid {
			t.Setenv(key, "true")
			require.Error(t, common.ValidateSiteMode(), key)
			t.Setenv(key, "")
		}
	}
}
