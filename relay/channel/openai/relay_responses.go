package openai

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// OaiResponsesHandler 转发完整 Responses 响应；商业请求缺失实报用量时不按工具附加价收费。
func OaiResponsesHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	defer service.CloseResponseBodyGracefully(resp)

	// read response body
	var responsesResponse dto.OpenAIResponsesResponse
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeReadResponseBodyFailed, http.StatusInternalServerError)
	}
	err = common.Unmarshal(responseBody, &responsesResponse)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
	}
	if oaiError := responsesResponse.GetOpenAIError(); oaiError != nil && oaiError.Type != "" {
		return nil, types.WithOpenAIError(*oaiError, resp.StatusCode)
	}

	service.ObserveResponsesEffort(info, &responsesResponse)
	if limit := c.GetInt("retail_search_stream_limit"); limit > 0 {
		// A buffered result must pass the same identity/count check before exposing output or storing its ID.
		seen := make(map[string]struct{})
		event := dto.ResponsesStreamResponse{Response: &responsesResponse}
		searchItems := 0
		for _, item := range responsesResponse.Output {
			if item.Type == dto.BuildInCallWebSearchCall {
				searchItems++
			}
		}
		if !retailSearchEventWithinLimit(&event, seen, limit) || searchItems != len(seen) {
			return nil, types.NewOpenAIError(fmt.Errorf("search usage exceeded the quoted limit or lacks a call ID"), "retail_search_usage_unverified", http.StatusBadGateway, types.ErrOptionWithSkipRetry())
		}
	}
	info.ObserveResponseModel(responsesResponse.Model)
	responseBody = rewriteSGLangResponsesCreatedAt(info, responseBody, "created_at", responsesResponse.CreatedAt)

	// 写入前记录归属，客户端收到回复即可用该 ID 续接。
	// 上游明确返回 store:false 时不能只凭成功状态登记可续接的 ID。
	if gjson.ParseBytes(responsesResponse.Status).String() == "completed" {
		service.RememberRetailResponse(c, responsesResponse.ID, gjson.ParseBytes(responseBody))
	}
	service.IOCopyBytesGracefully(c, resp, responseBody)

	// compute usage
	usage := &dto.Usage{}
	service.ApplyResponsesUsage(usage, responsesResponse.Usage)
	if c.GetBool("retail_request") && !dto.HasOpenAIUsageTokens(responsesResponse.Usage) {
		// 空 usage 不能证明采购或消费量，工具调用计数也不能替代上游用量凭据。
		common.SetContextKey(c, constant.ContextKeyLocalCountTokens, true)
	}
	// Count actual tool invocations from Output (not tool declarations).
	for _, output := range responsesResponse.Output {
		switch output.Type {
		case dto.BuildInCallWebSearchCall:
			info.CountBillableToolCall(dto.BuildInCallWebSearchCall, "")
		case dto.BuildInCallFileSearchCall:
			info.CountBillableToolCall(dto.BuildInCallFileSearchCall, "")
		case dto.BuildInCallFunctionCall:
			info.CountBillableToolCall(dto.BuildInCallFunctionCall, output.Name)
		}
	}
	info.ApplyVendorToolUsage(responseBody)

	imageCounter := &relaycommon.ImageGenerationCallCounter{}
	for i := range responsesResponse.Output {
		imageCounter.Observe(&responsesResponse.Output[i], &i)
	}
	imageCounter.Commit(info)

	return usage, nil
}

// retailProgressUsage 保存同一条 Responses 流里上游实报的累计用量，只在没有最终用量时使用。
// 不跨请求缓存，不从文本、HTTP 状态或预留金额推算客户消费。
type retailProgressUsage struct {
	id       string
	usage    *dto.Usage
	unusable bool
}

