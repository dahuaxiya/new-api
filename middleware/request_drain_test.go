package middleware

import (
	"bufio"
	"context"
	"errors"
	"github.com/QuantumNous/new-api/i18n"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRequestDrainPreservesActiveSSE 验证已开始的真实流保持可写，新请求被拒绝且就绪探针下线。
func TestRequestDrainPreservesActiveSSE(t *testing.T) {
	require.NoError(t, i18n.Init())
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	completed := make(chan error, 1)
	drain := NewRequestDrain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		<-release
		_, err := io.WriteString(w, "data: last\n\n")
		if err == nil {
			err = r.Context().Err()
		}
		completed <- err
	}), nil)
	server := httptest.NewServer(drain)
	t.Cleanup(server.Close)
	t.Cleanup(unblock)
	response, err := server.Client().Get(server.URL + "/v1/chat/completions")
	require.NoError(t, err)
	defer response.Body.Close()
	stream := bufio.NewReader(response.Body)
	line, err := stream.ReadString('\n')
	require.NoError(t, err)
	assert.Equal(t, "data: first\n", line)
	drain.BeginDrain()
	for path, code := range map[string]int{"/health/ready": 503, "/health/live": 200, "/v1/models": 503} {
		got, err := server.Client().Get(server.URL + path)
		require.NoError(t, err)
		assert.Equal(t, code, got.StatusCode, path)
		if path == "/v1/models" {
			assert.Equal(t, "1", got.Header.Get("Retry-After"))
		}
		require.NoError(t, got.Body.Close())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assert.ErrorIs(t, drain.Wait(ctx), context.Canceled, "active stream must still be counted")
	unblock()
	rest, err := io.ReadAll(stream)
	require.NoError(t, err)
	assert.Equal(t, "\ndata: last\n\n", string(rest))
	require.NoError(t, <-completed)
	require.NoError(t, drain.Wait(context.Background()))
}

// TestRequestDrainProbes 验证依赖故障只改变就绪状态，探针拒绝写方法且不泄漏内部错误。
func TestRequestDrainProbes(t *testing.T) {
	drain := NewRequestDrain(http.NotFoundHandler(), func(context.Context) error { return errors.New("private database address") })
	for _, tc := range []struct {
		method, path string
		status       int
	}{
		{"GET", "/health/ready", 503}, {"HEAD", "/health/live", 200}, {"POST", "/health/ready", 405},
	} {
		response := httptest.NewRecorder()
		drain.ServeHTTP(response, httptest.NewRequest(tc.method, tc.path, nil))
		assert.Equal(t, tc.status, response.Code)
		assert.Empty(t, response.Body.String())
		assert.Equal(t, "no-store", response.Header().Get("Cache-Control"))
	}
	drain = NewRequestDrain(http.NotFoundHandler(), func(context.Context) error { drain.BeginDrain(); return nil })
	response := httptest.NewRecorder()
	drain.ServeHTTP(response, httptest.NewRequest("GET", "/health/ready", nil))
	assert.Equal(t, 503, response.Code, "shutdown during dependency check must not publish stale readiness")
}
