package service

import (
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

// validateResponsesContract rejects known unsupported semantics on the selected route, not other suppliers.
// Null optional fields mean omitted. Fixed defaults remain usable for SDKs that send them explicitly.
func validateResponsesContract(raw []byte, limited bool) *types.NewAPIError {
	root := gjson.ParseBytes(raw)
	for _, field := range []string{"background", "service_tier", "prompt"} {
		v := root.Get(field)
		if v.Type == gjson.Null {
			continue
		}
		if field == "background" && v.Type == gjson.False || field == "service_tier" && v.Str == "default" {
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
		case "temperature", "top_p":
			if v.Type == gjson.Number && v.Float() == 1 {
				continue
			}
		case "parallel_tool_calls":
			if v.Type == gjson.True {
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
