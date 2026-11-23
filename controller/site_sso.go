package controller

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"net/url"
	"regexp"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

var siteSSORandomPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

const siteSSOCookie = "__Host-new_api_retail_sso"

// siteSSOFlow 保存浏览器绑定和 PKCE 私密材料，浏览器只看到一次性 state。
type siteSSOFlow struct {
	BrowserHash string                       `json:"browser_hash"`
	Verifier    string                       `json:"verifier"`
	Identity    *service.AuthIdentity        `json:"identity,omitempty"`
	Binding     *service.VerificationBinding `json:"binding,omitempty"`
}

// startSiteSSO 使用现有 OAuth 入口和弹窗组件；仅启用登录和敏感操作验证，不开放自助合并账号。
// @param c 当前请求，Origin 必须等于消费站的配置地址。
// @param request 已解码的 OAuth 流请求。
func startSiteSSO(c *gin.Context, request oauthStateRequest) {
	cfg := common.SiteSSO()
	if cfg == nil || !cfg.Consumer || c.GetHeader("Origin") != cfg.ClientURL || (request.Intent != "login" && request.Intent != "verify") {
		writeAccountStatusError(c, http.StatusForbidden, "Verification flow expired")
		return
	}
	verifier, err := common.GenerateRandomCharsKey(43)
	if err != nil {
		writeAuthSessionError(c, err)
		return
	}
	nonce, err := common.GenerateRandomCharsKey(43)
	if err != nil {
		writeAuthSessionError(c, err)
		return
	}
	payload := siteSSOFlow{Verifier: verifier, BrowserHash: common.GenerateHMACWithKey([]byte(common.SessionSecret), nonce)}
	if request.Intent == "verify" {
		identity, ok := middleware.GetSessionAuthIdentity(c)
		if !ok {
			writeAuthSessionError(c, service.ErrAuthTokenInvalid)
			return
		}
		if _, err := service.RequireVerificationMethod(identity, request.Scope, service.VerificationMethodOAuth); err != nil {
			writeSecurityOperationError(c, err)
			return
		}
		session, _, err := service.ValidateLoginSession(identity)
		if err != nil || session.FederatedIdentity == "" {
			writeAuthSessionError(c, service.ErrAuthTokenInvalid)
			return
		}
		binding, err := service.BindVerificationOperation(service.VerificationOperation{Scope: request.Scope, Context: request.Context})
		if err != nil {
			writeSecurityOperationError(c, err)
			return
		}
		payload.Identity = &identity
		payload.Binding = &binding
	} else if request.Scope != "" || len(request.Context) != 0 {
		common.ApiErrorT(c, "Invalid parameters")
		return
	}
	raw, err := common.Marshal(payload)
	if err != nil {
		writeAuthSessionError(c, err)
		return
	}
	create := model.AuthFlowCreate{Purpose: "site_sso_client", Provider: model.SiteSSOProvider, Intent: request.Intent, Payload: string(raw), ExpiresAt: time.Now().Add(5 * time.Minute)}
	if payload.Identity != nil {
		create.UserId = payload.Identity.UserID
		create.SessionId = payload.Identity.SessionID
	}
	state, _, err := model.CreateAuthFlow(create)
	if err != nil {
		writeAuthSessionError(c, err)
		return
	}
	sum := sha256.Sum256([]byte(verifier))
	query := url.Values{"state": {state}, "challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "client_id": {cfg.ClientID}, "verify": {request.Intent}}
	// Cookie 和服务端 state 必须同时存在；仅拿到回调 URL 不能把攻击者的账号塞给其他浏览器。
	http.SetCookie(c.Writer, &http.Cookie{Name: siteSSOCookie, Value: nonce, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: 300})
	setAuthNoStore(c)
	common.ApiSuccess(c, gin.H{"flow_token": state, "expires_at": create.ExpiresAt.Unix(), "authorization_url": cfg.IssuerURL + "/sso?" + query.Encode()})
}

// SiteSSOAuthorize 由用户在身份源页面点击确认后签发 60 秒授权码，验证操作必须先完成重新认证。
// @param c 已经由 UserAuth 验证的浏览器会话；PAT 不能签发身份。
func SiteSSOAuthorize(c *gin.Context) {
	cfg := common.SiteSSO()
	identity, ok := middleware.GetSessionAuthIdentity(c)
	var request struct {
		State     string `json:"state"`
		Challenge string `json:"challenge"`
		ClientID  string `json:"client_id"`
		Verify    string `json:"verify"`
	}
	if cfg == nil || cfg.Consumer || !ok || c.GetHeader("Origin") != cfg.IssuerURL || common.DecodeJson(c.Request.Body, &request) != nil || request.ClientID != cfg.ClientID || !siteSSORandomPattern.MatchString(request.State) || !siteSSORandomPattern.MatchString(request.Challenge) || (request.Verify != "login" && request.Verify != "verify") {
		writeAccountStatusError(c, http.StatusForbidden, "Verification flow expired")
		return
	}
	session, _, err := service.ValidateLoginSession(identity)
	if err != nil {
		writeAuthSessionError(c, err)
		return
	}
	verified := request.Verify == "verify"
	if verified {
		raw, _ := common.Marshal(map[string]string{"state": request.State, "challenge": request.Challenge})
		if middleware.RequireSecurityProof(c, service.VerificationOperation{Scope: service.VerificationScopeSiteSSO, Context: raw}) == nil {
			return
		}
	}
	payload, err := common.Marshal(model.SiteSSOGrant{Identity: identity, Challenge: request.Challenge, CodeExpiresAt: time.Now().Add(time.Minute).Unix(), Verified: verified})
	if err != nil {
		writeAuthSessionError(c, err)
		return
	}
	code, _, err := model.CreateAuthFlow(model.AuthFlowCreate{Purpose: model.SiteSSOCodePurpose, Provider: cfg.ClientID, Intent: request.Verify, UserId: identity.UserID, SessionId: identity.SessionID, Payload: string(payload), ExpiresAt: time.Unix(session.ExpiresAt, 0)})
	if err != nil {
		writeAuthSessionError(c, err)
		return
	}
	c.Header("Referrer-Policy", "no-referrer")
	setAuthNoStore(c)
	// 回调地址来自部署配置，不使用任何前端传入的 redirect_uri。
	common.ApiSuccess(c, gin.H{"redirect_url": cfg.ClientURL + "/oauth/site-sso?" + url.Values{"code": {code}, "state": {request.State}}.Encode()})
	recordUserSecurityAudit(c, identity.UserID, "user.sso_authorize", map[string]any{"client": cfg.ClientID, "verified": verified})
}

// SiteSSOBackchannel 仅允许固定消费站客户端调用，返回最小身份信息；不接受前端 bearer token。
// @param c exchange/session 路由；客户端密钥仅从 Basic 认证读取。
func SiteSSOBackchannel(c *gin.Context) {
	cfg := common.SiteSSO()
	client, secret, ok := c.Request.BasicAuth()
	if cfg == nil || cfg.Consumer || !ok || subtle.ConstantTimeCompare([]byte(client), []byte(cfg.ClientID)) != 1 || subtle.ConstantTimeCompare([]byte(secret), []byte(cfg.ClientSecret)) != 1 {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}
	var request struct {
		Code     string `json:"code"`
		Verifier string `json:"verifier"`
		GrantID  int64  `json:"grant_id"`
	}
	if common.DecodeJson(c.Request.Body, &request) != nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	if c.Param("action") == "logout" {
		// Only the authenticated consumer can address a previously exchanged login grant.
		// The issuer resolves its owner; the caller cannot supply a session ID or user ID.
		if err := model.LogoutSiteSSOGrant(cfg.ClientID, request.GrantID); err != nil {
			writeAuthSessionError(c, err)
			return
		}
		setAuthNoStore(c)
		c.Status(http.StatusNoContent)
		return
	}
	var grant *model.SiteSSOGrant
	var flow *model.AuthFlow
	var err error
	if c.Param("action") == "exchange" {
		if !siteSSORandomPattern.MatchString(request.Code) || !siteSSORandomPattern.MatchString(request.Verifier) {
			c.AbortWithStatus(http.StatusBadRequest)
			return
		}
		flow, grant, err = model.ExchangeSiteSSOCode(request.Code, cfg.ClientID, request.Verifier)
		if err == nil {
			request.GrantID = flow.Id
		}
	} else if c.Param("action") == "session" && request.GrantID > 0 {
		grant, err = model.ValidateSiteSSOGrant(cfg.ClientID, request.GrantID)
	} else {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	if err != nil {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}
	user, err := model.GetSelfUserById(grant.Identity.UserID)
	if err != nil {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}
	session, err := model.GetUserSessionBySID(grant.Identity.SessionID)
	if err != nil {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}
	name := user.DisplayName
	if name == "" {
		name = user.Username
	}
	setAuthNoStore(c)
	c.JSON(http.StatusOK, service.SiteSSOAssertion{Issuer: cfg.IssuerID, Audience: cfg.ClientID, GrantID: request.GrantID, Subject: user.Id, DisplayName: name, Verified: grant.Verified, ExpiresAt: session.ExpiresAt})
}

// completeSiteSSO 先校验浏览器绑定再消费 state，换码只走后通道；验证回调还必须来自原本站会话。
// @param c 现有 OAuth 回调 API，登录返回标准 AuthBundle，验证返回标准操作证明。
func completeSiteSSO(c *gin.Context) {
	cfg := common.SiteSSO()
	if cfg == nil || !cfg.Consumer {
		writeAuthSessionError(c, service.ErrAuthTokenInvalid)
		return
	}
	state := c.Query("state")
	flow, err := model.GetAuthFlow(state, model.AuthFlowMatch{Purpose: "site_sso_client", Provider: model.SiteSSOProvider})
	if err != nil {
		writeSecurityOperationError(c, err)
		return
	}
	var payload siteSSOFlow
	nonce, _ := c.Cookie(siteSSOCookie)
	if common.UnmarshalJsonStr(flow.Payload, &payload) != nil || nonce == "" || subtle.ConstantTimeCompare([]byte(payload.BrowserHash), []byte(common.GenerateHMACWithKey([]byte(common.SessionSecret), nonce))) != 1 {
		writeAuthSessionError(c, service.ErrAuthTokenInvalid)
		return
	}
	if flow.Intent == "verify" {
		identity, ok := middleware.GetSessionAuthIdentity(c)
		if !ok || payload.Identity == nil || payload.Binding == nil || *payload.Identity != identity {
			writeAuthSessionError(c, service.ErrAuthTokenInvalid)
			return
		}
	}
	_, err = model.ConsumeAuthFlowWithAction(state, model.AuthFlowMatch{Purpose: "site_sso_client", Provider: model.SiteSSOProvider, Intent: flow.Intent}, func(tx *gorm.DB, _ *model.AuthFlow) error {
		if payload.Identity != nil {
			return model.ValidateAuthSessionWithTx(tx, *payload.Identity)
		}
		return nil
	})
	if err != nil {
		writeSecurityOperationError(c, err)
		return
	}
	assertion, err := service.RequestSiteSSO(c.Request.Context(), "exchange", map[string]string{"code": c.Query("code"), "verifier": payload.Verifier})
	if err != nil {
		writeAuthSessionError(c, err)
		return
	}
	if flow.Intent == "verify" {
		session, _, err := service.ValidateLoginSession(*payload.Identity)
		var original service.SiteSSOAssertion
		if err != nil || common.UnmarshalJsonStr(session.FederatedIdentity, &original) != nil || !assertion.Verified || assertion.Subject != original.Subject {
			writeAuthSessionError(c, service.ErrAuthTokenInvalid)
			return
		}
		proof, err := service.CompleteSecurityVerification(*payload.Identity, *payload.Binding, service.VerificationMethodOAuth)
		if err != nil {
			writeSecurityOperationError(c, err)
			return
		}
		common.ApiSuccess(c, proof)
		return
	}
	if assertion.Verified {
		writeAuthSessionError(c, service.ErrAuthTokenInvalid)
		return
	}
	user, err := model.ResolveSiteSSOUser(cfg.IssuerID, assertion.Subject, assertion.DisplayName)
	if err != nil {
		writeAuthSessionError(c, err)
		return
	}
	bundle, err := service.CreateFederatedLoginSession(user.Id, assertion, c.ClientIP(), c.Request.UserAgent())
	if err != nil {
		writeAuthSessionError(c, err)
		return
	}
	profile, err := model.GetSelfUserById(user.Id)
	if err != nil {
		writeAuthSessionError(c, err)
		return
	}
	writeLoginResponse(c, profile, bundle)
}
