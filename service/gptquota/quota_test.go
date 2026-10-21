package gptquota

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

// TestPanelFailureRetainsOldData exercises outage backoff without inventing a new observation.
func TestPanelFailureRetainsOldData(t *testing.T) {
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(503) }))
	defer upstream.Close()
	svc, err := New(Config{PanelURL: upstream.URL + "/api/panel", Login: map[string]string{"username": "test", "password": "test"}, Users: map[string]Binding{"2": {Username: "a", Group: "a", Slot: "s"}}})
	require.NoError(t, err)
	old := time.Now().Add(-11 * time.Minute).UTC().Format(time.RFC3339)
	svc.cache["s"] = cacheEntry{view: View{Observed: &old, Status: "ok"}, fetched: time.Now().Add(-time.Minute)}
	view, err := svc.Self(context.Background(), 2, "a", "a")
	require.NoError(t, err)
	assert.Equal(t, "error", view.Status)
	assert.True(t, view.Stale)
	assert.Equal(t, old, *view.Observed)
	_, err = svc.Self(context.Background(), 2, "a", "a")
	require.NoError(t, err)
	assert.Equal(t, 1, calls)
}

// TestPanelRedirectAndExpiry ensures credentials stay on the configured origin and expiry retries once.
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
	svc.token = "expired"
	view, err := svc.Self(context.Background(), 2, "a", "a")
	require.NoError(t, err)
	assert.True(t, view.Stale)
	assert.Equal(t, 2, reads)
	assert.Equal(t, 1, logins)
}

// TestRefreshIsolationAndCooldown proves explicit refresh uses only the bound slot,
// preserves cached availability while probing, and lets a disconnected caller rejoin.
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
			fmt.Fprintf(w, `{"ok":true,"data":{"vm":{"id":"alice-slot","codex_usage":{"snapshot":{"updated_at":%q},"windows":[{"id":"7d","used_percent":60}]}}}}`, observed)
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
	svc.mu.Lock()
	flight := svc.flights["alice-slot"]
	svc.mu.Unlock()
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

// TestRefreshRejectsFailedOrUnpersistedProbe prevents reporting stale data as a successful refresh.
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
			svc.cache["s"] = cacheEntry{view: View{Observed: &old, Status: "ok"}, fetched: time.Now()}
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
