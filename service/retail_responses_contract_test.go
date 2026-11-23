package service

import (
	"github.com/QuantumNous/new-api/pkg/retailprice"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"testing"
)

// TestRetailResponsesContract ensures hard caps fail for JSON and SSE before any output,
// while an unrelated supplier or model can still implement the official parameter.
func TestRetailResponsesContract(t *testing.T) {
	b := retailprice.Book{SiteFeeds: map[int]retailprice.SiteRateFeed{9: {URL: "https://aixingxing.pro/api/public/rates"}}, RateSiteByChannel: map[int]int{4: 9}, CredentialGroups: map[int]string{4: "GPT-UltraFast"}, Products: []retailprice.Product{{ID: "sale", Routes: []retailprice.Route{{ChannelID: 4, Models: map[string]string{"alias": "gpt-6.2-astra"}}}}}}
	require.True(t, limitedResponsesRoute(b, 4, "sale", "alias"))
	require.False(t, limitedResponsesRoute(b, 4, "sale", "other"))
	b.SiteFeeds[9] = retailprice.SiteRateFeed{URL: "https://other.example/rates"}
	require.False(t, limitedResponsesRoute(b, 4, "sale", "alias"))
	for field, value := range map[string]string{"max_output_tokens": "16", "max_tool_calls": "1", "temperature": "0.3", "top_p": "0.4", "metadata": `{"tag":"a"}`, "top_logprobs": "2", "parallel_tool_calls": "false", "truncation": `"auto"`} {
		for _, stream := range []string{"true", "false"} {
			body := []byte(`{"tools":[{"type":"function","name":"lookup"}],"stream":` + stream + `,"` + field + `":` + value + `}`)
			e := validateResponsesContract(body, true)
			require.NotNil(t, e, field)
			require.Equal(t, 400, e.StatusCode)
			require.Equal(t, field, e.ToOpenAIError().Param)
			require.Nil(t, validateResponsesContract(body, false))
		}
	}
	for _, raw := range []string{`{"parallel_tool_calls":false}`, `{"tools":[],"parallel_tool_calls":false}`} {
		require.Nil(t, validateResponsesContract([]byte(raw), true))
	}
	require.Nil(t, validateResponsesContract([]byte(`{"reasoning":{"effort":"low"},"tools":[{"type":"web_search"}],"temperature":1,"metadata":{},"parallel_tool_calls":true}`), true))
}

// TestResponsesEffortInUsageLog exercises terminal SSE observation and public log projection,
// including omitted effort and a provider that reports a different value than requested.
func TestResponsesEffortInUsageLog(t *testing.T) {
	for _, tc := range []struct{ sent, returned, want, source string }{{"", "medium", "medium", "upstream"}, {"low", "low", "low", "upstream"}, {"low", "high", "high", "upstream"}, {"low", "", "low", "request"}, {"", "", "", ""}} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
		info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{}, ReasoningEffort: tc.sent}
		a := NewResponsesUsageAccumulator(info)
		a.Observe(&dto.ResponsesStreamResponse{Type: "response.completed", Response: &dto.OpenAIResponsesResponse{Reasoning: &dto.Reasoning{Effort: tc.returned}}}, nil)
		other := GenerateTextOtherInfo(c, info, 0, 0, 0, 0, 0, 0, 0).Snapshot()
		if tc.want == "" {
			require.NotContains(t, other, "reasoning_effort")
			continue
		}
		require.Equal(t, tc.want, other["reasoning_effort"])
		require.Equal(t, tc.source, other["reasoning_effort_source"])
	}
}
