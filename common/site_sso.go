package common

import (
	"errors"
	"net/url"
	"os"
)

// SiteSSOConfig 只接受运维配置的两个站点，不允许浏览器决定身份源或回调地址。
type SiteSSOConfig struct {
	IssuerURL    string `json:"issuer_url"`
	IssuerID     string `json:"-"`
	ClientURL    string `json:"client_url"`
	ClientID     string `json:"-"`
	ClientSecret string `json:"-"`
	Consumer     bool   `json:"consumer"`
}

// SiteSSO 返回当前进程的统一身份配置；未启用时保留原来的独立登录。
// @return 公开序列化会剔除客户端凭据；启动时必须先调用 ValidateSiteSSO。
func SiteSSO() *SiteSSOConfig {
	if os.Getenv("SITE_SSO_ISSUER_URL") == "" {
		return nil
	}
	issuerID := os.Getenv("SITE_SSO_ISSUER_ID")
	if issuerID == "" {
		issuerID = os.Getenv("SITE_SSO_ISSUER_URL")
	}
	return &SiteSSOConfig{IssuerID: issuerID, IssuerURL: os.Getenv("SITE_SSO_ISSUER_URL"), ClientURL: os.Getenv("SITE_SSO_CLIENT_URL"), ClientID: os.Getenv("SITE_SSO_CLIENT_ID"), ClientSecret: os.Getenv("SITE_SSO_CLIENT_SECRET"), Consumer: IsRetailSite()}
}

// ValidateSiteSSO 拒绝不完整配置和带路径、凭据、查询参数的地址，固定 TLS 信任边界。
// @return 配置不合法时阻止启动，不自动退回无统一认证的模式。
func ValidateSiteSSO() error {
	cfg := SiteSSO()
	if cfg == nil {
		for _, key := range []string{"SITE_SSO_ISSUER_ID", "SITE_SSO_CLIENT_URL", "SITE_SSO_CLIENT_ID", "SITE_SSO_CLIENT_SECRET"} {
			if os.Getenv(key) != "" {
				return errors.New("incomplete site SSO configuration")
			}
		}
		return nil
	}
	if !SessionCookieSecure {
		return errors.New("site SSO requires secure session cookies")
	}
	for _, raw := range []string{cfg.IssuerURL, cfg.ClientURL} {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || u.String() != raw {
			return errors.New("site SSO requires exact HTTPS origins")
		}
	}
	if len(cfg.IssuerID) > 128 || cfg.IssuerURL == cfg.ClientURL || len(cfg.ClientID) < 1 || len(cfg.ClientID) > 32 || len(cfg.ClientSecret) < 32 {
		return errors.New("invalid site SSO client configuration")
	}
	return nil
}
