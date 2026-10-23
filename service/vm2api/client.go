// Package vm2api 将开户所需的面板能力限制在服务端登记的实例及固定接口内。
package vm2api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
)

var identifier = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,80}$`)
var managedIdentifier = regexp.MustCompile(`^na-[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// Config 只能由服务器私有配置文件构造，浏览器不能选择任意上游地址。
type Config struct {
	Instances []InstanceConfig `json:"instances"`
}

// InstanceConfig 保存面板管理凭据；禁止将此结构序列化到 API 响应或日志。
type InstanceConfig struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	BaseURL  string   `json:"base_url"`
	Username string   `json:"username"`
	Password string   `json:"password"`
	Models   []string `json:"models"`
}

// InstanceView 仅提供开户选择器需要的名称和模型，不公开主机地址或凭据。
type InstanceView struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	BaseURL string   `json:"-"`
	Models  []string `json:"models"`
}

// Error 的文本和代码均由本地定义，不携带上游响应、授权码或管理密码。
type Error struct {
	Code       string
	HTTPStatus int
}

// Error 返回可记录的固定诊断信息。
func (e *Error) Error() string { return fmt.Sprintf("vm2api %s (HTTP %d)", e.Code, e.HTTPStatus) }

// Registry 持有固定实例及短期 OAuth 会话；重启后未完成授权必须重新发起。
type Registry struct {
	mu        sync.RWMutex
	clients   map[string]*Client
	order     []string
	sessions  map[string]oauthState
	oauthBusy map[string]bool
}

// Client 将管理会话仅缓存在服务端内存，并禁止重定向和环境代理泄露凭据。
type Client struct {
	cfg        InstanceConfig
	http       *http.Client
	mu         sync.Mutex
	token      string
	tokenUntil time.Time
}

// Load 读取权限收紧的私有文件；配置错误时不会回退到任意默认实例。
func Load(path string) (*Registry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("vm2api configuration unavailable")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 1<<20 {
		return nil, errors.New("vm2api configuration must be a private regular file")
	}
	data, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return nil, errors.New("vm2api configuration unavailable")
	}
	var cfg Config
	if common.Unmarshal(data, &cfg) != nil {
		return nil, errors.New("vm2api configuration invalid")
	}
	return New(cfg)
}

// Load 原子替换登记表；已有授权状态随配置更换失效，避免跨实例换票。
func (r *Registry) Load(path string) error {
	next, err := Load(path)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.oauthBusy) != 0 {
		return &Error{Code: "oauth_in_progress", HTTPStatus: 409}
	}
	r.clients, r.order, r.sessions, r.oauthBusy = next.clients, next.order, next.sessions, next.oauthBusy
	return nil
}

// New 校验固定服务地址；HTTP 仅允许数字形式的回环地址供本机部署使用。
func New(cfg Config) (*Registry, error) {
	r := &Registry{clients: map[string]*Client{}, sessions: map[string]oauthState{}, oauthBusy: map[string]bool{}}
	if len(cfg.Instances) > 32 {
		return nil, errors.New("too many vm2api instances")
	}
	origins := make(map[string]bool)
	for _, instance := range cfg.Instances {
		u, err := url.Parse(instance.BaseURL)
		if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (u.Path != "" && u.Path != "/") || u.Opaque != "" {
			return nil, errors.New("invalid vm2api base URL")
		}
		u.Scheme = strings.ToLower(u.Scheme)
		host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
		if host == "" {
			return nil, errors.New("invalid vm2api host")
		}
		ip := net.ParseIP(host)
		if u.Scheme != "https" && !(u.Scheme == "http" && ip != nil && ip.IsLoopback()) {
			return nil, errors.New("vm2api requires HTTPS")
		}
		if !identifier.MatchString(instance.ID) || len(instance.ID) > 64 || instance.Name == "" || len(instance.Name) > 100 || instance.Username == "" || instance.Password == "" || len(instance.Models) == 0 {
			return nil, errors.New("incomplete vm2api instance configuration")
		}
		if _, exists := r.clients[instance.ID]; exists {
			return nil, errors.New("duplicate vm2api instance")
		}
		for _, model := range instance.Models {
			if strings.TrimSpace(model) == "" || len(model) > 200 {
				return nil, errors.New("invalid vm2api model")
			}
		}
		// 同一实例不能通过大小写、默认端口或另一 ID 重复登记，否则槽位的实例级唯一约束会被绕过。
		port := u.Port()
		if port != "" {
			number, err := strconv.Atoi(port)
			if err != nil || number < 1 || number > 65535 {
				return nil, errors.New("invalid vm2api port")
			}
			port = strconv.Itoa(number)
			if (u.Scheme == "https" && number == 443) || (u.Scheme == "http" && number == 80) {
				port = ""
			}
		}
		if ip != nil {
			host = ip.String()
		}
		u.Host = host
		if strings.Contains(host, ":") {
			u.Host = "[" + host + "]"
		}
		if port != "" {
			u.Host = net.JoinHostPort(host, port)
		}
		u.Path, u.RawPath = "", ""
		instance.BaseURL = u.String()
		if origins[instance.BaseURL] {
			return nil, errors.New("duplicate vm2api origin")
		}
		origins[instance.BaseURL] = true
		instance.Models = slices.Clone(instance.Models)
		transport := http.DefaultTransport.(*http.Transport).Clone()
		// 固定实例可使用私网地址，但不允许 HTTP_PROXY 将管理密码转交给另一主机。
		transport.Proxy = nil
		r.clients[instance.ID] = &Client{cfg: instance, http: &http.Client{Timeout: 90 * time.Second, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
		r.order = append(r.order, instance.ID)
	}
	return r, nil
}

// Instances 返回独立副本，调用者不能修改登记表中的模型范围。
func (r *Registry) Instances() []InstanceView {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]InstanceView, 0, len(r.order))
	for _, id := range r.order {
		c := r.clients[id]
		out = append(out, InstanceView{ID: id, Name: c.cfg.Name, BaseURL: c.cfg.BaseURL, Models: slices.Clone(c.cfg.Models)})
	}
	return out
}

