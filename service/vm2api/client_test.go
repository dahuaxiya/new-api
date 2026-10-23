package vm2api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
)

const testSlot = "na-12345678-1234-4234-8234-123456789abc"
const testState = "12345678901234567890123456789012abcd"

type panelFixture struct {
	slotName                                        string
	mu                                              sync.Mutex
	slot                                            bool
	auth                                            bool
	pool                                            bool
	key                                             bool
	badPool                                         bool
	legacyPool                                      bool
	createCalls, poolCalls, keyCalls, exchangeCalls int
	exchangeStarted                                 chan struct{}
	exchangeContinue                                chan struct{}
}

// serve 模拟上游真实 JSON 包络和写后断线，用于验证重试不重复分配资源。
func (f *panelFixture) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	send := func(v any) { body, _ := common.Marshal(v); _, _ = w.Write(body) }
	slot := func() map[string]any {
		name := f.slotName
		if name == "" {
			name = "new-user"
		}
		return map[string]any{"id": testSlot, "name": name, "family": "codex", "runtime_type": "docker", "has_token": f.auth, "schedulable": f.auth, "status": "running", "proxy_id": "px-local", "email": "private@example.com", "password": "do-not-leak"}
	}
	if r.URL.Path == "/api/panel/login" {
		send(map[string]any{"ok": true, "token": "private-panel-session", "role": "admin", "expires_in": 3600})
		return
	}
	if r.Header.Get("Authorization") != "Bearer private-panel-session" {
		w.WriteHeader(401)
		return
	}
	switch r.URL.Path {
	case "/api/panel/vms/" + testSlot:
		if !f.slot {
			w.WriteHeader(404)
			return
		}
		send(map[string]any{"ok": true, "data": map[string]any{"vm": slot()}})
	case "/api/panel/vms":
		send(map[string]any{"ok": true, "data": map[string]any{"items": []any{slot()}}})
	case "/api/panel/proxies":
		send(map[string]any{"ok": true, "data": map[string]any{"proxies": []any{map[string]any{"id": "px-local", "kind": "local", "enabled": true, "status": "ok", "bind_limit": 5, "bound_count": 0, "host": "private-host", "password": "private-proxy"}}}})
	case "/api/panel/vms/create":
		var body map[string]any
		_ = common.DecodeJson(r.Body, &body)
		machine, _ := body["machine"].(map[string]any)
		if body["runtime_type"] != "docker" || body["kind"] != "codex" || body["proxy_id"] != "px-local" || machine["memory"] != "512m" || body["node_id"] != nil {
			w.WriteHeader(400)
			return
		}
		f.slot = true
		f.slotName, _ = body["name"].(string)
		f.createCalls++
		w.WriteHeader(502)
		send(map[string]any{"ok": false, "error": "write persisted then connection failed"})
	case "/api/panel/vm-pools":
		if r.Method == http.MethodPost {
			f.pool = true
			f.poolCalls++
			w.WriteHeader(502)
			return
		}
		pools := []any{}
		if f.pool {
			ids := []string{testSlot}
			if f.badPool {
				ids = append(ids, "other-customer")
			}
			poolName := testSlot
			if f.legacyPool {
				poolName = "zyqxx"
			}
			pools = append(pools, map[string]any{"id": "pool_one", "name": poolName, "enabled": true, "vm_ids": ids})
		}
		send(map[string]any{"ok": true, "data": map[string]any{"pools": pools}})
	case "/api/panel/api-keys":
		if r.Method == http.MethodPost {
			f.key = true
			f.keyCalls++
			w.WriteHeader(502)
			return
		}
		keys := []any{}
		if f.key {
			keys = append(keys, map[string]any{"id": "key_one", "name": testSlot, "status": "active", "vm_pool_id": "pool_one"})
		}
		send(map[string]any{"ok": true, "keys": keys})
	case "/api/panel/api-keys/key_one/reveal":
		send(map[string]any{"ok": true, "id": "key_one", "key": "private-upstream-key"})
	case "/api/panel/vms/" + testSlot + "/oauth/generate-auth-url":
		auth := "https://auth.openai.com/oauth/authorize?" + url.Values{"state": {testState}, "redirect_uri": {"http://localhost:1455/auth/callback"}, "response_type": {"code"}, "code_challenge_method": {"S256"}, "code_challenge": {strings.Repeat("a", 43)}}.Encode()
		wrapper := "https://chatgpt.com/codex/desktop-auth?authorize_url=" + url.QueryEscape(auth)
		send(map[string]any{"ok": true, "data": map[string]any{"auth_url": wrapper, "session_id": "session_one", "expires_at": time.Now().Add(time.Hour).UnixMilli()}})
	case "/api/panel/vms/" + testSlot + "/oauth/exchange-code":
		var body map[string]string
		_ = common.DecodeJson(r.Body, &body)
		if body["session_id"] != "session_one" || !strings.Contains(body["callback_url"], "state="+testState) {
			w.WriteHeader(400)
			return
		}
		f.exchangeCalls++
		if f.exchangeStarted != nil {
			close(f.exchangeStarted)
			<-f.exchangeContinue
		}
		f.auth = true
		send(map[string]any{"ok": true, "data": map[string]any{"access_token": "private-token", "vm": slot()}})
	default:
		w.WriteHeader(404)
	}
}

