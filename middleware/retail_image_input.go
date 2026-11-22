package middleware

import (
	"encoding/base64"

	"io"
	"net/http"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/pkg/retailprice"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

// RetailImageInput brings ordinary Key uploads into the same bounded JSON representation used for quoting and relay.
// It runs after authentication and rate limiting, before any downstream middleware reads the body.
func RetailImageInput() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !c.GetBool("retail_request") {
			c.Next()
			return
		}
		c.Set("retail_public_image", true)
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 8<<20)
		switch {
		case c.ContentType() == gin.MIMEJSON:
		case c.Request.URL.Path == "/v1/images/edits" && c.ContentType() == gin.MIMEMultipartPOSTForm:
			if err := normalizeRetailEditForm(c); err != nil {
				abortWithOpenAiMessage(c, http.StatusBadRequest, i18n.T(c, i18n.MsgRetailInvalidRequest))
				return
			}
		default:
			abortWithOpenAiMessage(c, http.StatusUnsupportedMediaType, i18n.T(c, i18n.MsgRetailInvalidRequest))
			return
		}
		c.Next()
	}
}

// normalizeRetailEditForm preserves upload order and typed form fields. The JSON validator applies the same tariff limits to both encodings.
func normalizeRetailEditForm(c *gin.Context) error {
	reader, err := c.Request.MultipartReader()
	if err != nil {
		return err
	}
	fields := make(map[string]any)
	var images []map[string]string
	remaining := int64(service.RetailReferenceBytes)
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		name := part.FormName()
		if part.FileName() != "" {
			// Only image parts may become inline media. Never open a path supplied by the client.
			if (name != "image" && name != "image[]") || len(images) >= service.RetailMaxReferences {
				return retailprice.ErrConfig
			}
			data, err := io.ReadAll(io.LimitReader(part, remaining+1))
			if err != nil || len(data) == 0 || int64(len(data)) > remaining {
				return retailprice.ErrConfig
			}
			remaining -= int64(len(data))
			images = append(images, map[string]string{"image_url": "data:" + http.DetectContentType(data) + ";base64," + base64.StdEncoding.EncodeToString(data)})
			continue
		}
		if _, exists := fields[name]; exists || len(fields) >= 10 {
			return retailprice.ErrConfig
		}
		data, err := io.ReadAll(io.LimitReader(part, 32001))
		if err != nil || len(data) > 32000 {
			return retailprice.ErrConfig
		}
		value := string(data)
		switch name {
		case "model", "prompt", "size", "quality", "output_format", "response_format":
			fields[name] = value
		case "n", "output_compression":
			n, err := strconv.Atoi(value)
			if err != nil {
				return err
			}
			fields[name] = n
		case "stream":
			if value != "false" {
				return retailprice.ErrConfig
			}
			fields[name] = false
		default:
			return retailprice.ErrConfig
		}
	}
	if len(images) == 0 {
		return retailprice.ErrConfig
	}
	fields["images"] = images
	raw, err := common.Marshal(fields)
	if err != nil || len(raw) > 8<<20 {
		return retailprice.ErrConfig
	}
	replacement, err := common.CreateBodyStorage(raw)
	if err != nil {
		return err
	}
	// Downstream readers, including pass-through channels, must see the same bytes as the price guard.
	_ = c.Request.Body.Close()
	common.CleanupBodyStorage(c)
	c.Set(common.KeyBodyStorage, replacement)
	c.Request.Body = io.NopCloser(replacement)
	c.Request.ContentLength = int64(len(raw))
	c.Request.Header.Set("Content-Length", strconv.Itoa(len(raw)))
	c.Request.Header.Set("Content-Type", gin.MIMEJSON)
	return nil
}
