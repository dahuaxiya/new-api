package service

import (
	"bytes"
	"errors"
	"io"
	"maps"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/retailprice"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	hosttypes "github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"github.com/tidwall/gjson"
)

// PrepareRetailPricing creates a CNY contract before legacy pricing can reserve any money.
// Only an unpublished book keeps legacy mode. Pausing a published book cannot bypass its safeguards.
func PrepareRetailPricing(c *gin.Context, info *relaycommon.RelayInfo) (bool, *types.NewAPIError) {
	if !c.GetBool("retail_request") {
		return false, nil
	}
	book, version, err := model.GetRetailPriceBook()
	if err != nil {
		return true, retailPriceError(err)
	}
	if !book.Enabled {
		if version > 0 || strings.HasSuffix(c.Request.URL.Path, "/images/generations") {
			return true, retailPriceError(retailprice.ErrExpired)
		}
		return false, nil
	}
	storage, err := common.GetBodyStorage(c)
	if err != nil {
		return true, retailPriceError(retailprice.ErrConfig)
	}
	reader, err := storage.NewReader()
	if err != nil {
		return true, retailPriceError(retailprice.ErrConfig)
	}
	raw, err := io.ReadAll(io.LimitReader(reader, (8<<20)+1))
	_ = reader.Close()
	if err != nil || len(raw) > 8<<20 {
		return true, retailPriceError(retailprice.ErrConfig)
	}
	// 保留客户端原始存储决定，渠道改写 store 不能为本地保留内容提供授权。
	store := gjson.GetBytes(raw, "store")
	c.Set("retail_client_store", store.Type == gjson.True)
	c.Set("retail_client_no_store", store.Type == gjson.False)
	output := uint(1024)
	switch req := info.Request.(type) {
	case *dto.GeneralOpenAIRequest:
		if req.MaxTokens != nil {
			output = *req.MaxTokens
		}
		if req.MaxCompletionTokens != nil {
			output = *req.MaxCompletionTokens
			if req.MaxTokens != nil {
				output = max(output, *req.MaxTokens)
			}
		}
		if req.N != nil && *req.N != 1 {
			return true, retailPriceError(retailprice.ErrConfig)
		}
	case *dto.OpenAIResponsesRequest:
		if req.MaxOutputTokens != nil {
			output = *req.MaxOutputTokens
		}
	case *dto.ImageRequest:
		output = 1 // MakeQuote replaces this with the verified image output bound.
	case *dto.ClaudeRequest:
		if req.MaxTokens != nil {
			output = *req.MaxTokens
		}
	default:
		return true, retailPriceError(retailprice.ErrConfig)
	}
	if output == 0 || output > 1000000 {
		return true, retailPriceError(retailprice.ErrBudget)
	}
	ids, err := model.RetailSupplyIDs(info.UsingGroup)
	if err != nil {
		return true, retailPriceError(err)
	}
	quote, err := model.RetailQuote(info.UserId, info.TokenId, info.OriginModelName, int(output), ids, c.GetString("retail_studio_product"))
	if err != nil || quote == nil {
		if err == nil {
			err = retailprice.ErrConfig
		}
		return true, retailPriceError(err)
	}
	_, imageRequest := info.Request.(*dto.ImageRequest)
	if imageRequest != (quote.Model.Kind == "image") {
		return true, retailPriceError(retailprice.ErrConfig)
	}
	// 在入站阶段登记经过检查的内嵌图片。出站只消费这份清单，不重新信任转换后的数据。
	if !imageRequest && c.Request.URL.Path == "/v1/responses" && !HasStudioVision(c.Request) {
		pictures, err := retailResponsesPictures(raw)
		if err != nil {
			return true, retailPriceError(err)
		}
		if len(pictures) > 0 {
			c.Request = c.Request.WithContext(WithStudioVision(c.Request.Context(), pictures))
		}
	}
	if err := validateRetailRequest(c, raw, quote.Model.Kind, c.Request.URL.Path); err != nil {
		return true, retailPriceError(err)
	}
	// Counting is mandatory for a guarded request, even when the legacy global estimate switch is off.
	meta := info.Request.GetTokenCountMeta()
	tokens, err := CountRequestToken(c, meta, info)
	if err != nil || tokens < 0 || tokens > quote.Model.Tiers[len(quote.Model.Tiers)-1].UpTo || (quote.InputLimit > 0 && tokens > quote.InputLimit) {
		return true, retailPriceError(retailprice.ErrBudget)
	}
	var replay []string
	if c.Request.URL.Path == "/v1/responses" {
		replay = retailReplayContents(raw)
	}
	// 本地分词器无法读出密文的上下文长度。与图片编辑相同，先预留可接受的上限，
	// 再按上游返回的真实 usage 结算。保留本次输入，出站前核对协议转换没有丢掉历史。
	if len(replay) > 0 {
		c.Set("retail_response_replay", replay)
	}
	if c.GetBool("retail_studio_image_edit") || len(replay) > 0 || c.GetString("retail_previous_response") != "" {
		tokens = quote.Model.Tiers[len(quote.Model.Tiers)-1].UpTo
		if quote.InputLimit > 0 {
			tokens = min(tokens, quote.InputLimit)
		}
	}
	quote, err = model.RetailQuoteWithInput(info.UserId, info.TokenId, info.OriginModelName, int(output), tokens, ids, c.GetString("retail_studio_product"))
	if err != nil || quote == nil {
		if err == nil {
			err = retailprice.ErrConfig
		}
		return true, retailPriceError(err)
	}
	if imageRequest != (quote.Model.Kind == "image") || (quote.InputLimit > 0 && tokens > quote.InputLimit) {
		return true, retailPriceError(retailprice.ErrBudget)
	}
	info.SetEstimatePromptTokens(tokens)
	quota, err := model.ReservePricedRetailCharge(info.RequestId, info.UserId, info.TokenId, quote)
	if err != nil {
		return true, retailPriceError(err)
	}
	info.RetailQuote = quote
	info.TieredBillingSnapshot = nil
	info.PriceData = hosttypes.PriceData{QuotaToPreConsume: quota, GroupRatioInfo: hosttypes.GroupRatioInfo{GroupRatio: 1}}
	session := &BillingSession{retail: true, relayInfo: info, funding: &WalletFunding{userId: info.UserId}, preConsumedQuota: quota}
	info.Billing = session
	session.syncRelayInfo()
	c.Set("retail_price_guard", true)
	return true, nil
}

