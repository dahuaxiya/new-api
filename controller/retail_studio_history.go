package controller

import (
	"bufio"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/shirou/gopsutil/disk"
)

// Each owner has a private directory on the retail data volume. Chunks stay outside the
// visible index until a complete, validated revision can replace the previous file by rename.
const studioChunk = 2 << 20
const studioRecordLimit = 76 << 20

var historyLocks [64]sync.Mutex

// Final validation temporarily holds the decoded image and its encoded copy in memory.
var historyFinalize sync.Mutex

type studioHistoryHead struct {
	ID        string `json:"id"`
	Product   string `json:"product"`
	Mode      string `json:"mode"`
	UpdatedAt int64  `json:"updatedAt"`
	Title     string `json:"title"`
}
type studioHistoryEntry struct {
	ID        int    `json:"id"`
	Model     string `json:"model"`
	Prompt    string `json:"prompt"`
	Content   string `json:"content"`
	Reasoning string `json:"reasoning"`
	Complete  bool   `json:"complete"`
	Error     string `json:"error,omitempty"`
	Image     string `json:"image,omitempty"`
	Reference string `json:"reference,omitempty"`
}
type studioHistoryRecord struct {
	ID        string               `json:"id"`
	Product   string               `json:"product"`
	Mode      string               `json:"mode"`
	UpdatedAt int64                `json:"updatedAt"`
	Entries   []studioHistoryEntry `json:"entries"`
}

// studioOwnerDir selects storage solely from the authenticated identity. The default lives beside the persistent database.
func studioOwnerDir(c *gin.Context) string {
	root := os.Getenv("STUDIO_HISTORY_DIR")
	if root == "" {
		dbfile, _, _ := strings.Cut(common.SQLitePath, "?")
		root = filepath.Join(filepath.Dir(dbfile), "studio-history")
	}
	return filepath.Join(root, strconv.Itoa(c.GetInt("id")))
}

// historyID accepts canonical UUIDs only. No request path component may name a filesystem path.
func historyID(raw string) bool { id, err := uuid.Parse(raw); return err == nil && id.String() == raw }

// historyError exposes a status without leaking account paths or stored content.
func historyError(c *gin.Context, status int) {
	c.AbortWithStatusJSON(status, gin.H{"success": false, "message": "Studio history unavailable"})
}

// ListStudioHistory reads only compact headers; opening the list never transfers saved images.
func ListStudioHistory(c *gin.Context) {
	dir := studioOwnerDir(c)
	files, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		historyError(c, 500)
		return
	}
	rows := []studioHistoryHead{}
	for _, item := range files {
		if !strings.HasSuffix(item.Name(), ".record") {
			continue
		}
		f, err := os.Open(filepath.Join(dir, item.Name()))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			historyError(c, 500)
			return
		}
		line, err := bufio.NewReader(f).ReadBytes('\n')
		f.Close()
		var head studioHistoryHead
		if err != nil || common.Unmarshal(line, &head) != nil {
			historyError(c, 500)
			return
		}
		rows = append(rows, head)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].UpdatedAt > rows[j].UpdatedAt })
	c.JSON(200, gin.H{"success": true, "data": rows})
}

// ReadStudioHistory returns the owner's saved snapshot without invoking a relay.
func ReadStudioHistory(c *gin.Context) {
	id := c.Param("id")
	if !historyID(id) {
		historyError(c, 404)
		return
	}
	f, err := os.Open(filepath.Join(studioOwnerDir(c), id+".record"))
	if err != nil {
		historyError(c, 404)
		return
	}
	defer f.Close()
	r := bufio.NewReader(f)
	if _, err = r.ReadBytes('\n'); err != nil {
		historyError(c, 500)
		return
	}
	c.Header("Content-Type", "application/json")
	_, _ = io.Copy(c.Writer, r)
}

