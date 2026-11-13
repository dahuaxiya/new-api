package middleware

import (
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
)

// SiteBoundary 在鉴权及业务处理前关闭另一站的接口，包括支付回调和管理员接口。
// 使用 Gin 已匹配的路由模板，避免尾斜杠、参数或路径转义绕过业务分类。
func SiteBoundary() gin.HandlerFunc {
	return func(c *gin.Context) {
		path := c.FullPath()
		hosting := strings.HasPrefix(path, "/api/gpt-hosting/") ||
			strings.HasPrefix(path, "/api/gpt-quota/") ||
			strings.HasPrefix(path, "/api/vm2api/onboarding") || path == "/api/gpt-hosting/admin"
		retail := strings.HasPrefix(path, "/api/retail/") ||
			strings.HasPrefix(path, "/api/wallet/alipay/") || path == "/api/user/alipay/query"
		if (common.IsRetailSite() && hosting) || (!common.IsRetailSite() && retail) {
			c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"success": false, "message": "Not found"})
			return
		}
		c.Next()
	}
}
