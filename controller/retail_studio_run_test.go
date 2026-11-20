package controller

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// TestStudioRunDetached verifies lost acknowledgements, Unicode cursor replay, explicit cancellation
// and byte-identical image retrieval against a deterministic, non-billable HTTP relay.
func TestStudioRunDetached(t *testing.T) {
	for _, mode := range []string{"text", "image", "stop", "empty", "denied"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("STUDIO_HISTORY_DIR", t.TempDir())
			id := uuid.NewString()
			var paid atomic.Int32
			started, release := make(chan struct{}), make(chan struct{})
			image := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aD1sAAAAASUVORK5CYII="
			upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				paid.Add(1)
				close(started)
				if mode == "empty" || mode == "denied" {
					<-release
					if mode == "empty" {
						io.WriteString(w, "data: [DONE]\n\n")
					} else {
						w.WriteHeader(403)
						io.WriteString(w, `{"success":false,"message":"mock policy rejection"}`)
					}
					return
				}
				if mode == "stop" {
					<-r.Context().Done()
					return
				}
				if mode == "image" {
					<-release
					io.WriteString(w, `{"data":[{"b64_json":"`+image+`"}]}`)
					return
				}
				io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"好\"}}]}\n\n")
				<-release
				io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"呀\"}}]}\n\ndata: [DONE]\n\n")
			})
			engine := gin.New()
			engine.Use(func(c *gin.Context) { owner, _ := strconv.Atoi(c.GetHeader("test-owner")); c.Set("id", owner) })
			engine.POST("/runs/:id", StartStudioRun(upstream))
			engine.GET("/runs/:id", GetStudioRun)
			engine.DELETE("/runs/:id", StopStudioRun)
			kind := mode
			if kind != "image" {
				kind = "text"
			}
			in := studioIntent{Record: studioHistoryRecord{ID: id, Mode: kind, Product: "group", Entries: []studioHistoryEntry{{ID: 1, Prompt: "test", Model: "test"}}}, MaxTokens: 10240, Size: "1024x1024", Quality: "medium"}
			raw, err := common.Marshal(in)
			require.NoError(t, err)
			request := func(ctx context.Context, method string, owner int, params string) *httptest.ResponseRecorder {
				req := httptest.NewRequest(method, "/runs/"+id+"?owner=1"+params, bytes.NewReader(raw)).WithContext(ctx)
				req.Header.Set("test-owner", strconv.Itoa(owner))
				rr := httptest.NewRecorder()
				engine.ServeHTTP(rr, req)
				return rr
			}
			ctx, cancel := context.WithCancel(context.Background())
			require.Equal(t, 202, request(ctx, "POST", 1, "").Code)
			cancel()
			<-started
			ctx = context.Background()
			require.Equal(t, 200, request(ctx, "POST", 1, "").Code)
			require.Equal(t, 404, request(ctx, "GET", 2, "").Code)
			require.Equal(t, 403, request(ctx, "DELETE", 2, "").Code)
			if mode == "text" {
				require.Eventually(t, func() bool { return strings.Contains(request(ctx, "GET", 1, "").Body.String(), `"nextContent":3`) }, time.Second, time.Millisecond)
				require.Contains(t, request(ctx, "GET", 1, "&content=3").Body.String(), `"content":""`)
			}
			if mode == "stop" {
				require.Equal(t, 200, request(ctx, "DELETE", 1, "").Code)
			}
			close(release)
			var end struct {
				Running bool
				Record  studioHistoryRecord
			}
			require.Eventually(t, func() bool {
				return common.Unmarshal(request(ctx, "GET", 1, "").Body.Bytes(), &end) == nil && !end.Running
			}, 2*time.Second, time.Millisecond)
			last := end.Record.Entries[0]
			if mode == "text" {
				require.Equal(t, "好呀", last.Content)
				require.True(t, last.Complete)
			}
			if mode == "image" {
				require.Equal(t, "data:image/png;base64,"+image, last.Image)
				require.True(t, last.Complete)
			}
			if mode == "stop" {
				require.NotEmpty(t, last.Error)
				require.False(t, last.Complete)
			}
			require.Equal(t, 200, request(ctx, "POST", 1, "").Code)
			if mode == "empty" || mode == "denied" {
				require.False(t, last.Complete)
				require.NotEmpty(t, last.Error)
			}
			if mode == "denied" {
				require.Equal(t, "mock policy rejection", last.Error)
			}
			require.EqualValues(t, 1, paid.Load())
		})
	}
}
