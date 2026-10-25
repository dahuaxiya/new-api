package vm2api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
)

// TestHostedOAuthBinding 验证用户、门户会话、绑定编号和 state 均匹配后才能一次性兑换回调。
func TestHostedOAuthBinding(t *testing.T) {
	var exchanged atomic.Int32
	state := strings.Repeat("s", 40)
	inner := "https://auth.openai.com/oauth/authorize?" + url.Values{"response_type": {"code"}, "state": {state}, "code_challenge_method": {"S256"}, "code_challenge": {strings.Repeat("c", 43)}, "redirect_uri": {"http://localhost:1455/auth/callback"}}.Encode()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/panel/login":
			fmt.Fprint(w, `{"ok":true,"token":"synthetic","role":"admin","expires_in":3600}`)
		case strings.HasSuffix(r.URL.Path, "/login"):
			fmt.Fprintf(w, `{"ok":true,"data":{"auth_url":%q,"session_id":"upstream-session","expires_at":%d}}`, inner, time.Now().Add(time.Minute).UnixMilli())
		case strings.HasSuffix(r.URL.Path, "/prepare"):
			exchanged.Add(1)
			fmt.Fprint(w, `{"ok":true,"data":{"vm_id":"slot","binding_id":"binding","state":"prepared"}}`)
		default:
			w.WriteHeader(409)
			fmt.Fprint(w, `{"ok":false,"error":{"code":"account_changed","message":"private token must not escape"}}`)
		}
	}))
	defer server.Close()
	registry, e := New(Config{Instances: []InstanceConfig{{ID: "cc", Name: "CC", BaseURL: server.URL, Username: "test", Password: "test", Models: []string{"gpt-test"}}}})
	require.NoError(t, e)
	auth, e := registry.StartHostedOAuth(context.Background(), 1, "portal-session", "cc", "slot", "binding", 1)
	require.NoError(t, e)
	callback := "http://localhost:1455/auth/callback?code=synthetic&state=" + state
	for _, bad := range []struct {
		id                         int
		session, binding, callback string
	}{{2, "portal-session", "binding", callback}, {1, "other", "binding", callback}, {1, "portal-session", "other", callback}, {1, "portal-session", "binding", strings.Replace(callback, state, "bad", 1)}, {1, "portal-session", "binding", callback + "&code=second"}, {1, "portal-session", "binding", strings.Replace(callback, "localhost:1455", "evil.test", 1)}} {
		_, e = registry.PrepareHostedOAuth(context.Background(), bad.id, bad.session, "cc", "slot", bad.binding, auth.SessionID, bad.callback, 2)
		require.Error(t, e)
	}
	require.Zero(t, exchanged.Load())
	_, e = registry.PrepareHostedOAuth(context.Background(), 1, "portal-session", "cc", "slot", "binding", auth.SessionID, callback, 2)
	require.NoError(t, e)
	_, e = registry.PrepareHostedOAuth(context.Background(), 1, "portal-session", "cc", "slot", "binding", auth.SessionID, callback, 2)
	require.Error(t, e)
	require.Equal(t, int32(1), exchanged.Load())
	client, _ := registry.Client("cc")
	_, e = client.HostingAction(context.Background(), "slot", "activate", map[string]any{})
	require.Equal(t, "vm2api account_changed (HTTP 409)", e.Error())
	raw, _ := common.Marshal(e)
	require.NotContains(t, string(raw), "private token")
}

// TestHostedClaudeOAuthSeats 验证同用户的两个授权互不取消，Claude 回调不能跨名额、跨平台或重放。
func TestHostedClaudeOAuthSeats(t *testing.T) {
	var exchanged atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/panel/login":
			fmt.Fprint(w, `{"ok":true,"token":"fixture","role":"admin","expires_in":3600}`)
		case strings.HasSuffix(r.URL.Path, "/login"):
			fmt.Fprintf(w, `{"ok":true,"data":{"auth_url":"https://claude.com/cai/oauth/authorize?state=seat-state&code_challenge=challenge&code_challenge_method=S256","session_id":"remote-session","expires_at":%d}}`, time.Now().Add(time.Minute).UnixMilli())
		case strings.HasSuffix(r.URL.Path, "/prepare"):
			exchanged.Add(1)
			fmt.Fprint(w, `{"ok":true,"data":{"state":"prepared"}}`)
		}
	}))
	defer server.Close()
	registry, err := New(Config{Instances: []InstanceConfig{{ID: "cc", Name: "CC", BaseURL: server.URL, Username: "fixture", Password: "fixture", Models: []string{"gpt-test"}, ClaudeModels: []string{"claude-test"}}}})
	require.NoError(t, err)
	first, err := registry.StartHostedOAuth(context.Background(), 1, "portal", "cc", "slot-a", "binding-a", 1, "claude")
	require.NoError(t, err)
	second, err := registry.StartHostedOAuth(context.Background(), 1, "portal", "cc", "slot-b", "binding-b", 1, "claude")
	require.NoError(t, err)
	for _, bad := range []string{"code#wrong-state", "code#seat-state\ninjected", "http://localhost:1455/auth/callback?code=code&state=seat-state"} {
		_, err = registry.PrepareHostedOAuth(context.Background(), 1, "portal", "cc", "slot-a", "binding-a", first.SessionID, bad, 2)
		require.Error(t, err)
	}
	_, err = registry.PrepareHostedOAuth(context.Background(), 1, "portal", "cc", "slot-b", "binding-b", first.SessionID, "code#seat-state", 2)
	require.Error(t, err)
	for _, entry := range []struct {
		auth          OAuthSession
		slot, binding string
	}{{first, "slot-a", "binding-a"}, {second, "slot-b", "binding-b"}} {
		_, err = registry.PrepareHostedOAuth(context.Background(), 1, "portal", "cc", entry.slot, entry.binding, entry.auth.SessionID, "code#seat-state", 2)
		require.NoError(t, err)
		_, err = registry.PrepareHostedOAuth(context.Background(), 1, "portal", "cc", entry.slot, entry.binding, entry.auth.SessionID, "code#seat-state", 2)
		require.Error(t, err)
	}
	require.EqualValues(t, 2, exchanged.Load())
	_, _, err = hostedAuthorization("https://evil.test/cai/oauth/authorize?state=s", "claude")
	require.Error(t, err)
	_, _, err = hostedAuthorization("https://claude.com/cai/oauth/authorize?state=s&code_challenge=x&code_challenge_method=plain", "claude")
	require.Error(t, err)
}