// retailPriceError exposes stable non-retryable codes without exposing supplier costs or database errors.
func retailPriceError(err error) *types.NewAPIError {
	code := "retail_price_unavailable"
	for _, known := range []error{retailprice.ErrAccess, retailprice.ErrCap, retailprice.ErrBudget, retailprice.ErrExpired, retailprice.ErrConfig, model.ErrRetailPriceConflict} {
		if errors.Is(err, known) {
			code = known.Error()
			break
		}
	}
	return types.NewErrorWithStatusCode(errors.New(code), types.ErrorCode(code), http.StatusForbidden, types.ErrOptionWithSkipRetry())
}

// retailDispatchOriginMatches 核对实际转发目的地与采购来源。旧星行绑定沿用固定域名，自定义来源只信任已保存的站点。
func retailDispatchOriginMatches(book retailprice.Book, channel int, destination *url.URL) bool {
	origin := ""
	if site := book.RateSiteByChannel[channel]; site != 0 {
		origin = retailprice.RateFeedOrigin(book.SiteFeeds[site].URL)
	} else if slices.ContainsFunc(book.GroupBindings, func(b retailprice.GroupBinding) bool { return b.ChannelID == channel }) {
		origin = "https://aixingxing.pro"
	} else {
		return true
	}
	return origin != "" && destination != nil && destination.Scheme == "https" && destination.User == nil && strings.EqualFold("https://"+destination.Host, origin)
}

