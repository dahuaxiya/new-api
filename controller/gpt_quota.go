package controller

import (
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
func GetGPTQuotaSelf(c *gin.Context) {
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
	view, err := service.Self(c.Request.Context(), id, c.GetString("username"), c.GetString("group"))
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "message": "Subscription quota temporarily unavailable"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": view})
}
