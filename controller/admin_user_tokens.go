package controller

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// adminTokenSession 仅接受浏览器会话，避免已有 PAT 的用户读取权限升级为密钥读取权限。
func adminTokenSession(c *gin.Context) (service.AuthIdentity, bool) {
	c.Header("Cache-Control", "no-store")
	identity, ok := middleware.GetSessionAuthIdentity(c)
	if !ok || c.GetBool("use_access_token") || strings.HasPrefix(identity.SessionID, "pat:") || c.GetInt("role") < common.RoleAdminUser {
		common.ApiErrorStatus(c, http.StatusForbidden, common.NewMessage("You do not have permission to manage this user's API keys"))
		return service.AuthIdentity{}, false
	}
	return identity, true
}

// writeAdminTokenError 对权限和不存在统一响应，内部数据库错误不向客户端透传。
func writeAdminTokenError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, model.ErrAdminTokenForbidden), errors.Is(err, gorm.ErrRecordNotFound), errors.Is(err, model.ErrUserSessionInactive):
		common.ApiErrorStatus(c, http.StatusForbidden, common.NewMessage("You do not have permission to manage this user's API keys"))
	case errors.Is(err, model.ErrAdminTokenUnusable):
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("An expired or exhausted API key cannot be enabled"))
	default:
		_ = c.Error(err)
		common.ApiErrorStatus(c, http.StatusInternalServerError, common.NewMessage("Please try again later."))
	}
}

// GetAdminUserTokenCounts 为当前用户列表一次加载计数，最多接受 100 个用户编号。
func GetAdminUserTokenCounts(c *gin.Context) {
	identity, ok := adminTokenSession(c)
	if !ok {
		return
	}
	values := strings.Split(c.Query("ids"), ",")
	if len(values) > 100 {
		common.ApiErrorT(c, "Invalid parameters")
		return
	}
	ids := make([]int, 0, len(values))
	for _, value := range values {
		id, err := strconv.Atoi(value)
		if err != nil || id <= 0 {
			common.ApiErrorT(c, "Invalid parameters")
			return
		}
		ids = append(ids, id)
	}
	counts, err := model.AdminUserTokenCounts(identity.UserID, ids)
	if err != nil {
		writeAdminTokenError(c, err)
		return
	}
	common.ApiSuccess(c, counts)
}

// GetAdminUserTokens 分页返回掩码列表，不会读取上游账号凭据。
func GetAdminUserTokens(c *gin.Context) {
	identity, ok := adminTokenSession(c)
	if !ok {
		return
	}
	userID, err := strconv.Atoi(c.Param("id"))
	page, pageErr := strconv.Atoi(c.DefaultQuery("p", "1"))
	size, sizeErr := strconv.Atoi(c.DefaultQuery("page_size", "10"))
	status, statusErr := strconv.Atoi(c.DefaultQuery("status", "0"))
	keyword := strings.TrimSpace(c.Query("keyword"))
	if err != nil || userID <= 0 || pageErr != nil || page < 1 || page > 100000 || sizeErr != nil || size < 1 || size > 50 || statusErr != nil || status < 0 || status > 4 || len(keyword) > 100 {
		common.ApiErrorT(c, "Invalid parameters")
		return
	}
	items, total, err := model.ListAdminUserTokens(identity.UserID, userID, (page-1)*size, size, status, keyword)
	if err != nil {
		writeAdminTokenError(c, err)
		return
	}
	common.ApiSuccess(c, gin.H{"items": items, "total": total})
}

// ManageAdminUserToken 每次查看、复制或启停都绑定用户、密钥和动作消耗一次证明。
// 审计只记录编号和结果，即使失败也不得把请求体或真实 Key 写进日志。
func ManageAdminUserToken(c *gin.Context) {
	success := false
	params := map[string]any{"success": false}
	defer func() {
		params["success"] = success
		recordUserSecurityAudit(c, c.GetInt("id"), "user.token.manage", params)
		markAuditLogged(c)
	}()
	identity, ok := adminTokenSession(c)
	if !ok {
		return
	}
	userID, err := strconv.Atoi(c.Param("id"))
	tokenID, tokenErr := strconv.Atoi(c.Param("token_id"))
	var request struct {
		Action string `json:"action"`
	}
	if err != nil || tokenErr != nil || userID <= 0 || tokenID <= 0 || common.DecodeJson(c.Request.Body, &request) != nil {
		common.ApiErrorT(c, "Invalid parameters")
		return
	}
	// 用路由区分读取与写入，避免查看证明被拿来触发启停。
	status := 0
	if c.Request.Method == http.MethodPost && (request.Action == "view" || request.Action == "copy") {
	} else if c.Request.Method == http.MethodPatch && request.Action == "enable" {
		status = common.TokenStatusEnabled
	} else if c.Request.Method == http.MethodPatch && request.Action == "disable" {
		status = common.TokenStatusDisabled
	} else {
		common.ApiErrorT(c, "Invalid parameters")
		return
	}
	params["target_user_id"], params["token_id"], params["action"] = userID, tokenID, request.Action
	if err := model.CheckAdminUserToken(identity.UserID, userID, tokenID); err != nil {
		writeAdminTokenError(c, err)
		return
	}
	authorization := requireAdminUserProof(c, service.VerificationScopeAdminUserToken, service.AdminUserTokenContext{UserID: userID, TokenID: tokenID, Action: request.Action})
	if authorization == nil {
		return
	}
	params["verification_method"] = authorization.Method
	key, err := model.AdminUserTokenOperation(identity, userID, tokenID, status)
	if err != nil {
		writeAdminTokenError(c, err)
		return
	}
	success = true
	if status == 0 {
		common.ApiSuccess(c, gin.H{"key": "sk-" + key})
		return
	}
	common.ApiSuccess(c, gin.H{"status": status})
}