// Client 只接受预登记 ID，绝不将未知 ID 解释成 URL。
func (r *Registry) Client(id string) (*Client, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.clients[id]
	if !ok {
		return nil, &Error{Code: "instance_not_found", HTTPStatus: 404}
	}
	return c, nil
}

// Config 仅供服务端渠道及额度适配使用；返回值包含管理密码，不能输出给浏览器。
func (c *Client) Config() InstanceConfig {
	result := c.cfg
	result.Models = slices.Clone(c.cfg.Models)
	return result
}

// request 仅供固定端点方法使用；一次 401 可以刷新会话，网络错误不会盲目重放写请求。
func (c *Client) request(ctx context.Context, method, path string, body, target any) error {
	for attempt := 0; attempt < 2; attempt++ {
		token, err := c.login(ctx)
		if err != nil {
			return err
		}
		status, err := c.roundTrip(ctx, method, path, body, token, target)
		if status != 401 || attempt == 1 {
			return err
		}
		c.mu.Lock()
		if c.token == token {
			c.token = ""
		}
		c.mu.Unlock()
	}
	return &Error{Code: "authentication_failed", HTTPStatus: 401}
}

// login 合并并发登录，既避免重复面板会话，也不向调用方暴露密码或 token。
func (c *Client) login(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && time.Now().Before(c.tokenUntil) {
		return c.token, nil
	}
	var response struct {
		Token     string `json:"token"`
		Role      string `json:"role"`
		ExpiresIn int    `json:"expires_in"`
	}
	_, err := c.roundTrip(ctx, http.MethodPost, "/api/panel/login", map[string]string{"username": c.cfg.Username, "password": c.cfg.Password}, "", &response)
	if err != nil {
		return "", err
	}
	if response.Token == "" || response.Role != "admin" {
		return "", &Error{Code: "admin_authentication_required", HTTPStatus: 403}
	}
	c.token = response.Token
	c.tokenUntil = time.Now().Add(time.Duration(max(1, min(response.ExpiresIn, 3600))) * time.Second)
	return c.token, nil
}

// roundTrip 丢弃所有上游错误正文；管理接口可能回显密码或授权码，不能透传。
func (c *Client) roundTrip(ctx context.Context, method, path string, body any, token string, target any) (int, error) {
	if !strings.HasPrefix(path, "/api/panel/") || strings.ContainsAny(path, "?#\\") || strings.Contains(path, "..") {
		return 0, &Error{Code: "invalid_path", HTTPStatus: 400}
	}
	var payload []byte
	if body != nil {
		var err error
		payload, err = common.Marshal(body)
		if err != nil {
			return 0, &Error{Code: "invalid_request", HTTPStatus: 400}
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.cfg.BaseURL+path, bytes.NewReader(payload))
	if err != nil {
		return 0, &Error{Code: "invalid_request", HTTPStatus: 400}
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return 0, &Error{Code: "upstream_unavailable", HTTPStatus: 502}
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		code := "upstream_rejected"
		// 托管协议仅传播白名单错误码，绝不传播 OAuth/代理响应正文。
		if strings.HasPrefix(path, "/api/panel/hosting/") {
			var failure struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if common.DecodeJson(io.LimitReader(res.Body, 8192), &failure) == nil {
				switch failure.Error.Code {
				case "slot_not_empty", "slot_unavailable", "exit_unavailable", "account_changed", "personal_account_required", "identity_invalid", "identity_unavailable", "hosting_busy", "cleanup_pending", "hosting_conflict":
					code = failure.Error.Code
				}
			}
		}
		return res.StatusCode, &Error{Code: code, HTTPStatus: res.StatusCode}
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, (2<<20)+1))
	if err != nil || len(data) > 2<<20 {
		return res.StatusCode, &Error{Code: "invalid_upstream_response", HTTPStatus: 502}
	}
	var envelope struct {
		OK bool `json:"ok"`
	}
	if common.Unmarshal(data, &envelope) != nil || !envelope.OK {
		return res.StatusCode, &Error{Code: "upstream_rejected", HTTPStatus: 502}
	}
	if target != nil && common.Unmarshal(data, target) != nil {
		return res.StatusCode, &Error{Code: "invalid_upstream_response", HTTPStatus: 502}
	}
	return res.StatusCode, nil
}
