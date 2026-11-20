package controller

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	_ "image/png"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

// studioChatRole binds one original file to its position in the immutable snapshot.
func studioChatRole(index int) string { return "chat-" + strconv.Itoa(index) }

// Only one large raster is decoded at a time on the shared retail instance.
var studioChatDecode = make(chan struct{}, 1)

// studioChatDataURL keeps small originals intact. For large attachments the supplier gets a
// resized copy; history always retains the original. Check dimensions before allocating pixels.
func studioChatDataURL(file *os.File, budget int) (string, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", errStudioMedia
	}
	cfg, format, err := image.DecodeConfig(file)
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width) > 40_000_000/int64(cfg.Height) {
		return "", errors.New("Unsupported or oversized image dimensions (maximum 40 megapixels).")
	}
	info, err := file.Stat()
	if err != nil {
		return "", errStudioMedia
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return "", errStudioMedia
	}
	var data []byte
	if info.Size() <= int64(budget) {
		data, err = io.ReadAll(io.LimitReader(file, int64(budget)+1))
		if err != nil || len(data) > budget {
			return "", errStudioMedia
		}
	} else {
		studioChatDecode <- struct{}{}
		defer func() { <-studioChatDecode }()
		src, _, err := image.Decode(file)
		if err != nil {
			return "", errStudioMedia
		}
		// Each turn shares an envelope with previous pictures and the converted provider protocol.
		for edge := min(3072, max(cfg.Width, cfg.Height)); edge >= 1; edge /= 2 {
			longest := max(cfg.Width, cfg.Height)
			rect := image.Rect(0, 0, max(1, cfg.Width*edge/longest), max(1, cfg.Height*edge/longest))
			dst := image.NewRGBA(rect)
			draw.Draw(dst, rect, image.NewUniform(color.White), image.Point{}, draw.Src)
			draw.ApproxBiLinear.Scale(dst, rect, src, src.Bounds(), draw.Over, nil)
			var out bytes.Buffer
			if jpeg.Encode(&out, dst, &jpeg.Options{Quality: 88}) != nil {
				return "", errStudioMedia
			}
			if out.Len() <= budget {
				data = out.Bytes()
				format = "jpeg"
				break
			}
		}
		if len(data) == 0 {
			return "", errors.New("Image context is full. Start a new conversation to send more pictures.")
		}
	}
	return "data:image/" + format + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

// studioChatImages reads only this account's unexpired parent and the current multipart upload.
// The caller holds the owner lock while verifying the parent and preparing its replacement.
func studioChatImages(dir string, in *studioIntent, upload *os.File) (map[int]string, error) {
	count := 0
	for _, e := range in.Record.Entries {
		if e.Reference != "" {
			count++
		}
	}
	if count > 0 {
		parent, err := os.Open(filepath.Join(dir, in.Parent+".record"))
		if err != nil {
			return nil, errors.New("The previous conversation is no longer available.")
		}
		head, err := readStudioHistoryHead(bufio.NewReader(parent))
		parent.Close()
		if err != nil || head.ID != in.Parent || head.Mode != "text" || head.expired(time.Now()) {
			return nil, errStudioMedia
		}
	}
	if upload != nil {
		count++
	}
	if count == 0 {
		return nil, nil
	}
	images := make(map[int]string, count)
	for i, e := range in.Record.Entries {
		f := upload
		last := i == len(in.Record.Entries)-1
		if !last {
			if e.Reference == "" || !e.Complete || e.Error != "" {
				continue
			}
			var err error
			f, err = os.Open(filepath.Join(dir, in.Parent+"."+studioChatRole(i)))
			if err != nil {
				return nil, errStudioMedia
			}
		}
		if f == nil {
			continue
		}
		uri, err := studioChatDataURL(f, (4<<20)/count)
		if !last {
			f.Close()
		}
		if err != nil {
			return nil, err
		}
		images[i] = uri
	}
	return images, nil
}

// commitStudioChatImages gives inherited originals new hard links before deletion of the parent.
// Every destination uses only a validated UUID and a server-generated numeric role.
func commitStudioChatImages(dir string, in *studioIntent, upload *os.File) error {
	for i := range in.Record.Entries {
		entry := &in.Record.Entries[i]
		role := studioChatRole(i)
		dest := filepath.Join(dir, in.Record.ID+"."+role)
		if i == len(in.Record.Entries)-1 && upload != nil {
			if err := upload.Sync(); err != nil {
				return err
			}
			if err := os.Rename(upload.Name(), dest); err != nil {
				return err
			}
		} else if entry.Reference != "" {
			if err := os.Link(filepath.Join(dir, in.Parent+"."+role), dest); err != nil {
				return err
			}
		} else {
			continue
		}
		entry.Reference = studioMediaPrefix + in.Record.ID + "/" + role
	}
	return nil
}
