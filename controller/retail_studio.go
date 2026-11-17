package controller

import (
	"errors"
	"io"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/retailprice"
	"github.com/QuantumNous/new-api/setting/retail_setting"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// GetRetailStudio reads setup state without creating credentials or changing budgets.
func GetRetailStudio(c *gin.Context) {
	request, daily, err := retail_setting.StudioBudgets()
	if err != nil {
		retailPricingManagementError(c, err)
		return
	}
	token, err := model.RetailStudioToken(c.GetInt("id"), false)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(200, gin.H{"success": true, "data": gin.H{"token_id": 0, "defaults": gin.H{"max_request_cny": request, "daily_budget_cny": daily}}})
		return
	}
	if err != nil {
		retailPricingManagementError(c, err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": gin.H{"token_id": token.Id, "defaults": gin.H{"max_request_cny": request, "daily_budget_cny": daily}}})
}

// OpenRetailStudio creates the user's internal accounting identity without
// returning a usable credential. A selected group initializes bounded defaults only once.
func OpenRetailStudio(c *gin.Context) {
	var input struct {
		ProductID string `json:"product_id"`
	}
	if err := c.ShouldBindJSON(&input); err != nil && !errors.Is(err, io.EOF) {
		retailPricingManagementError(c, retailprice.ErrConfig)
		return
	}
	token, err := model.RetailStudioToken(c.GetInt("id"), true, input.ProductID)
	if err != nil {
		retailPricingManagementError(c, err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": gin.H{"token_id": token.Id}})
}
