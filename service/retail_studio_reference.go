package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/retailprice"
)

// StudioReferenceLimit bounds the raw bytes independently of multipart headers.
const StudioReferenceLimit = 50 << 20

type studioReferenceKey struct{}

// WithStudioReference carries a server-owned file through the internal relay; no HTTP header can set it.
func WithStudioReference(ctx context.Context, path string) context.Context {
	return context.WithValue(ctx, studioReferenceKey{}, path)
}

// studioReferencePath returns the attachment selected by the durable job, if any.
func studioReferencePath(r *http.Request, editing bool) string {
	if !editing {
		return ""
	}
	path, _ := r.Context().Value(studioReferenceKey{}).(string)
	return path
}

type studioMultipartBody struct {
	io.Reader
	file *os.File
}

// Close releases the file even when the supplier rejects the request before reading the entire body.
func (body *studioMultipartBody) Close() error { return body.file.Close() }

// attachStudioReference transforms the already-validated post-override body into an official image edit.
func attachStudioReference(req *http.Request, raw []byte, path string, jsonOnly bool) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() {
		if f != nil {
			f.Close()
		}
	}()
	stat, err := f.Stat()
	if err != nil || !stat.Mode().IsRegular() || stat.Size() <= 0 || stat.Size() > StudioReferenceLimit {
		return retailprice.ErrConfig
	}
	var prefix [512]byte
	n, err := f.ReadAt(prefix[:], 0)
	if err != nil && err != io.EOF {
		return err
	}
	mimeType := http.DetectContentType(prefix[:n])
	if mimeType != "image/png" && mimeType != "image/jpeg" && mimeType != "image/webp" {
		return retailprice.ErrConfig
	}
	var fields map[string]common.RawMessage
	if common.Unmarshal(raw, &fields) != nil {
		return retailprice.ErrConfig
	}
	if jsonOnly {
		// A channel that requires JSON is configured before dispatch. Never retry a charged request in another format.
		payload := bytes.TrimSpace(raw)
		prefix := append(bytes.Clone(payload[:len(payload)-1]), []byte(`,"images":[{"image_url":"data:`+mimeType+`;base64,`)...)
		const suffix = `"}]}`
		reader, writer := io.Pipe()
		// Pipe backpressure keeps even the JSON fallback bounded. Transport closure unblocks the encoder.
		go writeStudioJSONReference(writer, f, stat.Size(), prefix, suffix)
		f = nil
		req.Body.Close()
		req.Body = reader
		req.ContentLength = int64(len(prefix)+len(suffix)) + int64(base64.StdEncoding.EncodedLen(int(stat.Size())))
		req.Header.Set("Content-Type", "application/json")
	} else {
		var form bytes.Buffer
		writer := multipart.NewWriter(&form)
		for name, value := range fields {
			var text string
			if common.Unmarshal(value, &text) != nil {
				text = string(value)
			}
			if err := writer.WriteField(name, text); err != nil {
				return err
			}
		}
		partHeaders := make(textproto.MIMEHeader)
		partHeaders.Set("Content-Disposition", `form-data; name="image[]"; filename="reference.`+strings.TrimPrefix(mimeType, "image/")+`"`)
		partHeaders.Set("Content-Type", mimeType)
		if _, err := writer.CreatePart(partHeaders); err != nil {
			return err
		}
		// Keep the framing in memory and stream the saved original between the two boundaries.
		header := bytes.Clone(form.Bytes())
		form.Reset()
		if err := writer.Close(); err != nil {
			return err
		}
		tail := form.Bytes()
		req.Body.Close()
		req.Body = &studioMultipartBody{Reader: io.MultiReader(bytes.NewReader(header), io.LimitReader(f, stat.Size()), bytes.NewReader(tail)), file: f}
		f = nil
		req.ContentLength = int64(len(header)+len(tail)) + stat.Size()
		req.Header.Set("Content-Type", writer.FormDataContentType())
	}
	req.Header.Set("Content-Length", strconv.FormatInt(req.ContentLength, 10))
	req.GetBody = nil
	return nil
}

// writeStudioJSONReference emits the already validated metadata around a streamed Base64 file.
func writeStudioJSONReference(writer *io.PipeWriter, file *os.File, size int64, prefix []byte, suffix string) {
	defer file.Close()
	_, err := writer.Write(prefix)
	if err == nil {
		encoder := base64.NewEncoder(base64.StdEncoding, writer)
		_, err = io.CopyN(encoder, file, size)
		if err == nil {
			err = encoder.Close()
		}
	}
	if err == nil {
		_, err = io.WriteString(writer, suffix)
	}
	writer.CloseWithError(err)
}

type studioVisionKey struct{}

// WithStudioVision binds validated inline images to the internal chat request. A counted
// multiset supports repeated copies of the same image and survives provider conversion.
func WithStudioVision(ctx context.Context, pictures map[int]string) context.Context {
	allowed := make(map[string]int, len(pictures))
	for _, uri := range pictures {
		allowed[uri]++
	}
	return context.WithValue(ctx, studioVisionKey{}, allowed)
}

// HasStudioVision reports whether this server-generated request contains verified chat images.
func HasStudioVision(req *http.Request) bool {
	if req == nil {
		return false
	}
	images, _ := req.Context().Value(studioVisionKey{}).(map[string]int)
	return len(images) > 0
}
