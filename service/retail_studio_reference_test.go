package service

import (
	"encoding/base64"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
)

// TestStudioReferenceJSONCompatibility verifies the configured JSON-only channel receives the same original bytes.
func TestStudioReferenceJSONCompatibility(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reference")
	image := []byte("\x89PNG\r\n\x1a\noriginal image")
	require.NoError(t, os.WriteFile(path, image, 0600))
	raw := []byte(`{"model":"gpt-image-2","prompt":"the \"tree\"","quality":"high","n":1,"stream":false}`)
	req := httptest.NewRequest("POST", "/v1/images/edits", strings.NewReader(string(raw)))
	require.NoError(t, attachStudioReference(req, raw, path, true))
	defer req.Body.Close()
	payload, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	require.EqualValues(t, len(payload), req.ContentLength)
	require.Equal(t, "application/json", req.Header.Get("Content-Type"))
	var decoded struct {
		Prompt string
		Images []struct {
			URL string `json:"image_url"`
		}
	}
	require.NoError(t, common.Unmarshal(payload, &decoded))
	require.Equal(t, "the \"tree\"", decoded.Prompt)
	require.Len(t, decoded.Images, 1)
	require.Equal(t, "data:image/png;base64,"+base64.StdEncoding.EncodeToString(image), decoded.Images[0].URL)
}

// TestRetailToolOutputPictures 验证旧会话中的工具截图通过两端校验，外链及替换后的图片仍被拒绝。
func TestRetailToolOutputPictures(t *testing.T) {
	const png = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jRZkAAAAASUVORK5CYII="
	for _, kind := range []string{"function_call_output", "custom_tool_call_output"} {
		raw := []byte(`{"input":[{"type":"` + kind + `","output":[{"type":"input_image","image_url":"` + png + `"}]}]}`)
		require.Error(t, validateRetailChat(raw, nil, true, ""))
		imgs, err := retailResponsesPictures(raw)
		require.NoError(t, err)
		require.Equal(t, map[int]string{0: png}, imgs)
		require.NoError(t, validateRetailChat(raw, map[string]int{png: 1}, true, ""))
		external := []byte(strings.ReplaceAll(string(raw), png, "https://example.com/image.png"))
		_, err = retailResponsesPictures(external)
		require.Error(t, err)
		require.Error(t, validateRetailChat(external, map[string]int{png: 1}, true, ""))
	}
}
