package controller

import (
	"bytes"
	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// TestStudioHistoryRoundTrip exercises the same chunk/list/detail contract used by browsers across logins.
func TestStudioHistoryRoundTrip(t *testing.T) {
	t.Setenv("STUDIO_HISTORY_DIR", t.TempDir())
	request := func(owner int, method, path string, body []byte) *httptest.ResponseRecorder {
		// Each request gets a fresh router so reads cannot depend on a previous handler's in-memory state.
		engine := gin.New()
		engine.Use(func(c *gin.Context) { c.Set("id", owner) })
		engine.GET("/history", ListStudioHistory)
		engine.GET("/history/:id", ReadStudioHistory)
		engine.PUT("/history/:id", PutStudioHistory)
		engine.DELETE("/history/:id", DeleteStudioHistory)
		response := httptest.NewRecorder()
		engine.ServeHTTP(response, httptest.NewRequest(method, path, bytes.NewReader(body)))
		return response
	}
	id, parent := uuid.NewString(), uuid.NewString()
	image := "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aD1sAAAAASUVORK5CYII="
	row := studioHistoryRecord{ID: parent, Product: "group", Mode: "text", UpdatedAt: time.Now().UnixMilli(), Entries: []studioHistoryEntry{{ID: 1, Model: "chat", Prompt: "题目", Content: "reply", Complete: true}}}
	raw, err := common.Marshal(row)
	require.NoError(t, err)
	path := "/history/" + parent + "?owner=1&upload=" + uuid.NewString() + "&offset=0&last=1"
	require.Equal(t, 200, request(1, "PUT", path, raw).Code)
	require.Equal(t, 403, request(2, "PUT", path, raw).Code)
	require.Equal(t, 404, request(2, "GET", "/history/"+parent, nil).Code)
	require.Equal(t, 403, request(2, "DELETE", "/history/"+parent+"?owner=1", nil).Code)
	require.Equal(t, 200, request(1, "GET", "/history/"+parent, nil).Code)
	require.NotContains(t, request(2, "GET", "/history", nil).Body.String(), "题目")

	row.ID = id
	row.Mode = "image"
	row.Entries[0].Image = image
	row.Entries[0].Reference = image
	raw, err = common.Marshal(row)
	require.NoError(t, err)
	path = "/history/" + id + "?owner=1&upload=" + uuid.NewString() + "&parent=" + parent
	split := len(raw) / 2
	require.Equal(t, 409, request(1, "PUT", path+"&offset=2", raw[:split]).Code)
	require.Equal(t, 200, request(1, "PUT", path+"&offset=0", raw[:split]).Code)
	require.Equal(t, 404, request(1, "GET", "/history/"+id, nil).Code)
	require.Equal(t, 200, request(1, "PUT", path+"&offset="+strconv.Itoa(split)+"&last=1", raw[split:]).Code)
	require.Equal(t, 404, request(1, "GET", "/history/"+parent, nil).Code)
	require.JSONEq(t, string(raw), request(1, "GET", "/history/"+id, nil).Body.String())
	list := request(1, "GET", "/history", nil).Body.String()
	require.Contains(t, list, "题目")
	require.NotContains(t, list, "base64")
	require.Equal(t, 404, request(1, "GET", "/history/not-a-uuid", nil).Code)
	require.Equal(t, 200, request(1, "DELETE", "/history/"+id+"?owner=1", nil).Code)
	require.Equal(t, 404, request(1, "GET", "/history/"+id, nil).Code)
	// An old import cannot renew retention; simulate an existing file written by the previous release.
	now := time.Now()
	row.UpdatedAt = now.Add(-studioHistoryRetention).UnixMilli()
	raw, err = common.Marshal(row)
	require.NoError(t, err)
	require.Equal(t, 410, request(1, "PUT", "/history/"+id+"?owner=1&upload="+uuid.NewString()+"&offset=0&last=1", raw).Code)
	dir := filepath.Join(os.Getenv("STUDIO_HISTORY_DIR"), "1")
	for _, mode := range []string{"text", "image"} {
		expired := studioHistoryHead{ID: uuid.NewString(), Mode: mode, UpdatedAt: row.UpdatedAt, Title: "expired"}
		recent := expired
		recent.ID = uuid.NewString()
		recent.UpdatedAt = now.Add(-studioHistoryRetention + time.Hour).UnixMilli()
		for _, head := range []studioHistoryHead{expired, recent} {
			header, err := common.Marshal(head)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(dir, head.ID+".record"), append(header, '\n', '{', '}'), 0600))
		}
		require.NotContains(t, request(1, "GET", "/history", nil).Body.String(), expired.ID)
		require.Equal(t, 404, request(1, "GET", "/history/"+expired.ID, nil).Code)
		require.Equal(t, 200, request(1, "GET", "/history/"+recent.ID, nil).Code)
		require.NoError(t, purgeStudioOwner(dir, 1, now))
		require.NoFileExists(t, filepath.Join(dir, expired.ID+".record"))
		require.FileExists(t, filepath.Join(dir, recent.ID+".record"))
	}

}
