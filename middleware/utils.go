package middleware

import (
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	pluginruntime "github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
)

// abortWithOpenAiMessage preserves the legacy response for middleware without a specific parameter.
func abortWithOpenAiMessage(c *gin.Context, statusCode int, message string, code ...types.ErrorCode) {
	codeStr := ""
	if len(code) > 0 {
		codeStr = string(code[0])
	}
	abortWithOpenAiError(c, statusCode, types.OpenAIError{Code: codeStr, Type: "new_api_error", Message: message})
}

// abortWithOpenAiError preserves protocol fields and the request ID for HTTP and keeps the plugin error envelope intact.
func abortWithOpenAiError(c *gin.Context, statusCode int, api types.OpenAIError) {
	codeStr, _ := api.Code.(string)
	message := api.Message
	userId := c.GetInt("id")
	_, preparedPluginRoute := c.Get(pluginruntime.ContextKeyRouteRequest)
	if !preparedPluginRoute || !RespondTaskPluginError(c, &dto.TaskError{
		Code:       codeStr,
		Message:    message,
		StatusCode: statusCode,
	}) {
		body := gin.H{"message": common.MessageWithRequestId(message, c.GetString(common.RequestIdKey)), "type": api.Type, "code": codeStr}
		if api.Param != "" {
			body["param"] = api.Param
		}
		c.JSON(statusCode, gin.H{"error": body})
	}
	c.Abort()
	logger.LogError(c.Request.Context(), common.LogText("user %d | %s", userId, message))
}

// invalidRequestStatus is the status of a request whose body could not be
// read or parsed: 413 when the body exceeds MAX_REQUEST_BODY_MB, else 400.
func invalidRequestStatus(err error) int {
	if common.IsRequestBodyTooLargeError(err) {
		return http.StatusRequestEntityTooLarge
	}
	return http.StatusBadRequest
}

func abortWithMidjourneyMessage(c *gin.Context, statusCode int, code int, description string) {
	c.JSON(statusCode, gin.H{
		"description": description,
		"type":        "new_api_error",
		"code":        code,
	})
	c.Abort()
	logger.LogError(c.Request.Context(), description)
}
