package vm2api

import (
	"context"
	"crypto/subtle"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
)

// HostedState 是托管协议的受限服务端响应；Key 只能写入渠道，禁止直接返回浏览器。
type HostedState struct {
	IdentitySource string `json:"identity_source"`
	VMID           string `json:"vm_id"`
	BindingID      string `json:"binding_id"`
	Generation     int64  `json:"generation"`
	State          string `json:"state"`
	AccountHash    string `json:"account_hash"`
	SubjectHash    string `json:"subject_hash"`
	Label          string `json:"label"`
	Plan           string `json:"plan"`
	KeyID          string `json:"key_id"`
	PoolID         string `json:"pool_id"`
	Key            string `json:"key"`
}

// HostedSlot 提供可分配资格与已有归属，不含出口认证信息。
type HostedSlot struct {
	Platform            string       `json:"platform"`
	LegacyAccountHashes []string     `json:"legacy_account_hashes"`
	LegacySubjectHashes []string     `json:"legacy_subject_hashes"`
	ID                  string       `json:"id"`
	Name                string       `json:"name"`
	Empty               bool         `json:"empty"`
	ExitReady           bool         `json:"exit_ready"`
	Managed             bool         `json:"managed"`
	Hosting             *HostedState `json:"hosting"`
}

// HostedSlots 要求实例安装版本化托管协议，旧实例不能被误认为可安全分配。
func (c *Client) HostedSlots(ctx context.Context) ([]HostedSlot, error) {
	var out struct {
		Data struct {
			Version int          `json:"version"`
			Slots   []HostedSlot `json:"slots"`
		} `json:"data"`
	}
	if err := c.request(ctx, http.MethodGet, "/api/panel/hosting/slots", nil, &out); err != nil {
		return nil, err
	}
	if out.Data.Version != 1 && out.Data.Version != 2 {
		return nil, &Error{Code: "hosting_unavailable", HTTPStatus: 503}
	}
	return out.Data.Slots, nil
}

// HostingAction 只调用固定实例、白名单槽位及动作；body 不允许控制上游地址。
func (c *Client) HostingAction(ctx context.Context, slot, action string, body any) (HostedState, error) {
	switch action {
	case "enroll", "reserve", "prepare", "activate", "policy", "release", "takeover-check", "takeover":
	default:
		return HostedState{}, &Error{Code: "hosting_conflict", HTTPStatus: 400}
	}
	if !identifier.MatchString(slot) {
		return HostedState{}, &Error{Code: "invalid_slot", HTTPStatus: 400}
	}
	var out struct {
		Data HostedState `json:"data"`
	}
	err := c.request(ctx, http.MethodPost, "/api/panel/hosting/slots/"+slot+"/"+action, body, &out)
	return out.Data, err
}

// StartHostedOAuth 把官方登录会话绑定到当前门户会话和持久化绑定编号；回调不保存在数据库。
func (r *Registry) StartHostedOAuth(ctx context.Context, userID int, loginSession, instance, slot, binding string, generation int64, platforms ...string) (OAuthSession, error) {
	c, err := r.Client(instance)
	if err != nil {
		return OAuthSession{}, err
	}
	if userID <= 0 || loginSession == "" || !identifier.MatchString(slot) {
		return OAuthSession{}, &Error{Code: "hosting_conflict", HTTPStatus: 400}
	}
	var out struct {
		Data struct {
			AuthURL   string `json:"auth_url"`
			SessionID string `json:"session_id"`
			ExpiresAt int64  `json:"expires_at"`
		} `json:"data"`
	}
	err = c.request(ctx, http.MethodPost, "/api/panel/hosting/slots/"+slot+"/login", map[string]any{"binding_id": binding, "generation": generation}, &out)
	if err != nil {
		return OAuthSession{}, err
	}
	platform := "codex"
	if len(platforms) > 0 {
		platform = platforms[0]
	}
	state, redirect, err := hostedAuthorization(out.Data.AuthURL, platform)
	if err != nil {
		return OAuthSession{}, err
	}
	expiry := min(out.Data.ExpiresAt, time.Now().Add(10*time.Minute).UnixMilli())
	if !identifier.MatchString(out.Data.SessionID) || expiry <= time.Now().UnixMilli() {
		return OAuthSession{}, &Error{Code: "oauth_session_expired", HTTPStatus: 409}
	}
	public := OAuthSession{Platform: platform, AuthURL: out.Data.AuthURL, SessionID: uuid.NewString(), ExpiresAt: time.UnixMilli(expiry)}
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, s := range r.sessions {
		if (s.AdminID == userID && s.HostedBinding == binding) || !time.Now().Before(s.ExpiresAt) {
			delete(r.sessions, id)
		}
	}
	if len(r.sessions) >= 512 {
		return OAuthSession{}, &Error{Code: "too_many_authorization_sessions", HTTPStatus: 429}
	}
	r.sessions[public.SessionID] = oauthState{Platform: platform, AdminID: userID, LoginSessionID: loginSession, InstanceID: instance, SlotID: slot, UpstreamID: out.Data.SessionID, State: state, RedirectURI: redirect, ExpiresAt: public.ExpiresAt, HostedBinding: binding}
	return public, nil
}

