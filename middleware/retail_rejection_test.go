package middleware

import (
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// TestRetailNormalizationFailureReportsField exercises the history normalizer and middleware JSON
// writer together. A broken saved tool result must not expose its data or hide the request ID.
func TestRetailNormalizationFailureReportsField(t *testing.T) {
	require.NoError(t, i18n.Init())
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	c.Set(common.RequestIdKey, "fixture-normalize")
	err := service.PrepareRetailResponsesInput(c, []byte(`[{"type":"image_generation_call","status":"completed","result":"not-base64-secret!"}]`))
	require.Error(t, err)
	api := service.RetailResponsesInputError(c, err)
	abortWithOpenAiError(c, api.StatusCode, api.ToOpenAIError())
	require.True(t, c.IsAborted())
	require.Equal(t, 400, w.Code)
	body := w.Body.String()
	require.Equal(t, "input.0.result", gjson.Get(body, "error.param").Str)
	require.Equal(t, "invalid_value", gjson.Get(body, "error.code").Str)
	require.Contains(t, body, "fixture-normalize")
	require.NotContains(t, body, "not-base64-secret")
}
