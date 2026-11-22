package service

import (
	"crypto/sha256"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"

	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/samber/hot"
	"github.com/tidwall/gjson"
)

// 仅记住已发出的 Responses 的归属。进程重启或缓存淘汰后，客户端须重放完整输入。
type retailResponseOrigin struct {
	channel int
	scope   [32]byte
}

var errRetailPreviousUnavailable = errors.New("previous_response_not_found")

var retailResponseOrigins = hot.NewHotCache[string, retailResponseOrigin](hot.LRU, 8192).Build()

// 只有显式请求存储、而上游明确未存储的轮次才使用短期回放。限制总内存，且只在拥有该 Key 的请求中读取。
const maxRetailHistory = 128 << 10

type retailSavedReply struct {
	origin retailResponseOrigin
	input  string
}

var retailSavedReplies = hot.NewHotCache[string, retailSavedReply](hot.LRU, 256).WithTTL(30 * time.Minute).Build()

// retailResponseKey 以本地用户与令牌隔离共享上游返回的 ID，不接受任意长的缓存键。
func retailResponseKey(c *gin.Context, id string) string {
	user, token := c.GetInt("id"), c.GetInt("token_id")
	if user <= 0 || token <= 0 || id == "" || len(id) > 256 {
		return ""
	}
	return strconv.Itoa(user) + "/" + strconv.Itoa(token) + "/" + id
}

// BindRetailPreviousResponse 只接受本 Key 曾收到的回复，选路时仍须满足本次模型和价表授权。
func BindRetailPreviousResponse(c *gin.Context, id string) bool {
	key := retailResponseKey(c, id)
	if saved, ok, err := retailSavedReplies.Get(key); err == nil && ok {
		c.Set("retail_previous_response", id)
		c.Set("retail_previous_origin", saved.origin)
		c.Set("retail_previous_history", saved.input)
		return true
	}
	origin, found, err := retailResponseOrigins.Get(key)
	if err != nil || !found {
		return false
	}
	c.Set("retail_previous_origin", origin)
	c.Set("retail_previous_response", id)
	return true
}

// checkRetailResponseOrigin 在最终请求上复核地址、渠道和实际认证凭据。只有原生
// OpenAI Responses 能记录绑定，参数改写、密钥轮换或协议转换都不能把旧引用带到别处。
func checkRetailResponseOrigin(c *gin.Context, req *http.Request, info *relaycommon.RelayInfo, raw []byte) bool {
	// 每次尝试独立记录，不能沿用先前失败渠道留下的输入或归属。
	c.Set("retail_response_origin", nil)
	c.Set("retail_replay_candidate", "")
	previous := c.GetString("retail_previous_response")
	ref := gjson.GetBytes(raw, "previous_response_id")
	if ref.Type != gjson.Null && (ref.Type != gjson.String || ref.Str != previous) {
		return false
	}
	if c.Request.URL.Path != "/v1/responses" || !strings.HasSuffix(req.URL.Path, "/responses") ||
		info.ChannelType != constant.ChannelTypeOpenAI || req.Header.Get("Authorization") == "" || len(info.HeadersOverride) != 0 || len(info.RuntimeHeadersOverride) != 0 {
		return previous == ""
	}
	// 不持久化凭据。站点、认证、组织或项目变化时，要求客户端回放，不能凭渠道号复用。
	scope := strings.Join([]string{req.URL.String(), req.Header.Get("Authorization"), req.Header.Get("OpenAI-Organization"),
		req.Header.Get("OpenAI-Project"), req.Header.Get("ChatGPT-Account-Id")}, "\x00")
	origin := retailResponseOrigin{channel: info.ChannelId, scope: sha256.Sum256([]byte(scope))}
	if previous != "" {
		bound, _ := c.Get("retail_previous_origin")
		// 本地回放在计费前已展开；若渠道覆盖参数又注入 ID，不能再次读取远端历史。
		local := c.GetString("retail_previous_history") != ""
		if bound != origin || (local && ref.Exists()) || (!local && ref.Str != previous) {
			return false
		}
	}
	if c.GetBool("retail_client_no_store") {
		return true
	}
	if gjson.GetBytes(raw, "store").Type != gjson.False {
		c.Set("retail_response_origin", origin)
		input := gjson.GetBytes(raw, "input")
		// 远端引用的完整历史不可见。不能把只有最后一轮的片段当作完整会话保存。
		if c.GetBool("retail_client_store") && !info.ChannelOtherSettings.DisableStore && gjson.GetBytes(raw, "store").Type == gjson.True && !ref.Exists() && len(input.Raw) <= maxRetailHistory {
			c.Set("retail_replay_candidate", input.Raw)
		}
	}
	return true
}

// RememberRetailResponse 按上游实际的存储结果登记已完成的回复。仅在显式授权、
// 输入和输出均完整且未超限时提供本地回放，其他未存储的回复沿用未知引用错误。
func RememberRetailResponse(c *gin.Context, id string, response gjson.Result) {
	value, _ := c.Get("retail_response_origin")
	origin, ok := value.(retailResponseOrigin)
	key := retailResponseKey(c, id)
	if !ok || key == "" {
		return
	}
	if response.Get("store").Type != gjson.False {
		retailResponseOrigins.Set(key, origin)
		return
	}
	input := c.GetString("retail_replay_candidate")
	output := response.Get("output")
	if input == "" || !output.IsArray() || len(input)+len(output.Raw) > maxRetailHistory {
		return
	}
	prefix, err := joinRetailInputs("[]", common.RawMessage(input))
	if err != nil {
		return
	}
	all, err := joinRetailInputs(string(prefix), common.RawMessage(output.Raw))
	if err == nil && len(all) <= maxRetailHistory {
		retailSavedReplies.Set(key, retailSavedReply{origin: origin, input: string(all)})
	}
}
