package service

import (
	"encoding/base64"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// TestRetailCodexHistory 验证回放顺序、图片许可和工具计费边界，并检查重试是否保持同一请求体。
func TestRetailCodexHistory(t *testing.T) {
	const png = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=="
	const svg = `<svg xmlns="http://www.w3.org/2000/svg"><rect width="4"/></svg>`
	input := `[{"type":"additional_tools","role":"developer","tools":[{"type":"image_generation"}]},{"type":"image_generation_call","status":"completed","result":"` + base64.StdEncoding.EncodeToString([]byte(svg)) + `","revised_prompt":"square"},{"type":"image_generation_call","status":"completed","result":"` + png + `"},{"role":"user","content":"continue"}]`
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"test","tools":[],"input":`+input+`}`))
	t.Cleanup(func() { common.CleanupBodyStorage(c) })
	require.NoError(t, PrepareRetailResponsesInput(c, []byte(input)))
	storage, err := common.GetBodyStorage(c)
	require.NoError(t, err)
	raw, err := storage.Bytes()
	require.NoError(t, err)
	root := gjson.ParseBytes(raw)
	require.Equal(t, "[]", root.Get("tools").Raw)
	require.False(t, root.Get("input.0.type").Exists())
	require.Contains(t, root.Get("input.0.content").Str, "image_generation")
	require.Contains(t, root.Get("input.1.content").Str, svg)
	require.Contains(t, root.Get("input.1.content").Str, "square")
	require.Equal(t, "continue", root.Get("input.3.content").Str)
	pictures, err := retailResponsesPictures(raw)
	require.NoError(t, err)
	uri := "data:image/png;base64," + png
	require.Equal(t, map[int]string{0: uri}, pictures)
	require.NoError(t, validateRetailChat(raw, map[string]int{uri: 1}, true, ""))
	limit, err := retailToolLimit(raw, "/v1/responses", "image_generation")
	require.NoError(t, err)
	require.Zero(t, limit)
	require.NoError(t, PrepareRetailResponsesInput(c, []byte(root.Get("input").Raw)))
	again, err := common.GetBodyStorage(c)
	require.NoError(t, err)
	require.Same(t, storage, again)
}

// TestRetailMagpieSummary 只回放按顺序提供的可读摘要，不能把其他私有字段带到下一家供应商。
func TestRetailMagpieSummary(t *testing.T) {
	item := gjson.Parse(`{"type":"magpie_reasoning","content":null,"id":"private","summary":[{"type":"summary_text","text":"Remember"},{"type":"summary_text","text":"green"}]}`)
	got, err := retailCodexHistoryMessage(item)
	require.NoError(t, err)
	require.JSONEq(t, `{"role":"assistant","content":"Remember\ngreen"}`, string(got))
	for _, bad := range []string{`[]`, `[{"type":"text","text":"private"}]`, `[{"type":"summary_text","text":{}}]`} {
		_, err = retailCodexHistoryMessage(gjson.Parse(`{"type":"magpie_reasoning","summary":` + bad + `}`))
		require.Error(t, err)
	}
}

// TestRetailCodexHistoryRejectsIncompleteResult 验证仅含上游 ID 或远程地址的图片不能冒充自带结果。
func TestRetailCodexHistoryRejectsIncompleteResult(t *testing.T) {
	for _, item := range []string{`{"type":"image_generation_call","id":"ig_other","status":"completed"}`, `{"type":"image_generation_call","status":"completed","result":"https://example.com/pic"}`, `{"type":"image_generation_call","status":"in_progress","result":"dGVzdA=="}`} {
		_, err := retailCodexHistoryMessage(gjson.Parse(item))
		require.Error(t, err)
	}
}
