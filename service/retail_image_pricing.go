package service

import (
	"encoding/base64"
	"net/http"
	"slices"
	"strconv"
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

// RetailReferenceModel limits reference billing to models with verified $8 image input and $2 cached image input.
func RetailReferenceModel(id string) bool {
	switch id {
	case "gpt-image-2", "gpt-image-2.5-sunburst", "gpt-image-2.5-flare", "gpt-image-2.6-sunburst", "gpt-image-2.6-flare":
		return true
	}
	return false
}

const RetailReferenceBytes = 5 << 20
const RetailMaxReferences = 16

// validRetailReference validates the ordered, bounded set of embedded raster images before procurement.
// Every image must use its actual media type. Supplier URLs, file IDs and masked edits need different contracts.
func validRetailReference(images gjson.Result) bool {
	items := images.Array()
	if !images.IsArray() || len(items) == 0 || len(items) > RetailMaxReferences {
		return false
	}
	remaining := RetailReferenceBytes
	for _, item := range items {
		if !item.IsObject() || len(item.Map()) != 1 {
			return false
		}
		uri := item.Get("image_url")
		prefix, encoded, found := strings.Cut(uri.Str, ",")
		if !found || len(encoded) > base64.StdEncoding.EncodedLen(remaining) {
			return false
		}
		data, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil || len(data) == 0 || len(data) > remaining {
			return false
		}
		mime := http.DetectContentType(data)
		if !slices.Contains([]string{"image/png", "image/jpeg", "image/webp"}, mime) || prefix != "data:"+mime+";base64" {
			return false
		}
		cfg, _, err := decodeImageConfig(data)
		if err != nil || cfg.Height <= 0 || cfg.Width <= 0 || int64(cfg.Width) > 40_000_000/int64(cfg.Height) {
			return false
		}
		remaining -= len(data)
	}
	return true
}

// ValidRetailImageSize preserves the common presets and permits flexible dimensions for GPT Image models.
func ValidRetailImageSize(model, size string) bool {
	if slices.Contains([]string{"1024x1024", "1024x1536", "1536x1024"}, size) {
		return true
	}
	if !RetailReferenceModel(model) {
		return false
	}
	if size == "auto" {
		return true
	}
	left, right, ok := strings.Cut(size, "x")
	if !ok || len(left) > 4 || len(right) > 4 {
		return false
	}
	width, werr := strconv.Atoi(left)
	height, herr := strconv.Atoi(right)
	// Bound each axis before multiplication and enforce the provider's pixel budget.
	return werr == nil && herr == nil && strconv.Itoa(width) == left && strconv.Itoa(height) == right &&
		min(width, height) > 0 && max(width, height) <= 3840 &&
		width%16 == 0 && height%16 == 0 && max(width, height) <= 3*min(width, height) &&
		width*height >= 655360 && width*height <= 8294400
}

// ValidRetailImageOptions checks the prior three quality levels plus the GPT Image output controls.
// Compression is an optional integer percentage and applies only to lossy output formats.
func ValidRetailImageOptions(model, quality, format string, compression *int) bool {
	extended := RetailReferenceModel(model)
	if quality != "" && !slices.Contains([]string{"low", "medium", "high"}, quality) && !(extended && quality == "auto") {
		return false
	}
	if format != "" && format != "png" && !(extended && (format == "jpeg" || format == "webp")) {
		return false
	}
	return compression == nil || ((format == "jpeg" || format == "webp") && *compression >= 0 && *compression <= 100)
}

// validateRetailBody admits only modalities covered by the selected tariff and fixed relay endpoint.
func validateRetailBody(raw []byte, kind string, edit, fileReference bool) error {
	if kind != "image" {
		return validateRetailTextBody(raw)
	}
	root := gjson.ParseBytes(raw)
	if !gjson.ValidBytes(raw) || !root.IsObject() || !UniqueRetailKeys(root) {
		return retailFieldError{"json_object_unique", "body"}
	}
	modelID := root.Get("model").String()
	format := root.Get("output_format").String()
	valid := true
	field := "body"
	seen := map[string]bool{}
	root.ForEach(func(key, value gjson.Result) bool {
		name := key.String()
		if seen[name] {
			valid = false
			return false
		}
		seen[name] = true
		switch name {
		case "images":
			valid = edit && !fileReference && validRetailReference(value)
		case "model", "prompt":
			valid = value.Type == gjson.String && strings.TrimSpace(value.String()) != ""
		case "size":
			valid = value.Type == gjson.String && ValidRetailImageSize(modelID, value.String())
		case "quality":
			valid = value.Type == gjson.String && value.String() != "" && ValidRetailImageOptions(modelID, value.String(), "", nil)
		case "n":
			valid = value.Type == gjson.Number && value.Float() == 1
		case "stream":
			valid = value.Type == gjson.False
		case "output_format":
			valid = value.Type == gjson.String && value.String() != "" && ValidRetailImageOptions(modelID, "", value.String(), nil)
		case "output_compression":
			compression := int(value.Int())
			valid = value.Type == gjson.Number && value.Float() == float64(compression) &&
				ValidRetailImageOptions(modelID, "", format, &compression)
		case "response_format":
			valid = value.String() == "b64_json"
		default:
			valid = false
			return false // Unknown keys may contain user data; report only the containing object.
		}
		if !valid {
			field = name
		}
		return valid
	})
	if !valid {
		return retailFieldError{"image_option", field}
	}
	if edit && ((!seen["images"] && !fileReference) || !RetailReferenceModel(modelID)) {
		return retailFieldError{"edit_reference", "images"}
	}
	if !seen["model"] {
		return retailFieldError{"required_model", "model"}
	}
	if !seen["prompt"] || len(root.Get("prompt").String()) > 32000 {
		return retailFieldError{"image_prompt", "prompt"}
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
	edit := c.Request.URL.Path == "/v1/images/edits"
	if details.ImageTokens < 0 || details.ImageTokens > usage.InputTokens || details.TextTokens != usage.InputTokens-details.ImageTokens || details.AudioTokens != 0 || details.CachedTokens < 0 || details.CachedTokens > usage.InputTokens || details.CacheWriteTokens != 0 || details.CachedCreationTokens != 0 || (edit != (details.ImageTokens > 0)) {
		return 0, retailprice.ErrUsage
	}
	cachedImage := 0
	// Cache hits have different prices per modality. Recover one missing component by subtraction only when unambiguous.
	cached := details.CachedTokensDetails
	if cached != nil && cached.AudioTokens != nil && *cached.AudioTokens != 0 {
		return 0, retailprice.ErrUsage
	}
	switch {
	case cached != nil && cached.ImageTokens != nil:
		cachedImage = *cached.ImageTokens
	case cached != nil && cached.TextTokens != nil:
		if *cached.TextTokens < 0 || *cached.TextTokens > details.CachedTokens {
			return 0, retailprice.ErrUsage
		}
		cachedImage = details.CachedTokens - *cached.TextTokens
	case details.CachedTokens == usage.InputTokens:
		cachedImage = details.ImageTokens
	case edit && details.CachedTokens != 0:
		return 0, retailprice.ErrUsage
	}
	if cachedImage < 0 || cachedImage > details.ImageTokens || cachedImage > details.CachedTokens {
		return 0, retailprice.ErrUsage
	}
	cachedText := details.CachedTokens - cachedImage
	if cachedText > details.TextTokens || (cached != nil && cached.TextTokens != nil && *cached.TextTokens != cachedText) {
		return 0, retailprice.ErrUsage
	}
	if out := usage.OutputTokensDetails; out != nil && (out.AudioTokens != 0 || out.TextTokens != 0 || out.ReasoningTokens != 0 || out.ImageTokens != usage.OutputTokens) {
		return 0, retailprice.ErrUsage
	}
	if usage.PromptTokens != usage.InputTokens || usage.CompletionTokens != usage.OutputTokens || usage.TotalTokens != usage.InputTokens+usage.OutputTokens {
		return 0, retailprice.ErrUsage
	}
	amount, err := info.RetailQuote.ChargeImage(usage.InputTokens, [5]int{details.TextTokens - cachedText, usage.OutputTokens, cachedText, 0, 0}, details.ImageTokens-cachedImage, cachedImage)
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
