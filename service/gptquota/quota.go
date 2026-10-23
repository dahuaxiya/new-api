// Package gptquota reads vm2api subscription windows and supports explicit user
// refreshes. It never changes New API billing or inference routing.
package gptquota

import (
	"bytes"
	"context"
	"errors"
	"io"
	"maps"
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

// DefaultInstanceID 标识旧版单实例配置，未指定实例的历史绑定仍使用此后端。
const DefaultInstanceID = "legacy"

type Binding struct {
	InstanceID string `json:"instance_id"`
	Username   string `json:"username"`
	Group      string `json:"group"`
	Slot       string `json:"slot"`
	Label      string `json:"label"`
}

// BackendConfig 只能由管理员的部署配置提供，不能接受浏览器传入的地址或凭据。
type BackendConfig struct {
	PanelURL string            `json:"panel_url"`
	Login    map[string]string `json:"vm_login"`
}
type Config struct {
	PanelURL  string                   `json:"panel_url"`
	Login     map[string]string        `json:"vm_login"`
	Users     map[string]Binding       `json:"users"`
	Instances map[string]BackendConfig `json:"instances"`
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
	// 配置创建后保持只读；每次请求传入的开户绑定不会写入历史静态用户映射。
	config   Config
	backends map[string]*backend
}

type backend struct {
	config BackendConfig
	client *http.Client
	// 只串行化同一实例的短查询和登录续期。官方探测使用独立客户端，
	// 一个后端的慢请求、失败退避或同名槽位都不会阻塞或污染其他实例。
	mu          sync.Mutex
	token       string
	cache       map[string]cacheEntry
	retryAfter  time.Time
	flights     map[string]*refreshFlight
	lastRefresh map[string]time.Time
}

// Load 只读取管理员指定的配置文件，浏览器不能借此选择映射、后端地址或凭据。
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

// New 验证并复制部署配置；历史单后端归入 legacy，新配置也可只声明多个实例。
// 顶层与实例表同时定义 legacy 时拒绝启动，避免同一绑定在不同配置顺序下指向不同机器。
func New(cfg Config) (*Service, error) {
	instances := maps.Clone(cfg.Instances)
	if instances == nil {
		instances = make(map[string]BackendConfig)
	}
	if cfg.PanelURL != "" || len(cfg.Login) != 0 {
		if _, duplicate := instances[DefaultInstanceID]; duplicate {
			return nil, ErrUnavailable
		}
		instances[DefaultInstanceID] = BackendConfig{PanelURL: cfg.PanelURL, Login: cfg.Login}
	}
	if len(instances) == 0 {
		return nil, ErrUnavailable
	}
	svc := &Service{config: Config{Users: maps.Clone(cfg.Users)}, backends: make(map[string]*backend)}
	for id, configuration := range instances {
		if !slotName.MatchString(id) {
			return nil, ErrUnavailable
		}
		u, err := url.Parse(configuration.PanelURL)
		if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "/api/panel" || u.RawPath != "" || u.Host == "" {
			return nil, ErrUnavailable
		}
		if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "::1")) {
			return nil, ErrUnavailable
		}
		if configuration.Login["username"] == "" || configuration.Login["password"] == "" {
			return nil, ErrUnavailable
		}
		// 复制凭据，调用方后续复用或修改传入 map 不得更改已经验证的后端。
		configuration.Login = maps.Clone(configuration.Login)
		svc.backends[id] = newBackend(configuration)
	}
	for id, binding := range svc.config.Users {
		n, err := strconv.Atoi(id)
		if err != nil || n <= 0 || strconv.Itoa(n) != id || binding.Username == "" || binding.Group == "" {
			return nil, ErrUnavailable
		}
		if _, err := svc.boundBackend(binding); err != nil {
			return nil, ErrUnavailable
		}
	}
	return svc, nil
}

