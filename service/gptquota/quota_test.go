package gptquota

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestObserveForBypassesCachedOwner 验证历史采集不会把同槽位上一位用户的 UI 缓存记给新用户，
// 且对外实时视图不泄漏用于内部账号分段的身份指纹。
func TestObserveForBypassesCachedOwner(t *testing.T) {
	var calls atomic.Int32
	panel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/panel/login" {
			fmt.Fprint(w, `{"token":"fixture"}`)
			return
		}
		account := "old-private-account"
		if calls.Add(1) > 1 {
			account = "new-private-account"
		}
		fmt.Fprintf(w, `{"ok":true,"data":{"vm":{"id":"s","org_uuid":%q,"codex_usage":{"snapshot":{"updated_at":%q},"windows":[{"id":"7d","used_percent":20}]}}}}`, account, time.Now().UTC().Format(time.RFC3339))
	}))
	defer panel.Close()
	svc, err := New(Config{PanelURL: panel.URL + "/api/panel", Login: map[string]string{"username": "fixture", "password": "fixture"}})
	require.NoError(t, err)
	old, err := svc.SelfFor(context.Background(), 2, "alice", "alice", Binding{Username: "alice", Group: "alice", Slot: "s"})
	require.NoError(t, err)
	current, err := svc.ObserveFor(context.Background(), 3, "bob", "bob", Binding{Username: "bob", Group: "bob", Slot: "s"})
	require.NoError(t, err)
	require.NotEqual(t, old.AccountFingerprint, current.AccountFingerprint)
	require.EqualValues(t, 2, calls.Load())
	raw, err := common.Marshal(current)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "private-account")
	require.NotContains(t, string(raw), current.AccountFingerprint)
	_, err = svc.ObserveFor(context.Background(), 3, "bob", "bob", Binding{Username: "alice", Group: "alice", Slot: "s"})
	require.ErrorIs(t, err, ErrUnbound)
	require.EqualValues(t, 2, calls.Load())
}

// TestSelfIsolationAndPersistedReads proves callers cannot cross bindings or trigger a probe.
func TestSelfIsolationAndPersistedReads(t *testing.T) {
	reads, logins := 0, 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/panel/login" {
			logins++
			fmt.Fprint(w, `{"token":"panel-test"}`)
			return
		}
		require.Equal(t, http.MethodGet, r.Method)
		require.Equal(t, "Bearer panel-test", r.Header.Get("Authorization"))
		require.Equal(t, "/api/panel/vms/alice-slot", r.URL.Path)
		reads++
		fmt.Fprintf(w, `{"ok":true,"data":{"vm":{"id":"alice-slot","plan_type":"plus","codex_usage":{"snapshot":{"updated_at":%q},"windows":[{"id":"7d","used_percent":39,"reset_at":"2026-12-01T00:00:00Z"}]}}}}`, time.Now().UTC().Format(time.RFC3339))
	}))
	defer upstream.Close()
	svc, err := New(Config{PanelURL: upstream.URL + "/api/panel", Login: map[string]string{"username": "test", "password": "test"}, Users: map[string]Binding{"2": {Username: "alice", Group: "alice", Slot: "alice-slot", Label: "Alice"}}})
	require.NoError(t, err)
	for _, identity := range []struct {
		id          int
		name, group string
	}{{3, "bob", "bob"}, {2, "alice", "bob"}, {2, "bob", "alice"}} {
		view, err := svc.Self(context.Background(), identity.id, identity.name, identity.group)
		require.NoError(t, err)
		assert.False(t, view.Bound)
	}
	assert.Zero(t, logins)
	for range 2 {
		view, err := svc.Self(context.Background(), 2, "alice", "alice")
		require.NoError(t, err)
		assert.True(t, view.Bound)
		assert.False(t, view.Stale)
		assert.Nil(t, view.Windows[0].Used)
		assert.Nil(t, view.Windows[0].Remaining)
		require.NotNil(t, view.Windows[1].Remaining)
		assert.Equal(t, 61.0, *view.Windows[1].Remaining)
	}
	assert.Equal(t, 1, logins)
	assert.Equal(t, 1, reads)
}

