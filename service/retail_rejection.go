package service

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/pkg/retailprice"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// retailFieldError carries only a server-defined rule and a safe schema path; never retain the rejected value.
type retailFieldError struct{ rule, field string }

// Error identifies the rule to internal callers without serializing the request.
func (e retailFieldError) Error() string { return e.rule }

// Unwrap preserves the fail-closed sentinel used by validators shared with the dispatch guard.
func (e retailFieldError) Unwrap() error { return retailprice.ErrConfig }

// retailItemField reconstructs the offending array position only on rejection. Object keys outside
// the protocol vocabulary may be user data; fall back to the schema field instead of echoing them.
func retailItemField(raw []byte, item gjson.Result, suffix string) string {
	path := item.Path(string(raw))
	fallback := suffix
	if fallback == "" {
		fallback = "body"
	}
	if path == "" || len(path) > 160 {
		return fallback
	}
	for _, part := range strings.Split(path, ".") {
		switch part {
		case "tools", "messages", "system", "input", "content", "output":
			continue
		}
		if _, err := strconv.ParseUint(part, 10, 32); err != nil {
			return fallback
		}
	}
	if suffix == "" {
		return path
	}
	return path + "." + suffix
}

// retailRejection logs one searchable, value-free diagnostic. The same field violation is a
// client error before pricing, but a site error after channel overrides have changed the body.
func retailRejection(c *gin.Context, stage, rule string, err error) *types.NewAPIError {
	var field retailFieldError
	var api *types.NewAPIError
	param := ""
	// The global body limit can reject before the retail 8 MiB check; do not claim which limit fired.
	if common.IsRequestBodyTooLargeError(err) {
		err = retailFieldError{"body_too_large", "body"}
	}
	var configRule retailprice.ConfigRule
	if errors.As(err, &configRule) {
		rule = string(configRule)
	}
	switch {
	case errors.As(err, &api):
		// Only callers producing fixed schema fields pass an API error here. Preserve SDK recovery hints.
		param = api.ToOpenAIError().Param
	case errors.As(err, &field) && stage != "dispatch":
		rule, param = field.rule, field.field
		status, code, key := http.StatusBadRequest, "invalid_value", i18n.MsgRetailFieldInvalid
		switch rule {
		case "unsupported_parameter", "unsupported_content_type", "unsupported_tool":
			code, key = "unsupported_parameter", i18n.MsgRetailFieldUnsupported
		case "body_too_large":
			status, code, key = http.StatusRequestEntityTooLarge, "request_too_large", i18n.MsgRetailBodyTooLarge
		}
		msg := i18n.T(c, key, map[string]any{"Field": param})
		api = types.WithOpenAIError(types.OpenAIError{Type: "invalid_request_error", Code: code, Param: param, Message: msg}, status, types.ErrOptionWithSkipRetry())
	case stage == "dispatch" || errors.Is(err, retailprice.ErrConfig):
		if errors.As(err, &field) {
			rule, param = field.rule, field.field
		}
		code := "retail_price_configuration_invalid"
		if stage == "dispatch" {
			code = "retail_dispatch_validation_failed"
		}
		api = types.WithOpenAIError(types.OpenAIError{Type: "server_error", Code: code, Message: i18n.T(c, i18n.MsgRetailSiteValidationFailed)}, http.StatusInternalServerError, types.ErrOptionWithSkipRetry())
	default:
		api = retailPriceError(err)
		// A database/storage failure is not a permission denial. Keep known wallet and policy responses intact.
		if api.GetErrorCode() == "retail_price_unavailable" {
			api = types.WithOpenAIError(types.OpenAIError{Type: "server_error", Code: "retail_price_unavailable", Message: i18n.T(c, i18n.MsgRetailSiteValidationFailed)}, http.StatusServiceUnavailable, types.ErrOptionWithSkipRetry())
		}
	}
	// The logger prefixes the request ID. Do not pass err.Error(), upstream URLs, key material or body values.
	logger.LogWarn(c, "retail_rejection stage=%s rule=%s field=%q status=%d code=%s channel_id=%d price_version=%d", stage, rule, param, api.StatusCode, api.GetErrorCode(), common.GetContextKeyInt(c, constant.ContextKeyChannelId), c.GetInt64("retail_diagnostic_price_version"))
	return api
}

// RetailResponsesInputError classifies failures preparing the shared request body before channel selection.
// Unexpected storage and serialization errors remain internal; never return their text to the client.
func RetailResponsesInputError(c *gin.Context, err error) *types.NewAPIError {
	return retailRejection(c, "normalize", "responses_input", err)
}
