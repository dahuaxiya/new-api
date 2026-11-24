package relay

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// runRetailCall 让零售 WebSocket 复用完整的 HTTP Responses 请求链。终止帧在账本处理及限流完成后才发送。
func (s *responsesWSSession) runRetailCall(c *gin.Context, state *responsesWSCallState, create responsesWSCreateRequest) *types.NewAPIError {
	if s.retailRelay == nil || (len(create.Generate) != 0 && string(create.Generate) != "true") {
		return newResponsesWSInvalidRequestError(errors.New("this retail endpoint requires generation"))
	}
	body, err := sjson.SetBytes(create.Body, "stream", true)
	if err != nil {
		return newResponsesWSInvalidRequestError(err)
	}
	ctx, cancel := context.WithCancel(c.Request.Context())
	defer cancel()
	c.Request = c.Request.WithContext(ctx)
	c.Request.ContentLength = int64(len(body))
	c.Request.Body = io.NopCloser(bytes.NewReader(body))

	// HTTP 链根据上下文取消供货和 SSE 读取。控制帧不会独立修改钱包状态。
	go func() {
		select {
		case <-state.controls:
			cancel()
		case <-ctx.Done():
		}
	}()
	writer := &retailWSWriter{ResponseWriter: c.Writer, session: s, streamID: create.StreamID}
	c.Writer = writer
	s.retailRelay(c)
	state.terminal = writer.terminal
	if writer.status >= 400 {
		var result struct {
			Error *types.OpenAIError `json:"error"`
		}
		if common.Unmarshal(writer.buffer.Bytes(), &result) == nil && result.Error != nil {
			return types.WithOpenAIError(*result.Error, writer.status, types.ErrOptionWithSkipRetry())
		}
		return types.NewErrorWithStatusCode(errors.New(http.StatusText(writer.status)), types.ErrorCodeBadResponse, writer.status, types.ErrOptionWithSkipRetry())
	}
	if state.terminal == nil {
		return types.NewErrorWithStatusCode(errors.New("Responses stream ended before its terminal event"), types.ErrorCodeBadResponse, http.StatusBadGateway, types.ErrOptionWithSkipRetry())
	}
	return nil
}

// retailWSWriter 将 HTTP 响应转换成逐个 WebSocket 事件，不缓存完整流。
type retailWSWriter struct {
	gin.ResponseWriter
	mu       sync.Mutex
	session  *responsesWSSession
	streamID string
	status   int
	buffer   bytes.Buffer
	terminal *responsesWSMessage
}

// WriteHeader 保留 HTTP 错误状态，供限流和 WebSocket 错误帧使用。
func (w *retailWSWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

// WriteString 接收 Gin 自定义 SSE 渲染器的文本片段。
func (w *retailWSWriter) WriteString(data string) (int, error) { return w.Write([]byte(data)) }

// Write 收集分段写入的一条 SSE 数据行。终止事件由轮次处理器在结算后发送。
func (w *retailWSWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.buffer.Len()+len(data) > 8<<20 {
		return 0, errors.New("response event exceeds websocket bridge limit")
	}
	_, _ = w.buffer.Write(data)
	if w.status >= 400 {
		return len(data), nil
	}
	for {
		line, _, found := bytes.Cut(w.buffer.Bytes(), []byte{'\n'})
		if !found {
			return len(data), nil
		}
		w.buffer.Next(len(line) + 1)
		payload, ok := bytes.CutPrefix(line, []byte("data: "))
		if !ok || !gjson.ValidBytes(payload) {
			continue
		}
		if w.streamID != "" {
			var err error
			payload, err = sjson.SetBytes(payload, "stream_id", w.streamID)
			if err != nil {
				return 0, err
			}
		}
		switch strings.TrimSpace(gjson.GetBytes(payload, "type").Str) {
		case "response.completed", "response.done", "response.incomplete", "response.failed", "response.cancelled", "response.canceled", "error":
			w.terminal = &responsesWSMessage{kind: websocket.TextMessage, body: bytes.Clone(payload)}
		default:
			if err := w.session.writeClient(websocket.TextMessage, payload); err != nil {
				return 0, err
			}
		}
	}
}

// Flush 由 Write 直接发送完整事件，此处无需额外刷新 HTTP 连接。
func (w *retailWSWriter) Flush() {}
