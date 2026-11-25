package service

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
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
	"github.com/QuantumNous/new-api/setting/operation_setting"
	hosttypes "github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"github.com/tidwall/gjson"
)

// PrepareRetailPricing 在旧计费流程预扣前为零售请求创建人民币报价和预留。
// 只有从未发布过的价表才允许回退到旧计费，已发布价表暂停后继续拦截。
// 返回值依次表示是否接管请求计费和是否因价格、线路或请求参数而拒绝。
func PrepareRetailPricing(c *gin.Context, info *relaycommon.RelayInfo) (bool, *types.NewAPIError) {
	if !c.GetBool("retail_request") {
		return false, nil
	}
	book, version, err := model.GetRetailPriceBook()
	if err != nil {
		return true, retailPriceError(err)
	}
	if !book.Enabled {
		if version > 0 || info.RelayFormat == types.RelayFormatGemini || c.GetString("retail_responses_utility") != "" || c.Request.URL.Path == "/v1/images/generations" || c.Request.URL.Path == "/v1/images/edits" {
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
	// 首次查价仅辨认商品与模型，不应把本地占位数写成客户的输出限制。
	output := uint(1)
	explicitOutput := false
	switch req := info.Request.(type) {
	case *dto.GeneralOpenAIRequest:
		explicitOutput = req.MaxTokens != nil || req.MaxCompletionTokens != nil
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
		explicitOutput = req.MaxOutputTokens != nil
		if req.MaxOutputTokens != nil {
			output = *req.MaxOutputTokens
		}
	case *dto.GeminiChatRequest:
		explicitOutput = req.GenerationConfig.MaxOutputTokens != nil
		if req.GenerationConfig.MaxOutputTokens != nil {
			output = *req.GenerationConfig.MaxOutputTokens
		}
	case *dto.ImageRequest:
		output = 1 // MakeQuote replaces this with the verified image output bound.
	case *dto.ClaudeRequest:
		explicitOutput = req.MaxTokens != nil
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
		var limit *retailprice.OutputLimitError
		if req, ok := info.Request.(*dto.OpenAIResponsesRequest); ok && req.MaxOutputTokens != nil && errors.As(err, &limit) {
			// Magpie 收到字段明确的 400 才会省略自己补入的上限重试。原请求仍在预留与出站前拒绝。
			return true, types.WithOpenAIError(types.OpenAIError{
				Type: "invalid_request_error", Code: "invalid_value", Param: "max_output_tokens",
				Message: fmt.Sprintf("max_output_tokens: 不能超过该模型的输出上限 %d。", limit.MaxOutput),
			}, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
		}
		if err == nil {
			err = retailprice.ErrConfig
		}
		return true, retailPriceError(err)
	}
	clientLimit := 0
	if explicitOutput {
		clientLimit = int(output)
	} else if quote.EstimatedTextHold {
		// 分组 Key 按输入预留；未声明上限时只用模型容量校验账单，出站保留供应商默认值。
		output = uint(quote.Model.MaxOutput)
	} else {
		// 带显式金额预算的旧 Key 仍须在发单前锁定最大费用。
		output = uint(min(1024, quote.Model.MaxOutput))
	}
	responses := c.Request.URL.Path == "/v1/responses"
	if responses {
		// Read the selected route before reserving: rejecting only after dispatch leaves a wallet hold.
		channelID := common.GetContextKeyInt(c, constant.ContextKeyChannelId)
		limited := limitedResponsesRoute(book, channelID, quote.ProductID, info.OriginModelName)
		fastGroup := limited && book.RegisteredGroups()[channelID] == "GPT-UltraFast"
		magpieProbe := limited && isMagpieResponsesProbe(raw)
		if apiErr := validateResponsesContract(raw, limited, magpieProbe, quote.Model.MaxOutput, fastGroup); apiErr != nil {
			return true, apiErr
		}
		tier := gjson.GetBytes(raw, "service_tier").Str
		if fastGroup && (tier == "priority" || tier == "ultrafast") {
			// 优先级是供货 Key 的能力，出站必须保持同一高速组，不能降到普通组。
			c.Set("retail_fast_channel", channelID)
			c.Header("X-Luobo-Service-Tier", "ultrafast")
		}
		if magpieProbe || limited && gjson.GetBytes(raw, "max_output_tokens").Int() == int64(quote.Model.MaxOutput) {
			// 此线路使用模型上限结算。显式值不代表上游执行了独立的截断。
			c.Header("X-Luobo-Output-Limit", "best-effort")
		}
		if !explicitOutput && (limited || c.GetString("retail_responses_utility") == "compact") {
			// 无法约束输出的线路以模型上限校验实报用量；输出上限不参与分组文本的输入预扣。
			output = uint(quote.Model.MaxOutput)
		}
		c.Set("retail_limited_responses", limited)
	}
	_, imageRequest := info.Request.(*dto.ImageRequest)
	if imageRequest != (quote.Model.Kind == "image") {
		return true, retailPriceError(retailprice.ErrConfig)
	}
	// 在入站阶段登记经过检查的内嵌图片。出站只消费这份清单，不重新信任转换后的数据。
	if !imageRequest && responses && !HasStudioVision(c.Request) {
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
	// 内嵌参考图经过协议转换与渠道参数覆盖，必须在出站前核对原顺序及内容。
	if c.Request.URL.Path == "/v1/images/edits" && studioReferencePath(c.Request, true) == "" {
		c.Set("retail_edit_references", retailEditDigests(raw))
	}
	// Counting is mandatory for a guarded request, even when the legacy global estimate switch is off.
	meta := info.Request.GetTokenCountMeta()
	tokens, err := CountRequestToken(c, meta, info)
	if err != nil || tokens < 0 || tokens > quote.Model.Tiers[len(quote.Model.Tiers)-1].UpTo || (quote.InputLimit > 0 && tokens > quote.InputLimit) {
		return true, retailPriceError(retailprice.ErrBudget)
	}
	var replay []string
	if responses {
		replay = retailReplayContents(raw)
	}
	// 出站时核对密文历史是否完整。密文校验独立于计费方式。
	if len(replay) > 0 {
		c.Set("retail_response_replay", replay)
	}
	if quote.EstimatedTextHold {
		// 仅有历史引用时可能没有明文 token，以一个输入 token 作为最小估算。
		tokens = max(1, tokens)
	} else if c.Request.URL.Path == "/v1/images/edits" || len(replay) > 0 || c.GetString("retail_previous_response") != "" {
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
	quote.ClientOutputLimit = clientLimit
	if imageRequest != (quote.Model.Kind == "image") || (quote.InputLimit > 0 && tokens > quote.InputLimit) {
		return true, retailPriceError(retailprice.ErrBudget)
	}
	if quote.EstimatedTextHold {
		// 与普通 New API 相同，倍率只调整输入预扣，最终的官方单价和工具费不变。
		multiplier, err := operation_setting.InputPreConsumeMultiplier()
		if err != nil {
			return true, retailPriceError(retailprice.ErrConfig)
		}
		base, err := retailprice.Amount(quote.ReservedCNY)
		if err != nil {
			return true, retailPriceError(err)
		}
		quote.ReservedCNY = base.Mul(decimal.NewFromFloat(multiplier)).RoundCeil(8).String()
	}
	info.SetEstimatePromptTokens(tokens)
	// 渠道覆盖发生在预留之后；不能把客户端明确指定的一次误认为可省略的默认值。
	if cap := gjson.GetBytes(raw, "max_tool_calls"); cap.Exists() {
		c.Set("retail_explicit_tool_cap", cap.Raw)
	}
	for _, tool := range []string{dto.BuildInToolImageGeneration, dto.BuildInToolWebSearch} {
		limit, err := retailToolLimit(raw, c.Request.URL.Path, tool)
		if err != nil {
			return true, retailPriceError(err)
		}
		if limit > 0 {
			price := operation_setting.GetToolPriceForModel(tool, info.OriginModelName)
			if err := freezeRetailTool(quote, tool, price, limit); err != nil {
				return true, retailPriceError(err)
			}
		}
	}
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
	// 钱包或 Key 额度不足是可预期的预扣失败，不能误报成价表不可用。
	if errors.Is(err, model.ErrRetailQuota) {
		return types.WithOpenAIError(types.OpenAIError{Type: "insufficient_quota", Code: "insufficient_quota", Message: "可用余额或 Key 额度不足以预留本次请求的费用。"}, http.StatusPaymentRequired, types.ErrOptionWithSkipRetry())
	}
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
	if fast := c.GetInt("retail_fast_channel"); fast > 0 && (fast != info.ChannelId || book.RegisteredGroups()[info.ChannelId] != "GPT-UltraFast" || !limitedResponsesRoute(book, info.ChannelId, quote.ProductID, info.OriginModelName)) {
		return retailprice.ErrAccess
	}
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
	// Gemini 零售只支持转换成已验证的 Chat 计费路径，原生 Gemini 用量未纳入此账本。
	if info.RelayFormat == types.RelayFormatGemini && !strings.HasSuffix(req.URL.Path, "/chat/completions") {
		return retailprice.ErrConfig
	}
	reference := studioReferencePath(c.Request, c.Request.URL.Path == "/v1/images/edits")
	if err := validateRetailRequest(c, raw, quote.Model.Kind, req.URL.Path); err != nil {
		return err
	}
	if original, ok := c.Get("retail_edit_references"); ok && !slices.Equal(original.([][sha256.Size]byte), retailEditDigests(raw)) {
		return retailprice.ErrConfig
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
		// 出站参数可能经过渠道覆盖。图片预算只授权原请求中的工具与调用次数。
		outgoingLimit, err := retailToolLimit(raw, req.URL.Path, dto.BuildInToolImageGeneration)
		if err != nil || outgoingLimit != quote.ImageToolLimit {
			return retailprice.ErrConfig
		}
		// 只把已报价的原生搜索转给有同一种调用计数语义的渠道；不能在协议转换时丢掉工具。
		searchLimit, err := retailToolLimit(raw, req.URL.Path, dto.BuildInToolWebSearch)
		if err != nil || searchLimit != quote.SearchLimit {
			return retailprice.ErrConfig
		}
		if searchLimit > 0 && (info.ChannelMeta == nil || (info.ChannelType != constant.ChannelTypeOpenAI && info.ChannelType != constant.ChannelTypeCodex)) {
			return retailprice.ErrAccess
		}
		outgoingLimit = max(outgoingLimit, searchLimit)
		setOutput := !gjson.GetBytes(raw, limitField).Exists()
		if quote.EstimatedTextHold && quote.ClientOutputLimit == 0 && (strings.HasSuffix(req.URL.Path, "/responses") || strings.HasSuffix(req.URL.Path, "/chat/completions")) {
			// 不向支持省略上限的 OpenAI 接口注入模型最大值；某些线路拒收该参数。
			setOutput = false
		}
		setTool := outgoingLimit > 0 && !gjson.GetBytes(raw, "max_tool_calls").Exists()
		// 只允许已配置的原生 HTTP 流走本地次数检查。非流式和生图仍要求上游硬上限。
		if explicit := c.GetString("retail_explicit_tool_cap"); explicit != "" && outgoingLimit > 0 && gjson.GetBytes(raw, "max_tool_calls").Raw != explicit {
			return retailprice.ErrConfig
		}
		if setTool && quote.ImageToolLimit == 0 && searchLimit > 0 && c.GetBool("retail_limited_responses") && info.RelayFormat == types.RelayFormatOpenAIResponses && strings.HasSuffix(req.URL.Path, "/responses") {
			// The supplier rejects or ignores the injected cap. Inspect only a complete JSON result
			// or each native SSE event; the client has not requested a provider-side hard limit.
			c.Set("retail_search_stream_limit", searchLimit)
			setTool = false
		}
		if setTool && quote.ImageToolLimit == 0 && searchLimit > 0 && info.ChannelSetting.RetailSearchStreamLimit && info.IsStream && info.RelayFormat == types.RelayFormatOpenAIResponses && gjson.GetBytes(raw, "stream").Type == gjson.True {
			// 普通线路不接收站点补写的参数；响应处理器在转发每个事件前检查预算。
			c.Set("retail_search_stream_limit", searchLimit)
			setTool = false
		}
		if setOutput || setTool {
			var body map[string]common.RawMessage
			if common.Unmarshal(raw, &body) != nil {
				return retailprice.ErrConfig
			}
			if setTool {
				body["max_tool_calls"], err = common.Marshal(outgoingLimit)
				if err != nil {
					return err
				}
			}
			if setOutput {
				body[limitField], err = common.Marshal(quote.OutputLimit)
			}
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
	case quote.Model.Kind == "image" && ((strings.HasSuffix(req.URL.Path, "/images/edits") && c.Request.URL.Path == "/v1/images/edits") || (strings.HasSuffix(req.URL.Path, "/images/generations") && c.Request.URL.Path != "/v1/images/edits")):
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
	// 普通 API 的文件已规范化为内嵌图片。在最终计费校验后按渠道约定编码，只发送一次。
	if c.Request.URL.Path == "/v1/images/edits" && c.GetBool("retail_public_image") && !info.ChannelSetting.StudioImageEditJSON {
		return attachRetailEditForm(req, raw)
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
		if strings.HasPrefix(path, "/v1beta/models/") {
			return validateRetailGeminiRequest(raw, extractRetailGeminiModel(path))
		}
		var vision map[string]int
		if HasStudioVision(c.Request) {
			vision = c.Request.Context().Value(studioVisionKey{}).(map[string]int)
		}
		return validateRetailChatWithOptions(raw, vision, strings.HasSuffix(path, "/responses"), c.GetString("retail_previous_response"), c.GetString("retail_responses_utility") == "compact", c.GetInt("retail_fast_channel") > 0)
	}
	edit := c.Request.URL.Path == "/v1/images/edits"
	return validateRetailBody(raw, kind, edit, studioReferencePath(c.Request, edit) != "")
}

// validateRetailTextBody 按普通文本协议校验消息，不开放 Responses 专用的回放输入。
func validateRetailTextBody(raw []byte) error {
	return validateRetailChat(raw, nil, false, "")
}

// UniqueRetailKeys 在解析、改写或计量之前检查重复字段，确保各阶段看到同一份输入。
func UniqueRetailKeys(root gjson.Result) bool {
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
	return validateRetailChatWithOptions(raw, vision, responses, previous, false, false)
}

// validateRetailChatWithOptions 仅在服务端标记的新协议路径开放压缩触发和已验证的高速服务档位。
func validateRetailChatWithOptions(raw []byte, vision map[string]int, responses bool, previous string, compact, fastGroup bool) error {
	root := gjson.ParseBytes(raw)
	if !gjson.ValidBytes(raw) || !root.IsObject() {
		return retailprice.ErrConfig
	}
	if !UniqueRetailKeys(root) {
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
		if field == "service_tier" && (v.String() == "default" || responses && retailServiceTierAllowed(v.Str, fastGroup)) {
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
			valid = typ == "function" || (responses && (typ == "custom" || typ == dto.BuildInToolImageGeneration || typ == dto.BuildInToolWebSearch)) || (typ == "" && tool.Get("input_schema").Exists())
		}
		return valid
	})
	if !valid {
		return retailprice.ErrConfig
	}
	remaining := maps.Clone(vision)
	queue := []gjson.Result{root.Get("messages"), root.Get("system")}
	input := root.Get("input")
	if compact {
		items := input.Array()
		if len(items) < 2 || items[len(items)-1].Get("type").Str != "compaction_trigger" || root.Get("stream").Type != gjson.True {
			return retailprice.ErrConfig
		}
	}
	if responses && input.IsArray() {
		items := input.Array()
		for index, item := range items {
			// 只有直接位于 Responses input 的条目才是工具历史；工具返回的媒体
			// 仍须走普通内容检查，不能因为外层调用已完成就放行。
			_, encrypted := retailReplayCiphertext(item)
			switch item.Get("type").Str {
			case "compaction_trigger":
				// 只允许独立压缩入口追加一个无参数触发项，渠道覆盖也必须保留末尾触发项。
				if !compact || index != len(items)-1 || len(item.Map()) != 1 {
					return retailprice.ErrConfig
				}
				continue
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
	imageFee, err := retailImageToolCharge(info)
	if err != nil {
		return 0, err
	}
	searchFee := decimal.Zero
	if q := info.RetailQuote; q.SearchLimit > 0 {
		// 声明工具只预留余额；有真实调用才收费。上游用量异常时保留预授权交给原有复核流程。
		if info.ResponsesUsageInfo == nil || info.BuiltInTools[dto.BuildInToolWebSearch] == nil {
			return 0, retailprice.ErrUsage
		}
		count := info.BuiltInTools[dto.BuildInToolWebSearch].CallCount
		price, err := retailprice.Amount(q.SearchUnitCNY)
		if err != nil || !price.IsPositive() || count < 0 || count > q.SearchLimit {
			return 0, retailprice.ErrUsage
		}
		searchFee = price.Mul(decimal.NewFromInt(int64(count)))
	}
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
			if output == nil {
				continue
			}
			// 上游若把生成图片 token 算进总输出，只有已交付且按张报价的图片可以扣除这部分。
			if output.AudioTokens != 0 || output.ImageTokens < 0 || output.ImageTokens > max(source.CompletionTokens, source.OutputTokens) || (output.ImageTokens > 0 && !imageFee.IsPositive()) {
				return 0, retailprice.ErrUsage
			}
		}
	}
	// 按张收取的图片不再按文本输出 token 重复计费。
	params := BuildTieredTokenParams(usage, claude, map[string]bool{"cr": true, "cc": true, "cc1h": true, "img_o": imageFee.IsPositive()})
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
	quota, charged, err := model.RetailCNYQuota(amount.Add(imageFee).Add(searchFee), info.RetailQuote.WalletCNYPerUnit)
	if err != nil {
		return 0, err
	}
	reserve, _ := decimal.NewFromString(info.RetailQuote.ReservedCNY)
	if !info.RetailQuote.EstimatedTextHold && charged.GreaterThan(reserve) {
		return 0, retailprice.ErrBudget
	}
	c.Set("retail_actual_cny", charged.String())
	return quota, nil
}

// retailToolLimit 确定指定内置工具可采购的最大次数，仅原生 Responses 使用上游内置工具。
// 未指定次数按一次报价，出站按渠道能力选择原生上限或流式检查。显式多次调用按请求上限预留。
func retailToolLimit(raw []byte, path, kind string) (int, error) {
	tools := gjson.GetBytes(raw, "tools")
	found := false
	for _, tool := range tools.Array() {
		if tool.Get("type").Str != kind {
			continue
		}
		if found || !strings.HasSuffix(path, "/responses") {
			return 0, retailprice.ErrConfig
		}
		found = true
	}
	if !found {
		return 0, nil
	}
	cap := gjson.GetBytes(raw, "max_tool_calls")
	if !cap.Exists() {
		return 1, nil
	}
	if cap.Type != gjson.Number || cap.Float() != math.Trunc(cap.Float()) || cap.Float() < 1 || cap.Float() > dto.MaxImageN {
		return 0, retailprice.ErrBudget
	}
	return int(cap.Int()), nil
}

// freezeRetailTool 在钱包预留前冻结工具的人民币单价；站内工具价以每千次为单位。
// 搜索按官方基价和分组原始倍率，图片继续按销售倍率。结算沿用已冻结单价。
func freezeRetailTool(q *retailprice.Quote, tool string, perThousand float64, limit int) error {
	if limit < 1 || limit > dto.MaxImageN || perThousand <= 0 || math.IsNaN(perThousand) || math.IsInf(perThousand, 0) {
		return retailprice.ErrConfig
	}
	unit := decimal.NewFromFloat(perThousand).Shift(-3).Mul(q.SaleFactor).RoundCeil(8)
	if tool == dto.BuildInToolWebSearch {
		factor, err := retailprice.Amount(q.GroupMultiplier)
		if err != nil || !factor.IsPositive() {
			return retailprice.ErrConfig
		}
		// 零售钱包沿用本站美元数值计人民币的约定，搜索不再加收零售利润。
		unit = decimal.New(1, -2).Mul(factor).RoundCeil(8)
	}
	if !unit.IsPositive() {
		return retailprice.ErrConfig
	}
	if _, err := retailprice.Amount(unit.String()); err != nil {
		return err
	}
	held, err := retailprice.Amount(q.ReservedCNY)
	if err != nil {
		return err
	}
	switch tool {
	case dto.BuildInToolImageGeneration:
		q.ImageToolUnitCNY, q.ImageToolLimit = unit.String(), limit
	case dto.BuildInToolWebSearch:
		q.SearchUnitCNY, q.SearchLimit = unit.String(), limit
	default:
		return retailprice.ErrConfig
	}
	q.ReservedCNY = held.Add(unit.Mul(decimal.NewFromInt(int64(limit)))).String()
	return nil
}

// retailImageToolCharge 使用流式与非流式适配器去重后的完成数。上游超出授权上限时进入用量复核。
func retailImageToolCharge(info *relaycommon.RelayInfo) (decimal.Decimal, error) {
	quote := info.RetailQuote
	if info.ResponsesUsageInfo == nil || info.BuiltInTools[dto.BuildInToolImageGeneration] == nil {
		if quote.ImageToolLimit > 0 {
			return decimal.Zero, retailprice.ErrUsage
		}
		return decimal.Zero, nil
	}
	count := info.BuiltInTools[dto.BuildInToolImageGeneration].CallCount
	if count < 0 || count > quote.ImageToolLimit {
		return decimal.Zero, retailprice.ErrUsage
	}
	if count == 0 {
		return decimal.Zero, nil
	}
	unit, err := retailprice.Amount(quote.ImageToolUnitCNY)
	if err != nil || !unit.IsPositive() {
		return decimal.Zero, retailprice.ErrUsage
	}
	return unit.Mul(decimal.NewFromInt(int64(count))), nil
}