// fixtureRegistry 为测试提供回环 HTTP 固定实例，不允许生产关闭 TLS 校验。
func fixtureRegistry(t *testing.T, f *panelFixture) (*Registry, *Client) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(server.Close)
	r, err := New(Config{Instances: []InstanceConfig{{ID: "cc", Name: "CloudCone", BaseURL: server.URL, Username: "admin", Password: "private-password", Models: []string{"gpt-test"}}}})
	if err != nil {
		t.Fatal(err)
	}
	c, err := r.Client("cc")
	if err != nil {
		t.Fatal(err)
	}
	return r, c
}

// TestConfigRejectsUnsafeOrigins 确认调用方不能用 URL 语法或环境代理绕过固定实例配置。
func TestConfigRejectsUnsafeOrigins(t *testing.T) {
	for _, origin := range []string{"http://example.com", "http://localhost:8787", "https://u:p@example.com", "https://example.com/api/panel", "https://example.com?x=1", "file:///tmp/key", "https://example.com#token"} {
		_, err := New(Config{Instances: []InstanceConfig{{ID: "cc", Name: "CC", BaseURL: origin, Username: "u", Password: "p", Models: []string{"m"}}}})
		if err == nil {
			t.Errorf("unsafe origin accepted: %s", origin)
		}
	}
	r, c := fixtureRegistry(t, &panelFixture{})
	if _, err := r.Client("https://evil.example"); err == nil {
		t.Fatal("accepted unknown instance")
	}
	views, _ := common.Marshal(r.Instances())
	if strings.Contains(string(views), "127.0.0.1") || strings.Contains(string(views), "private-password") {
		t.Fatal("private config leaked")
	}
	if _, err := c.GetSlot(t.Context(), "../login"); err == nil {
		t.Fatal("accepted traversal")
	}
}