// GuardRetailUpstream validates the final serialized body after model/parameter overrides, immediately before I/O.
// Rates must still support the original sale quote; publishing a new version never reprices this request.
func GuardRetailUpstream(c *gin.Context, req *http.Request, info *relaycommon.RelayInfo) error {
	quote := info.RetailQuote
	if quote == nil {
		// 已认证的历史引用只能走有最终凭据检查的计费路径。
		if c.GetString("retail_previous_response") != "" {
			return retailprice.ErrAccess
		}
		return nil
	}
	if quote.ValidUntil <= time.Now().Unix() || req.GetBody == nil {
		return retailprice.ErrExpired
	}
	if strings.Contains(strings.ToLower(req.Header.Get("anthropic-beta")), "fast") {
		return retailprice.ErrConfig
	}
	book, version, err := model.GetRetailPriceBook()
	if err != nil || !book.Enabled || version != quote.Version {
		return retailprice.ErrExpired
	}
	if !retailDispatchOriginMatches(book, info.ChannelId, req.URL) {
		return retailprice.ErrAccess
	}
	// First release disables automatic retries for guarded calls: failed procurement has unknown cost.
	if info.RetryIndex > 0 {
		return retailprice.ErrUsage
	}
	ids, err := model.RetailSupplyIDs(info.UsingGroup)
	if err != nil || !slices.Contains(ids, info.ChannelId) {
		return retailprice.ErrAccess
	}
	fresh, err := model.RetailQuoteWithInput(info.UserId, info.TokenId, info.OriginModelName, quote.OutputLimit, quote.ReservedInputTokens, []int{info.ChannelId}, c.GetString("retail_studio_product"))
	// A publication between these reads invalidates dispatch, even if it lowers
	// prices. This keeps model mapping and tariff from different versions apart.
	if err != nil || fresh == nil || fresh.Version != version || fresh.ProductID != quote.ProductID || fresh.WalletCNYPerUnit != quote.WalletCNYPerUnit || len(fresh.Sale) != len(quote.Sale) {
		return retailprice.ErrExpired
	}
	for i, tier := range fresh.Sale {
		if tier.UpTo != quote.Sale[i].UpTo {
			return retailprice.ErrExpired
		}
		for j, raw := range tier.Prices.Values() {
			current, _ := retailprice.Amount(raw)
			accepted, _ := retailprice.Amount(quote.Sale[i].Prices.Values()[j])
			if current.GreaterThan(accepted) {
				return retailprice.ErrCap
			}
		}
	}
	upstreamModel := ""
	rateSourceID := 0
	for _, product := range book.Products {
		if product.ID == quote.ProductID {
			rateSourceID = product.RateSourceChannelID
			for _, route := range product.Routes {
				if route.ChannelID == info.ChannelId {
					upstreamModel = route.Models[quote.Model.ID]
				}
			}
		}
	}
	reader, err := req.GetBody()
	if err != nil {
		return err
	}
	raw, err := io.ReadAll(io.LimitReader(reader, (8<<20)+1))
	_ = reader.Close()
	if err != nil || len(raw) > 8<<20 || !gjson.ValidBytes(raw) {
		return retailprice.ErrConfig
	}
	if !checkRetailResponseOrigin(c, req, info, raw) {
		// 凭据在绑定后轮换，或出站协议无法表达该引用时，客户端可改为发送完整历史。
		if c.GetString("retail_previous_response") != "" {
			msg := i18n.T(c, i18n.MsgRetailStateUnsupported, map[string]any{"Field": "previous_response_id"})
			return types.NewOpenAIError(errors.New(msg), "previous_response_not_found", http.StatusBadRequest, types.ErrOptionWithSkipRetry())
		}
		return retailprice.ErrAccess
	}
	reference := studioReferencePath(c.Request, c.GetBool("retail_studio_image_edit"))
	if err := validateRetailRequest(c, raw, quote.Model.Kind, req.URL.Path); err != nil {
		return err
	}
	// 只有原生 Responses 能可靠传递这类历史。渠道覆盖参数或跨协议转换若增删、
	// 调换了密文，拒绝发单，避免静默丢上下文或在预留之外增加不可计数的输入。
	var outgoingReplay []string
	if strings.HasSuffix(req.URL.Path, "/responses") {
		outgoingReplay = retailReplayContents(raw)
	}
	if !slices.Equal(outgoingReplay, c.GetStringSlice("retail_response_replay")) {
		return retailprice.ErrConfig
	}
	if gjson.GetBytes(raw, "model").String() != upstreamModel || upstreamModel == "" {
		return retailprice.ErrAccess
	}
	if quote.Model.Kind != "image" {
		limitField := "max_tokens"
		if strings.HasSuffix(req.URL.Path, "/responses") {
			limitField = "max_output_tokens"
		} else if strings.HasSuffix(req.URL.Path, "/chat/completions") && gjson.GetBytes(raw, "max_completion_tokens").Exists() {
			limitField = "max_completion_tokens"
		}
		for _, field := range []string{"max_tokens", "max_completion_tokens", "max_output_tokens"} {
			v := gjson.GetBytes(raw, field)
			if v.Exists() && (v.Type != gjson.Number || v.Float() < 1 || v.Float() > float64(quote.OutputLimit) || v.Float() != math.Trunc(v.Float())) {
				return retailprice.ErrBudget
			}
		}
		if !gjson.GetBytes(raw, limitField).Exists() {
			var body map[string]common.RawMessage
			if common.Unmarshal(raw, &body) != nil {
				return retailprice.ErrConfig
			}
			body[limitField], err = common.Marshal(quote.OutputLimit)
			if err != nil {
				return err
			}
			raw, err = common.Marshal(body)
			if err != nil {
				return err
			}
			_ = req.Body.Close()
			req.Body = io.NopCloser(bytes.NewReader(raw))
			req.ContentLength = int64(len(raw))
			req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(raw)), nil }
		}
	}
	// Count the body after administrator overrides and protocol conversion as well;
	// the original request's estimate does not bound an injected system prompt.
	var finalRequest dto.Request
	switch {
	case quote.Model.Kind == "image" && ((strings.HasSuffix(req.URL.Path, "/images/edits") && c.GetBool("retail_studio_image_edit")) || (strings.HasSuffix(req.URL.Path, "/images/generations") && !c.GetBool("retail_studio_image_edit"))):
		finalRequest = &dto.ImageRequest{}
	case strings.HasSuffix(req.URL.Path, "/responses") && quote.Model.Kind != "image":
		finalRequest = &dto.OpenAIResponsesRequest{}
	case strings.HasSuffix(req.URL.Path, "/messages") && quote.Model.Kind != "image":
		finalRequest = &dto.ClaudeRequest{}
	case strings.HasSuffix(req.URL.Path, "/chat/completions") && quote.Model.Kind != "image":
		finalRequest = &dto.GeneralOpenAIRequest{}
	default:
		return retailprice.ErrConfig
	}
	if common.Unmarshal(raw, finalRequest) != nil {
		return retailprice.ErrConfig
	}
	tokens, err := CountRequestToken(c, finalRequest.GetTokenCountMeta(), info)
	if err != nil || tokens < 0 || tokens > quote.Model.Tiers[len(quote.Model.Tiers)-1].UpTo || (quote.InputLimit > 0 && tokens > quote.InputLimit) {
		return retailprice.ErrBudget
	}
	// Persist the actual supplier tariff, never just the mutable channel identity.
	audit := map[string]any{"pricing_version": version, "product": quote.ProductID, "model": fresh.Model, "rates": book.Rates, "group_bindings": book.GroupBindings, "public_rate_sync": book.PublicRateSync}
	// 只记本次用到的两个分组及其报价时间，避免把所有上游的完整快照复制进每笔流水。
	if len(book.SiteFeeds) > 0 {
		tariffs := map[int]any{}
		groups := book.RegisteredGroups()
		for _, id := range []int{rateSourceID, info.ChannelId} {
			site := book.RateSiteByChannel[id]
			if site == 0 || groups[id] == "" {
				continue
			}
			rate, err := book.BoundRate(retailprice.GroupBinding{ChannelID: id, UpstreamGroup: groups[id]})
			if err != nil {
				return err
			}
			feed := book.SiteFeeds[site]
			tariffs[id] = map[string]any{"site_id": site, "rate": rate, "source": feed.URL, "updated_at": feed.Snapshot.UpdatedAt, "fetched_at": feed.Snapshot.FetchedAt}
		}
		audit["site_group_tariffs"] = tariffs
	}
	snapshot, err := common.Marshal(audit)
	if err != nil {
		return err
	}
	result := model.DB.Model(&model.RetailAttempt{}).Where("id = ? AND request_id = ?", c.GetInt("retail_attempt_id"), info.RequestId).Update("supply_snapshot", string(snapshot))
	if result.Error != nil {
		return result.Error
	}
	// A successful SQL statement can still match no attempt. Never procure without
	// the durable record needed to reconcile the exact price accepted for this call.
	if result.RowsAffected != 1 {
		return retailprice.ErrUsage
	}
	if reference != "" {
		return attachStudioReference(req, raw, reference, info.ChannelSetting.StudioImageEditJSON)
	}
	return nil
}

