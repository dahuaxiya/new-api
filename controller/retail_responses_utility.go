package controller

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// ResponsesUtilityFormat 保留非零售请求的协议，零售工具由标准 Responses 执行配额预留和结算。
func ResponsesUtilityFormat(c *gin.Context, original types.RelayFormat) types.RelayFormat {
	if c.GetString("retail_responses_utility") != "" {
		return types.RelayFormatOpenAIResponses
	}
	return original
}

// RetailResponsesUtility 将零售压缩和搜索转换成一次 Responses 调用，并在结算后还原独立接口的响应。
func RetailResponsesUtility(c *gin.Context) {
	if !c.GetBool("retail_request") {
		c.Next()
		return
	}
	storage, err := common.GetBodyStorage(c)
	if err != nil || storage.Size() > 8<<20 {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": types.OpenAIError{Type: "invalid_request_error", Message: "Invalid utility request body"}})
		return
	}
	raw, err := storage.Bytes()
	if err != nil || !gjson.ValidBytes(raw) {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": types.OpenAIError{Type: "invalid_request_error", Message: "Invalid utility request body"}})
		return
	}
	kind := "search"
	if c.Request.URL.Path == "/v1/responses/compact" {
		kind = "compact"
	}
	body, kept, err := retailResponsesUtilityRequest(gjson.ParseBytes(raw), kind)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": types.OpenAIError{Type: "invalid_request_error", Code: "unsupported_parameter", Message: err.Error()}})
		return
	}
	replacement, err := common.CreateBodyStorage(body)
	if err != nil {
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	// 选路、验参、出站和重试读取同一份转换后的请求，不使用独立工具的旧账本。
	common.CleanupBodyStorage(c)
	c.Set(common.KeyBodyStorage, replacement)
	c.Request.Body = io.NopCloser(replacement)
	c.Request.ContentLength = int64(len(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.URL.Path = "/v1/responses"
	c.Request.URL.RawPath = ""
	c.Set("retail_responses_utility", kind)
	writer := &retailUtilityWriter{ResponseWriter: c.Writer}
	c.Writer = writer
	c.Next()
	c.Writer = writer.ResponseWriter
	for _, key := range []string{"Content-Type", "Content-Length", "Transfer-Encoding", "Connection", "Content-Encoding"} {
		c.Writer.Header().Del(key)
	}
	if writer.Status() >= http.StatusBadRequest {
		c.Data(writer.Status(), "application/json", writer.body.Bytes())
		return
	}
	result := gjson.ParseBytes(writer.body.Bytes())
	if kind == "compact" {
		result = gjson.ParseBytes(writer.terminal)
	}
	response, err := retailResponsesUtilityResponse(result, kind, kept)
	if err != nil || c.GetString("retail_settlement_state") != "settled" {
		c.JSON(http.StatusBadGateway, gin.H{"error": types.OpenAIError{Type: "upstream_error", Code: "retail_utility_result_unverified", Message: "Upstream did not return a verifiable utility result"}})
		return
	}
	c.JSON(http.StatusOK, response)
}

// retailResponsesUtilityRequest 只转换可以无损表达的搜索命令和可重放压缩输入。
func retailResponsesUtilityRequest(root gjson.Result, kind string) ([]byte, []common.RawMessage, error) {
	if !root.IsObject() || !service.UniqueRetailKeys(root) || root.Get("model").Str == "" {
		return nil, nil, errors.New("model is required")
	}
	out := map[string]any{"model": root.Get("model").Str, "store": false}
	var kept []common.RawMessage
	if kind == "compact" {
		if root.Get("previous_response_id").Type != gjson.Null {
			return nil, nil, errors.New("previous_response_id requires full input replay for compaction")
		}
		input := root.Get("input")
		items := input.Array()
		if input.Type == gjson.String && input.Str != "" {
			message, _ := common.Marshal(gin.H{"type": "message", "role": "user", "content": []gin.H{{"type": "input_text", "text": input.Str}}})
			items = []gjson.Result{gjson.ParseBytes(message)}
		}
		if len(items) == 0 {
			return nil, nil, errors.New("input is required")
		}
		payload := make([]common.RawMessage, 0, len(items)+1)
		for _, item := range items {
			payload = append(payload, common.RawMessage(item.Raw))
			if item.Get("role").Str == "user" && slices.Contains([]string{"", "message"}, item.Get("type").Str) {
				kept = append(kept, common.RawMessage(item.Raw))
			}
		}
		out["input"] = append(payload, common.RawMessage(`{"type":"compaction_trigger"}`))
		out["stream"] = true
		for _, field := range []string{"instructions", "prompt_cache_key", "prompt_cache_options", "prompt_cache_retention", "service_tier"} {
			if v := root.Get(field); v.Type != gjson.Null {
				out[field] = common.RawMessage(v.Raw)
			}
		}
	} else {
		commands := root.Get("commands")
		queries := commands.Get("search_query").Array()
		if len(commands.Map()) != 1 || len(queries) != 1 || root.Get("settings.external_web_access").Type == gjson.False {
			return nil, nil, errors.New("one live search_query is supported")
		}
		query := queries[0].Get("q").Str
		if len(query) > 4096 || strings.TrimSpace(query) == "" || queries[0].Get("domains").Type != gjson.Null && (!queries[0].Get("domains").IsArray() || len(queries[0].Get("domains").Array()) != 0) {
			return nil, nil, errors.New("search query is too long, empty, or has a domain filter")
		}
		out["input"] = query
		out["instructions"] = "Search the web for the user's query. Return a concise answer with citations."
		out["tools"] = []gin.H{{"type": "web_search"}}
		out["tool_choice"] = "required"
		out["include"] = []string{"web_search_call.action.sources"}
	}
	body, err := common.Marshal(out)
	return body, kept, err
}

// retailResponsesUtilityResponse 仅交付上游已完成的结果，保留可重放的加密压缩项和实报用量。
func retailResponsesUtilityResponse(result gjson.Result, kind string, kept []common.RawMessage) (gin.H, error) {
	if result.Get("status").Str != "completed" || result.Get("usage.input_tokens").Int() <= 0 {
		return nil, errors.New("missing completed response or usage")
	}
	if kind == "compact" {
		for _, item := range result.Get("output").Array() {
			if item.Get("type").Str == "compaction" && item.Get("encrypted_content").Str != "" {
				return gin.H{"id": result.Get("id").Str, "object": "response.compaction", "created_at": result.Get("created_at").Int(), "output": append(kept, common.RawMessage(item.Raw)), "usage": common.RawMessage(result.Get("usage").Raw)}, nil
			}
		}
		return nil, errors.New("missing encrypted compaction")
	}
	var sources []string
	calls := 0
	for _, item := range result.Get("output").Array() {
		if item.Get("type").Str != "web_search_call" || item.Get("status").Str != "completed" {
			continue
		}
		calls++
		for _, source := range item.Get("action.sources").Array() {
			raw := source.Get("url").Str
			u, err := url.Parse(raw)
			if err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" && u.User == nil && !slices.Contains(sources, raw) {
				sources = append(sources, raw)
			}
		}
	}
	if calls != 1 || len(sources) == 0 {
		return nil, errors.New("missing completed web search with sources")
	}
	return gin.H{"output": strings.Join(sources, "\n"), "encrypted_output": nil}, nil
}

// retailUtilityWriter 只缓存一个有界 JSON 响应或 SSE 终止事件，避免在结算前把上游响应写给客户端。
type retailUtilityWriter struct {
	gin.ResponseWriter
	mu       sync.Mutex
	body     bytes.Buffer
	terminal []byte
}

// WriteString 接收 Gin 的 SSE 渲染器输出。
func (w *retailUtilityWriter) WriteString(p string) (int, error) { return w.Write([]byte(p)) }

// Write 保存非流式结果或提取完整终帧。中间流式事件无需保留。
func (w *retailUtilityWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.body.Len()+len(p) > 8<<20 {
		return 0, errors.New("utility result exceeds response limit")
	}
	_, _ = w.body.Write(p)
	if !strings.HasPrefix(w.Header().Get("Content-Type"), "text/event-stream") {
		return len(p), nil
	}
	for {
		line, _, found := bytes.Cut(w.body.Bytes(), []byte{'\n'})
		if !found {
			break
		}
		w.body.Next(len(line) + 1)
		payload, data := bytes.CutPrefix(line, []byte("data: "))
		if data && gjson.GetBytes(payload, "type").Str == "response.completed" {
			w.terminal = []byte(gjson.GetBytes(payload, "response").Raw)
		}
	}
	return len(p), nil
}

// Flush 等待结算后的单次响应写入，不能提前提交 HTTP 头。
func (w *retailUtilityWriter) Flush() {}
