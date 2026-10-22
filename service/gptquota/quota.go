// Package gptquota reads vm2api subscription windows and supports explicit user
// refreshes. It never changes New API billing or inference routing.
package gptquota

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
)

var ErrUnavailable = errors.New("subscription quota unavailable")
var ErrCooldown = errors.New("quota refresh cooldown")
var ErrUnbound = errors.New("quota account not bound")

type refreshFlight struct {
	done chan struct{}
	err  error
}

var slotName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

type Binding struct {
	Username string `json:"username"`
	Group    string `json:"group"`
	Slot     string `json:"slot"`
	Label    string `json:"label"`
}
type Config struct {
	PanelURL string             `json:"panel_url"`
	Login    map[string]string  `json:"vm_login"`
	Users    map[string]Binding `json:"users"`
}
type Window struct {
	ID        string   `json:"id"`
	Used      *float64 `json:"used_percent"`
	Remaining *float64 `json:"remaining_percent"`
	Reset     *string  `json:"reset_at"`
}
type View struct {
	Bound       bool     `json:"bound"`
	Label       string   `json:"account_label,omitempty"`
	Plan        *string  `json:"plan"`
	Windows     []Window `json:"windows"`
	Observed    *string  `json:"observed_at"`
	Checked     *string  `json:"last_checked_at"`
	Status      string   `json:"sync_status"`
	Stale       bool     `json:"stale"`
	PollSeconds int      `json:"poll_seconds"`
}
type persistedVM struct {
	ID    string `json:"id"`
	Plan  string `json:"plan_type"`
	Usage struct {
		Snapshot struct {
			Updated string `json:"updated_at"`
		} `json:"snapshot"`
		Windows []Window `json:"windows"`
	} `json:"codex_usage"`
}
type cacheEntry struct {
	view    View
	fetched time.Time
}
type Service struct {
	config Config
	client *http.Client
	// Serialize short cached panel reads and session renewal. No official probe is
	// performed while holding this lock; background refresh workers have their own client.
	mu          sync.Mutex
	token       string
	cache       map[string]cacheEntry
	retryAfter  time.Time
	flights     map[string]*refreshFlight
	lastRefresh map[string]time.Time
}

// Load reads only the operator-selected file; browsers cannot choose a mapping or URL.
func Load(path string) (*Service, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, ErrUnavailable
	}
	var cfg Config
	if common.Unmarshal(raw, &cfg) != nil {
		return nil, ErrUnavailable
	}
	return New(cfg)
}

