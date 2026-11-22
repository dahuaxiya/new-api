package controller

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
				require.Equal(t, studioMediaPrefix+id+"/result", last.Image)
				stored, err := os.ReadFile(filepath.Join(os.Getenv("STUDIO_HISTORY_DIR"), "1", id+".result"))
				require.NoError(t, err)
				require.Equal(t, image, base64.StdEncoding.EncodeToString(stored))
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

// TestStudioRunImageOptions verifies legacy defaults and rejects invalid options before a run is claimed.
func TestStudioRunImageOptions(t *testing.T) {
	for _, scenario := range []struct {
		name, model, size, quality, format string
		compression                        *int
		wantFormat                         string
	}{
		{name: "legacy", model: "test", size: "1024x1024", quality: "medium", wantFormat: "png"},
		{name: "extended", model: "gpt-image-2", size: "1920x1088", quality: "auto", format: "webp", compression: new(0), wantFormat: "webp"},
		{name: "unaligned dimension", model: "gpt-image-2", size: "1920x1080", quality: "high"},
		{name: "outside pixel budget", model: "gpt-image-2", size: "3840x3840", quality: "high"},
		{name: "compression out of range", model: "gpt-image-2", size: "auto", quality: "low", format: "jpeg", compression: new(101)},
		{name: "compression with PNG", model: "gpt-image-2", size: "auto", quality: "low", compression: new(70)},
		{name: "unsupported model", model: "test", size: "1920x1088", quality: "high"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			in := studioIntent{
				Record: studioHistoryRecord{
					Mode: "image", Product: "standard",
					Entries: []studioHistoryEntry{{ID: 1, Prompt: "A tree", Model: scenario.model}},
				},
				Size: scenario.size, Quality: scenario.quality,
				OutputFormat: scenario.format, OutputCompression: scenario.compression,
			}
			path, raw, err := studioRunRequest(&in)
			if scenario.wantFormat == "" {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, "/api/retail/studio/images/generations", path)
			var body map[string]any
			require.NoError(t, common.Unmarshal(raw, &body))
			require.Equal(t, scenario.size, body["size"])
			require.Equal(t, scenario.quality, body["quality"])
			require.Equal(t, scenario.wantFormat, body["output_format"])
			if scenario.compression != nil {
				require.EqualValues(t, *scenario.compression, body["output_compression"])
			} else {
				require.NotContains(t, body, "output_compression")
			}
		})
	}
}

// TestStudioImageThread exercises the public run entry point through generation, repeated edits,
// stale-parent rejection and duplicate acknowledgement. Every historical original remains readable.
func TestStudioImageThread(t *testing.T) {
	t.Setenv("STUDIO_HISTORY_DIR", t.TempDir())
	image := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aD1sAAAAASUVORK5CYII="
	png, err := base64.StdEncoding.DecodeString(image)
	require.NoError(t, err)
	var calls atomic.Int32
	engine := gin.New()
	engine.Use(func(c *gin.Context) { c.Set("id", 1); c.Next() })
	engine.POST("/run/:id", StartStudioRun(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		var payload map[string]any
		require.NoError(t, common.DecodeJson(r.Body, &payload))
		require.Equal(t, []string{
			"A red fox",
			"Previous requests:\nA red fox\n\n\nCurrent request:\nAdd snow",
			"Previous requests:\nA red fox\nAdd snow\n\n\nCurrent request:\nMake it night",
		}[n-1], payload["prompt"])
		if n == 2 {
			require.Contains(t, r.URL.Path, "/edits")
			require.Contains(t, payload, "images")
		} else {
			require.Contains(t, r.URL.Path, "/generations")
			require.NotContains(t, payload, "images")
		}
		io.WriteString(w, `{"data":[{"b64_json":"`+image+`"}]}`)
	})))
	engine.GET("/run/:id", GetStudioRun)
	engine.GET(studioMediaPrefix+":id/:role", ReadStudioMedia)
	call := func(method, path string, payload []byte) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, req)
		return w
	}
	parent := ""
	for i, prompt := range []string{"A red fox", "Add snow", "Make it night"} {
		id := uuid.NewString()
		in := studioIntent{Parent: parent, Size: "auto", Quality: "auto", Record: studioHistoryRecord{ID: id, Mode: "image", Product: "standard", Entries: []studioHistoryEntry{{ID: i + 1, Model: "gpt-image-2", Prompt: prompt}}}}
		// Only the middle turn selects an image. The third keeps both earlier prompts without replaying the attachment.
		if i == 1 {
			in.Record.Entries[0].Reference = "data:image/png;base64," + image
		}
		raw, err := common.Marshal(in)
		require.NoError(t, err)
		path := "/run/" + id + "?owner=1"
		require.Equal(t, 202, call("POST", path, raw).Code)
		var out struct{ Record studioHistoryRecord }
		require.Eventually(t, func() bool {
			return common.Unmarshal(call("GET", "/run/"+id, nil).Body.Bytes(), &out) == nil && len(out.Record.Entries) > 0
		}, time.Second, time.Millisecond)
		require.Len(t, out.Record.Entries, i+1)
		for j, e := range out.Record.Entries {
			require.True(t, e.Complete, e.Error)
			require.Equal(t, png, call("GET", e.Image, nil).Body.Bytes())
			if j == 1 {
				require.Equal(t, png, call("GET", e.Reference, nil).Body.Bytes())
			} else {
				require.Empty(t, e.Reference)
			}
		}
		require.Equal(t, 200, call("POST", path, raw).Code)
		if parent != "" {
			require.Equal(t, 404, call("GET", "/run/"+parent, nil).Code)
			in.Record.ID = uuid.NewString()
			raw, _ = common.Marshal(in)
			require.Equal(t, 409, call("POST", "/run/"+in.Record.ID+"?owner=1", raw).Code)
		}
		parent = id
	}
	require.EqualValues(t, 3, calls.Load())
}
