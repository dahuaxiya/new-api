package service

import (
	"slices"
	"strings"

	"github.com/QuantumNous/new-api/pkg/retailprice"
	"github.com/tidwall/gjson"
)

// extractRetailGeminiModel 取已通过入口校验的 Gemini 路径里的模型，用于核对 Magpie 附带的模型字段。
func extractRetailGeminiModel(path string) string {
	model, _, _ := strings.Cut(strings.TrimPrefix(path, "/v1beta/models/"), ":")
	return model
}

// validateRetailGeminiRequest 只接受能原样表达为单候选 Chat 请求的文本参数。
// 媒体、服务端缓存、工具和不支持的设置在转换前拒绝，以免丢字段或在预留之外计费。
func validateRetailGeminiRequest(raw []byte, model string) error {
	root := gjson.ParseBytes(raw)
	if !gjson.ValidBytes(raw) || !root.IsObject() || !UniqueRetailKeys(root) {
		return retailprice.ErrConfig
	}
	for key, value := range root.Map() {
		switch key {
		case "model":
			if value.Type != gjson.String || value.Str != model {
				return retailprice.ErrConfig
			}
		case "contents":
			if !value.IsArray() || len(value.Array()) == 0 {
				return retailprice.ErrConfig
			}
			for _, content := range value.Array() {
				if !retailGeminiTextContent(content, false) {
					return retailprice.ErrConfig
				}
			}
		case "systemInstruction", "system_instruction":
			if root.Get("systemInstruction").Exists() && root.Get("system_instruction").Exists() || !retailGeminiTextContent(value, true) {
				return retailprice.ErrConfig
			}
		case "generationConfig":
			if !value.IsObject() {
				return retailprice.ErrConfig
			}
			for field, v := range value.Map() {
				switch field {
				case "candidateCount":
					if v.Type != gjson.Number || v.Float() != 1 {
						return retailprice.ErrConfig
					}
				case "maxOutputTokens", "temperature", "topP":
					if v.Type != gjson.Number {
						return retailprice.ErrConfig
					}
				case "stopSequences":
					if !v.IsArray() || slices.ContainsFunc(v.Array(), func(item gjson.Result) bool { return item.Type != gjson.String }) {
						return retailprice.ErrConfig
					}
				default:
					return retailprice.ErrConfig
				}
			}
		default:
			return retailprice.ErrConfig
		}
	}
	return nil
}

// retailGeminiTextContent 检查单条消息及其角色，仅允许文本分段进入现有计量和转换器。
func retailGeminiTextContent(content gjson.Result, system bool) bool {
	role := content.Get("role")
	if !content.IsObject() || (!system && role.Str != "user" && role.Str != "model") || (system && role.Exists()) {
		return false
	}
	fields := content.Map()
	parts := fields["parts"]
	expectedFields := 2
	if system {
		expectedFields = 1
	}
	if !parts.IsArray() || len(parts.Array()) == 0 || len(fields) != expectedFields {
		return false
	}
	for _, part := range parts.Array() {
		if !part.IsObject() || len(part.Map()) != 1 || part.Get("text").Type != gjson.String {
			return false
		}
	}
	return true
}
