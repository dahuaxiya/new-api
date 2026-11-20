package controller

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// TestStudioReferenceByteLimit exercises the real multipart parser at the raw file boundary.
func TestStudioReferenceByteLimit(t *testing.T) {
	for _, delta := range []int{0, 1} {
		ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
		dir := t.TempDir()
		var request bytes.Buffer
		writer := multipart.NewWriter(&request)
		require.NoError(t, writer.WriteField("intent", "{}"))
		image, err := writer.CreateFormFile("image", "original.png")
		require.NoError(t, err)
		const signature = "\x89PNG\r\n\x1a\n"
		_, err = io.WriteString(image, signature)
		require.NoError(t, err)
		_, err = image.Write(make([]byte, service.StudioReferenceLimit+delta-len(signature)))
		require.NoError(t, err)
		require.NoError(t, writer.Close())
		ctx.Request = httptest.NewRequest("POST", "/", &request)
		ctx.Request.Header.Set("Content-Type", writer.FormDataContentType())
		file, err := readStudioIntent(ctx, dir, &studioIntent{})
		if delta == 0 {
			require.NoError(t, err)
			info, err := file.Stat()
			require.NoError(t, err)
			require.EqualValues(t, service.StudioReferenceLimit, info.Size())
			require.NoError(t, file.Close())
		} else {
			require.ErrorAs(t, err, new(*http.MaxBytesError))
			items, err := os.ReadDir(dir)
			require.NoError(t, err)
			require.Empty(t, items, "failed uploads leave no temporary media")
		}
	}
}

// TestStudioMediaExpiry makes attachments disappear with an expired record, while keeping fresh history readable.
func TestStudioMediaExpiry(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	for _, age := range []time.Duration{time.Hour, 31 * 24 * time.Hour} {
		id := uuid.NewString()
		row := studioHistoryRecord{
			ID: id, Mode: "image", Product: "test", UpdatedAt: now.Add(-age).UnixMilli(),
			Entries: []studioHistoryEntry{{ID: 1, Prompt: "test", Image: studioMediaPrefix + id + "/result", Reference: studioMediaPrefix + id + "/reference"}},
		}
		key := filepath.Join(dir, id)
		require.NoError(t, storeStudioImage(key+".result", []byte("\x89PNG\r\n\x1a\n")))
		require.NoError(t, storeStudioImage(key+".reference", []byte("\x89PNG\r\n\x1a\n")))
		require.NoError(t, storeStudioRecord(dir, &row))
		require.NoError(t, purgeStudioOwner(dir, 1, now))
		for _, ext := range []string{".record", ".reference", ".result"} {
			_, err := os.Stat(key + ext)
			if age > studioHistoryRetention {
				require.ErrorIs(t, err, os.ErrNotExist)
			} else {
				require.NoError(t, err)
			}
		}
	}
}
