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

// OaiResponsesStreamHandler 透传 SSE 并汇总用量；商业计费排除断流后本地补算的 token。
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

	helper.StreamScannerHandler(c, resp, info, func(data string, sr *helper.StreamResult) {

		// 检查当前数据是否包含 completed 状态和 usage 信息
		var streamResponse dto.ResponsesStreamResponse
		if err := common.UnmarshalJsonStr(data, &streamResponse); err != nil {
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
