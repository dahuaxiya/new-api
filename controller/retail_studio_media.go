package controller

import (
	"bufio"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/shirou/gopsutil/disk"
)

const studioMediaPrefix = "/api/retail/studio/media/"

var errStudioMedia = errors.New("invalid studio image")
var errStudioDisk = errors.New("insufficient studio storage")

// readStudioIntent bounds multipart metadata separately and spools the single image to the account volume.
func readStudioIntent(c *gin.Context, dir string, in *studioIntent) (*os.File, error) {
	if c.ContentType() != "multipart/form-data" {
		return nil, common.DecodeJson(http.MaxBytesReader(c.Writer, c.Request.Body, 8<<20), in)
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, service.StudioReferenceLimit+(1<<20))
	parts, err := c.Request.MultipartReader()
	if err != nil {
		return nil, err
	}
	metadata, err := parts.NextPart()
	if err != nil || metadata.FormName() != "intent" || metadata.FileName() != "" {
		return nil, errStudioMedia
	}
	raw, err := io.ReadAll(io.LimitReader(metadata, (512<<10)+1))
	if err != nil || len(raw) > 512<<10 || common.Unmarshal(raw, in) != nil {
		return nil, errStudioMedia
	}
	image, err := parts.NextPart()
	if err != nil || image.FormName() != "image" || image.FileName() == "" {
		return nil, errStudioMedia
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	// Do not spool a large upload on a volume already close to the history reserve.
	space, err := disk.Usage(dir)
	if err != nil || space.Free < 512<<20 {
		return nil, errStudioDisk
	}
	f, err := os.CreateTemp(dir, "reference-*.part")
	if err != nil {
		return nil, err
	}
	// Spooling avoids allocating 50 MiB per connection; charge only after a complete, valid upload.
	size, err := io.Copy(f, io.LimitReader(image, service.StudioReferenceLimit+1))
	if err == nil && size > service.StudioReferenceLimit {
		err = &http.MaxBytesError{Limit: service.StudioReferenceLimit}
	}
	if err == nil {
		_, err = parts.NextPart()
		if err == nil {
			err = errStudioMedia
		} else if errors.Is(err, io.EOF) {
			_, err = studioMediaType(f)
		}
	}
	if err != nil {
		f.Close()
		os.Remove(f.Name())
		return nil, err
	}
	return f, nil
}

// studioMediaType checks the bytes at the start of a seekable file without moving its cursor.
func studioMediaType(f *os.File) (string, error) {
	var signature [512]byte
	n, err := f.ReadAt(signature[:], 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	mimeType := http.DetectContentType(signature[:n])
	if mimeType != "image/png" && mimeType != "image/jpeg" && mimeType != "image/webp" {
		return "", errStudioMedia
	}
	return mimeType, nil
}

// storeStudioImage commits decoded raster bytes before a history snapshot references them.
func storeStudioImage(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), "result-*.part")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), path)
}

// deleteStudioRecord hides a record first and then cleans up its attachments. The sweep retries orphan cleanup.
func deleteStudioRecord(key string) error {
	if err := os.Remove(key + ".record"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return errors.Join(removeStudioMedia(key+".reference"), removeStudioMedia(key+".result"))
}

// removeStudioMedia makes repeated cleanup safe after deletion or an interrupted write.
func removeStudioMedia(path string) error {
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// ReadStudioMedia serves a fixed role under the logged-in owner's unexpired record.
func ReadStudioMedia(c *gin.Context) {
	id, role := c.Param("id"), c.Param("role")
	if !historyID(id) || (role != "reference" && role != "result") {
		historyError(c, 404)
		return
	}
	key := filepath.Join(studioOwnerDir(c), id)
	record, err := os.Open(key + ".record")
	if err != nil {
		historyError(c, 404)
		return
	}
	head, err := readStudioHistoryHead(bufio.NewReader(record))
	record.Close()
	if err != nil || head.expired(time.Now()) || head.Mode != "image" {
		historyError(c, 404)
		return
	}
	f, err := os.Open(key + "." + role)
	if err != nil {
		historyError(c, 404)
		return
	}
	defer f.Close()
	mimeType, err := studioMediaType(f)
	if err != nil {
		historyError(c, 404)
		return
	}
	ext := strings.TrimPrefix(mimeType, "image/")
	c.Header("Content-Type", mimeType)
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Content-Disposition", mime.FormatMediaType("inline", map[string]string{"filename": role + "." + ext}))
	http.ServeContent(c.Writer, c.Request, role+"."+ext, time.Time{}, f)
}
