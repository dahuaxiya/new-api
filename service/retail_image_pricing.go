package service

import (
	"slices"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/retailprice"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"github.com/tidwall/gjson"
)

// validateRetailBody admits only modalities covered by the selected official tariff.
// Image v1 is text-to-image, one completed PNG, with no edits, URLs, tools or streaming.
func validateRetailBody(raw []byte, kind string) error {
	if kind != "image" {
		return validateRetailTextBody(raw)
	}
	root := gjson.ParseBytes(raw)
	if !gjson.ValidBytes(raw) || !root.IsObject() {
		return retailprice.ErrConfig
	}
	valid := true
	seen := map[string]bool{}
	root.ForEach(func(key, value gjson.Result) bool {
		name := key.String()
		if seen[name] {
			valid = false
			return false
		}
		seen[name] = true
		switch name {
		case "model", "prompt":
			valid = value.Type == gjson.String && strings.TrimSpace(value.String()) != ""
		case "size":
			valid = slices.Contains([]string{"1024x1024", "1024x1536", "1536x1024"}, value.String())
		case "quality":
			valid = slices.Contains([]string{"low", "medium", "high"}, value.String())
		case "n":
			valid = value.Type == gjson.Number && value.Float() == 1
		case "stream":
			valid = value.Type == gjson.False
		case "output_format":
			valid = value.String() == "png"
		case "response_format":
			valid = value.String() == "b64_json"
		default:
			valid = false // Reject unknown cost-affecting overrides before upstream I/O.
		}
		return valid
	})
	if !valid || !seen["model"] || !seen["prompt"] || len(root.Get("prompt").String()) > 32000 {
		return retailprice.ErrConfig
	}
	return nil
}

// settleRetailImagePrice charges reported text input and image output tokens.
// Missing modality detail or ambiguous cached usage retains the reservation for review;
// using image-count estimates here would silently misprice token-billed GPT Image models.
func settleRetailImagePrice(c *gin.Context, info *relaycommon.RelayInfo, usage *dto.Usage) (int, error) {
	if usage == nil || common.GetContextKeyBool(c, constant.ContextKeyLocalCountTokens) || usage.BillingUsage != nil && usage.BillingUsage.Estimated || info.BillingImageCount == nil || *info.BillingImageCount != 1 {
		return 0, retailprice.ErrUsage
	}
	usage = effectiveBillingUsage(usage)
	if usage == nil || usage.InputTokens <= 0 || usage.OutputTokens <= 0 || usage.InputTokensDetails == nil {
		return 0, retailprice.ErrUsage
	}
	details := usage.InputTokensDetails
	if details.ImageTokens != 0 || details.AudioTokens != 0 || details.TextTokens != usage.InputTokens || details.CachedTokens < 0 || details.CachedTokens > usage.InputTokens || details.CacheWriteTokens != 0 || details.CachedCreationTokens != 0 {
		return 0, retailprice.ErrUsage
	}
	if cached := details.CachedTokensDetails; cached != nil {
		if cached.ImageTokens != nil && *cached.ImageTokens != 0 || cached.AudioTokens != nil && *cached.AudioTokens != 0 || cached.TextTokens != nil && *cached.TextTokens != details.CachedTokens {
			return 0, retailprice.ErrUsage
		}
	}
	if out := usage.OutputTokensDetails; out != nil && (out.AudioTokens != 0 || out.TextTokens != 0 || out.ReasoningTokens != 0 || out.ImageTokens != usage.OutputTokens) {
		return 0, retailprice.ErrUsage
	}
	if usage.PromptTokens != usage.InputTokens || usage.CompletionTokens != usage.OutputTokens || usage.TotalTokens != usage.InputTokens+usage.OutputTokens {
		return 0, retailprice.ErrUsage
	}
	amount, err := info.RetailQuote.Charge(usage.InputTokens, [5]int{usage.InputTokens - details.CachedTokens, usage.OutputTokens, details.CachedTokens, 0, 0})
	if err != nil {
		return 0, err
	}
	quota, charged, err := model.RetailCNYQuota(amount, info.RetailQuote.WalletCNYPerUnit)
	reserve, parseErr := decimal.NewFromString(info.RetailQuote.ReservedCNY)
	if err != nil || parseErr != nil || charged.GreaterThan(reserve) {
		return 0, retailprice.ErrBudget
	}
	c.Set("retail_actual_cny", charged.String())
	return quota, nil
}
