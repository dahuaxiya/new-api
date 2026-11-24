package service

import (
	"bytes"
	"encoding/base64"
	"io"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/retailprice"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// PrepareRetailResponsesInput 在报价前统一处理客户端和本地保存的 Responses 历史，使计量、重试与实际出站使用同一份内容。
func PrepareRetailResponsesInput(c *gin.Context, input common.RawMessage) error {
	history := c.GetString("retail_previous_history")
	if len(input) > 8<<20 {
		return retailprice.ErrConfig
	}
	var err error
	if history != "" {
		// 先拼接，旧版本保存的记录也需要经过与本次输入相同的转换。
		input, err = joinRetailInputs(history, input)
		if err != nil || len(input) > 8<<20 {
			return retailprice.ErrConfig
		}
	}
	root := gjson.ParseBytes(input)
	var items []common.RawMessage
	for i, item := range root.Array() {
		replacement, err := retailCodexHistoryMessage(item)
		if err != nil {
			return err
		}
		if replacement != nil {
			if items == nil {
				if err := common.Unmarshal(input, &items); err != nil {
					return err
				}
			}
			items[i] = replacement
			continue
		}
		if item.Get("role").Str != "user" || !item.Get("content").IsArray() || (item.Get("type").Exists() && item.Get("type").Str != "message") {
			continue
		}
		var parts []common.RawMessage
		for j, part := range item.Get("content").Array() {
			if part.Get("type").Str != "input_file" {
				continue
			}
			name := part.Get("filename").Str
			data, ok := strings.CutPrefix(part.Get("file_data").Str, "data:text/plain;base64,")
			if !ok || !strings.EqualFold(filepath.Ext(name), ".txt") || part.Get("file_url").Exists() || part.Get("file_id").Exists() {
				return retailprice.ErrConfig
			}
			decoded, err := base64.StdEncoding.DecodeString(data)
			if err != nil || !utf8.Valid(decoded) || bytes.IndexByte(decoded, 0) >= 0 {
				return retailprice.ErrConfig
			}
			// 每条消息只拆分和重建一次，避免多附件请求反复复制整个请求体。
			if parts == nil {
				if err := common.Unmarshal([]byte(item.Get("content").Raw), &parts); err != nil {
					return err
				}
			}
			parts[j], err = common.Marshal(map[string]string{"type": "input_text", "text": name + "\n" + string(decoded)})
			if err != nil {
				return err
			}
		}
		if parts != nil {
			if items == nil {
				if err := common.Unmarshal(input, &items); err != nil {
					return err
				}
			}
			content, err := common.Marshal(parts)
			if err != nil {
				return err
			}
			items[i], err = sjson.SetRawBytes(items[i], "content", content)
			if err != nil {
				return err
			}
		}
	}
	if history == "" && items == nil {
		return nil
	}
	storage, err := common.GetBodyStorage(c)
	if err != nil || storage.Size() > 8<<20 {
		return retailprice.ErrConfig
	}
	raw, err := storage.Bytes()
	if err != nil || !gjson.ValidBytes(raw) || !UniqueRetailKeys(gjson.ParseBytes(raw)) {
		return retailprice.ErrConfig
	}
	if items != nil {
		input, err = common.Marshal(items)
		if err != nil {
			return err
		}
	}
	if history != "" {
		// 历史已经包含上一轮输入和输出。删除远端引用，避免上游重复加载同一段历史。
		raw, err = sjson.DeleteBytes(raw, "previous_response_id")
		if err != nil {
			return err
		}
	}
	raw, err = sjson.SetRawBytes(raw, "input", input)
	if err != nil || len(raw) > 8<<20 {
		return retailprice.ErrConfig
	}
	replacement, err := common.CreateBodyStorage(raw)
	if err != nil {
		return err
	}
	// 替换成功后才关闭旧存储，重试以及按原始字节透传的渠道都走新缓存。
	common.CleanupBodyStorage(c)
	c.Set(common.KeyBodyStorage, replacement)
	c.Request.Body = io.NopCloser(replacement)
	c.Request.ContentLength = int64(len(raw))
	return nil
}

// joinRetailInputs 把字符串输入按用户消息展开，数组按原顺序拼接，不继承上一轮的 instructions。
func joinRetailInputs(history string, input common.RawMessage) ([]byte, error) {
	var items []common.RawMessage
	if err := common.Unmarshal([]byte(history), &items); err != nil {
		return nil, err
	}
	switch next := gjson.ParseBytes(input); {
	case next.Type == gjson.String:
		message, err := common.Marshal(map[string]string{"role": "user", "content": next.Str})
		if err != nil {
			return nil, err
		}
		items = append(items, message)
	case next.IsArray():
		for _, item := range next.Array() {
			items = append(items, common.RawMessage(item.Raw))
		}
	default:
		return nil, retailprice.ErrConfig
	}
	return common.Marshal(items)
}

// retailCodexHistoryMessage 将 Codex 私有的历史事件转换成可计量的上下文；返回 nil 表示保留原条目。
func retailCodexHistoryMessage(item gjson.Result) (common.RawMessage, error) {
	switch item.Get("type").Str {
	case "additional_tools":
		tools := item.Get("tools")
		if item.Get("role").Str != "developer" || !tools.IsArray() {
			return nil, retailprice.ErrConfig
		}
		// 旧工具定义是历史资料。能否调用工具只取决于本轮顶层 tools，不能从历史恢复权限。
		return common.Marshal(map[string]string{"role": "assistant", "content": "Previously available tool definitions:\n" + tools.Raw})
	case "image_generation_call":
		result, prompt := item.Get("result"), item.Get("revised_prompt")
		if item.Get("status").Str != "completed" || result.Type != gjson.String || (prompt.Exists() && prompt.Type != gjson.String) || len(result.Str) > base64.StdEncoding.EncodedLen(4<<20) {
			return nil, retailprice.ErrConfig
		}
		data, err := base64.StdEncoding.DecodeString(result.Str)
		if err != nil || len(data) == 0 || len(data) > 4<<20 {
			return nil, retailprice.ErrConfig
		}
		text := "Previous image generation result.\n" + prompt.Str
		// Codex 的结果也可能是 SVG。把源码当普通文本保留，不执行 XML，也不加载外部资源。
		if utf8.Valid(data) && bytes.HasPrefix(bytes.TrimSpace(data), []byte("<svg")) {
			return common.Marshal(map[string]string{"role": "assistant", "content": text + "\n" + string(data)})
		}
		_, format, err := decodeImageConfig(data)
		if err != nil || (format != "png" && format != "jpeg" && format != "webp") {
			return nil, retailprice.ErrConfig
		}
		// Responses 用 user/input_image 承载输入图片。原字节仍经过同一组总大小、像素和出站一致性检查。
		parts := []map[string]string{{"type": "input_text", "text": text}, {"type": "input_image", "image_url": "data:image/" + format + ";base64," + result.Str}}
		return common.Marshal(map[string]any{"role": "user", "content": parts})
	default:
		return nil, nil
	}
}