// New validates fixed deployment configuration and forbids credential-bearing redirects.
// HTTP is accepted only on loopback; remote panel access must use verified TLS.
func New(cfg Config) (*Service, error) {
	u, err := url.Parse(cfg.PanelURL)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "/api/panel" || u.Host == "" {
		return nil, ErrUnavailable
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "::1")) {
		return nil, ErrUnavailable
	}
	if cfg.Login["username"] == "" || cfg.Login["password"] == "" {
		return nil, ErrUnavailable
	}
	for id, b := range cfg.Users {
		n, err := strconv.Atoi(id)
		if err != nil || n <= 0 || strconv.Itoa(n) != id || !slotName.MatchString(b.Slot) || b.Username == "" || b.Group == "" {
			return nil, ErrUnavailable
		}
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &Service{config: cfg, cache: make(map[string]cacheEntry), flights: make(map[string]*refreshFlight), lastRefresh: make(map[string]time.Time), client: &http.Client{
		Transport: transport, Timeout: 8 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

// Self resolves the authenticated identity before any cache lookup. Username and
// group checks fail closed if an operator repurposes a numeric user ID.
func (s *Service) Self(ctx context.Context, id int, username, group string) (View, error) {
	b, ok := s.config.Users[strconv.Itoa(id)]
	if !ok || b.Username != username || b.Group != group {
		return View{Bound: false}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	entry, cached := s.cache[b.Slot]
	if !cached || now.Sub(entry.fetched) >= 15*time.Second {
		// Failed reads retain dated samples; retry throttling avoids login storms if
		// the panel is down. Authorization itself is never cached by this service.
		if now.Before(s.retryAfter) {
			if !cached {
				return View{}, ErrUnavailable
			}
			entry.view.Status = "error"
		} else {
			vm, err := s.readVM(ctx, b.Slot)
			if err != nil {
				s.retryAfter = now.Add(15 * time.Second)
				if !cached {
					return View{}, ErrUnavailable
				}
				entry.view.Status = "error"
			} else {
				entry.view = quotaView(vm, now)
			}
		}
		entry.fetched = now
		checked := now.UTC().Format(time.RFC3339)
		entry.view.Checked = &checked
		s.cache[b.Slot] = entry
	}
	view := entry.view
	view.Bound, view.Label, view.PollSeconds = true, b.Label, 300
	view.Stale = true
	if view.Observed != nil {
		observed, err := time.Parse(time.RFC3339Nano, *view.Observed)
		view.Stale = err != nil || observed.After(now) || now.Sub(observed) > 600*time.Second
	}
	return view, nil
}

// readVM renews the panel session once after expiry and only reads the bound slot.
// A 403 is not retried: it is a permission failure, not evidence of token expiry.
func (s *Service) readVM(ctx context.Context, slot string) (persistedVM, error) {
	for attempt := 0; attempt < 2; attempt++ {
		if s.token == "" {
			var login struct {
				Token string `json:"token"`
			}
			if _, err := s.request(ctx, "/login", s.config.Login, &login); err != nil || login.Token == "" {
				return persistedVM{}, ErrUnavailable
			}
			s.token = login.Token
		}
		var result struct {
			OK   bool `json:"ok"`
			Data struct {
				VM persistedVM `json:"vm"`
			} `json:"data"`
		}
		status, err := s.request(ctx, "/vms/"+slot, nil, &result)
		if status == http.StatusUnauthorized {
			s.token = ""
			continue
		}
		if err != nil || !result.OK || result.Data.VM.ID != slot {
			return persistedVM{}, ErrUnavailable
		}
		return result.Data.VM, nil
	}
	return persistedVM{}, ErrUnavailable
}

// request bounds panel reads and returns generic errors, excluding credentials
// and upstream bodies from public responses and logs. Callers either hold s.mu
// or own an isolated worker instance so session state is never shared concurrently.
func (s *Service) request(ctx context.Context, path string, body any, target any) (int, error) {
	method := http.MethodGet
	var reader io.Reader
	if body != nil {
		raw, err := common.Marshal(body)
		if err != nil {
			return 0, ErrUnavailable
		}
		reader, method = bytes.NewReader(raw), http.MethodPost
	}
	req, err := http.NewRequestWithContext(ctx, method, s.config.PanelURL+path, reader)
	if err != nil {
		return 0, ErrUnavailable
	}
	req.Header.Set("Content-Type", "application/json")
	if path != "/login" {
		req.Header.Set("Authorization", "Bearer "+s.token)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return 0, ErrUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, ErrUnavailable
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 || common.Unmarshal(raw, target) != nil {
		return resp.StatusCode, ErrUnavailable
	}
	return resp.StatusCode, nil
}

// quotaView exposes only window percentages and dates. Missing windows remain
// unknown; reset time passing never fabricates a fresh zero-percent reading.
func quotaView(vm persistedVM, now time.Time) View {
	view := View{Windows: []Window{{ID: "5h"}, {ID: "7d"}}, Status: "pending"}
	if strings.Contains("|free|plus|pro|team|business|enterprise|edu|", "|"+vm.Plan+"|") && vm.Plan != "" {
		view.Plan = &vm.Plan
	}
	view.Observed = validTime(vm.Usage.Snapshot.Updated)
	if view.Observed != nil {
		view.Status = "ok"
	}
	for i := range view.Windows {
		for _, row := range vm.Usage.Windows {
			if row.ID != view.Windows[i].ID {
				continue
			}
			view.Windows[i].Reset = validTimeValue(row.Reset)
			if row.Used != nil && !math.IsNaN(*row.Used) && !math.IsInf(*row.Used, 0) && *row.Used >= 0 && *row.Used <= 100 {
				used := *row.Used
				remaining := math.Round((100-used)*1e4) / 1e4
				view.Windows[i].Used, view.Windows[i].Remaining = &used, &remaining
			}
		}
	}
	return view
}

// validTimeValue preserves null dates returned by windows missing upstream data.
func validTimeValue(value *string) *string {
	if value == nil {
		return nil
	}
	return validTime(*value)
}

// validTime accepts timezone-qualified timestamps only and normalizes them to UTC.
func validTime(value string) *string {
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return nil
	}
	normalized := t.UTC().Format(time.RFC3339Nano)
	return &normalized
}

// Refresh starts at most one official probe per bound slot. Concurrent callers
// share its completion; new attempts (including failures) have a 30-second gap.
// The bounded worker survives a browser disconnect so retries cannot duplicate it.
func (s *Service) Refresh(ctx context.Context, id int, username, group string) (View, error) {
	b, ok := s.config.Users[strconv.Itoa(id)]
	if !ok || b.Username != username || b.Group != group {
		return View{}, ErrUnbound
	}
	s.mu.Lock()
	flight := s.flights[b.Slot]
	if flight == nil {
		if time.Since(s.lastRefresh[b.Slot]) < 30*time.Second {
			s.mu.Unlock()
			return View{}, ErrCooldown
		}
		flight = &refreshFlight{done: make(chan struct{})}
		s.flights[b.Slot], s.lastRefresh[b.Slot] = flight, time.Now()
		go s.probeBoundSlot(b.Slot, flight)
	}
	s.mu.Unlock()
	select {
	case <-ctx.Done():
		return View{}, ctx.Err()
	case <-flight.done:
		if flight.err != nil {
			return View{}, flight.err
		}
		return s.Self(ctx, id, username, group)
	}
}

// probeBoundSlot uses a short-lived panel session and separate transport so a
// slow official probe cannot block other users' cached reads. Only operator-
// configured slot names enter this path; no browser-supplied IDs are accepted.
func (s *Service) probeBoundSlot(slot string, flight *refreshFlight) {
	started := time.Now()
	stage := "panel_read_before_probe"
	probeStatus := 0
	ctx, cancel := context.WithTimeout(context.Background(), 70*time.Second)
	defer cancel()
	worker, err := New(s.config)
	var view View
	if err == nil {
		worker.client.Timeout = 65 * time.Second
		defer worker.client.CloseIdleConnections()
		// Login/read verifies the slot before the only mutating panel request.
		var before persistedVM
		before, err = worker.readVM(ctx, slot)
		if err == nil {
			var result struct {
				OK   bool `json:"ok"`
				Data struct {
					OK       bool   `json:"ok"`
					ProbedAt string `json:"probed_at"`
				} `json:"data"`
			}
			stage = "official_probe"
			probeStatus, err = worker.request(ctx, "/vms/"+slot+"/probe", map[string]any{}, &result)
			if err == nil && (!result.OK || !result.Data.OK || validTime(result.Data.ProbedAt) == nil) {
				stage = "invalid_probe_response"
				err = ErrUnavailable
			}
			if err == nil {
				stage = "panel_read_after_probe"
				var vm persistedVM
				vm, err = worker.readVM(ctx, slot)
				if err == nil {
					observed, parseErr := time.Parse(time.RFC3339Nano, vm.Usage.Snapshot.Updated)
					previous, previousErr := time.Parse(time.RFC3339Nano, before.Usage.Snapshot.Updated)
					// vm2api stamps the quota sample before its reset-credit metadata.
					// probed_at describes that later metadata and can be milliseconds newer
					// than a successfully persisted quota sample. Compare the same snapshot
					// field before/after instead; this also avoids cross-host clock assumptions.
					stage = "snapshot_not_advanced"
					if parseErr != nil || (previousErr == nil && !observed.After(previous)) {
						err = ErrUnavailable
					} else {
						view = quotaView(vm, time.Now())
					}
				}
			}
		}
		if worker.token != "" {
			// Logout has its own deadline even if the probe timed out. Never expose the
			// panel credential or a probe error body through the user-facing endpoint.
			logoutCtx, logoutCancel := context.WithTimeout(context.Background(), 5*time.Second)
			var ignored any
			_, _ = worker.request(logoutCtx, "/logout", map[string]any{}, &ignored)
			logoutCancel()
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if err == nil {
		checked := now.UTC().Format(time.RFC3339)
		view.Checked = &checked
		s.cache[slot] = cacheEntry{view: view, fetched: now}
	} else {
		flight.err = ErrUnavailable
		// Record only fixed stages, a validated slot name and status/timing. Raw
		// upstream bodies and session credentials must never reach logs or users.
		common.SysError(common.LogText("GPT quota refresh failed: slot=%s stage=%s probe_status=%d elapsed_ms=%d", slot, stage, probeStatus, time.Since(started).Milliseconds()))
		if entry, ok := s.cache[slot]; ok {
			entry.view.Status = "error"
			s.cache[slot] = entry
		}
	}
	delete(s.flights, slot)
	close(flight.done)
}