// TestQuotaViewUnknownAndZero distinguishes unknown windows, valid zero and invalid units.
func TestQuotaViewUnknownAndZero(t *testing.T) {
	for _, tc := range []struct {
		name  string
		used  float64
		known bool
	}{{"zero", 0, true}, {"full", 100, true}, {"invalid", 101, false}, {"negative", -1, false}} {
		t.Run(tc.name, func(t *testing.T) {
			vm := persistedVM{}
			vm.Usage.Windows = []Window{{ID: "5h", Used: &tc.used}}
			view := quotaView(vm, time.Now())
			assert.Equal(t, "pending", view.Status)
			assert.Nil(t, view.Observed)
			if tc.known {
				require.NotNil(t, view.Windows[0].Remaining)
				assert.Equal(t, 100-tc.used, *view.Windows[0].Remaining)
			} else {
				assert.Nil(t, view.Windows[0].Used)
			}
		})
	}
	assert.Nil(t, validTime("2026-01-01T00:00:00"))
}

// TestPanelFailureRetainsOldData 验证面板故障时保留旧观察时间，并限制失败重试。
func TestPanelFailureRetainsOldData(t *testing.T) {
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(503) }))
	defer upstream.Close()
	svc, err := New(Config{PanelURL: upstream.URL + "/api/panel", Login: map[string]string{"username": "test", "password": "test"}, Users: map[string]Binding{"2": {Username: "a", Group: "a", Slot: "s"}}})
	require.NoError(t, err)
	old := time.Now().Add(-11 * time.Minute).UTC().Format(time.RFC3339)
	svc.backends[DefaultInstanceID].cache["s"] = cacheEntry{view: View{Observed: &old, Status: "ok"}, fetched: time.Now().Add(-time.Minute)}
	view, err := svc.Self(context.Background(), 2, "a", "a")
	require.NoError(t, err)
	assert.Equal(t, "error", view.Status)
	assert.True(t, view.Stale)
	assert.Equal(t, old, *view.Observed)
	_, err = svc.Self(context.Background(), 2, "a", "a")
	require.NoError(t, err)
	assert.Equal(t, 1, calls)
}

// TestPanelRedirectAndExpiry 验证凭据不会随重定向泄漏，且令牌过期只重新登录一次。
func TestPanelRedirectAndExpiry(t *testing.T) {
	leaked := false
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked = true }))
	defer other.Close()
	panel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, other.URL, 302) }))
	defer panel.Close()
	cfg := Config{PanelURL: panel.URL + "/api/panel", Login: map[string]string{"username": "test", "password": "test"}, Users: map[string]Binding{"2": {Username: "a", Group: "a", Slot: "s"}}}
	svc, err := New(cfg)
	require.NoError(t, err)
	_, err = svc.Self(context.Background(), 2, "a", "a")
	require.ErrorIs(t, err, ErrUnavailable)
	assert.False(t, leaked)
	cfg.Users["2"] = Binding{Username: "a", Group: "a", Slot: "../s"}
	_, err = New(cfg)
	require.Error(t, err)
	logins, reads := 0, 0
	refresh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/login") {
			logins++
			fmt.Fprint(w, `{"token":"new"}`)
			return
		}
		reads++
		if r.Header.Get("Authorization") == "Bearer expired" {
			w.WriteHeader(401)
			return
		}
		fmt.Fprint(w, `{"ok":true,"data":{"vm":{"id":"s"}}}`)
	}))
	defer refresh.Close()
	cfg.PanelURL = refresh.URL + "/api/panel"
	cfg.Users["2"] = Binding{Username: "a", Group: "a", Slot: "s"}
	svc, err = New(cfg)
	require.NoError(t, err)
	svc.backends[DefaultInstanceID].token = "expired"
	view, err := svc.Self(context.Background(), 2, "a", "a")
	require.NoError(t, err)
	assert.True(t, view.Stale)
	assert.Equal(t, 2, reads)
	assert.Equal(t, 1, logins)
}

