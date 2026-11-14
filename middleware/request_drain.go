package middleware

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
)

// RequestDrain 在 HTTP 入口维护排空边界；直接传递原 ResponseWriter，保留 SSE Flush 和协议升级能力。
// 就绪探针仅表示此进程及依赖可接请求，不表示供货可用或已经具备多网关一致性。
type RequestDrain struct {
	next     http.Handler
	ready    func(context.Context) error
	mu       sync.Mutex
	draining bool
	active   int
	idle     chan struct{}
}

// NewRequestDrain 包装业务路由并提供固定探针；ready 由服务端提供依赖检查，不得由请求指定探测目标。
func NewRequestDrain(next http.Handler, ready func(context.Context) error) *RequestDrain {
	idle := make(chan struct{})
	close(idle)
	return &RequestDrain{next: next, ready: ready, idle: idle}
}

// BeginDrain 单向关闭新业务请求入口；已进入处理器的请求继续使用原上下文完成响应和结算。
func (d *RequestDrain) BeginDrain() {
	d.mu.Lock()
	d.draining = true
	d.mu.Unlock()
}

// Wait 等待已接收请求退出；必须先 BeginDrain，否则后续仍可接收新请求。
func (d *RequestDrain) Wait(ctx context.Context) error {
	d.mu.Lock()
	idle := d.idle
	d.mu.Unlock()
	select {
	case <-idle:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ServeHTTP 将排空检查与在途计数放在同一临界区，避免 Wait 已返回后又加入业务请求。
func (d *RequestDrain) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/health/ready" || r.URL.Path == "/health/live" {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Path == "/health/ready" {
			d.mu.Lock()
			draining := d.draining
			d.mu.Unlock()
			if draining {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			if d.ready != nil {
				ctx, cancel := context.WithTimeout(r.Context(), time.Second)
				err := d.ready(ctx)
				cancel()
				if err != nil {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
			}
			// 探测期间也可能收到退出信号；不能在摘流量之后返回过期的 ready。
			d.mu.Lock()
			draining = d.draining
			d.mu.Unlock()
			if draining {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
		}
		w.WriteHeader(http.StatusOK)
		return
	}
	d.mu.Lock()
	if d.draining {
		d.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Retry-After", "1")
		w.Header().Set("Connection", "close")
		w.WriteHeader(http.StatusServiceUnavailable)
		// 入口尚未经过认证或语言中间件，只依据 Accept-Language 和站点默认语言返回提示。
		message := i18n.Translate(i18n.ParseAcceptLanguage(r.Header.Get("Accept-Language")), i18n.MsgServerDraining)
		body, _ := common.Marshal(map[string]any{"error": map[string]string{"type": "server_draining", "message": message}})
		_, _ = w.Write(body)
		return
	}
	if d.active == 0 {
		d.idle = make(chan struct{})
	}
	d.active++
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		d.active--
		if d.active == 0 {
			close(d.idle)
		}
		d.mu.Unlock()
	}()
	d.next.ServeHTTP(w, r)
}
