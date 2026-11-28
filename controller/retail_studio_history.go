package controller

import (
	"bufio"
	"context"
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
const studioHistoryRetention = 30 * 24 * time.Hour

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
	Video     string `json:"video,omitempty"`
	Reference string `json:"reference,omitempty"`
}
type studioHistoryRecord struct {
	Running   bool                 `json:"running,omitempty"`
	ID        string               `json:"id"`
	Product   string               `json:"product"`
	Mode      string               `json:"mode"`
	UpdatedAt int64                `json:"updatedAt"`
	Entries   []studioHistoryEntry `json:"entries"`
}

// studioHistoryRoot resolves the same persistent volume for API handlers and background cleanup.
func studioHistoryRoot() string {
	if root := os.Getenv("STUDIO_HISTORY_DIR"); root != "" {
		return root
	}
	dbfile, _, _ := strings.Cut(common.SQLitePath, "?")
	return filepath.Join(filepath.Dir(dbfile), "studio-history")
}

// studioOwnerDir uses only the authenticated identity to select the private directory.
func studioOwnerDir(c *gin.Context) string {
	return filepath.Join(studioHistoryRoot(), strconv.Itoa(c.GetInt("id")))
}

// readStudioHistoryHead leaves the reader at the payload and uses the saved timestamp even on older files.
func readStudioHistoryHead(r *bufio.Reader) (studioHistoryHead, error) {
	var head studioHistoryHead
	line, err := r.ReadBytes('\n')
	if err == nil {
		err = common.Unmarshal(line, &head)
	}
	return head, err
}

// expired uses the last content update. Looking at a record never extends retention.
func (head studioHistoryHead) expired(now time.Time) bool {
	return head.UpdatedAt <= now.Add(-studioHistoryRetention).UnixMilli()
}

// studioRecordExpired 保持待核账视频可见且可恢复，防止正常保留期结束后用户无法追查预留金额。
func studioRecordExpired(head studioHistoryHead, key string, now time.Time) bool {
	return head.expired(now) && !(head.Mode == "video" && studioVideoAccountingPending(key))
}

// purgeStudioOwner drops expired records and abandoned uploads even for accounts that never sign in again.
func purgeStudioOwner(dir string, owner int, now time.Time) error {
	lock := &historyLocks[owner%len(historyLocks)]
	lock.Lock()
	defer lock.Unlock()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var firstErr error
	for _, item := range entries {
		path := filepath.Join(dir, item.Name())
		var expired bool
		var err error
		if item.Type().IsRegular() && strings.HasSuffix(item.Name(), ".record") {
			var f *os.File
			f, err = os.Open(path)
			if err == nil {
				var head studioHistoryHead
				head, err = readStudioHistoryHead(bufio.NewReader(f))
				f.Close()
				expired = err == nil && studioRecordExpired(head, strings.TrimSuffix(path, ".record"), now)
			}
		} else if item.Type().IsRegular() && strings.HasSuffix(item.Name(), ".run") {
			var info os.FileInfo
			info, err = item.Info()
			expired = err == nil && now.Sub(info.ModTime()) >= studioHistoryRetention
		} else if item.Type().IsRegular() && (studioImageRole(strings.TrimPrefix(filepath.Ext(item.Name()), ".")) || strings.HasPrefix(filepath.Ext(item.Name()), ".chat-")) {
			key := strings.TrimSuffix(path, filepath.Ext(path))
			_, statErr := os.Stat(key + ".record")
			expired = errors.Is(statErr, os.ErrNotExist)
		} else if item.Type().IsRegular() && strings.HasSuffix(item.Name(), ".part") {
			var info os.FileInfo
			info, err = item.Info()
			expired = err == nil && now.Sub(info.ModTime()) >= time.Hour
		}
		// A failed read is retried on the next sweep; do not destroy an unreadable record.
		if expired {
			if strings.HasSuffix(path, ".record") {
				err = deleteStudioRecord(strings.TrimSuffix(path, ".record"))
			} else {
				err = removeStudioMedia(path)
			}
		}
		if firstErr == nil && err != nil {
			firstErr = err
		}
	}
	return firstErr
}