// TestRefreshIsolationAndCooldown 验证探测只操作绑定槽位，探测期间仍可读缓存，
// 浏览器断连后重新加入同一任务不会重复调用上游。
func TestRefreshIsolationAndCooldown(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var probes atomic.Int32
	observed := time.Now().UTC().Format(time.RFC3339Nano)
	panel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/panel/login" {
			fmt.Fprint(w, `{"token":"private-panel"}`)
			return
		}
		assert.Equal(t, "Bearer private-panel", r.Header.Get("Authorization"))
		switch r.URL.Path {
		case "/api/panel/logout":
			fmt.Fprint(w, `{"ok":true}`)
		case "/api/panel/vms/alice-slot/probe":
			assert.Equal(t, http.MethodPost, r.Method)
			probes.Add(1)
			close(entered)
			<-release
			fmt.Fprintf(w, `{"ok":true,"data":{"ok":true,"probed_at":%q}}`, observed)
		case "/api/panel/vms/alice-slot":
			assert.Equal(t, http.MethodGet, r.Method)
			sample := observed
			if probes.Load() == 0 {
				sample = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)
			}
			fmt.Fprintf(w, `{"ok":true,"data":{"vm":{"id":"alice-slot","codex_usage":{"snapshot":{"updated_at":%q},"windows":[{"id":"7d","used_percent":60}]}}}}`, sample)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer panel.Close()
	svc, err := New(Config{PanelURL: panel.URL + "/api/panel", Login: map[string]string{"username": "a", "password": "p"}, Users: map[string]Binding{"2": {Username: "alice", Group: "alice", Slot: "alice-slot"}}})
	require.NoError(t, err)
	for _, identity := range []struct {
		id          int
		name, group string
	}{{3, "bob", "bob"}, {2, "bob", "alice"}, {2, "alice", "bob"}} {
		_, err := svc.Refresh(context.Background(), identity.id, identity.name, identity.group)
		require.ErrorIs(t, err, ErrUnbound)
	}
	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() { _, err := svc.Refresh(ctx, 2, "alice", "alice"); first <- err }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("probe did not start")
	}
	cancel()
	require.ErrorIs(t, <-first, context.Canceled)
	// A cache read is independent of the long-running probe's client and mutex.
	readCtx, readCancel := context.WithTimeout(context.Background(), time.Second)
	defer readCancel()
	_, err = svc.Self(readCtx, 2, "alice", "alice")
	require.NoError(t, err)
	// Joining the existing flight with a canceled waiter must not start another probe.
	_, err = svc.Refresh(ctx, 2, "alice", "alice")
	require.ErrorIs(t, err, context.Canceled)
	svc.backends[DefaultInstanceID].mu.Lock()
	flight := svc.backends[DefaultInstanceID].flights["alice-slot"]
	svc.backends[DefaultInstanceID].mu.Unlock()
	close(release)
	select {
	case <-flight.done:
	case <-time.After(3 * time.Second):
		t.Fatal("probe did not finish")
	}
	require.NoError(t, flight.err)
	view, err := svc.Self(context.Background(), 2, "alice", "alice")
	require.NoError(t, err)
	require.NotNil(t, view.Windows[1].Remaining)
	assert.Equal(t, 40.0, *view.Windows[1].Remaining)
	assert.Equal(t, observed, *view.Observed)
	assert.EqualValues(t, 1, probes.Load())
	_, err = svc.Refresh(context.Background(), 2, "alice", "alice")
	require.ErrorIs(t, err, ErrCooldown)
}

// TestRefreshRejectsFailedOrUnpersistedProbe 验证探测失败或快照未更新时不会报告刷新成功。
func TestRefreshRejectsFailedOrUnpersistedProbe(t *testing.T) {
	for _, mode := range []string{"http failure", "probe failure", "old snapshot"} {
		t.Run(mode, func(t *testing.T) {
			old := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)
			panel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasSuffix(r.URL.Path, "/login"):
					fmt.Fprint(w, `{"token":"panel"}`)
				case strings.HasSuffix(r.URL.Path, "/logout"):
					fmt.Fprint(w, `{"ok":true}`)
				case strings.HasSuffix(r.URL.Path, "/probe"):
					switch mode {
					case "http failure":
						w.WriteHeader(503)
					case "probe failure":
						fmt.Fprint(w, `{"ok":true,"data":{"ok":false}}`)
					default:
						fmt.Fprintf(w, `{"ok":true,"data":{"ok":true,"probed_at":%q}}`, time.Now().UTC().Format(time.RFC3339Nano))
					}
				default:
					fmt.Fprintf(w, `{"ok":true,"data":{"vm":{"id":"s","codex_usage":{"snapshot":{"updated_at":%q}}}}}`, old)
				}
			}))
			defer panel.Close()
			svc, err := New(Config{PanelURL: panel.URL + "/api/panel", Login: map[string]string{"username": "a", "password": "p"}, Users: map[string]Binding{"2": {Username: "a", Group: "a", Slot: "s"}}})
			require.NoError(t, err)
			svc.backends[DefaultInstanceID].cache["s"] = cacheEntry{view: View{Observed: &old, Status: "ok"}, fetched: time.Now()}
			_, err = svc.Refresh(context.Background(), 2, "a", "a")
			require.ErrorIs(t, err, ErrUnavailable)
			view, err := svc.Self(context.Background(), 2, "a", "a")
			require.NoError(t, err)
			assert.Equal(t, old, *view.Observed)
			assert.Equal(t, "error", view.Status)
			_, err = svc.Refresh(context.Background(), 2, "a", "a")
			require.ErrorIs(t, err, ErrCooldown)
		})
	}
}