// retailReplayContents 按顺序提取客户端随 Responses input 数组携带的推理或压缩历史。
// 比较类型和内容，避免把外观相同但语义不同的输入项视为等价；只在本次请求内使用。
func retailReplayContents(raw []byte) []string {
	var contents []string
	input := gjson.GetBytes(raw, "input")
	if !input.IsArray() {
		return nil
	}
	input.ForEach(func(_, item gjson.Result) bool {
		if ciphertext, ok := retailReplayCiphertext(item); ok {
			contents = append(contents, item.Get("type").String(), ciphertext)
		}
		return true
	})
	return contents
}

// retailReplayCiphertext 识别 Responses 支持的自包含密文，不把普通消息或工具字段当成凭据。
func retailReplayCiphertext(item gjson.Result) (string, bool) {
	kind := item.Get("type").String()
	ciphertext := item.Get("encrypted_content")
	return ciphertext.Str, (kind == "reasoning" || kind == "compaction") && ciphertext.Type == gjson.String && ciphertext.Str != ""
}

// validateRetailRequest 校验入站和最终出站的实际协议，工作台上传图片的许可仅限本次请求。
func validateRetailRequest(c *gin.Context, raw []byte, kind, path string) error {
	if kind != "image" {
		var vision map[string]int
		if HasStudioVision(c.Request) {
			vision = c.Request.Context().Value(studioVisionKey{}).(map[string]int)
		}
		return validateRetailChat(raw, vision, strings.HasSuffix(path, "/responses"), c.GetString("retail_previous_response"))
	}
	edit := c.GetBool("retail_studio_image_edit")
	return validateRetailBody(raw, kind, edit, studioReferencePath(c.Request, edit) != "")
}