// newBackend 为一个已经验证过的固定后端创建独立会话和缓存；探测工作器也复用此构造。
// 禁用代理与自动重定向，面板密码和令牌只发送到管理员配置的原始地址。
func newBackend(cfg BackendConfig) *backend {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &backend{config: cfg, cache: make(map[string]cacheEntry), flights: make(map[string]*refreshFlight), lastRefresh: make(map[string]time.Time), client: &http.Client{
		Transport: transport, Timeout: 8 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

// boundBackend 只解析已配置的实例和合法槽位；未知实例不会回退到 legacy 或其他账号。
// 调用前必须验证绑定与当前身份一致，避免未授权请求触发缓存查询或后端访问。
func (s *Service) boundBackend(b Binding) (*backend, error) {
	if !slotName.MatchString(b.Slot) {
		return nil, ErrUnavailable
	}
	instanceID := b.InstanceID
	if instanceID == "" {
		instanceID = DefaultInstanceID
	}
	result, exists := s.backends[instanceID]
	if !exists {
		return nil, ErrUnavailable
	}
	return result, nil
}

// Self 使用历史静态用户映射查询额度；授权判断和动态绑定采用同一流程。
func (s *Service) Self(ctx context.Context, id int, username, group string) (View, error) {
	return s.SelfFor(ctx, id, username, group, s.config.Users[strconv.Itoa(id)])
}

// SelfFor 查询由服务端数据库解析的开户绑定；调用方必须按当前用户 ID 查询绑定记录。
// 用户名和分组每次都与已认证身份核对，避免数字 ID 被复用后读到原用户额度。
// 此方法不会写入静态 Users，新增用户无需重启即可查询已配置实例上的本人槽位。
func (s *Service) SelfFor(ctx context.Context, id int, username, group string, b Binding) (View, error) {
	if id <= 0 || username == "" || group == "" || b.Username != username || b.Group != group {
		return View{Bound: false}, nil
	}
	instance, err := s.boundBackend(b)
	if err != nil {
		return View{}, err
	}
	return instance.self(ctx, b)
}

// self 读取一个已验证绑定的快照，缓存与失败退避只作用于该实例。
func (s *backend) self(ctx context.Context, b Binding) (View, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	entry, cached := s.cache[b.Slot]
	if !cached || now.Sub(entry.fetched) >= 15*time.Second {
		// 后端不可用时保留旧快照及其观察时间，并限制重新登录频率。
		// 用户授权不使用缓存，每次访问都在进入此方法前重新验证。
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

// readVM 只读取已绑定槽位，令牌过期时最多重新登录一次。
// 403 表示权限拒绝而非令牌过期，不重试登录；会话只能属于当前实例。
func (s *backend) readVM(ctx context.Context, slot string) (persistedVM, error) {
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

// request 限制面板响应大小并返回通用错误，不泄漏上游响应体或凭据。
// 调用方须持有当前实例锁或独占工作器，避免会话令牌被并发读写。
func (s *backend) request(ctx context.Context, path string, body any, target any) (int, error) {
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

// Refresh 保持旧版静态用户映射的手动刷新入口，并复用动态绑定的身份检查。
func (s *Service) Refresh(ctx context.Context, id int, username, group string) (View, error) {
	return s.RefreshFor(ctx, id, username, group, s.config.Users[strconv.Itoa(id)])
}

// RefreshFor 对服务端解析的绑定执行官方探测；绑定必须与当前身份一致。
// 实例内同槽位共享一次探测与冷却时间，浏览器断连不会取消工作器或绕过限流。
func (s *Service) RefreshFor(ctx context.Context, id int, username, group string, b Binding) (View, error) {
	if id <= 0 || username == "" || group == "" || b.Username != username || b.Group != group {
		return View{}, ErrUnbound
	}
	instance, err := s.boundBackend(b)
	if err != nil {
		return View{}, err
	}
	return instance.refresh(ctx, b)
}

// refresh 原子检查同一实例、同一槽位的运行中探测和冷却时间；失败尝试也计入冷却。
func (s *backend) refresh(ctx context.Context, b Binding) (View, error) {
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
		return s.self(ctx, b)
	}
}

// probeBoundSlot 使用当前实例的独立短期会话，慢探测不阻塞缓存查询。
// 槽位必须来自经过身份检查的服务端绑定，完成后注销探测会话并通知所有等待者。
func (s *backend) probeBoundSlot(slot string, flight *refreshFlight) {
	started := time.Now()
	stage := "panel_read_before_probe"
	probeStatus := 0
	ctx, cancel := context.WithTimeout(context.Background(), 70*time.Second)
	defer cancel()
	worker := newBackend(s.config)
	var err error
	var view View
	worker.client.Timeout = 65 * time.Second
	defer worker.client.CloseIdleConnections()
	// 官方探测会改变上游状态，执行前先登录并确认槽位存在。
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
				// vm2api 先写快照，再写重置信息；probed_at 可能比成功快照稍晚。
				// 只比较同一快照字段的前后值，避免误判及跨机器时钟误差。
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
		// 即使探测超时也独立限时注销会话，凭据和上游错误体不进入用户响应。
		logoutCtx, logoutCancel := context.WithTimeout(context.Background(), 5*time.Second)
		var ignored any
		_, _ = worker.request(logoutCtx, "/logout", map[string]any{}, &ignored)
		logoutCancel()
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
		// 仅记录固定阶段、已验证槽位和状态耗时，日志不包含上游正文或会话凭据。
		common.SysError(common.LogText("GPT quota refresh failed: slot=%s stage=%s probe_status=%d elapsed_ms=%d", slot, stage, probeStatus, time.Since(started).Milliseconds()))
		if entry, ok := s.cache[slot]; ok {
			entry.view.Status = "error"
			s.cache[slot] = entry
		}
	}
	delete(s.flights, slot)
	close(flight.done)
}
