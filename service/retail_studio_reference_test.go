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
