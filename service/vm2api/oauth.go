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

// OAuthSession 仅公开官方登录链接和短期关联 ID，不包含验证码、PKCE verifier 或 token。
type OAuthSession struct {
	AuthURL   string    `json:"auth_url"`
	SessionID string    `json:"session_id"`
	ExpiresAt time.Time `json:"expires_at"`
}

type oauthState struct {
	AdminID                                                            int
	InstanceID, SlotID, UpstreamID, State, RedirectURI, LoginSessionID string
	ExpiresAt                                                          time.Time
	HostedBinding                                                      string
}

// officialAuthorization 只接受当前 Codex 官方 PKCE 流程，拒绝上游给出的任意跳转链接。
func officialAuthorization(raw string) (state, redirectURI string, err error) {
	bad := &Error{Code: "invalid_authorization_url", HTTPStatus: 502}
	if len(raw) > 16384 {
		return "", "", bad
	}
	u, e := url.Parse(raw)
	if e != nil || u.Scheme != "https" || u.User != nil || u.Fragment != "" {
		return "", "", bad
	}
	if u.Host == "chatgpt.com" && u.Path == "/codex/desktop-auth" {
		nested := u.Query()["authorize_url"]
		if len(nested) != 1 {
			return "", "", bad
		}
		u, e = url.Parse(nested[0])
		if e != nil {
			return "", "", bad
		}
	}
	if u.Scheme != "https" || u.Host != "auth.openai.com" || u.Path != "/oauth/authorize" || u.User != nil || u.Fragment != "" {
		return "", "", bad
	}
	q := u.Query()
	for _, key := range []string{"state", "redirect_uri", "response_type", "code_challenge_method", "code_challenge"} {
		if len(q[key]) != 1 {
			return "", "", bad
		}
	}
	if q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" || len(q.Get("code_challenge")) < 43 || len(q.Get("state")) < 32 || len(q.Get("state")) > 256 || q.Get("redirect_uri") != "http://localhost:1455/auth/callback" {
		return "", "", bad
	}
	return q.Get("state"), q.Get("redirect_uri"), nil
}

// StartOAuth 为本机未登录槽位创建官方授权流程；调用方必须已验证管理员身份、登录会话及该槽位作业归属。
func (r *Registry) StartOAuth(ctx context.Context, adminID int, instanceID, slotID, loginSessionID string) (OAuthSession, error) {
	if adminID <= 0 || loginSessionID == "" || len(loginSessionID) > 200 {
		return OAuthSession{}, &Error{Code: "administrator_required", HTTPStatus: 403}
	}
	c, err := r.Client(instanceID)
	if err != nil {
		return OAuthSession{}, err
	}
	if !identifier.MatchString(slotID) {
		return OAuthSession{}, &Error{Code: "invalid_slot", HTTPStatus: 400}
	}
	operation := instanceID + "\x00" + slotID
	r.mu.Lock()
	if r.clients[instanceID] != c {
		r.mu.Unlock()
		return OAuthSession{}, &Error{Code: "instance_changed", HTTPStatus: 409}
	}
	if r.oauthBusy[operation] {
		r.mu.Unlock()
		return OAuthSession{}, &Error{Code: "oauth_in_progress", HTTPStatus: 409}
	}
	r.oauthBusy[operation] = true
	// 新登录开始前就废弃旧会话；换票执行期间拒绝创建新会话，防止两份凭据相互覆盖。
	for id, session := range r.sessions {
		if session.InstanceID == instanceID && session.SlotID == slotID {
			delete(r.sessions, id)
		}
	}
	r.mu.Unlock()
	defer r.releaseOAuthOperation(operation)
	slot, err := c.GetSlot(ctx, slotID)
	if err != nil {
		return OAuthSession{}, err
	}
	if slot.HasAuth {
		return OAuthSession{}, &Error{Code: "slot_already_authenticated", HTTPStatus: 409}
	}
	var response struct {
		Data struct {
			AuthURL   string `json:"auth_url"`
			SessionID string `json:"session_id"`
			ExpiresAt int64  `json:"expires_at"`
		} `json:"data"`
	}
	if err := c.request(ctx, http.MethodPost, "/api/panel/vms/"+slotID+"/oauth/generate-auth-url", map[string]any{}, &response); err != nil {
		return OAuthSession{}, err
	}
	state, redirect, err := officialAuthorization(response.Data.AuthURL)
	if err != nil {
		return OAuthSession{}, err
	}
	now := time.Now()
	expires := min(response.Data.ExpiresAt, now.Add(10*time.Minute).UnixMilli())
	if !identifier.MatchString(response.Data.SessionID) || expires <= now.UnixMilli() {
		return OAuthSession{}, &Error{Code: "invalid_authorization_session", HTTPStatus: 502}
	}
	public := OAuthSession{AuthURL: response.Data.AuthURL, SessionID: uuid.NewString(), ExpiresAt: time.UnixMilli(expires)}
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, session := range r.sessions {
		// 每个槽位只保留一个活动登录，避免管理员多次点击后把旧页面的账号误关联进来。
		if !now.Before(session.ExpiresAt) || (session.InstanceID == instanceID && session.SlotID == slotID) {
			delete(r.sessions, id)
		}
	}
	if len(r.sessions) >= 512 {
		return OAuthSession{}, &Error{Code: "too_many_authorization_sessions", HTTPStatus: 429}
	}
	r.sessions[public.SessionID] = oauthState{AdminID: adminID, InstanceID: instanceID, SlotID: slotID, LoginSessionID: loginSessionID, UpstreamID: response.Data.SessionID, State: state, RedirectURI: redirect, ExpiresAt: public.ExpiresAt}
	return public, nil
}

