package controller

import (
	"errors"

	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// GetRetailStudio reads setup state without creating credentials or changing budgets.
func GetRetailStudio(c *gin.Context) {
	token, err := model.RetailStudioToken(c.GetInt("id"), false)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(200, gin.H{"success": true, "data": gin.H{"token_id": 0}})
		return
	}
	if err != nil {
		retailPricingManagementError(c, err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": gin.H{"token_id": token.Id}})
}

// OpenRetailStudio creates the user's internal accounting identity without
// returning a usable credential or consenting to any spending on their behalf.
func OpenRetailStudio(c *gin.Context) {
	token, err := model.RetailStudioToken(c.GetInt("id"), true)
	if err != nil {
		retailPricingManagementError(c, err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": gin.H{"token_id": token.Id}})
}
