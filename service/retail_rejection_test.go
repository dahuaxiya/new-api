package service

import (
	"bytes"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/retailprice"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// TestRetailRejectionBoundary verifies public attribution and private diagnostics for the same malformed content
// before and after channel overrides, as well as configuration, wallet, and storage failures.
func TestRetailRejectionBoundary(t *testing.T) {
	var logs bytes.Buffer
	common.LogWriterMu.Lock()
	old := gin.DefaultErrorWriter
	gin.DefaultErrorWriter = &logs
	common.LogWriterMu.Unlock()
	t.Cleanup(func() { common.LogWriterMu.Lock(); gin.DefaultErrorWriter = old; common.LogWriterMu.Unlock() })
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	c.Set(common.RequestIdKey, "fixture-request-42")
	const secret = "SENSITIVE_FIXTURE_VALUE"
	raw := []byte(`{"input":[{"role":"user","content":[{"type":"SENSITIVE_FIXTURE_VALUE"}]}]}`)
	bad := validateRetailChat(raw, nil, true, "")
	require.Error(t, bad)
	for _, tc := range []struct {
		stage, rule          string
		err                  error
		status               int
		code, field, logRule string
	}{
		{"input", "request_contract", bad, 400, "unsupported_parameter", "input.0.content.0.type", "unsupported_content_type"},
		{"dispatch", "request_contract", bad, 500, "retail_dispatch_validation_failed", "", "unsupported_content_type"},
		{"pricing", "price_book_load", retailprice.ConfigRule("tier_price"), 500, "retail_price_configuration_invalid", "", "tier_price"},
		{"storage", "body_read", errors.New(secret), 503, "retail_price_unavailable", "", "body_read"},
		{"wallet", "reserve_charge", model.ErrRetailQuota, 402, "insufficient_quota", "", "reserve_charge"},
		{"pricing", "initial_quote", retailprice.ErrAccess, 403, retailprice.ErrAccess.Error(), "", "initial_quote"},
		{"input", "body_too_large", retailFieldError{"body_too_large", "body"}, 413, "request_too_large", "body", "body_too_large"},
	} {
		logs.Reset()
		api := retailRejection(c, tc.stage, tc.rule, tc.err)
		response := api.ToOpenAIError()
		require.Equal(t, tc.status, api.StatusCode)
		require.EqualValues(t, tc.code, api.GetErrorCode())
		require.Equal(t, tc.field, response.Param)
		require.True(t, types.IsSkipRetryError(api))
		require.Contains(t, logs.String(), "fixture-request-42")
		require.Contains(t, logs.String(), "stage="+tc.stage+" rule="+tc.logRule)
		require.NotContains(t, logs.String(), secret)
		require.NotContains(t, response.Message, secret)
	}
}