// DeleteStudioHistory serializes removal with finalization and touches only this owner's record.
func DeleteStudioHistory(c *gin.Context) {
	// Reject an upload queued before a login switch even if transport refreshed its credentials.
	if c.Query("owner") != strconv.Itoa(c.GetInt("id")) || c.GetInt("id") <= 0 {
		historyError(c, 403)
		return
	}
	id := c.Param("id")
	if !historyID(id) {
		historyError(c, 404)
		return
	}
	lock := &historyLocks[c.GetInt("id")%len(historyLocks)]
	lock.Lock()
	defer lock.Unlock()
	err := os.Remove(filepath.Join(studioOwnerDir(c), id+".record"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		historyError(c, 500)
		return
	}
	c.JSON(200, gin.H{"success": true})
}

// safeStudioRaster permits inline raster payloads only; stored history cannot load remote URLs or active markup.
func safeStudioRaster(data string) bool {
	return data == "" || strings.HasPrefix(data, "data:image/png;base64,iVBORw0KGgo") || strings.HasPrefix(data, "data:image/jpeg;base64,/9j/") || strings.HasPrefix(data, "data:image/webp;base64,UklGR")
}

// PutStudioHistory accepts ordered chunks below proxy limits. Offsets permit retries and distinct upload IDs isolate devices.
func PutStudioHistory(c *gin.Context) {
	// Reject an upload queued before a login switch even if transport refreshed its credentials.
	if c.Query("owner") != strconv.Itoa(c.GetInt("id")) || c.GetInt("id") <= 0 {
		historyError(c, 403)
		return
	}
	id, upload := c.Param("id"), c.Query("upload")
	start, err := strconv.ParseInt(c.Query("offset"), 10, 64)
	parent := c.Query("parent")
	if !historyID(id) || !historyID(upload) || err != nil || start < 0 || start > studioRecordLimit || (parent != "" && (!historyID(parent) || parent == id)) {
		historyError(c, 400)
		return
	}
	chunk, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, studioChunk))
	if err != nil || len(chunk) == 0 || start+int64(len(chunk)) > studioRecordLimit {
		historyError(c, 413)
		return
	}
	dir := studioOwnerDir(c)
	lock := &historyLocks[c.GetInt("id")%len(historyLocks)]
	lock.Lock()
	defer lock.Unlock()
	if os.MkdirAll(dir, 0700) != nil {
		historyError(c, 500)
		return
	}
	files, err := os.ReadDir(dir)
	if err != nil {
		historyError(c, 500)
		return
	}
	partName := id + "." + upload + ".part"
	part := filepath.Join(dir, partName)
	var used, existing int64
	var count int
	var staged bool
	for _, file := range files {
		stat, err := file.Info()
		if err != nil {
			historyError(c, 500)
			return
		}
		// A dropped connection leaves a private staging file; reclaim it on the next write after one hour.
		if strings.HasSuffix(file.Name(), ".part") && time.Since(stat.ModTime()) > time.Hour {
			if os.Remove(filepath.Join(dir, file.Name())) == nil {
				continue
			}
		}
		count++
		if file.Name() == partName {
			existing = stat.Size()
			staged = true
		}
		used += stat.Size()
	}
	if start > existing {
		historyError(c, 409)
		return
	}
	growth := max(int64(0), start+int64(len(chunk))-existing)
	if used+growth > 512<<20 || (!staged && count >= 4000) {
		historyError(c, 507)
		return
	}
	// The database shares this volume. Reject a write before it can consume the remaining operating space.
	space, err := disk.Usage(dir)
	if err != nil || space.Free < uint64(512<<20+studioRecordLimit) {
		historyError(c, 507)
		return
	}
	f, err := os.OpenFile(part, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		historyError(c, 500)
		return
	}
	defer f.Close()
	if _, err = f.WriteAt(chunk, start); err != nil {
		historyError(c, 500)
		return
	}
	if c.Query("last") != "1" {
		c.JSON(200, gin.H{"success": true})
		return
	}
	defer os.Remove(part)
	if f.Truncate(start+int64(len(chunk))) != nil {
		historyError(c, 500)
		return
	}
	historyFinalize.Lock()
	defer historyFinalize.Unlock()
	var row studioHistoryRecord
	if common.DecodeJson(f, &row) != nil || row.ID != id || (row.Mode != "text" && row.Mode != "image") || row.Product == "" || len(row.Product) > 256 || len(row.Entries) == 0 || row.UpdatedAt <= 0 {
		historyError(c, 400)
		return
	}
	// Preserve original dates on import and tolerate an ahead-of-time device clock.
	row.UpdatedAt = min(row.UpdatedAt, time.Now().UnixMilli())
	for _, e := range row.Entries {
		if e.Prompt == "" || e.Model == "" || !safeStudioRaster(e.Image) || !safeStudioRaster(e.Reference) || (row.Mode == "text" && (e.Image != "" || e.Reference != "")) {
			historyError(c, 400)
			return
		}
	}
	if row.Mode == "image" && len(row.Entries) != 1 {
		historyError(c, 400)
		return
	}
	title := row.Entries[0].Prompt
	if utf8.RuneCountInString(title) > 100 {
		title = string([]rune(title)[:100])
	}
	// Store only typed fields. Re-encoding removes any unrecognized client-supplied content.
	payload, err := common.Marshal(row)
	if err != nil {
		historyError(c, 500)
		return
	}
	head, err := common.Marshal(studioHistoryHead{id, row.Product, row.Mode, row.UpdatedAt, title})
	if err != nil {
		historyError(c, 500)
		return
	}
	dest, err := os.CreateTemp(dir, "*.part")
	if err != nil {
		historyError(c, 500)
		return
	}
	defer os.Remove(dest.Name())
	if _, err = dest.Write(append(head, '\n')); err == nil {
		_, err = dest.Write(payload)
	}
	if err == nil {
		err = dest.Sync()
	}
	closeErr := dest.Close()
	if err != nil || closeErr != nil || os.Rename(dest.Name(), filepath.Join(dir, id+".record")) != nil {
		historyError(c, 500)
		return
	}
	if parent != "" {
		err = os.Remove(filepath.Join(dir, parent+".record"))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			historyError(c, 500)
			return
		}
	}
	c.JSON(200, gin.H{"success": true})
}
