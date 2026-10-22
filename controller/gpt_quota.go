package controller

import (
	"errors"
	"net/http"
	"os"
	"strconv"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/gptquota"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

var quotaService = sync.OnceValues(func() (*gptquota.Service, error) {
	return gptquota.Load(os.Getenv("GPT_QUOTA_CONFIG"))
})

// GetGPTQuotaSelf uses the identity established by UserAuth on every request.
// Neither query parameters nor New-Api-User may select another account's slot.
func GetGPTQuotaSelf(c *gin.Context) { respondGPTQuota(c, false) }

// RefreshGPTQuotaSelf triggers an official probe only for the verified viewer.
func RefreshGPTQuotaSelf(c *gin.Context) { respondGPTQuota(c, true) }

// GetGPTQuotaUser exposes a saved quota reading to an authenticated administrator.
// The target's current database identity must match the configured binding; the
// browser cannot supply a username, group or slot to redirect the lookup.
func GetGPTQuotaUser(c *gin.Context) {
	c.Header("Cache-Control", "no-store, private")
	actor := c.GetInt("id")
	if hint := c.GetHeader("New-Api-User"); actor <= 0 || (hint != "" && hint != strconv.Itoa(actor)) {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "Authentication required"})
		return
	}
	// AdminAuth validates the live session and role; keep this guard as well so a
	// future route registration cannot accidentally expose other users' quotas.
	if c.GetInt("role") < common.RoleAdminUser {
		c.JSON(http.StatusForbidden, gin.H{"success": false, "message": "Access denied"})
		return
	}
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 || strconv.Itoa(id) != c.Param("id") || c.Request.URL.RawQuery != "" {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "Not found"})
		return
	}
	target, err := model.GetUserById(id, false)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "Not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "message": "Subscription quota temporarily unavailable"})
		return
	}
	svc, err := quotaService()
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "message": "Subscription quota temporarily unavailable"})
		return
	}
	// Read the shared snapshot only: opening a page must not trigger N official
	// probes or consume each account's manual-refresh cooldown.
	view, err := svc.Self(c.Request.Context(), target.Id, target.Username, target.Group)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "message": "Subscription quota temporarily unavailable"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": view})
}

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