// validateRetailTextBody 按普通文本协议校验消息，不开放 Responses 专用的回放输入。
func validateRetailTextBody(raw []byte) error {
	return validateRetailChat(raw, nil, false, "")
}

// uniqueRetailKeys 在解析、改写或计量之前检查重复字段，确保各阶段看到同一份输入。
func uniqueRetailKeys(root gjson.Result) bool {
	// Reject duplicate keys at every depth: inspectors and upstream JSON parsers
	// may otherwise disagree about which model, quantity or content type wins.
	objects := []gjson.Result{root}
	for len(objects) > 0 {
		item := objects[len(objects)-1]
		objects = objects[:len(objects)-1]
		seen := map[string]bool{}
		duplicate := false
		item.ForEach(func(key, value gjson.Result) bool {
			if item.IsObject() {
				if seen[key.String()] {
					duplicate = true
					return false
				}
				seen[key.String()] = true
			}
			if value.IsObject() || value.IsArray() {
				objects = append(objects, value)
			}
			return true
		})
		if duplicate {
			return false
		}
	}
	return true
}

// validateRetailChat 校验文本计费契约。原生 Responses 只在 input 数组的直接元素中
// 接受加密历史和工具回放；工具结果仍须遵守本次请求的媒体许可。
func validateRetailChat(raw []byte, vision map[string]int, responses bool, previous string) error {
	root := gjson.ParseBytes(raw)
	if !gjson.ValidBytes(raw) || !root.IsObject() {
		return retailprice.ErrConfig
	}
	if !uniqueRetailKeys(root) {
		return retailprice.ErrConfig
	}
	// 引用必须是中间件已经按本地用户和 Key 验证过的同一个回复 ID。
	if ref := root.Get("previous_response_id"); ref.Type != gjson.Null && (!responses || ref.Type != gjson.String || ref.Str != previous) {
		return retailprice.ErrConfig
	}
	for _, field := range []string{"audio", "modalities", "background", "conversation", "prompt", "service_tier", "speed", "parameters"} {
		v := root.Get(field)
		if !v.Exists() || v.Type == gjson.Null {
			continue
		}
		if field == "service_tier" && v.String() == "default" {
			continue
		}
		if field == "background" && v.Type == gjson.False {
			continue
		}
		return retailprice.ErrConfig
	}
	if n := root.Get("n"); n.Exists() && (n.Type != gjson.Number || n.Float() != 1) {
		return retailprice.ErrConfig
	}
	valid := true
	root.Get("tools").ForEach(func(_, tool gjson.Result) bool {
		typ := tool.Get("type").String()
		if responses && typ == "namespace" {
			// 命名空间仅组织客户端工具，逐项核对子工具以保留服务端工具的计费限制。
			members := tool.Get("tools")
			valid = tool.Get("name").Str != "" && members.IsArray() && len(members.Array()) > 0
			if valid {
				members.ForEach(func(_, member gjson.Result) bool {
					valid = member.Get("name").Str != "" && slices.Contains([]string{"function", "custom"}, member.Get("type").Str)
					return valid
				})
			}
		} else {
			valid = typ == "function" || (responses && typ == "custom") || (typ == "" && tool.Get("input_schema").Exists())
		}
		return valid
	})
	if !valid {
		return retailprice.ErrConfig
	}
	remaining := maps.Clone(vision)
	queue := []gjson.Result{root.Get("messages"), root.Get("system")}
	input := root.Get("input")
	if responses && input.IsArray() {
		for _, item := range input.Array() {
			// 只有直接位于 Responses input 的条目才是工具历史；工具返回的媒体
			// 仍须走普通内容检查，不能因为外层调用已完成就放行。
			_, encrypted := retailReplayCiphertext(item)
			switch item.Get("type").Str {
			case "custom_tool_call":
				if item.Get("input").Type != gjson.String {
					return retailprice.ErrConfig
				}
			case "custom_tool_call_output":
				output := item.Get("output")
				if output.Type != gjson.String && !output.IsArray() {
					return retailprice.ErrConfig
				}
			case "web_search_call":
				// 历史动作只作为模型上下文，不访问 URL，也不启用新搜索。
				if item.Get("status").Str != "completed" {
					return retailprice.ErrConfig
				}
				if _, err := dto.NormalizeResponsesWebSearchAction([]byte(item.Get("action").Raw)); err != nil {
					return retailprice.ErrConfig
				}
			default:
				if !encrypted {
					queue = append(queue, item)
					continue
				}
			}
			if item.Get("image_url").Exists() || item.Get("audio").Exists() || (!encrypted && item.Get("encrypted_content").Exists()) {
				return retailprice.ErrConfig
			}
			queue = append(queue, item.Get("content"), item.Get("output"))
		}
	} else {
		queue = append(queue, input)
	}
	for len(queue) > 0 {
		item := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		if item.IsArray() {
			queue = append(queue, item.Array()...)
			continue
		}
		if !item.IsObject() {
			continue
		}
		typ := item.Get("type").String()
		if typ == "image_url" || typ == "input_image" || typ == "image" {
			// Protocol conversion may change the wrapper, but cannot add URLs or substitute bytes.
			var uri string
			switch typ {
			case "image_url":
				uri = item.Get("image_url.url").String()
			case "input_image":
				uri = item.Get("image_url").String()
			case "image":
				if item.Get("source.type").String() == "base64" {
					uri = "data:" + item.Get("source.media_type").String() + ";base64," + item.Get("source.data").String()
				}
			}
			if remaining[uri] == 0 {
				return retailprice.ErrConfig
			}
			remaining[uri]--
			if remaining[uri] == 0 {
				delete(remaining, uri)
			}
			continue
		}
		if !slices.Contains([]string{"", "message", "text", "input_text", "output_text", "function_call", "function_call_output", "tool_use", "tool_result", "thinking", "reasoning"}, typ) {
			return retailprice.ErrConfig
		}
		if item.Get("encrypted_content").Exists() || item.Get("image_url").Exists() || item.Get("audio").Exists() {
			return retailprice.ErrConfig
		}
		// Responses function results use output, while messages/tool results use
		// content. Both can contain media and therefore need the same text-only gate.
		queue = append(queue, item.Get("content"), item.Get("output"))
	}
	if len(remaining) > 0 {
		return retailprice.ErrConfig
	}
	return nil
}

