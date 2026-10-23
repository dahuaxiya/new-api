package controller

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// TestGPTHostingSessionBoundary 验证真实登录会话隔离、跨站拒绝、名额停用以及敏感动作所需证明。
func TestGPTHostingSessionBoundary(t *testing.T) {
	user, identity := setupSecurityEnrollmentTest(t)
	t.Setenv("GPT_HOSTING_ENABLED", "true")
	require.NoError(t, model.DB.AutoMigrate(&model.GPTHostingGrant{}, &model.GPTHostingBinding{}, &model.GPTHostingInventory{}))
	now := time.Now().Unix()
	require.NoError(t, model.DB.Create(&model.GPTHostingGrant{UserID: user.Id, StartsAt: now - 1, EndsAt: now + 3600, Enabled: true, Group: "acct_fixture"}).Error)
	require.NoError(t, model.DB.Create(&model.GPTHostingBinding{UserID: user.Id, BindingID: uuid.NewString(), State: "unbound"}).Error)
	token, _, e := service.IssueAccessToken(identity)
	require.NoError(t, e)
	r := gin.New()
	r.GET("/self", middleware.UserAuth(), GetGPTHostingSelf)
	r.GET("/admin", middleware.RootAuth(), GetGPTHostingAdmin)
	r.POST("/login", middleware.UserAuth(), middleware.SessionCookieOriginGuard(), StartGPTHostingLogin)
	r.POST("/unbind", middleware.UserAuth(), middleware.SessionCookieOriginGuard(), UnbindGPTHosting)
	request := func(method, path, auth, origin string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "http://portal.test"+path, bytes.NewBufferString(`{}`))
		req.Header.Set("Content-Type", "application/json")
		if auth != "" {
			req.Header.Set("Authorization", "Bearer "+auth)
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	own := request("GET", "/self?user_id=999", token, "")
	require.Equal(t, 200, own.Code, own.Body.String())
	require.Contains(t, own.Body.String(), `"binding_id"`)
	for _, private := range []string{"account_hash", "subject_hash", "slot_id", "instance_id", "operation_token", "key_id"} {
		require.NotContains(t, own.Body.String(), `"`+private+`"`)
	}
	require.Equal(t, 401, request("GET", "/self", "", "").Code)
	require.Equal(t, 403, request("GET", "/admin", token, "").Code)
	require.Equal(t, 403, request("POST", "/login", token, "https://untrusted.test").Code)
	for _, action := range []string{"/login", "/unbind"} {
		denied := request("POST", action, token, "http://portal.test")
		require.NotEqual(t, http.StatusOK, denied.Code, denied.Body.String())
		var binding model.GPTHostingBinding
		require.NoError(t, model.DB.First(&binding, "user_id = ?", user.Id).Error)
		require.Equal(t, "unbound", binding.State, "missing proof must not reserve or clear")
	}
	require.NoError(t, model.DB.Model(&model.GPTHostingGrant{}).Where("user_id = ?", user.Id).Update("ends_at", now-1).Error)
	denied := request("POST", "/login", token, "http://portal.test")
	require.Contains(t, denied.Body.String(), "hosting_expired")
	require.Equal(t, common.RoleCommonUser, user.Role)
}