// FinishOAuth 要求完整回调 URL 和正确 state。会话与管理员登录会话、实例、槽位绑定，兑换前原子消费，失败后也不能重放。
func (r *Registry) FinishOAuth(ctx context.Context, adminID int, instanceID, slotID, sessionID, callbackURL, loginSessionID string) (Slot, error) {
	if len(callbackURL) > 16384 {
		return Slot{}, &Error{Code: "invalid_oauth_callback", HTTPStatus: 400}
	}
	callback, err := url.Parse(strings.TrimSpace(callbackURL))
	if err != nil || callback.User != nil || callback.Fragment != "" || callback.Opaque != "" {
		return Slot{}, &Error{Code: "invalid_oauth_callback", HTTPStatus: 400}
	}
	q, err := url.ParseQuery(callback.RawQuery)
	if err != nil || len(q["code"]) != 1 || len(q["state"]) != 1 || q.Get("code") == "" || q.Get("state") == "" || q.Has("error") {
		return Slot{}, &Error{Code: "invalid_oauth_callback", HTTPStatus: 400}
	}
	r.mu.Lock()
	session, ok := r.sessions[sessionID]
	if !ok || !time.Now().Before(session.ExpiresAt) {
		delete(r.sessions, sessionID)
		r.mu.Unlock()
		return Slot{}, &Error{Code: "oauth_session_expired", HTTPStatus: 409}
	}
	if adminID <= 0 || session.AdminID != adminID || loginSessionID == "" || session.LoginSessionID != loginSessionID || session.InstanceID != instanceID || session.SlotID != slotID {
		r.mu.Unlock()
		return Slot{}, &Error{Code: "oauth_session_mismatch", HTTPStatus: 403}
	}
	callback.RawQuery = ""
	if callback.String() != session.RedirectURI || subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(session.State)) != 1 {
		r.mu.Unlock()
		return Slot{}, &Error{Code: "oauth_state_mismatch", HTTPStatus: 400}
	}
	operation := instanceID + "\x00" + slotID
	if r.oauthBusy[operation] {
		r.mu.Unlock()
		return Slot{}, &Error{Code: "oauth_in_progress", HTTPStatus: 409}
	}
	r.oauthBusy[operation] = true
	// 在任何网络操作前移除会话；整个换票及回读期间独占该槽位，不能同时发起下一轮授权。
	delete(r.sessions, sessionID)
	r.mu.Unlock()
	defer r.releaseOAuthOperation(operation)
	c, err := r.Client(instanceID)
	if err != nil {
		return Slot{}, err
	}
	slot, err := c.GetSlot(ctx, slotID)
	if err != nil {
		return Slot{}, err
	}
	if slot.HasAuth {
		return Slot{}, &Error{Code: "slot_already_authenticated", HTTPStatus: 409}
	}
	if err := c.request(ctx, http.MethodPost, "/api/panel/vms/"+slotID+"/oauth/exchange-code", map[string]string{"session_id": session.UpstreamID, "callback_url": strings.TrimSpace(callbackURL)}, nil); err != nil {
		return Slot{}, err
	}
	// 兑换响应可能携带账号资料；丢弃后只返回重新读取的白名单槽位状态。
	return c.GetSlot(ctx, slotID)
}

// releaseOAuthOperation 在网络请求完成或失败后释放槽位授权锁，避免异常路径永久挡住重新授权。
func (r *Registry) releaseOAuthOperation(operation string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.oauthBusy, operation)
}
