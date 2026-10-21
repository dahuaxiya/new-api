package controller

import (
	"errors"
	"net/http"
	"os"
	"strconv"
	"sync"

	"github.com/QuantumNous/new-api/service/gptquota"
	"github.com/gin-gonic/gin"
)

var quotaService = sync.OnceValues(func() (*gptquota.Service, error) {
	return gptquota.Load(os.Getenv("GPT_QUOTA_CONFIG"))
})

// GetGPTQuotaSelf uses the identity established by UserAuth on every request.
// Neither query parameters nor New-Api-User may select another account's slot.
func GetGPTQuotaSelf(c *gin.Context) { respondGPTQuota(c, false) }

// RefreshGPTQuotaSelf triggers an official probe only for the verified viewer.
func RefreshGPTQuotaSelf(c *gin.Context) { respondGPTQuota(c, true) }

// respondGPTQuota keeps identity and query checks identical on read and refresh.
func respondGPTQuota(c *gin.Context, refresh bool) {
	c.Header("Cache-Control", "no-store, private")
	if c.Request.URL.RawQuery != "" {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "Not found"})
		return
	}
	id := c.GetInt("id")
	if hint := c.GetHeader("New-Api-User"); id <= 0 || (hint != "" && hint != strconv.Itoa(id)) {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Authentication required"})
		return
	}
	service, err := quotaService()
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "message": "Subscription quota temporarily unavailable"})
		return
	}
	var view gptquota.View
	if refresh {
		view, err = service.Refresh(c.Request.Context(), id, c.GetString("username"), c.GetString("group"))
	} else {
		view, err = service.Self(c.Request.Context(), id, c.GetString("username"), c.GetString("group"))
	}
	if errors.Is(err, gptquota.ErrCooldown) {
		c.Header("Retry-After", "30")
		c.JSON(http.StatusTooManyRequests, gin.H{"success": false, "message": "Please wait 30 seconds before refreshing quota again"})
		return
	}
	if errors.Is(err, gptquota.ErrUnbound) {
		c.JSON(http.StatusForbidden, gin.H{"success": false, "message": "No linked quota account"})
		return
	}
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "message": "Subscription quota temporarily unavailable"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": view})
}
