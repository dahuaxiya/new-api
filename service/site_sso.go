package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

// SiteSSOAssertion 是通过已认证 TLS 后通道取得的身份声明，不是可转交的 bearer token。
type SiteSSOAssertion struct {
	Issuer      string `json:"issuer"`
	Audience    string `json:"audience"`
	GrantID     int64  `json:"grant_id"`
	Subject     int    `json:"subject"`
	DisplayName string `json:"display_name"`
	Verified    bool   `json:"verified"`
	ExpiresAt   int64  `json:"expires_at"`
}

var siteSSOHTTPClient = &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}

// RequestSiteSSO 只请求运维固定的身份源；禁止跟随重定向，避免客户端凭据发给其他主机。
// @param ctx 请求取消上下文。
// @param action 固定的 exchange 或 session 操作。
// @param payload 不包含客户端密钥的请求数据。
// @return 核验签发者、受众、有效期后的声明；失败时不返回部分身份。
func RequestSiteSSO(ctx context.Context, action string, payload any) (*SiteSSOAssertion, error) {
	cfg := common.SiteSSO()
	if cfg == nil || !cfg.Consumer || (action != "exchange" && action != "session") {
		return nil, ErrAuthTokenInvalid
	}
	response, err := postSiteSSO(ctx, cfg, action, payload)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, ErrLoginSessionRevoked
	}
	var assertion SiteSSOAssertion
	if err = common.DecodeJson(io.LimitReader(response.Body, 8192), &assertion); err != nil {
		return nil, ErrAuthTokenInvalid
	}
	if assertion.Issuer != cfg.IssuerID || assertion.Audience != cfg.ClientID || assertion.GrantID <= 0 || assertion.Subject <= 0 || assertion.ExpiresAt <= time.Now().Unix() {
		return nil, ErrAuthTokenInvalid
	}
	return &assertion, nil
}

// postSiteSSO sends a server-selected operation to the configured issuer; callers own the response body.
func postSiteSSO(ctx context.Context, cfg *common.SiteSSOConfig, action string, payload any) (*http.Response, error) {
	body, err := common.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.IssuerURL+"/api/site-sso/"+action, bytes.NewReader(body))
	if err != nil {
		return nil, ErrAuthTokenInvalid
	}
	req.SetBasicAuth(cfg.ClientID, cfg.ClientSecret)
	req.Header.Set("Content-Type", "application/json")
	response, err := siteSSOHTTPClient.Do(req)
	if err != nil {
		return nil, errors.New("unified account service is unavailable")
	}
	return response, nil
}

// logoutIssuerSession ends the originating session before local credentials are discarded.
// A missing acknowledgement leaves the browser able to retry the same idempotent request.
func logoutIssuerSession(ctx context.Context, s *model.UserSession) error {
	if s.LoginMethod != model.SiteSSOProvider && s.FederatedIdentity == "" {
		return nil
	}
	cfg := common.SiteSSO()
	var grant SiteSSOAssertion
	if cfg == nil || !cfg.Consumer || common.UnmarshalJsonStr(s.FederatedIdentity, &grant) != nil || grant.Issuer != cfg.IssuerID || grant.Audience != cfg.ClientID || grant.GrantID <= 0 {
		return ErrAuthTokenInvalid
	}
	response, err := postSiteSSO(ctx, cfg, "logout", map[string]int64{"grant_id": grant.GrantID})
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		return errors.New("unified logout was not acknowledged")
	}
	return nil
}

// ValidateFederatedSession 对统一登录的每次管理请求和续期重新校验源会话，普通本地会话不受影响。
// @param session 本站已核验的服务端会话。
// @return 源站改密、禁用、退出或不可达均拒绝；不缓存肯定结果造成撤销延迟。
func ValidateFederatedSession(session *model.UserSession) error {
	if session.FederatedIdentity == "" {
		if session.LoginMethod == model.SiteSSOProvider {
			return ErrAuthTokenInvalid
		}
		return nil
	}
	var expected SiteSSOAssertion
	cfg := common.SiteSSO()
	if cfg == nil || !cfg.Consumer || common.UnmarshalJsonStr(session.FederatedIdentity, &expected) != nil || expected.Issuer != cfg.IssuerID || expected.Audience != cfg.ClientID {
		return ErrAuthTokenInvalid
	}
	actual, err := RequestSiteSSO(context.Background(), "session", map[string]any{"grant_id": expected.GrantID})
	if err != nil {
		return err
	}
	if actual.Subject != expected.Subject || actual.GrantID != expected.GrantID {
		return ErrAuthTokenInvalid
	}
	return nil
}

// CreateFederatedLoginSession 建立本站独立会话，并记录不可省略的源授权引用。
// @param userID 本站用户 ID，已通过唯一声明表解析。
// @param assertion 已校验的源站声明。
// @param ip 当前请求来源。
// @param userAgent 浏览器标识，仅用于审计。
// @return 本站签名的会话；最长不超过源授权有效期。
func CreateFederatedLoginSession(userID int, assertion *SiteSSOAssertion, ip, userAgent string) (*AuthBundle, error) {
	if assertion == nil {
		return nil, ErrAuthTokenInvalid
	}
	return createLoginSession(userID, 0, model.SiteSSOProvider, ip, userAgent, assertion)
}