// TestRefreshAcceptsSeparateProbeTimestamp reproduces vm2api's two clock reads:
// quota snapshot is stamped before reset-credit metadata generates probed_at.
// A later metadata timestamp must not turn a newly persisted sample into failure.
func TestRefreshAcceptsSeparateProbeTimestamp(t *testing.T) {
	for _, offset := range []time.Duration{0, time.Millisecond, 50 * time.Millisecond} {
		t.Run(offset.String(), func(t *testing.T) {
			fresh := time.Now().UTC()
			old := fresh.Add(-time.Minute).Format(time.RFC3339Nano)
			var completed atomic.Bool
			panel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasSuffix(r.URL.Path, "/login"):
					fmt.Fprint(w, `{"token":"panel"}`)
				case strings.HasSuffix(r.URL.Path, "/logout"):
					fmt.Fprint(w, `{"ok":true}`)
				case strings.HasSuffix(r.URL.Path, "/probe"):
					completed.Store(true)
					fmt.Fprintf(w, `{"ok":true,"data":{"ok":true,"probed_at":%q}}`, fresh.Add(offset).Format(time.RFC3339Nano))
				default:
					observed := old
					if completed.Load() {
						observed = fresh.Format(time.RFC3339Nano)
					}
					fmt.Fprintf(w, `{"ok":true,"data":{"vm":{"id":"s","codex_usage":{"snapshot":{"updated_at":%q},"windows":[{"id":"7d","used_percent":71}]}}}}`, observed)
				}
			}))
			defer panel.Close()
			svc, err := New(Config{PanelURL: panel.URL + "/api/panel", Login: map[string]string{"username": "a", "password": "p"}, Users: map[string]Binding{"2": {Username: "a", Group: "a", Slot: "s"}}})
			require.NoError(t, err)
			view, err := svc.Refresh(context.Background(), 2, "a", "a")
			require.NoError(t, err)
			assert.Equal(t, fresh.Format(time.RFC3339Nano), *view.Observed)
			assert.Equal(t, 29.0, *view.Windows[1].Remaining)
		})
	}
}

// quotaPanelFixture 模拟一个独立面板，验证实例凭据不会串用，并记录可观察的查询和探测次数。
type quotaPanelFixture struct {
	config    BackendConfig
	reads     atomic.Int32
	logins    atomic.Int32
	probes    atomic.Int32
	failReads atomic.Bool
	failProbe atomic.Bool
}

