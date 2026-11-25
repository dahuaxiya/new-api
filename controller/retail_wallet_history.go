package controller

import (
	"net/http"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

// GetRetailWalletHistory returns only the signed-in user's ledger. The client may
// select a time window but cannot nominate the owner or request raw charge snapshots.
func GetRetailWalletHistory(c *gin.Context) {
	if !common.IsRetailSite() {
		c.Status(http.StatusNotFound)
		return
	}
	days, err := strconv.Atoi(c.DefaultQuery("days", "1"))
	if err != nil || (days != 1 && days != 7 && days != 30) {
		c.Status(http.StatusBadRequest)
		return
	}
	c.Header("Cache-Control", "private, no-store")
	before, e1 := strconv.ParseInt(c.DefaultQuery("before", "0"), 10, 64)
	source, e2 := strconv.Atoi(c.DefaultQuery("source", "0"))
	key := c.Query("key")
	if e1 != nil || e2 != nil || before < 0 || len(key) > 255 || (before > 0 && (source < 1 || source > 5 || key == "")) {
		c.Status(http.StatusBadRequest)
		return
	}
	// 老客户端默认仍读原始流水；视图只改变呈现，不改变账本或所有者。
	view := c.DefaultQuery("view", "events")
	if view != "activity" && view != "pending" && view != "events" {
		c.Status(http.StatusBadRequest)
		return
	}
	history, err := model.ReadRetailWalletView(c.Request.Context(), c.GetInt("id"), days, time.Now(), before, source, key, view)
	if err != nil {
		c.Status(http.StatusInternalServerError)
		return
	}
	common.ApiSuccess(c, history)
}
