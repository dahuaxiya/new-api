package service

import (
	"crypto/sha256"
	"errors"
	"net/http"
	"strconv"
	"strings"

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
	origin, found, err := retailResponseOrigins.Get(retailResponseKey(c, id))
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
		if ref.Str != previous || bound != origin {
			return false
		}
	}
	if gjson.GetBytes(raw, "store").Type != gjson.False {
		c.Set("retail_response_origin", origin)
	}
	return true
}

// RememberRetailResponse 在完整回复或 SSE completed 交给客户端前登记来源。仅保存
// 本地已验证出站的响应，缺少归属的第三方响应 ID 不能借此进入共享账号。
func RememberRetailResponse(c *gin.Context, id string) {
	value, exists := c.Get("retail_response_origin")
	if !exists {
		return
	}
	if key := retailResponseKey(c, id); key != "" {
		retailResponseOrigins.Set(key, value.(retailResponseOrigin))
	}
}