// PrepareHostedOAuth 验证完整回调及 session/state 后一次性消费，远端仅暂存加密授权，尚不允许推理。
func (r *Registry) PrepareHostedOAuth(ctx context.Context, userID int, loginSession, instance, slot, binding, sessionID, callback string, generation int64) (HostedState, error) {
	if len(callback) > 16384 {
		return HostedState{}, &Error{Code: "invalid_oauth_callback", HTTPStatus: 400}
	}
	r.mu.Lock()
	s, ok := r.sessions[sessionID]
	if !ok || !time.Now().Before(s.ExpiresAt) {
		delete(r.sessions, sessionID)
		r.mu.Unlock()
		return HostedState{}, &Error{Code: "oauth_session_expired", HTTPStatus: 409}
	}
	if s.AdminID != userID || s.LoginSessionID != loginSession || s.InstanceID != instance || s.SlotID != slot || s.HostedBinding != binding {
		r.mu.Unlock()
		return HostedState{}, &Error{Code: "oauth_session_mismatch", HTTPStatus: 403}
	}
	if s.Platform == "claude" {
		// 官方 CLI 验证 PKCE 与完整 code#state；门户同时将短期会话绑定到本人、名额和登录会话。
		code, state, found := strings.Cut(strings.TrimSpace(callback), "#")
		if !found || code == "" || state == "" || strings.ContainsAny(callback, "\r\n") || subtle.ConstantTimeCompare([]byte(state), []byte(s.State)) != 1 {
			r.mu.Unlock()
			return HostedState{}, &Error{Code: "invalid_oauth_callback", HTTPStatus: 400}
		}
	} else {
		u, err := url.Parse(strings.TrimSpace(callback))
		if err != nil || u.User != nil || u.Fragment != "" || u.Opaque != "" {
			r.mu.Unlock()
			return HostedState{}, &Error{Code: "invalid_oauth_callback", HTTPStatus: 400}
		}
		q, err := url.ParseQuery(u.RawQuery)
		u.RawQuery = ""
		if err != nil || len(q["code"]) != 1 || len(q["state"]) != 1 || q.Get("code") == "" || q.Has("error") || u.String() != s.RedirectURI || subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(s.State)) != 1 {
			r.mu.Unlock()
			return HostedState{}, &Error{Code: "invalid_oauth_callback", HTTPStatus: 400}
		}
	}
	delete(r.sessions, sessionID)
	r.mu.Unlock()
	c, err := r.Client(instance)
	if err != nil {
		return HostedState{}, err
	}
	return c.HostingAction(ctx, slot, "prepare", map[string]any{"binding_id": binding, "generation": generation, "session_id": s.UpstreamID, "callback_url": strings.TrimSpace(callback)})
}

// hostedAuthorization 严格区分授权服务器，固定 HTTPS 来源与路径，不能把另一平台会话混入换票。
func hostedAuthorization(raw, platform string) (string, string, error) {
	if platform == "codex" {
		return officialAuthorization(raw)
	}
	if platform != "claude" {
		return "", "", &Error{Code: "invalid_authorization_url", HTTPStatus: 502}
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host != "claude.com" || u.Path != "/cai/oauth/authorize" || u.User != nil || u.Fragment != "" {
		return "", "", &Error{Code: "invalid_authorization_url", HTTPStatus: 502}
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(q["state"]) != 1 || q.Get("state") == "" || q.Get("code_challenge") == "" || q.Get("code_challenge_method") != "S256" {
		return "", "", &Error{Code: "invalid_authorization_url", HTTPStatus: 502}
	}
	return q.Get("state"), q.Get("redirect_uri"), nil
}
