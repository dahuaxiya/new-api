package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestRetailGeminiContract 保护兼容入口的单候选文本边界，避免转换时静默丢弃购量相关字段。
func TestRetailGeminiContract(t *testing.T) {
	const contents = `"contents":[{"role":"user","parts":[{"text":"hello"}]}]`
	require.NoError(t, validateRetailGeminiRequest([]byte(`{"model":"astra",`+contents+`,"generationConfig":{"maxOutputTokens":24,"candidateCount":1}}`), "astra"))
	for _, bad := range []string{
		`{"model":"different",` + contents + `}`,
		`{` + contents + `,"generationConfig":{"candidateCount":2}}`,
		`{` + contents + `,"cachedContent":"cachedContents/other"}`,
		`{"contents":[{"role":"user","parts":[{"fileData":{"fileUri":"https://example.com/test"}}]}]}`,
		`{` + contents + `,"tools":[{"googleSearch":{}}]}`,
	} {
		require.Error(t, validateRetailGeminiRequest([]byte(bad), "astra"))
	}
}