// TestCreateAndPoolRecoverAfterLostResponse 验证写后断线可找回同一资源，并拒绝扩大账号池范围。
func TestCreateAndPoolRecoverAfterLostResponse(t *testing.T) {
	f := &panelFixture{}
	_, c := fixtureRegistry(t, f)
	request := CreateSlotRequest{ID: testSlot, Name: "new-user", ExitID: "px-local"}
	for range 2 {
		slot, err := c.CreateSlot(t.Context(), request)
		if err != nil || slot.ID != testSlot {
			t.Fatalf("slot recovery: %v", err)
		}
	}
	if f.createCalls != 1 {
		t.Fatalf("duplicate slot creation: %d", f.createCalls)
	}
	f.mu.Lock()
	f.auth = true
	f.mu.Unlock()
	for range 2 {
		key, err := c.EnsurePoolKey(t.Context(), testSlot, testSlot)
		if err != nil || key.Key != "private-upstream-key" {
			t.Fatalf("pool/key recovery: %v", err)
		}
		encoded, _ := common.Marshal(key)
		if strings.Contains(string(encoded), "private-upstream-key") {
			t.Fatal("key serialized")
		}
	}
	if f.poolCalls != 1 || f.keyCalls != 1 {
		t.Fatalf("duplicated pool/key: %d %d", f.poolCalls, f.keyCalls)
	}
	f.mu.Lock()
	f.badPool = true
	f.mu.Unlock()
	if _, err := c.EnsurePoolKey(t.Context(), testSlot, testSlot); err == nil {
		t.Fatal("accepted multi-user pool")
	}
	slots, err := c.ListSlots(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := common.Marshal(slots)
	if strings.Contains(string(encoded), "private") || strings.Contains(string(encoded), "password") || strings.Contains(string(encoded), "email") {
		t.Fatal("slot leaked non-whitelisted fields")
	}
}

// TestOAuthBindingExpiryStateAndReplay 验证管理员/槽位绑定、到期、完整回调、state 和一次性消费。
func TestOAuthBindingExpiryStateAndReplay(t *testing.T) {
	f := &panelFixture{slot: true}
	r, _ := fixtureRegistry(t, f)
	session, err := r.StartOAuth(t.Context(), 1, "cc", testSlot, "verified-login-session")
	if err != nil {
		t.Fatal(err)
	}
	if session.ExpiresAt.After(time.Now().Add(10 * time.Minute)) {
		t.Fatal("TTL exceeds ten minutes")
	}
	valid := "http://localhost:1455/auth/callback?code=private-code&state=" + testState
	tests := []struct {
		admin                    int
		instance, slot, callback string
	}{{2, "cc", testSlot, valid}, {1, "cc", "another", valid}, {1, "cc", testSlot, "private-code"}, {1, "cc", testSlot, "http://localhost:1455/auth/callback?code=private-code"}, {1, "cc", testSlot, strings.Replace(valid, testState, "wrong", 1)}, {1, "cc", testSlot, strings.Replace(valid, "localhost:1455", "evil.example", 1)}, {1, "cc", testSlot, valid + "&state=" + testState}}
	for _, tc := range tests {
		if _, err := r.FinishOAuth(t.Context(), tc.admin, tc.instance, tc.slot, session.SessionID, tc.callback, "verified-login-session"); err == nil {
			t.Errorf("invalid callback accepted: admin=%d slot=%s", tc.admin, tc.slot)
		}
	}
	if _, err := r.FinishOAuth(t.Context(), 1, "cc", testSlot, session.SessionID, valid, "other-login-session"); err == nil {
		t.Fatal("another login session accepted")
	}
	if _, err := r.StartOAuth(t.Context(), 1, "cc", testSlot, ""); err == nil {
		t.Fatal("empty login session accepted")
	}
	if f.exchangeCalls != 0 {
		t.Fatal("invalid callback reached upstream")
	}
	slot, err := r.FinishOAuth(t.Context(), 1, "cc", testSlot, session.SessionID, valid, "verified-login-session")
	if err != nil || !slot.HasAuth {
		t.Fatalf("complete: %v", err)
	}
	if _, err := r.FinishOAuth(t.Context(), 1, "cc", testSlot, session.SessionID, valid, "verified-login-session"); err == nil {
		t.Fatal("replay accepted")
	}
	if _, err := r.StartOAuth(t.Context(), 1, "cc", testSlot, "verified-login-session"); err == nil {
		t.Fatal("existing auth replacement accepted")
	}
	f.mu.Lock()
	f.auth = false
	f.mu.Unlock()
	expired, err := r.StartOAuth(t.Context(), 1, "cc", testSlot, "verified-login-session")
	if err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	state := r.sessions[expired.SessionID]
	state.ExpiresAt = time.Now().Add(-time.Second)
	r.sessions[expired.SessionID] = state
	r.mu.Unlock()
	if _, err := r.FinishOAuth(t.Context(), 1, "cc", testSlot, expired.SessionID, valid, "verified-login-session"); err == nil {
		t.Fatal("expired session accepted")
	}
	if f.exchangeCalls != 1 {
		t.Fatalf("unexpected exchanges: %d", f.exchangeCalls)
	}
}

// TestOAuthConcurrentSubmit 验证两个同时提交的合法回调只有一次到达上游。
func TestOAuthConcurrentSubmit(t *testing.T) {
	f := &panelFixture{slot: true}
	r, _ := fixtureRegistry(t, f)
	session, err := r.StartOAuth(t.Context(), 1, "cc", testSlot, "verified-login-session")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			_, _ = r.FinishOAuth(context.Background(), 1, "cc", testSlot, session.SessionID, "http://localhost:1455/auth/callback?code=private-code&state="+testState, "verified-login-session")
		})
	}
	wg.Wait()
	if f.exchangeCalls != 1 {
		t.Fatalf("exchange count=%d", f.exchangeCalls)
	}
}

// TestRedirectAndErrorRedaction 验证上游不能通过重定向把管理凭据发给第三方，错误正文不会泄露。
func TestRedirectAndErrorRedaction(t *testing.T) {
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("redirect followed") }))
	defer destination.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", destination.URL)
		w.WriteHeader(302)
		_, _ = fmt.Fprint(w, "secret-code=never-show")
	}))
	defer source.Close()
	r, err := New(Config{Instances: []InstanceConfig{{ID: "cc", Name: "CC", BaseURL: source.URL, Username: "u", Password: "p", Models: []string{"m"}}}})
	if err != nil {
		t.Fatal(err)
	}
	c, _ := r.Client("cc")
	_, err = c.ListSlots(t.Context())
	if err == nil || strings.Contains(err.Error(), "never-show") {
		t.Fatalf("unsafe error %v", err)
	}
	_, _, err = officialAuthorization("https://evil.example/oauth/authorize")
	if err == nil {
		t.Fatal("untrusted authorization site accepted")
	}
}

