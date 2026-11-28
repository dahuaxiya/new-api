package controller

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/retailprice"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// TestStudioVideoValidation 拒绝无法计价的规格、旧会话和参考附件，保留合法视频入口。
func TestStudioVideoValidation(t *testing.T) {
	for _, scenario := range []string{"valid", "high-resolution", "zero", "oversized", "aspect", "model", "parent", "reference", "upload"} {
		t.Run(scenario, func(t *testing.T) {
			in := studioIntent{
				Record: studioHistoryRecord{Mode: "video", Product: "ultra", Entries: []studioHistoryEntry{{Prompt: "A carrot", Model: "grok-imagine-video"}}},
				Video:  &retailprice.VideoRequest{Duration: 1, Resolution: "480p", AspectRatio: "16:9"},
			}
			switch scenario {
			case "high-resolution":
				in.Video.Resolution = "1080p"
			case "zero":
				in.Video.Duration = 0
			case "oversized":
				in.Video.Duration = 16
			case "aspect":
				in.Video.AspectRatio = "invalid"
			case "model":
				in.Record.Entries[0].Model = "chat-model"
			case "parent":
				in.Parent = uuid.NewString()
			case "reference":
				in.Record.Entries[0].Reference = "data:image/png;base64,example"
			case "upload":
				in.uploadedReference = true
			}
			path, _, err := studioRunRequest(&in)
			if scenario == "valid" {
				require.NoError(t, err)
				require.Equal(t, "/api/retail/studio/videos/generations", path)
				in.Record.Entries[0].Model, in.Video.Resolution = "grok-imagine-video-1.5", "1080p"
				_, _, err = studioRunRequest(&in)
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

// TestStudioVideoRecovery 覆盖待核账记录保护和文件已完成、公开快照尚未提交时的恢复。
func TestStudioVideoRecovery(t *testing.T) {
	dir, id := t.TempDir(), uuid.NewString()
	key := filepath.Join(dir, id)
	row := studioHistoryRecord{ID: id, Mode: "video", Running: true, UpdatedAt: time.Now().UnixMilli(), Entries: []studioHistoryEntry{{ID: 1, Prompt: "A carrot"}}}
	require.NoError(t, storeStudioRecord(dir, &row))
	state := studioVideoState{TaskID: "task_existing", Phase: "review"}
	require.NoError(t, storeStudioVideoState(key, &state))
	require.False(t, resumeStudioVideo(1, key, &row))
	require.ErrorIs(t, deleteStudioRecord(key), errStudioVideoAccountingPending)
	expiredHead := studioHistoryHead{Mode: "video", UpdatedAt: time.Now().Add(-2 * studioHistoryRetention).UnixMilli()}
	require.False(t, studioRecordExpired(expiredHead, key, time.Now()))
	_, err := os.Stat(key + ".record")
	require.NoError(t, err)
	for _, phase := range []string{"accepted", "completed", "submitting", "settled"} {
		state.Phase = phase
		require.NoError(t, storeStudioVideoState(key, &state))
		require.ErrorIs(t, deleteStudioRecord(key), errStudioVideoAccountingPending)
	}
	state.Phase = "finished"
	require.NoError(t, storeStudioVideoState(key, &state))
	require.True(t, studioRecordExpired(expiredHead, key, time.Now()))
	require.NoError(t, storeStudioImage(key+".video", []byte("existing verified file")))
	require.False(t, resumeStudioVideo(1, key, &row))
	require.False(t, row.Running)
	require.True(t, row.Entries[0].Complete)
	require.Equal(t, studioMediaPrefix+id+"/video", row.Entries[0].Video)
	require.NoError(t, deleteStudioRecord(key))
	_, err = os.Stat(key + ".video-state")
	require.ErrorIs(t, err, os.ErrNotExist)
}

// TestStudioVideoMedia 验证先查询制品列表，再携访问凭证下载同源 MP4，失败时保留已保存文件。
func TestStudioVideoMedia(t *testing.T) {
	setupSecurityEnrollmentTest(t)
	require.NoError(t, model.DB.AutoMigrate(&model.Channel{}, &model.GPTHostingBinding{}))
	base := "https://aixingxing.pro"
	require.NoError(t, model.DB.Create(&model.Channel{Id: 101, Name: "video", Type: 1, BaseURL: &base, Key: "test-only", Status: common.ChannelStatusEnabled}).Error)
	previous := studioVideoClient
	t.Cleanup(func() { studioVideoClient = previous })
	key := filepath.Join(t.TempDir(), uuid.NewString())
	calls := 0
	body, status := "\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00mp42isom", http.StatusOK
	contentPath := "/v1/tasks/task_existing/artifacts/video/content"
	contentURL := base + contentPath + "?access=test-capability"
	listedTask := "task_existing"
	studioVideoClient = &http.Client{Transport: retailPublicRatesTestTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		require.Equal(t, "aixingxing.pro", r.URL.Host)
		require.Equal(t, "Bearer test-only", r.Header.Get("Authorization"))
		if r.URL.Path == "/v1/tasks/task_existing/artifacts" {
			listing, err := common.Marshal(gin.H{"task_id": listedTask, "artifacts": []taskArtifactResponse{{Key: "video", Type: "video", MimeType: "video/mp4", ContentURL: contentURL}}})
			require.NoError(t, err)
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(listing))}, nil
		}
		require.Equal(t, contentPath, r.URL.Path)
		require.Equal(t, "test-capability", r.URL.Query().Get("access"))
		return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	require.Error(t, storeStudioVideoFile(context.Background(), key, 101, "../other"))
	require.Zero(t, calls)
	for _, target := range []string{"https://evil.example" + contentPath, "//evil.example" + contentPath, "http://aixingxing.pro" + contentPath, "/v1/tasks/task_other/artifacts/video/content", "/v1/videos/id/content", "/admin"} {
		contentURL = target
		before := calls
		require.Error(t, storeStudioVideoFile(context.Background(), key, 101, "task_existing"))
		require.Equal(t, before+1, calls)
	}
	contentURL = base + contentPath + "?access=test-capability"
	listedTask = "task_other"
	require.Error(t, storeStudioVideoFile(context.Background(), key, 101, "task_existing"))
	listedTask = "task_existing"
	require.NoError(t, storeStudioVideoFile(context.Background(), key, 101, "task_existing"))
	saved, err := os.ReadFile(key + ".video")
	require.NoError(t, err)
	require.Equal(t, body, string(saved))
	for _, failure := range []string{"html", "redirect", "oversized"} {
		body, status = "<html>not a video</html>", http.StatusOK
		if failure == "redirect" {
			status = http.StatusFound
		}
		if failure == "oversized" {
			body = strings.Repeat("x", (76<<20)+1)
		}
		require.Error(t, storeStudioVideoFile(context.Background(), key, 101, "task_existing"))
		current, err := os.ReadFile(key + ".video")
		require.NoError(t, err)
		require.Equal(t, saved, current)
	}
}

// TestStudioChatResult replays a split Gemini tool call and checks the saved result that a
// reconnecting browser reads. A missing stream terminator must take precedence over tool hints.
func TestStudioChatResult(t *testing.T) {
	const plan = `{"choices":[{"delta":{"reasoning_content":"plan"}}]}`
	const call = `{"choices":[{"delta":{"tool_calls":[{"function":{"arguments":"private-argument"}}]}}]}`
	const finish = `{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`
	const text = `{"choices":[{"delta":{"content":"<p>ok</p>"}}]}`
	const whitespace = `{"choices":[{"delta":{"content":" \n"}}]}`
	const toolError = "The model requested a tool but returned no reply."
	for _, tc := range []struct {
		name                string
		frames              []string
		wantBody, wantError string
		contextError        error
		panicRelay          bool
	}{
		{name: "captured shape", frames: []string{plan, call, finish, "[DONE]"}, wantError: toolError},
		{name: "tool delta only", frames: []string{plan, call, "[DONE]"}, wantError: toolError},
		{name: "tool finish only", frames: []string{plan, finish, "[DONE]"}, wantError: toolError},
		{name: "reasoning and whitespace", frames: []string{plan, whitespace, "[DONE]"}, wantBody: " \n", wantError: "The model returned no reply."},
		{name: "visible answer", frames: []string{plan, call, text, "[DONE]"}, wantBody: "<p>ok</p>"},
		{name: "empty reply with terminator", frames: []string{"[DONE]"}, wantError: "The model returned no reply."},
		{name: "lost connection preserves partial answer", frames: []string{plan, text}, wantBody: "<p>ok</p>", wantError: "The model stream ended before completion. Check usage before trying again."},
		{name: "explicit stop", frames: []string{plan}, contextError: context.Canceled, wantError: "Stopped. Check usage before trying again."},
		{name: "generation deadline", frames: []string{plan}, contextError: context.DeadlineExceeded, wantError: "Generation timed out. Check usage before trying again."},
		{name: "relay panic", frames: []string{plan}, panicRelay: true, wantError: "Generation was interrupted. Check usage before trying again."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, id := t.TempDir(), uuid.NewString()
			ctx, cancel := context.WithCancel(context.Background())
			if tc.contextError == context.DeadlineExceeded {
				cancel()
				ctx, cancel = context.WithDeadline(context.Background(), time.Unix(1, 0))
			} else if tc.contextError == context.Canceled {
				cancel()
			}
			defer cancel()
			worker := studioWorker{cancel: cancel}
			row := studioHistoryRecord{ID: id, Mode: "text", Entries: []studioHistoryEntry{{ID: 1, Prompt: "test", Model: "test"}}}
			relay := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				for _, frame := range tc.frames {
					io.WriteString(w, "data: "+frame+"\n\n")
				}
				if tc.panicRelay {
					panic("test relay failure")
				}
			})
			studioSlots <- struct{}{}
			worker.execute(relay, httptest.NewRequestWithContext(ctx, http.MethodPost, "/", nil), dir, filepath.Join(dir, id), &sync.Mutex{}, &row)
			raw, err := os.ReadFile(filepath.Join(dir, id+".record"))
			require.NoError(t, err)
			_, snapshot, ok := bytes.Cut(raw, []byte("\n"))
			require.True(t, ok)
			var saved studioHistoryRecord
			require.NoError(t, common.Unmarshal(snapshot, &saved))
			entry := saved.Entries[0]
			require.Equal(t, tc.wantError, entry.Error)
			require.Equal(t, tc.wantError == "", entry.Complete)
			require.Equal(t, tc.wantBody, entry.Content)
			if tc.name != "empty reply with terminator" {
				require.Equal(t, "plan", entry.Reasoning)
			}
			require.NotContains(t, string(raw), "private-argument")
		})
	}
}

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
				require.Equal(t, "Stopped. Check usage before trying again.", last.Error)
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

// TestStudioImageURLAllowed covers both supplier origins and rejects other CDN tenants and ports.
func TestStudioImageURLAllowed(t *testing.T) {
	const bucket = "pub-13fd1d3607a441b4a9d232b3a223f5f6"
	for raw, allowed := range map[string]bool{
		"https://" + bucket + ".r2.lat/images/file.png?sig=test": true,
		"https://" + bucket + ".r2.dev/images/file.png":          true,
		"https://other.r2.lat/images/file.png":                   false,
		"https://" + bucket + ".r2.lat:444/images/file.png":      false,
		"https://" + bucket + ".r2.lat/private/file.png":         false,
	} {
		asset, err := url.Parse(raw)
		require.NoError(t, err)
		require.Equal(t, allowed, studioImageURLAllowed(asset), raw)
	}
}

// TestStudioResultImageFallback checks that a broken field does not hide usable bytes in the same
// response and that the diagnostic for a rejected link cannot expose its signed query.
func TestStudioResultImageFallback(t *testing.T) {
	const png = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aD1sAAAAASUVORK5CYII="
	got, err := studioResultImage(context.Background(), []byte(`{"data":{"b64_json":"!","url":"data:image/png;base64,`+png+`"}}`))
	require.NoError(t, err)
	require.Equal(t, png, base64.StdEncoding.EncodeToString(got))
	_, err = studioResultImage(context.Background(), []byte(`{"data":[{"url":"https://untrusted.invalid/images/a?signature=private"}]}`))
	require.Equal(t, studioImageFailure("url_rejected"), err)
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
		// The supplier may return an expiring URL unless both generations and edits ask for bytes.
		if payload["response_format"] != "b64_json" {
			io.WriteString(w, `{"data":[{"url":"https://untrusted.invalid/temporary.png"}]}`)
			return
		}
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
