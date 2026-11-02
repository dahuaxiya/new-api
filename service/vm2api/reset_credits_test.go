package vm2api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// TestHostedResetCredits 验证两个平台只调用固定槽位端点，且网络故障不会重放消费。
func TestHostedResetCredits(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/panel/login" {
			fmt.Fprint(w, `{"ok":true,"token":"fixture","role":"admin","expires_in":3600}`)
			return
		}
		calls.Add(1)
		if strings.HasSuffix(r.URL.Path, "/reset-use") {
			// 关闭连接模拟响应丢失，而不是由客户端触发第二次消费。
			conn, _, _ := w.(http.Hijacker).Hijack()
			_ = conn.Close()
			return
		}
		require.Equal(t, "/api/panel/hosting/slots/slot-a/reset-query", r.URL.Path)
		fmt.Fprint(w, `{"ok":true,"data":{"platform":"claude","available_count":2,"items":[],"private":"hidden"}}`)
	}))
	defer server.Close()
	registry, err := New(Config{Instances: []InstanceConfig{{ID: "fixture", Name: "fixture", BaseURL: server.URL, Username: "fixture", Password: "fixture", Models: []string{"model"}}}})
	require.NoError(t, err)
	client, err := registry.Client("fixture")
	require.NoError(t, err)
	binding, account, subject := uuid.NewString(), strings.Repeat("a", 64), strings.Repeat("b", 64)
	result, err := client.HostedResetCredits(context.Background(), "slot-a", binding, account, subject, "claude", "reset-query", "", 1)
	require.NoError(t, err)
	require.Equal(t, 2, result.AvailableCount)
	require.Empty(t, result.Items)
	_, err = client.HostedResetCredits(context.Background(), "../bad", binding, account, subject, "codex", "reset-use", uuid.NewString(), 1)
	require.Error(t, err)
	require.EqualValues(t, 1, calls.Load())
	_, err = client.HostedResetCredits(context.Background(), "slot-a", binding, account, subject, "codex", "reset-use", uuid.NewString(), 2)
	require.Error(t, err)
	require.EqualValues(t, 2, calls.Load(), "消费响应丢失不能重发")
}