// newQuotaPanelFixture 为同名槽位提供不同额度，调用方通过故障开关验证实例隔离。
// t 管理测试生命周期；name 区分面板凭据，used 表示探测前已用百分比。
func newQuotaPanelFixture(t *testing.T, name string, used float64) *quotaPanelFixture {
	t.Helper()
	fixture := &quotaPanelFixture{}
	fresh := time.Now().UTC()
	panel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/panel/login" {
			var login map[string]string
			if !assert.NoError(t, common.DecodeJson(r.Body, &login)) || !assert.Equal(t, name, login["username"]) || !assert.Equal(t, "password-"+name, login["password"]) {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			fixture.logins.Add(1)
			fmt.Fprintf(w, `{"token":%q}`, "token-"+name)
			return
		}
		if !assert.Equal(t, "Bearer token-"+name, r.Header.Get("Authorization")) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/panel/logout":
			fmt.Fprint(w, `{"ok":true}`)
		case "/api/panel/vms/shared-slot":
			fixture.reads.Add(1)
			if fixture.failReads.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			sample, percent := fresh.Add(-time.Minute), used
			if fixture.probes.Load() > 0 && !fixture.failProbe.Load() {
				sample, percent = fresh, used+5
			}
			fmt.Fprintf(w, `{"ok":true,"data":{"vm":{"id":"shared-slot","codex_usage":{"snapshot":{"updated_at":%q},"windows":[{"id":"5h","used_percent":%v}]}}}}`, sample.Format(time.RFC3339Nano), percent)
		case "/api/panel/vms/shared-slot/probe":
			fixture.probes.Add(1)
			if fixture.failProbe.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			fmt.Fprintf(w, `{"ok":true,"data":{"ok":true,"probed_at":%q}}`, fresh.Format(time.RFC3339Nano))
		default:
			t.Errorf("unexpected panel path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(panel.Close)
	fixture.config = BackendConfig{PanelURL: panel.URL + "/api/panel", Login: map[string]string{"username": name, "password": "password-" + name}}
	return fixture
}

// TestLoadLegacyAndInstanceConfiguration 验证旧配置文件与具名实例共存，加载后调用方修改原 map 不会重定向绑定。
func TestLoadLegacyAndInstanceConfiguration(t *testing.T) {
	legacy := newQuotaPanelFixture(t, "legacy", 10)
	second := newQuotaPanelFixture(t, "second", 70)
	cfg := Config{
		PanelURL: legacy.config.PanelURL, Login: legacy.config.Login,
		Users:     map[string]Binding{"2": {Username: "alice", Group: "alice", Slot: "shared-slot"}, "3": {Username: "bob", Group: "bob", Slot: "shared-slot", InstanceID: "second"}},
		Instances: map[string]BackendConfig{"second": second.config},
	}
	raw, err := common.Marshal(cfg)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "quota.json")
	require.NoError(t, os.WriteFile(path, raw, 0600))
	svc, err := Load(path)
	require.NoError(t, err)
	alice, err := svc.Self(context.Background(), 2, "alice", "alice")
	require.NoError(t, err)
	require.NotNil(t, alice.Windows[0].Used)
	assert.Equal(t, 10.0, *alice.Windows[0].Used)
	bob, err := svc.Self(context.Background(), 3, "bob", "bob")
	require.NoError(t, err)
	require.NotNil(t, bob.Windows[0].Used)
	assert.Equal(t, 70.0, *bob.Windows[0].Used)

	snapshot, err := New(cfg)
	require.NoError(t, err)
	cfg.Users["2"] = cfg.Users["3"]
	cfg.Login["password"] = "changed"
	view, err := snapshot.Self(context.Background(), 2, "alice", "alice")
	require.NoError(t, err)
	assert.True(t, view.Bound)
	assert.Equal(t, 10.0, *view.Windows[0].Used)
}

// TestSelfForIdentityAndInstanceIsolation 验证动态绑定先授权，且同名槽位的缓存、令牌、失败退避互不影响。
func TestSelfForIdentityAndInstanceIsolation(t *testing.T) {
	first := newQuotaPanelFixture(t, "first", 10)
	second := newQuotaPanelFixture(t, "second", 70)
	svc, err := New(Config{Instances: map[string]BackendConfig{"first": first.config, "second": second.config}})
	require.NoError(t, err)
	binding := Binding{Username: "alice", Group: "alice", Slot: "shared-slot", InstanceID: "first", Label: "Alice"}
	for _, identity := range []struct {
		id              int
		username, group string
	}{{2, "bob", "alice"}, {2, "alice", "bob"}, {0, "alice", "alice"}} {
		view, err := svc.SelfFor(context.Background(), identity.id, identity.username, identity.group, binding)
		require.NoError(t, err)
		assert.False(t, view.Bound)
		_, err = svc.RefreshFor(context.Background(), identity.id, identity.username, identity.group, binding)
		require.ErrorIs(t, err, ErrUnbound)
	}
	for _, invalid := range []Binding{
		{Username: "alice", Group: "alice", Slot: "shared-slot", InstanceID: "unknown"},
		{Username: "alice", Group: "alice", Slot: "../shared-slot", InstanceID: "first"},
		{Username: "alice", Group: "alice", Slot: "shared-slot"},
	} {
		_, err := svc.SelfFor(context.Background(), 2, "alice", "alice", invalid)
		require.ErrorIs(t, err, ErrUnavailable)
		_, err = svc.RefreshFor(context.Background(), 2, "alice", "alice", invalid)
		require.ErrorIs(t, err, ErrUnavailable)
	}
	assert.Zero(t, first.logins.Load())
	assert.Zero(t, second.logins.Load())

	first.failReads.Store(true)
	for range 2 {
		_, err = svc.SelfFor(context.Background(), 2, "alice", "alice", binding)
		require.ErrorIs(t, err, ErrUnavailable)
	}
	assert.EqualValues(t, 1, first.reads.Load())
	other := Binding{Username: "bob", Group: "bob", Slot: "shared-slot", InstanceID: "second", Label: "Bob"}
	for range 2 {
		view, err := svc.SelfFor(context.Background(), 3, "bob", "bob", other)
		require.NoError(t, err)
		require.NotNil(t, view.Windows[0].Used)
		assert.Equal(t, 70.0, *view.Windows[0].Used)
		assert.Equal(t, "Bob", view.Label)
	}
	assert.EqualValues(t, 1, second.reads.Load())
	assert.EqualValues(t, 1, second.logins.Load())
	unbound, err := svc.Self(context.Background(), 3, "bob", "bob")
	require.NoError(t, err)
	assert.False(t, unbound.Bound, "dynamic bindings must not mutate static Users")
}