// TestOAuthSlotExclusiveDuringExchange 防止旧授权尚在换票时新授权取得第二个可兑换会话。
func TestOAuthSlotExclusiveDuringExchange(t *testing.T) {
	f := &panelFixture{slot: true, exchangeStarted: make(chan struct{}), exchangeContinue: make(chan struct{})}
	r, _ := fixtureRegistry(t, f)
	old, err := r.StartOAuth(t.Context(), 1, "cc", testSlot, "verified-login-session")
	if err != nil {
		t.Fatal(err)
	}
	current, err := r.StartOAuth(t.Context(), 1, "cc", testSlot, "verified-login-session")
	if err != nil {
		t.Fatal(err)
	}
	callback := "http://localhost:1455/auth/callback?code=private-code&state=" + testState
	if _, err := r.FinishOAuth(t.Context(), 1, "cc", testSlot, old.SessionID, callback, "verified-login-session"); err == nil {
		t.Fatal("superseded session accepted")
	}
	result := make(chan error, 1)
	go func() {
		_, err := r.FinishOAuth(t.Context(), 1, "cc", testSlot, current.SessionID, callback, "verified-login-session")
		result <- err
	}()
	select {
	case <-f.exchangeStarted:
	case <-time.After(3 * time.Second):
		close(f.exchangeContinue)
		t.Fatal("exchange did not start")
	}
	_, err = r.StartOAuth(t.Context(), 1, "cc", testSlot, "verified-login-session")
	close(f.exchangeContinue)
	if err == nil || !strings.Contains(err.Error(), "oauth_in_progress") {
		t.Fatalf("new authorization during exchange: %v", err)
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if f.exchangeCalls != 1 {
		t.Fatalf("exchange count=%d", f.exchangeCalls)
	}
}

// TestReuseDedicatedPoolKeepsExistingPool 将历史独占池接入新门户时只新增专用 Key，不重建或改写账号池。
func TestReuseDedicatedPoolKeepsExistingPool(t *testing.T) {
	f := &panelFixture{slot: true, auth: true, pool: true, legacyPool: true}
	_, c := fixtureRegistry(t, f)
	for range 2 {
		key, err := c.EnsurePoolKey(t.Context(), testSlot, testSlot)
		if err != nil || key.PoolID != "pool_one" {
			t.Fatalf("dedicated pool reuse: %v", err)
		}
	}
	if f.poolCalls != 0 || f.keyCalls != 1 {
		t.Fatalf("pool/key creation count=%d/%d", f.poolCalls, f.keyCalls)
	}
}

// TestSlotNameUsesCharacterLimit 确认中文名称和页面一致按60个字符限制，并在远端写入前拒绝非法名称。
func TestSlotNameUsesCharacterLimit(t *testing.T) {
	for _, tc := range []struct {
		name  string
		valid bool
	}{{strings.Repeat("字", 60), true}, {strings.Repeat("字", 61), false}, {"line\nbreak", false}, {string([]byte{0xff}), false}} {
		f := &panelFixture{}
		_, c := fixtureRegistry(t, f)
		slot, err := c.CreateSlot(t.Context(), CreateSlotRequest{ID: testSlot, Name: tc.name, ExitID: "px-local"})
		if tc.valid {
			if err != nil || slot.Name != tc.name {
				t.Fatalf("valid name rejected: %v", err)
			}
		} else if err == nil || f.createCalls != 0 {
			t.Fatal("invalid name reached upstream")
		}
	}
}

// TestRegistryRejectsAliasesOfSameOrigin 防止同一实例用不同登记ID绕过单槽位唯一绑定。
func TestRegistryRejectsAliasesOfSameOrigin(t *testing.T) {
	for _, origins := range [][2]string{{"https://EXAMPLE.com:443/", "https://example.com"}, {"https://example.com.:0443", "https://example.com"}, {"http://127.0.0.1:80", "http://127.0.0.1"}, {"https://[0:0:0:0:0:0:0:1]:443", "https://[::1]"}} {
		cfg := Config{Instances: []InstanceConfig{{ID: "one", Name: "One", BaseURL: origins[0], Username: "u", Password: "p", Models: []string{"m"}}, {ID: "two", Name: "Two", BaseURL: origins[1], Username: "u", Password: "p", Models: []string{"m"}}}}
		if _, err := New(cfg); err == nil {
			t.Fatalf("duplicate origins accepted: %v", origins)
		}
	}
	cfg := Config{Instances: []InstanceConfig{{ID: strings.Repeat("a", 65), Name: "One", BaseURL: "https://example.com", Username: "u", Password: "p", Models: []string{"m"}}}}
	if _, err := New(cfg); err == nil {
		t.Fatal("instance ID exceeds database length")
	}
	cfg.Instances[0].ID = strings.Repeat("a", 64)
	if _, err := New(cfg); err != nil {
		t.Fatalf("valid instance ID: %v", err)
	}
}