// RunStudioHistoryCleanup sweeps at startup and hourly. API reads enforce expiry between sweeps.
func RunStudioHistoryCleanup(ctx context.Context) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		root := studioHistoryRoot()
		accounts, err := os.ReadDir(root)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			common.SysError("Unable to scan studio history storage")
		}
		for _, account := range accounts {
			if ctx.Err() != nil {
				return
			}
			owner, err := strconv.Atoi(account.Name())
			if err != nil || owner <= 0 || !account.IsDir() || strconv.Itoa(owner) != account.Name() {
				continue
			}
			if purgeStudioOwner(filepath.Join(root, account.Name()), owner, time.Now()) != nil {
				common.SysError("Unable to clean up studio history storage")
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
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
		head, err := readStudioHistoryHead(bufio.NewReader(f))
		f.Close()
		if err != nil {
			historyError(c, 500)
			return
		}
		if studioRecordExpired(head, filepath.Join(dir, strings.TrimSuffix(item.Name(), ".record")), time.Now()) {
			continue
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
	head, err := readStudioHistoryHead(r)
	if err != nil {
		historyError(c, 500)
		return
	}
	if studioRecordExpired(head, filepath.Join(studioOwnerDir(c), id), time.Now()) {
		historyError(c, 404)
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
	if _, active := studioRuns.Load(filepath.Join(studioOwnerDir(c), id)); active {
		historyError(c, 409)
		return
	}
	err := deleteStudioRecord(filepath.Join(studioOwnerDir(c), id))
	if errors.Is(err, errStudioVideoAccountingPending) {
		historyError(c, 409)
		return
	}
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
	now := time.Now()
	row.UpdatedAt = min(row.UpdatedAt, now.UnixMilli())
	if row.UpdatedAt <= now.Add(-studioHistoryRetention).UnixMilli() {
		historyError(c, 410)
		return
	}
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
	// Browser history imports cannot create a pending job.
	row.Running = false
	if _, err := os.Stat(filepath.Join(dir, id+".run")); err == nil {
		historyError(c, 409)
		return
	}
	if _, active := studioRuns.Load(filepath.Join(dir, parent)); parent != "" && active {
		historyError(c, 409)
		return
	}
	if err := storeStudioRecord(dir, &row); err != nil {
		historyError(c, 500)
		return
	}
	if parent != "" {
		err = deleteStudioRecord(filepath.Join(dir, parent))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			historyError(c, 500)
			return
		}
	}
	c.JSON(200, gin.H{"success": true})
}

// storeStudioRecord commits one typed snapshot atomically while the caller holds the owner lock.
func storeStudioRecord(dir string, row *studioHistoryRecord) error {
	title := row.Entries[0].Prompt
	if utf8.RuneCountInString(title) > 100 {
		title = string([]rune(title)[:100])
	}
	// Store only typed fields. Re-encoding removes any unrecognized client-supplied content.
	payload, err := common.Marshal(row)
	if err != nil {
		return errors.New("cannot save studio record")
	}
	head, err := common.Marshal(studioHistoryHead{row.ID, row.Product, row.Mode, row.UpdatedAt, title})
	if err != nil {
		return errors.New("cannot save studio record")
	}
	dest, err := os.CreateTemp(dir, "*.part")
	if err != nil {
		return errors.New("cannot save studio record")
	}
	defer os.Remove(dest.Name())
	if _, err = dest.Write(append(head, '\n')); err == nil {
		_, err = dest.Write(payload)
	}
	if err == nil {
		err = dest.Sync()
	}
	closeErr := dest.Close()
	if err != nil || closeErr != nil || os.Rename(dest.Name(), filepath.Join(dir, row.ID+".record")) != nil {
		return errors.New("cannot save studio record")
	}
	return nil
}
