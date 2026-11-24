package service

import (
	"bytes"
	"encoding/json"
	"net/http"
	"slices"

	"github.com/QuantumNous/new-api/pkg/retailprice"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/tidwall/gjson"
)

// limitedResponsesRoute identifies only the tested supplier, credential group and mapped model.
// Local group names and numeric channel IDs are administrator-defined and can be reused elsewhere.
func limitedResponsesRoute(book retailprice.Book, channel int, product, model string) bool {
	group := book.RegisteredGroups()[channel]
	if group != "99缓 OAI官渠" && group != "GPT-UltraFast" {
		return false
	}
	if site := book.RateSiteByChannel[channel]; site != 0 {
		if retailprice.RateFeedOrigin(book.SiteFeeds[site].URL) != "https://aixingxing.pro" {
			return false
		}
	} else if !slices.ContainsFunc(book.GroupBindings, func(b retailprice.GroupBinding) bool { return b.ChannelID == channel }) {
		// Only legacy bindings receive the fixed supplier origin in retailDispatchOriginMatches.
		return false
	}
	for _, p := range book.Products {
		if p.ID == product {
			for _, r := range p.Routes {
				if r.ChannelID == channel {
					return r.Models[model] == "gpt-6.2-astra"
				}
			}
		}
	}
	return false
}

// responsesParameterError is returned before reservation; Param is a server-owned field name only.
func responsesParameterError(field, why string) *types.NewAPIError {
	return types.WithOpenAIError(types.OpenAIError{Type: "invalid_request_error", Code: "unsupported_parameter", Param: field, Message: field + ": " + why}, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
}

// isMagpieResponsesProbe 识别 Magpie 0.1.4725 固定生成的单条 hi 协议探测。
// 仅允许客户端原样发出的请求形状和可选的流式开关。任何其他字段或消息都按正式请求校验。
func isMagpieResponsesProbe(raw []byte) bool {
	// 原客户端使用固定字段顺序。保留这一约束，重复字段也无法匹配，避免宽松解析扩张兼容范围。
	if len(raw) > 512 {
		return false
	}
	var compact bytes.Buffer
	if json.Compact(&compact, raw) != nil {
		return false
	}
	model := gjson.GetBytes(compact.Bytes(), "model")
	if model.Type != gjson.String || model.Str == "" {
		return false
	}
	probe := `{"model":` + model.Raw + `,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],"max_output_tokens":16`
	return compact.String() == probe+`}` || compact.String() == probe+`,"stream":true}`
}

// validateResponsesContract 校验请求与选中供应线路的参数约定。
// magpieProbe 仅由入口按请求原文识别，允许固定探测以尽力限制输出的方式交给上游。
// Null optional fields mean omitted. Fixed defaults remain usable for SDKs that send them explicitly.
func validateResponsesContract(raw []byte, limited, magpieProbe bool, modelOutputLimit int, fasterGroup bool) *types.NewAPIError {
	root := gjson.ParseBytes(raw)
	for _, field := range []string{"background", "service_tier", "prompt"} {
		v := root.Get(field)
		if v.Type == gjson.Null {
			continue
		}
		if field == "background" && v.Type == gjson.False || field == "service_tier" && retailServiceTierAllowed(v.Str, fasterGroup) {
			continue
		}
		return responsesParameterError(field, "当前零售接口不支持此设置。")
	}
	for _, t := range root.Get("tools").Array() {
		if t.Get("type").Str == "file_search" {
			return responsesParameterError("tools", "当前零售接口不支持 file_search。")
		}
	}
	if !limited {
		return nil
	}
	for _, field := range []string{"max_output_tokens", "max_tool_calls", "temperature", "top_p", "parallel_tool_calls", "metadata", "top_logprobs", "truncation"} {
		v := root.Get(field)
		if v.Type == gjson.Null {
			continue
		}
		switch field {
		case "max_output_tokens":
			// 探测仍把原值发给上游，并按实报用量结算。只对固定探测放宽硬上限校验。
			// 目录公开的上限与该线路的报价上限相同。不把任意较小值承诺为硬限制。
			if magpieProbe || (modelOutputLimit > 0 && v.Type == gjson.Number && v.Float() == float64(modelOutputLimit)) {
				continue
			}
		case "temperature", "top_p":
			if v.Type == gjson.Number && v.Float() == 1 {
				continue
			}
		case "parallel_tool_calls":
			// 压缩对话时客户端显式禁用并行；空工具列表下没有工具可并行。
			tools := root.Get("tools")
			noTools := tools.Type == gjson.Null || (tools.IsArray() && len(tools.Array()) == 0)
			if v.Type == gjson.True || (v.Type == gjson.False && noTools) {
				continue
			}
		case "metadata":
			if v.IsObject() && len(v.Map()) == 0 {
				continue
			}
		case "top_logprobs":
			if v.Type == gjson.Number && v.Float() == 0 {
				continue
			}
		case "truncation":
			if v.Type == gjson.String && v.Str == "disabled" {
				continue
			}
		}
		return responsesParameterError(field, "当前模型线路不能保证执行这个值；请省略该参数，或选择支持它的线路。")
	}
	// A local search-only counter cannot protect a mixed search/image call budget.
	image, search := false, false
	for _, t := range root.Get("tools").Array() {
		switch t.Get("type").Str {
		case "image_generation":
			image = true
		case "web_search":
			search = true
		}
	}
	if image && search {
		return responsesParameterError("tools", "当前线路不能在同一请求中为搜索与生图提供合计次数上限。")
	}
	return nil
}

// retailServiceTierAllowed 允许 SDK 的默认值。付费高速线路的 Key 自带 ultrafast 服务，不改变客户分组。
func retailServiceTierAllowed(tier string, fasterGroup bool) bool {
	return tier == "default" || tier == "auto" || fasterGroup && (tier == "priority" || tier == "ultrafast")
}
