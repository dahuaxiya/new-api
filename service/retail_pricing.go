package service

import (
	"bytes"
	"errors"
	"io"
	"math"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
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
	if err := validateRetailBody(raw, quote.Model.Kind, c.GetBool("retail_studio_image_edit")); err != nil {
		return true, retailPriceError(err)
	}
	// Counting is mandatory for a guarded request, even when the legacy global estimate switch is off.
	meta := info.Request.GetTokenCountMeta()
	tokens, err := CountRequestToken(c, meta, info)
	if err != nil || tokens < 0 || tokens > quote.Model.Tiers[len(quote.Model.Tiers)-1].UpTo || (quote.InputLimit > 0 && tokens > quote.InputLimit) {
		return true, retailPriceError(retailprice.ErrBudget)
	}
	// The image DTO estimates only the text prompt. For an edit reserve the admissible
	// context so a short prompt cannot dispatch an unreserved image input.
	if c.GetBool("retail_studio_image_edit") {
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

// GuardRetailUpstream validates the final serialized body after model/parameter overrides, immediately before I/O.
// Rates must still support the original sale quote; publishing a new version never reprices this request.
func GuardRetailUpstream(c *gin.Context, req *http.Request, info *relaycommon.RelayInfo) error {
	quote := info.RetailQuote
	if quote == nil {
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
	// Channel configuration can change after a binding is saved. A Xingxing cost
	// snapshot must never authorize procurement from a different supplier endpoint.
	if slices.ContainsFunc(book.GroupBindings, func(binding retailprice.GroupBinding) bool { return binding.ChannelID == info.ChannelId }) {
		if req.URL == nil || req.URL.Scheme != "https" || req.URL.Host != "aixingxing.pro" || req.URL.User != nil {
			return retailprice.ErrAccess
		}
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
	for _, product := range book.Products {
		if product.ID == quote.ProductID {
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
	if err := validateRetailBody(raw, quote.Model.Kind, c.GetBool("retail_studio_image_edit")); err != nil {
		return err
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
	snapshot, err := common.Marshal(map[string]any{"pricing_version": version, "product": quote.ProductID, "model": fresh.Model, "rates": book.Rates, "group_bindings": book.GroupBindings, "public_rate_sync": book.PublicRateSync})
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
	return nil
}

// validateRetailTextBody excludes metered tools, alternate tiers and media not priced by the text contract.
// JSON schemas inside function tools are not recursively treated as message content.
func validateRetailTextBody(raw []byte) error {
	root := gjson.ParseBytes(raw)
	if !gjson.ValidBytes(raw) || !root.IsObject() {
		return retailprice.ErrConfig
	}
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
			return retailprice.ErrConfig
		}
	}
	for _, field := range []string{"audio", "modalities", "background", "previous_response_id", "conversation", "prompt", "service_tier", "speed", "parameters"} {
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
		if typ != "function" && !(typ == "" && tool.Get("input_schema").Exists()) {
			valid = false
		}
		return valid
	})
	if !valid {
		return retailprice.ErrConfig
	}
	queue := []gjson.Result{root.Get("messages"), root.Get("input"), root.Get("system")}
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
			if input.ImageTokens != 0 || input.AudioTokens != 0 {
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
