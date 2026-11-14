package middleware

import (
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
)

// SiteIdentityBoundary 在统一身份的消费站关闭独立注册、恢复和身份变更，避免通过隐藏接口创建第二套凭据。
// @return 只限制身份操作；余额、API Key、会话管理和本地 root 恢复登录保留。
func SiteIdentityBoundary() gin.HandlerFunc {
	return func(c *gin.Context) {
		cfg := common.SiteSSO()
		if cfg == nil || !cfg.Consumer {
			c.Next()
			return
		}
		path := c.FullPath()
		blocked := path == "/api/user/register" || path == "/api/user/reset" || path == "/api/reset_password" || path == "/api/verification" ||
			strings.HasPrefix(path, "/api/user/passkey/") || strings.HasPrefix(path, "/api/user/login/passkey/") ||
			(strings.HasPrefix(path, "/api/user/2fa/") && c.Request.Method != http.MethodGet) ||
			(strings.HasPrefix(path, "/api/oauth/") && path != "/api/oauth/state" && path != "/api/oauth/:provider") ||
			(path == "/api/user/self" && c.Request.Method == http.MethodDelete) ||
			(strings.HasPrefix(path, "/api/user/oauth/bindings") && c.Request.Method != http.MethodGet) ||
			(path == "/api/user/" && c.Request.Method == http.MethodPost)
		if blocked {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"success": false, "message": "Manage your unified account on the hosting site."})
			return
		}
		c.Next()
	}
}