// observe 只接受纯文本进度帧内能独立定价的计数；重复帧覆盖，不累加。
// 有最终用量、回复归属冲突或用量倒退时，不再拿旧快照替代最终账单。
func (p *retailProgressUsage) observe(event *dto.ResponsesStreamResponse, raw string) {
	r := event.Response
	if p.unusable || r == nil {
		return
	}
	if r.ID != "" {
		if p.id != "" && p.id != r.ID {
			p.unusable = true
			return
		}
		p.id = r.ID
	}
	if r.Usage == nil {
		return
	}
	switch event.Type {
	case "response.completed", "response.done", "response.incomplete", "response.failed", "response.cancelled", "response.canceled":
		// 即使终帧只给出部分字段，也不能用更早的数字掩盖它；由原结算路径处理。
		p.unusable = true
		return
	case "response.in_progress":
	default:
		return
	}
	u := r.Usage
	wire := gjson.Get(raw, "response.usage")
	// DTO 的零值不能区分“缓存为零”和“未报告缓存”。缺少拆分时按普通输入扣会多收。
	if r.ID == "" || wire.Get("input_tokens").Type != gjson.Number || wire.Get("output_tokens").Type != gjson.Number ||
		wire.Get("input_tokens_details.cached_tokens").Type != gjson.Number || u.InputTokensDetails == nil ||
		u.InputTokens < 0 || u.OutputTokens < 0 || u.InputTokens > 2_000_000 || u.OutputTokens > 2_000_000 ||
		u.InputTokensDetails.CachedTokens < 0 || u.InputTokensDetails.CachedTokens > u.InputTokens {
		p.unusable = true
		return
	}
	if total := wire.Get("total_tokens"); total.Exists() && (total.Type != gjson.Number || u.TotalTokens != u.InputTokens+u.OutputTokens) {
		p.unusable = true
		return
	}
	// 此回退只处理已知的文本及缓存读；音频、图片、缓存写和不认识的计费字段留待复核。
	for key := range wire.Map() {
		switch key {
		case "input_tokens", "output_tokens", "total_tokens", "input_tokens_details", "output_tokens_details":
		default:
			p.unusable = true
			return
		}
	}
	for key := range wire.Get("input_tokens_details").Map() {
		if key != "cached_tokens" {
			p.unusable = true
			return
		}
	}
	for key, value := range wire.Get("output_tokens_details").Map() {
		if key != "reasoning_tokens" || value.Type != gjson.Number || value.Int() < 0 || value.Int() > int64(u.OutputTokens) {
			p.unusable = true
			return
		}
	}
	if prev := p.usage; prev != nil && (prev.InputTokens != u.InputTokens || prev.InputTokensDetails.CachedTokens != u.InputTokensDetails.CachedTokens || prev.OutputTokens > u.OutputTokens) {
		p.unusable = true
		return
	}
	// 全零通常是进度占位；它本身不能把预留核销成零元。
	if u.InputTokens+u.OutputTokens > 0 {
		p.usage = u
	}
}

