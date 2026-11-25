package controller

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/shirou/gopsutil/disk"
)

// A UUID names one paid intent for one account. The durable claim survives process restarts.
// Only active tasks live in memory on the retail instance; a lost worker is never redispatched.
var studioRuns sync.Map
var studioSlots = make(chan struct{}, 12)
var studioUploadSlots = make(chan struct{}, 8)

const interruptedRun = "Stopped. Check usage before trying again."

type studioIntent struct {
	Record            studioHistoryRecord `json:"record"`
	Parent            string              `json:"parent"`
	MaxTokens         int                 `json:"maxTokens"`
	Size              string              `json:"size"`
	Quality           string              `json:"quality"`
	OutputFormat      string              `json:"outputFormat,omitempty"`
	OutputCompression *int                `json:"outputCompression,omitempty"`
	uploadedReference bool
	chatImages        map[int]string
}
type studioWorker struct {
	cancel             context.CancelFunc
	mu                 sync.Mutex
	content, reasoning strings.Builder
}
type studioFrame struct {
	Success *bool  `json:"success"`
	Message string `json:"message"`
	Choices []struct {
		Delta struct {
			Content   string `json:"content"`
			Reasoning string `json:"reasoning_content"`
			Alternate string `json:"reasoning"`
		} `json:"delta"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// studioRunRequest reconstructs only supported chat/image parameters. Routing and wallet checks
// are still enforced by the original paid relay, including the owner's current group and budget.
func studioRunRequest(in *studioIntent) (string, []byte, error) {
	row := &in.Record
	if row.Product == "" || len(row.Product) > 256 || len(row.Entries) == 0 {
		return "", nil, errors.New("invalid history")
	}
	last := &row.Entries[len(row.Entries)-1]
	if last.Prompt == "" || last.Model == "" {
		return "", nil, errors.New("missing prompt or model")
	}
	last.Content = ""
	last.Reasoning = ""
	last.Error = ""
	last.Image = ""
	last.Complete = false
	path := "/api/retail/studio/"
	body := gin.H{"model": last.Model}
	switch row.Mode {
	case "text":
		if in.MaxTokens < 1 || in.MaxTokens > 32768 {
			return "", nil, errors.New("invalid output limit")
		}
		messages := []gin.H{}
		for i, e := range row.Entries {
			if e.Image != "" || (e.Reference != "" && (i == len(row.Entries)-1 || in.Parent == "" || e.Reference != studioMediaPrefix+in.Parent+"/"+studioChatRole(i))) {
				return "", nil, errors.New("unexpected chat image")
			}
			if i == len(row.Entries)-1 || e.Complete && e.Error == "" {
				var content any = e.Prompt
				if uri := in.chatImages[i]; uri != "" {
					content = []gin.H{{"type": "text", "text": e.Prompt}, {"type": "image_url", "image_url": gin.H{"url": uri}}}
				}
				messages = append(messages, gin.H{"role": "user", "content": content})
				if i < len(row.Entries)-1 {
					messages = append(messages, gin.H{"role": "assistant", "content": e.Content})
				}
			}
		}
		body["messages"] = messages
		body["stream"] = true
		body["stream_options"] = gin.H{"include_usage": true}
		body["max_tokens"] = in.MaxTokens
		path += "chat/completions"
	case "image":
		if len(row.Entries) != 1 || !safeStudioRaster(last.Reference) || (in.uploadedReference && (last.Reference != "" || !service.RetailReferenceModel(last.Model))) {
			return "", nil, errors.New("invalid reference")
		}
		body["prompt"] = last.Prompt
		body["n"] = 1
		body["size"] = in.Size
		body["quality"] = in.Quality
		format := in.OutputFormat
		if format == "" {
			format = "png" // Older reconnect intents did not carry an output format.
		}
		if !service.ValidRetailImageSize(last.Model, in.Size) || in.Quality == "" ||
			!service.ValidRetailImageOptions(last.Model, in.Quality, format, in.OutputCompression) {
			return "", nil, errors.New("invalid image options")
		}
		body["output_format"] = format
		// Ask for portable bytes: output_format selects the codec, not how the image is delivered.
		body["response_format"] = "b64_json"
		if in.OutputCompression != nil {
			body["output_compression"] = *in.OutputCompression
		}
		body["stream"] = false
		if last.Reference != "" {
			path += "images/edits"
			body["images"] = []gin.H{{"image_url": last.Reference}}
		} else if in.uploadedReference {
			path += "images/edits"
		} else {
			path += "images/generations"
		}
	default:
		return "", nil, errors.New("unknown mode")
	}
	data, err := common.Marshal(body)
	return path, data, err
}

// StartStudioRun durably claims a client ID before invoking the existing authenticated relay once.
func StartStudioRun(relay http.Handler) gin.HandlerFunc {
	return func(c *gin.Context) {
		owner := c.GetInt("id")
		id := c.Param("id")
		if owner <= 0 || !historyID(id) || c.Query("owner") != strconv.Itoa(owner) {
			historyError(c, 403)
			return
		}
		dir := studioOwnerDir(c)
		if c.ContentType() == "multipart/form-data" {
			select {
			case studioUploadSlots <- struct{}{}:
				defer func() { <-studioUploadSlots }()
			default:
				historyError(c, 429)
				return
			}
		}
		var in studioIntent
		file, uploadErr := readStudioIntent(c, dir, &in)
		if file != nil {
			defer file.Close()
			defer os.Remove(file.Name())
		}
		var sizeError *http.MaxBytesError
		if errors.As(uploadErr, &sizeError) {
			c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, gin.H{"success": false, "message": "Reference image must be 50 MiB or smaller."})
			return
		}
		if errors.Is(uploadErr, errStudioDisk) {
			historyError(c, 507)
			return
		}
		if uploadErr != nil || in.Record.ID != id || (in.Parent != "" && (!historyID(in.Parent) || in.Parent == id)) {
			historyError(c, 400)
			return
		}
		in.uploadedReference = file != nil
		path, body, err := studioRunRequest(&in)
		if err != nil {
			historyError(c, 400)
			return
		}
		key := filepath.Join(dir, id)
		lock := &historyLocks[owner%len(historyLocks)]
		lock.Lock()
		defer lock.Unlock()
		if os.MkdirAll(dir, 0700) != nil {
			historyError(c, 500)
			return
		}
		// Bind the claim to the immutable request, so a retried ID cannot silently refer to another prompt.
		intentBytes, err := common.Marshal(in)
		if err != nil {
			historyError(c, 400)
			return
		}
		fingerprint := sha256.Sum256(intentBytes)
		if file != nil {
			// Hash actual file bytes so an acknowledgement retry cannot substitute another image.
			hash := sha256.New()
			hash.Write(fingerprint[:])
			if _, err := file.Seek(0, io.SeekStart); err != nil {
				historyError(c, 500)
				return
			}
			if _, err := io.Copy(hash, file); err != nil {
				historyError(c, 500)
				return
			}
			copy(fingerprint[:], hash.Sum(nil))
		}
		if existing, readErr := os.ReadFile(key + ".run"); readErr == nil {
			if !bytes.Equal(existing, fingerprint[:]) {
				historyError(c, 409)
				return
			}
			if _, err = os.Stat(key + ".record"); err != nil {
				historyError(c, 410)
				return
			}
			c.JSON(200, gin.H{"success": true})
			return
		} else if !errors.Is(readErr, os.ErrNotExist) {
			historyError(c, 500)
			return
		}
		if _, err = os.Stat(key + ".record"); !errors.Is(err, os.ErrNotExist) {
			historyError(c, 409)
			return
		}
		if _, active := studioRuns.Load(filepath.Join(dir, in.Parent)); in.Parent != "" && active {
			historyError(c, 409)
			return
		}
		files, err := os.ReadDir(dir)
		if err != nil {
			historyError(c, 500)
			return
		}
		var used, reserved int64
		for _, file := range files {
			stat, e := file.Info()
			if e != nil {
				historyError(c, 500)
				return
			}
			used += stat.Size()
		}
		// Reserve space for pending large images before accepting more paid work.
		studioRuns.Range(func(k, v any) bool {
			if filepath.Dir(k.(string)) == dir {
				reserved += studioRecordLimit
			}
			return true
		})
		space, err := disk.Usage(dir)
		if len(files) >= 3998 || used+reserved+studioRecordLimit > 512<<20 || err != nil || space.Free < uint64(512<<20+studioRecordLimit*(1+len(studioSlots))) {
			historyError(c, 507)
			return
		}
		select {
		case studioSlots <- struct{}{}:
		default:
			historyError(c, 429)
			return
		}
		// Historical file names are resolved only after duplicate detection: the parent may already
		// have been removed by the first successful attempt.
		if in.Record.Mode == "text" {
			in.chatImages, err = studioChatImages(dir, &in, file)
			if err == nil && len(in.chatImages) > 0 {
				path, body, err = studioRunRequest(&in)
			}
			if err != nil {
				<-studioSlots
				c.AbortWithStatusJSON(400, gin.H{"success": false, "message": err.Error()})
				return
			}
		}
		// The claim precedes all paid work. Only pre-dispatch failures can safely release it.
		claim, err := os.OpenFile(key+".run", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			<-studioSlots
			historyError(c, 500)
			return
		}
		if _, err = claim.Write(fingerprint[:]); err == nil {
			err = claim.Sync()
		}
		claim.Close()
		in.Record.Running = true
		in.Record.UpdatedAt = time.Now().UnixMilli()
		last := &in.Record.Entries[len(in.Record.Entries)-1]
		if err == nil && in.Record.Mode == "text" {
			// The new snapshot must own every attachment before the parent is removed.
			err = commitStudioChatImages(dir, &in, file)
		} else if err == nil && file != nil {
			err = file.Sync()
			if err == nil {
				err = os.Rename(file.Name(), key+".reference")
			}
			if err == nil {
				last.Reference = studioMediaPrefix + id + "/reference"
			}
		} else if err == nil && last.Reference != "" {
			// JSON reconnects built by older clients retain their fingerprint and relay shape.
			_, encoded, _ := strings.Cut(last.Reference, ",")
			var decoded []byte
			decoded, err = base64.StdEncoding.DecodeString(encoded)
			if err == nil && len(decoded) <= 5<<20 {
				switch http.DetectContentType(decoded) {
				case "image/png", "image/jpeg", "image/webp":
					err = storeStudioImage(key+".reference", decoded)
					if err == nil {
						last.Reference = studioMediaPrefix + id + "/reference"
					}
				}
			}
		}
		if err == nil && in.Record.Mode == "image" && in.Parent != "" {
			// Hydrate from the account snapshot after duplicate detection and before retiring
			// the parent. A stale second tab cannot start a paid request from an obsolete turn.
			err = continueStudioImage(dir, &in)
			if err == nil {
				// 生图接口无状态：补入同一会话的历史文字，当前请求的附件和路由只取本轮显式选择。
				var payload map[string]any
				if err = common.Unmarshal(body, &payload); err == nil {
					payload["prompt"] = studioImagePrompt(in.Record.Entries)
					body, err = common.Marshal(payload)
				}
			}
		}
		if err == nil {
			err = storeStudioRecord(dir, &in.Record)
		}
		// A directory sync commits both names before work can be handed to the paid relay.
		if err == nil {
			var directory *os.File
			directory, err = os.Open(dir)
			if err == nil {
				err = directory.Sync()
				directory.Close()
			}
		}
		if err != nil {
			_ = deleteStudioRecord(key)
			os.Remove(key + ".run")
			<-studioSlots
			if errors.Is(err, errImageParent) {
				historyError(c, 409)
			} else {
				historyError(c, 500)
			}
			return
		}
		if in.Parent != "" {
			_ = deleteStudioRecord(filepath.Join(dir, in.Parent))
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		if len(in.chatImages) > 0 {
			ctx = service.WithStudioVision(ctx, in.chatImages)
		} else if in.Record.Mode == "image" && in.uploadedReference {
			ctx = service.WithStudioReference(ctx, key+".reference")
		}
		worker := &studioWorker{cancel: cancel}
		studioRuns.Store(key, worker)
		// Never retain gin.Context or forward arbitrary request headers to the nested relay.
		req := c.Request.Clone(ctx)
		req.Method = http.MethodPost
		req.URL = &url.URL{Path: path}
		req.RequestURI = path
		req.Body = io.NopCloser(bytes.NewReader(body))
		req.ContentLength = int64(len(body))
		req.Header = make(http.Header)
		for _, name := range []string{"Authorization", "Cookie", "Origin", "Referer", "New-Api-User", "X-Real-IP", "X-Forwarded-For", "X-Forwarded-Proto"} {
			if value := c.GetHeader(name); value != "" {
				req.Header.Set(name, value)
			}
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Retail-Product", in.Record.Product)
		go worker.execute(relay, req, dir, key, lock, &in.Record)
		c.JSON(202, gin.H{"success": true})
	}
}

// studioPipe exposes a streaming response to the worker without binding it to the mobile connection.
type studioPipe struct {
	w       *io.PipeWriter
	headers http.Header
	status  int
}

func (p *studioPipe) Header() http.Header { return p.headers }
func (p *studioPipe) WriteHeader(n int) {
	if p.status == 0 {
		p.status = n
	}
}
func (p *studioPipe) Write(b []byte) (int, error) { return p.w.Write(b) }
func (p *studioPipe) Flush()                      {}

// execute archives the result even when no browser is connected. A worker failure is terminal
// because upstream acceptance or billing may already have happened.
func (w *studioWorker) execute(relay http.Handler, req *http.Request, dir, key string, lock *sync.Mutex, row *studioHistoryRecord) {
	last := &row.Entries[len(row.Entries)-1]
	var err error
	defer func() {
		if recover() != nil {
			err = errors.New(interruptedRun)
		}
		w.cancel()
		last.Content = w.content.String()
		last.Reasoning = w.reasoning.String()
		if err != nil {
			last.Error = err.Error()
		} else {
			last.Complete = true
		}
		row.Running = false
		row.UpdatedAt = time.Now().UnixMilli()
		lock.Lock()
		if storeStudioRecord(dir, row) != nil {
			if last.Image != "" {
				_ = removeStudioMedia(key + ".result")
			}
			common.SysError("Unable to finalize studio run")
		}
		studioRuns.Delete(key)
		lock.Unlock()
		<-studioSlots
	}()
	reader, writer := io.Pipe()
	defer reader.Close()
	sink := &studioPipe{w: writer, headers: http.Header{}}
	go func() {
		defer func() {
			if recover() != nil {
				writer.CloseWithError(errors.New(interruptedRun))
			} else {
				writer.Close()
			}
		}()
		relay.ServeHTTP(sink, req)
	}()
	if row.Mode == "image" {
		var data []byte
		data, err = io.ReadAll(io.LimitReader(reader, studioRecordLimit+1))
		if err == nil && len(data) <= studioRecordLimit {
			var image []byte
			image, err = studioResultImage(req.Context(), data)
			if reason, ok := err.(studioImageFailure); ok {
				// Only log a fixed failure code and our run ID, never signed URLs or image data.
				common.SysError(fmt.Sprintf("Studio image retrieval failed: run=%s reason=%s", row.ID, string(reason)))
			}
			if err == nil {
				if storeStudioImage(key+".result", image) != nil {
					err = errors.New("Unable to save the generated image. Check usage before trying again.")
				} else {
					last.Image = studioMediaPrefix + row.ID + "/result"
				}
			}
		} else if err == nil {
			err = errors.New("Image response too large")
		}
		return
	}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 8192), 2<<20)
	done := false
	checkpoint := time.Now()
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		data, ok := strings.CutPrefix(line, "data:")
		data = strings.TrimSpace(data)
		if ok && data == "[DONE]" {
			done = true
			continue
		}
		if !ok {
			if !strings.HasPrefix(line, "{") {
				continue
			}
			data = line
		}
		var frame studioFrame
		if common.UnmarshalJsonStr(data, &frame) != nil {
			err = errors.New("Invalid response from model")
			return
		}
		if frame.Success != nil && !*frame.Success {
			err = errors.New(studioRelayMessage(frame.Message))
			return
		}
		if frame.Error != nil {
			err = errors.New(studioRelayMessage(frame.Error.Message))
			return
		}
		if len(frame.Choices) == 0 {
			continue
		}
		delta := frame.Choices[0].Delta
		w.mu.Lock()
		w.content.WriteString(delta.Content)
		if delta.Reasoning != "" {
			w.reasoning.WriteString(delta.Reasoning)
		} else {
			w.reasoning.WriteString(delta.Alternate)
		}
		size := w.content.Len() + w.reasoning.Len()
		w.mu.Unlock()
		if size > 4<<20 {
			err = errors.New("Response too large")
			return
		}
		if time.Since(checkpoint) >= 2*time.Second {
			last.Content = w.content.String()
			last.Reasoning = w.reasoning.String()
			lock.Lock()
			err = storeStudioRecord(dir, row)
			lock.Unlock()
			if err != nil {
				return
			}
			checkpoint = time.Now()
		}
	}
	err = scanner.Err()
	if req.Context().Err() != nil || !done || w.content.Len()+w.reasoning.Len() == 0 {
		err = errors.New(interruptedRun)
	}
}

// studioRelayMessage preserves the relay's user-facing rejection even if no SSE tokens were emitted.
func studioRelayMessage(message string) string {
	if strings.TrimSpace(message) == "" {
		return interruptedRun
	}
	return message
}

type studioImageFailure string

// Error preserves the translated public message while server logs distinguish delivery failures.
func (studioImageFailure) Error() string {
	return "The image response is incomplete. Check usage before trying again."
}

// studioImageURLAllowed admits only the supplier's two observed HTTPS bucket origins.
// Match the entire host (including any port), never the shared CDN domain suffix.
func studioImageURLAllowed(asset *url.URL) bool {
	const bucket = "pub-13fd1d3607a441b4a9d232b3a223f5f6"
	return asset.Scheme == "https" && asset.User == nil && strings.HasPrefix(asset.Path, "/images/") &&
		(asset.Host == bucket+".r2.dev" || asset.Host == bucket+".r2.lat")
}

// studioResultImage prefers inline bytes; a rejected candidate can fall back to another field
// in the same paid response. It never sends a second generation request.
func studioResultImage(ctx context.Context, data []byte) ([]byte, error) {
	var result struct {
		Success *bool
		Message string
		Data    common.RawMessage
		Error   *struct{ Message string }
	}
	if common.Unmarshal(data, &result) != nil {
		return nil, errors.New("Invalid image response")
	}
	if result.Success != nil && !*result.Success {
		return nil, errors.New(studioRelayMessage(result.Message))
	}
	if result.Error != nil {
		return nil, errors.New(studioRelayMessage(result.Error.Message))
	}
	type image struct {
		B64 string `json:"b64_json"`
		URL string `json:"url"`
	}
	var imgs []image
	if common.Unmarshal(result.Data, &imgs) != nil {
		var one image
		if common.Unmarshal(result.Data, &one) == nil {
			imgs = []image{one}
		}
	}
	reason := studioImageFailure("missing_image_data")
	for _, img := range imgs {
		for _, raw := range []string{img.B64, img.URL} {
			if raw == "" {
				continue
			}
			if strings.HasPrefix(raw, "https://") {
				u, e := url.Parse(raw)
				if e != nil || !studioImageURLAllowed(u) {
					reason = "url_rejected"
					continue
				}
				request, e := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
				if e != nil {
					reason = "url_invalid"
					continue
				}
				// Never forward session credentials or follow provider-supplied redirects.
				client := http.Client{Timeout: time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
				resp, e := client.Do(request)
				if e != nil {
					reason = "download_failed"
					continue
				}
				if resp.StatusCode != http.StatusOK {
					resp.Body.Close()
					reason = studioImageFailure(fmt.Sprintf("download_http_%d", resp.StatusCode))
					continue
				}
				b, e := io.ReadAll(io.LimitReader(resp.Body, (48<<20)+1))
				resp.Body.Close()
				if e != nil || len(b) > 48<<20 {
					reason = "download_unreadable_or_oversize"
					continue
				}
				switch http.DetectContentType(b) {
				case "image/png", "image/jpeg", "image/webp":
					return b, nil
				}
				reason = "unsupported_image_type"
				continue
			}
			if strings.HasPrefix(raw, "data:") {
				_, raw, _ = strings.Cut(raw, ",")
			}
			if len(raw) > 64<<20 {
				reason = "inline_oversize"
				continue
			}
			// Detect the decoded signature, including WEBP after RIFF, before archiving untrusted image data.
			decoded, e := base64.StdEncoding.DecodeString(raw)
			if e != nil || len(decoded) == 0 {
				reason = "invalid_base64"
				continue
			}
			mime := http.DetectContentType(decoded)
			switch mime {
			case "image/png", "image/jpeg", "image/webp":
				return decoded, nil
			}
			reason = "unsupported_image_type"
		}
	}
	return nil, reason
}

// GetStudioRun returns only bytes after the caller's cursors. Terminal snapshots are read from disk.
func GetStudioRun(c *gin.Context) {
	id := c.Param("id")
	if !historyID(id) {
		historyError(c, 404)
		return
	}
	key := filepath.Join(studioOwnerDir(c), id)
	if value, ok := studioRuns.Load(key); ok {
		w := value.(*studioWorker)
		w.mu.Lock()
		content, reasoning := w.content.String(), w.reasoning.String()
		x, _ := strconv.Atoi(c.Query("content"))
		y, _ := strconv.Atoi(c.Query("reasoning"))
		if x < 0 || y < 0 || x > len(content) || y > len(reasoning) {
			w.mu.Unlock()
			historyError(c, 400)
			return
		}
		reply := gin.H{"running": true, "content": content[x:], "reasoning": reasoning[y:], "nextContent": len(content), "nextReasoning": len(reasoning)}
		w.mu.Unlock()
		c.JSON(200, reply)
		return
	}
	f, err := os.Open(key + ".record")
	if err != nil {
		historyError(c, 404)
		return
	}
	defer f.Close()
	r := bufio.NewReader(f)
	head, err := readStudioHistoryHead(r)
	if err != nil || head.expired(time.Now()) {
		historyError(c, 404)
		return
	}
	var row studioHistoryRecord
	if common.DecodeJson(r, &row) != nil || len(row.Entries) == 0 {
		historyError(c, 500)
		return
	}
	if row.Running {
		row.Running = false
		row.Entries[len(row.Entries)-1].Error = interruptedRun
	}
	c.JSON(200, gin.H{"running": false, "record": row})
}

// StopStudioRun cancels only the owner's running generation; an ordinary reader disconnect has no effect.
func StopStudioRun(c *gin.Context) {
	id := c.Param("id")
	if !historyID(id) || c.Query("owner") != fmt.Sprint(c.GetInt("id")) {
		historyError(c, 403)
		return
	}
	if value, ok := studioRuns.Load(filepath.Join(studioOwnerDir(c), id)); ok {
		value.(*studioWorker).cancel()
	}
	c.JSON(200, gin.H{"success": true})
}
