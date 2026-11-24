package controller

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// TestRetailUtilityCompaction 放行可重放的原生压缩结果，并拒绝缺少密文或用量的响应。
func TestRetailUtilityCompaction(t *testing.T) {
	body, kept, err := retailResponsesUtilityRequest(gjson.Parse(`{"model":"astra","input":[{"type":"message","role":"user","content":"hello"},{"role":"assistant","content":"hi"}]}`), "compact")
	require.NoError(t, err)
	require.Len(t, kept, 1)
	require.Equal(t, "compaction_trigger", gjson.GetBytes(body, "input.2.type").Str)
	require.True(t, gjson.GetBytes(body, "stream").Bool())
	good := `{"id":"resp_01","status":"completed","usage":{"input_tokens":12,"output_tokens":5},"output":[{"type":"compaction","encrypted_content":"sealed"}]}`
	result, err := retailResponsesUtilityResponse(gjson.Parse(good), "compact", kept)
	require.NoError(t, err)
	require.Equal(t, "response.compaction", result["object"])
	for _, bad := range []string{`{"status":"completed","usage":{"input_tokens":12}}`, `{"status":"completed","output":[{"type":"compaction","encrypted_content":"sealed"}]}`} {
		_, err := retailResponsesUtilityResponse(gjson.Parse(bad), "compact", kept)
		require.Error(t, err)
	}
}

// TestRetailUtilitySearch 只允许单次可计价的搜索，并要求实际完成的调用带有来源。
func TestRetailUtilitySearch(t *testing.T) {
	body, _, err := retailResponsesUtilityRequest(gjson.Parse(`{"model":"astra","commands":{"search_query":[{"q":"Python docs","domains":[]}]}}`), "search")
	require.NoError(t, err)
	require.Equal(t, "web_search", gjson.GetBytes(body, "tools.0.type").Str)
	require.Equal(t, "required", gjson.GetBytes(body, "tool_choice").Str)
	for _, bad := range []string{
		`{"model":"astra","model":"sol","commands":{"search_query":[{"q":"Python docs"}]}}`,
		`{"model":"astra","commands":{"search_query":[{"q":"Python docs"},{"q":"Go docs"}]}}`,
		`{"model":"astra","commands":{"search_query":[{"q":"Python docs"}],"open":[]}}`,
		`{"model":"astra","commands":{"search_query":[{"q":"Python docs"}]},"settings":{"external_web_access":false}}`,
	} {
		_, _, err := retailResponsesUtilityRequest(gjson.Parse(bad), "search")
		require.Error(t, err)
	}
	res, err := retailResponsesUtilityResponse(gjson.Parse(`{"status":"completed","usage":{"input_tokens":10},"output":[{"type":"web_search_call","status":"completed","action":{"sources":[{"url":"https://docs.python.org/"},{"url":"file:///etc/hosts"}]}}]}`), "search", nil)
	require.NoError(t, err)
	require.Equal(t, "https://docs.python.org/", res["output"])
}