// OaiResponsesStreamHandler 透传 SSE，以终帧用量优先；纯文本断流最多收取本站已经收到的上游实报用量。
func OaiResponsesStreamHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	if resp == nil || resp.Body == nil {
		logger.LogError(c, common.LogText("invalid response or response body"))
		return nil, types.NewError(fmt.Errorf("invalid response"), types.ErrorCodeBadResponse)
	}

	defer service.CloseResponseBodyGracefully(resp)

	accumulator := service.NewResponsesUsageAccumulator(info)
	limit := c.GetInt("retail_search_stream_limit")
	seenSearch := make(map[string]struct{})
	var rejected bool
	// 旧版文本报价的 Kind 为空；图片和工具另有交付、计数要求，不能用文本进度结算。
	q := info.RetailQuote
	trackProgress := c.GetBool("retail_request") && q != nil && (q.Model.Kind == "text" || q.Model.Kind == "") && q.SearchLimit == 0 && q.ImageToolLimit == 0 && !service.HasStudioVision(c.Request)
	var progress retailProgressUsage

	helper.StreamScannerHandler(c, resp, info, func(data string, sr *helper.StreamResult) {

		// 检查当前数据是否包含 completed 状态和 usage 信息
		var streamResponse dto.ResponsesStreamResponse
		if err := common.UnmarshalJsonStr(data, &streamResponse); err != nil {
			progress.unusable = true
			logger.LogError(c, common.LogText("failed to unmarshal stream response: %s", err.Error()))
			if limit > 0 {
				// 本地计量依赖逐帧识别；无法解析时不能跳过本帧后继续按较低次数收费。
				rejected = true
				_ = resp.Body.Close()
				sr.Stop(err)
				return
			}
			sr.Error(err)
			return
		}
		if limit > 0 && !retailSearchEventWithinLimit(&streamResponse, seenSearch, limit) {
			// 不把超限事件或成功终帧交给客户端。中止上游读取；已执行的超额采购不能按一次自动结算。
			rejected = true
			_ = resp.Body.Close()
			sendResponsesStreamData(c, dto.ResponsesStreamResponse{Type: "error"}, `{"type":"error","code":"retail_search_usage_unverified","message":"Search call limit exceeded or usage could not be verified."}`)
			sr.Stop(fmt.Errorf("retail search usage exceeds reservation or lacks call identity"))
			return
		}
		if trackProgress {
			progress.observe(&streamResponse, data)
		}
		if streamResponse.Response != nil {
			// 只绑定已完成的回复；断流或创建事件不能授权后续引用。
			if streamResponse.Type == "response.completed" {
				service.RememberRetailResponse(c, streamResponse.Response.ID, gjson.Get(data, "response"))
			}
			data = string(rewriteSGLangResponsesCreatedAt(info, []byte(data), "response.created_at", streamResponse.Response.CreatedAt))
			if c.GetBool("retail_request") && streamResponse.Response.Usage != nil {
				// 原始 Responses usage 不一定携带内部快照；在补算前保存，供 Finish 标记估算。
				streamResponse.Response.Usage.BillingUsage = dto.NewOpenAIResponsesBillingUsage(streamResponse.Response.Usage)
			}
		}
		sendResponsesStreamData(c, streamResponse, data)
		accumulator.Observe(&streamResponse, common.StringToByteSlice(data))
	})

	common.SetContextKey(c, constant.ContextKeyResponseStreamStatus, info.StreamStatus)
	info.StreamStatus.RequireTerminal()
	usage := accumulator.Finish()
	if trackProgress && !rejected && !progress.unusable && progress.usage != nil && info.StreamStatus.ResponseOutcome() != string(relaycommon.ResponseOutcomeCompleted) {
		// 使用完整的一帧，不与 Finish 为旧计费估算的字段拼接。仍由冻结报价和钱包既有事务结算一次。
		usage = &dto.Usage{}
		service.ApplyResponsesUsage(usage, progress.usage)
		usage.BillingUsage = dto.NewOpenAIResponsesBillingUsage(progress.usage)
		c.Set("retail_progress_usage", true)
	}
	// 若上游只报告开始、未报告搜索结果，无法区分已计费和取消；保留预授权等待核对。
	if limit > 0 && info.ResponsesUsageInfo != nil {
		tool := info.BuiltInTools[dto.BuildInToolWebSearch]
		rejected = rejected || tool == nil || tool.CallCount != len(seenSearch)
	}
	if rejected || c.GetBool("retail_request") && (usage.BillingUsage == nil || usage.BillingUsage.Estimated) {
		// Finish 为旧计费补算 token，但商业钱包只能依据上游明确返回的用量结算。
		common.SetContextKey(c, constant.ContextKeyLocalCountTokens, true)
	}
	return usage, nil
}

func rewriteSGLangResponsesCreatedAt(info *relaycommon.RelayInfo, payload []byte, path string, createdAt dto.IntValue) []byte {
	if info.GetChannelType() != constant.ChannelTypeSGLang {
		return payload
	}
	if !gjson.GetBytes(payload, path).Exists() {
		return payload
	}
	patched, err := sjson.SetBytes(payload, path, int(createdAt))
	if err != nil {
		return payload
	}
	return patched
}

// retailSearchEventWithinLimit counts distinct native search executions from their earliest observable event.
// The caller closes the upstream stream and keeps the reservation for reconciliation on an unverifiable ID or excess call.
func retailSearchEventWithinLimit(event *dto.ResponsesStreamResponse, seen map[string]struct{}, limit int) bool {
	accept := func(id string) bool {
		if id == "" {
			return false
		}
		if _, ok := seen[id]; ok {
			return true
		}
		if len(seen) >= limit {
			return false
		}
		seen[id] = struct{}{}
		return true
	}
	if event.Item != nil && event.Item.Type == dto.BuildInCallWebSearchCall && !accept(event.Item.ID) {
		return false
	}
	if strings.HasPrefix(event.Type, "response.web_search_call.") && !accept(event.ItemID) {
		return false
	}
	// 部分渠道不发送增量工具事件，终帧的所有工具也须在转发和登记回复 ID 之前核对。
	if event.Response != nil {
		for _, item := range event.Response.Output {
			if item.Type == dto.BuildInCallWebSearchCall && !accept(item.ID) {
				return false
			}
		}
	}
	return true
}