// TestRefreshForDynamicBindingAndInstanceIsolation 验证新绑定无需重建服务就能刷新，且同名槽位的失败和冷却只影响所属实例。
func TestRefreshForDynamicBindingAndInstanceIsolation(t *testing.T) {
	first := newQuotaPanelFixture(t, "first", 10)
	second := newQuotaPanelFixture(t, "second", 70)
	svc, err := New(Config{Instances: map[string]BackendConfig{"first": first.config, "second": second.config}})
	require.NoError(t, err)
	alice := Binding{Username: "alice", Group: "alice", Slot: "shared-slot", InstanceID: "first"}
	bob := Binding{Username: "bob", Group: "bob", Slot: "shared-slot", InstanceID: "second"}
	for _, tc := range []struct {
		id      int
		binding Binding
		used    float64
	}{{2, alice, 10}, {3, bob, 70}} {
		view, err := svc.SelfFor(context.Background(), tc.id, tc.binding.Username, tc.binding.Group, tc.binding)
		require.NoError(t, err)
		require.NotNil(t, view.Windows[0].Used)
		assert.Equal(t, tc.used, *view.Windows[0].Used)
	}
	first.failProbe.Store(true)
	_, err = svc.RefreshFor(context.Background(), 2, "alice", "alice", alice)
	require.ErrorIs(t, err, ErrUnavailable)
	view, err := svc.RefreshFor(context.Background(), 3, "bob", "bob", bob)
	require.NoError(t, err)
	assert.Equal(t, 75.0, *view.Windows[0].Used)
	assert.Equal(t, "ok", view.Status)
	old, err := svc.SelfFor(context.Background(), 2, "alice", "alice", alice)
	require.NoError(t, err)
	assert.Equal(t, "error", old.Status)
	assert.Equal(t, 10.0, *old.Windows[0].Used)
	for _, tc := range []struct {
		id      int
		binding Binding
	}{{2, alice}, {3, bob}} {
		_, err = svc.RefreshFor(context.Background(), tc.id, tc.binding.Username, tc.binding.Group, tc.binding)
		require.ErrorIs(t, err, ErrCooldown)
	}
	assert.EqualValues(t, 1, first.probes.Load())
	assert.EqualValues(t, 1, second.probes.Load())
	_, err = svc.Refresh(context.Background(), 3, "bob", "bob")
	require.ErrorIs(t, err, ErrUnbound)
}

// TestInstanceConfigurationRejectsAmbiguity 拒绝未知静态实例、重名默认实例及不安全后端地址。
func TestInstanceConfigurationRejectsAmbiguity(t *testing.T) {
	backend := BackendConfig{PanelURL: "https://panel.example/api/panel", Login: map[string]string{"username": "admin", "password": "test"}}
	for _, tc := range []struct {
		name   string
		config Config
	}{
		{"no backend", Config{}},
		{"empty instance ID", Config{Instances: map[string]BackendConfig{"": backend}}},
		{"duplicate legacy", Config{PanelURL: backend.PanelURL, Login: backend.Login, Instances: map[string]BackendConfig{DefaultInstanceID: backend}}},
		{"unknown static instance", Config{Instances: map[string]BackendConfig{"first": backend}, Users: map[string]Binding{"2": {Username: "a", Group: "a", Slot: "s", InstanceID: "unknown"}}}},
		{"remote plaintext", Config{Instances: map[string]BackendConfig{"first": {PanelURL: "http://panel.example/api/panel", Login: backend.Login}}}},
	} {
		t.Run(tc.name, func(t *testing.T) { _, err := New(tc.config); require.ErrorIs(t, err, ErrUnavailable) })
	}
}