// SettleRetailTextPrice uses existing protocol normalization but prices the result only in the frozen CNY schedule.
func SettleRetailTextPrice(c *gin.Context, info *relaycommon.RelayInfo, usage *dto.Usage, claude bool) (int, error) {
	if info.RetailQuote.Model.Kind == "image" {
		return settleRetailImagePrice(c, info, usage)
	}
	if usage == nil || common.GetContextKeyBool(c, constant.ContextKeyLocalCountTokens) || usage.BillingUsage != nil && usage.BillingUsage.Estimated {
		return 0, retailprice.ErrUsage
	}
	// Validate raw aliases and the frozen provider snapshot before normalization:
	// positive-only fallback rules can otherwise hide malformed negative counters.
	sources := []*dto.Usage{usage}
	if snapshot := usage.BillingUsage; snapshot != nil {
		if snapshot.GeminiUsageMetadata != nil {
			return 0, retailprice.ErrUsage // This contract admits only OpenAI/Claude text usage.
		}
		if snapshot.OpenAIUsage != nil {
			sources = append(sources, snapshot.OpenAIUsage)
		}
		if raw := snapshot.ClaudeUsage; raw != nil {
			for _, count := range []int{raw.InputTokens, raw.OutputTokens, raw.CacheReadInputTokens, raw.CacheCreationInputTokens, raw.ClaudeCacheCreation5mTokens, raw.ClaudeCacheCreation1hTokens, raw.GetCacheCreation5mTokens(), raw.GetCacheCreation1hTokens()} {
				if count < 0 || count > 2000000 {
					return 0, retailprice.ErrUsage
				}
			}
			if tools := raw.ServerToolUse; tools != nil && (tools.WebSearchRequests != 0 || tools.WebFetchRequests != 0 || tools.CodeExecutionRequests != 0 || tools.ToolSearchRequests != 0) {
				return 0, retailprice.ErrUsage
			}
		}
	}
	usage = effectiveBillingUsage(usage)
	sources = append(sources, usage)
	for _, source := range sources {
		if source == nil {
			return 0, retailprice.ErrUsage
		}
		for _, count := range []int{source.PromptTokens, source.CompletionTokens, source.InputTokens, source.OutputTokens, source.PromptCacheHitTokens, source.ClaudeCacheCreation5mTokens, source.ClaudeCacheCreation1hTokens} {
			if count < 0 || count > 2000000 {
				return 0, retailprice.ErrUsage
			}
		}
		for _, input := range []*dto.InputTokenDetails{&source.PromptTokensDetails, source.InputTokensDetails} {
			if input == nil {
				continue
			}
			// Vision tokens are included in provider input totals. A breakdown must fit that
			// total and is billable only for requests whose attachment set we verified.
			if input.AudioTokens != 0 || input.ImageTokens < 0 || input.ImageTokens > max(source.PromptTokens, source.InputTokens) || (input.ImageTokens > 0 && !HasStudioVision(c.Request)) {
				return 0, retailprice.ErrUsage
			}
			for _, count := range []int{input.CachedTokens, input.CacheWriteTokens, input.CachedCreationTokens} {
				if count < 0 || count > 2000000 {
					return 0, retailprice.ErrUsage
				}
			}
		}
		for _, output := range []*dto.OutputTokenDetails{&source.CompletionTokenDetails, source.OutputTokensDetails} {
			if output != nil && (output.AudioTokens != 0 || output.ImageTokens != 0) {
				return 0, retailprice.ErrUsage
			}
		}
	}
	params := BuildTieredTokenParams(usage, claude, map[string]bool{"cr": true, "cc": true, "cc1h": true})
	values := []float64{params.P, params.C, params.CR, params.CC, params.CC1h, params.Len}
	for _, v := range values {
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 2000000 || math.Trunc(v) != v {
			return 0, retailprice.ErrUsage
		}
	}
	amount, err := info.RetailQuote.Charge(int(params.Len), [5]int{int(params.P), int(params.C), int(params.CR), int(params.CC), int(params.CC1h)})
	if err != nil {
		return 0, err
	}
	quota, charged, err := model.RetailCNYQuota(amount, info.RetailQuote.WalletCNYPerUnit)
	if err != nil {
		return 0, err
	}
	reserve, _ := decimal.NewFromString(info.RetailQuote.ReservedCNY)
	if charged.GreaterThan(reserve) {
		return 0, retailprice.ErrBudget
	}
	c.Set("retail_actual_cny", charged.String())
	return quota, nil
}
