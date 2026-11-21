package controller

import (
	"fmt"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/retailprice"
	"github.com/gin-gonic/gin"
)

// GetRetailCredentials returns the root-only, secret-free inventory on the retail deployment.
func GetRetailCredentials(c *gin.Context) {
	if !common.IsRetailSite() {
		c.Status(http.StatusNotFound)
		return
	}
	out, err := model.ReadRetailCredentials(c.Request.Context())
	if err != nil {
		retailPricingManagementError(c, err)
		return
	}
	common.ApiSuccess(c, out)
}

// PutRetailCredential accepts one explicit action. Neither errors nor audit metadata include the submitted Key.
func PutRetailCredential(c *gin.Context) {
	if !common.IsRetailSite() {
		c.Status(http.StatusNotFound)
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 32<<10)
	var in model.RetailCredentialEdit
	if c.ShouldBindJSON(&in) != nil {
		retailPricingManagementError(c, retailprice.ErrConfig)
		return
	}
	id, err := model.EditRetailCredential(c.Request.Context(), in, c.GetInt("id"))
	switch err {
	case nil:
		model.InitChannelCache()
		model.RecordAuditLog(c, model.AuditLog{UserId: c.GetInt("id"), ActorRole: c.GetInt("role"), Action: "retail.credential." + in.Action, Category: model.AuditCategoryOperation, Content: fmt.Sprintf("site #%d, channel #%d", in.Site, id), Success: true})
		common.ApiSuccess(c, gin.H{"id": id})
	case model.ErrRetailPriceConflict:
		c.JSON(http.StatusConflict, gin.H{"success": false, "message": "Retail pricing changed. Reload before saving."})
	case model.ErrRetailSupplyGroupKey, model.ErrRetailSupplyGroupName, model.ErrRetailSupplyGroupIncomplete:
		common.ApiError(c, err)
	default:
		retailPricingManagementError(c, err)
	}
}

// GetRetailKeyLibrary returns only metadata. Reading the library never imports or decrypts a legacy credential.
func GetRetailKeyLibrary(c *gin.Context) {
	if !common.IsRetailSite() {
		c.Status(http.StatusNotFound)
		return
	}
	state, err := model.ReadRetailKeyLibrary(c.Request.Context())
	if err != nil {
		retailPricingManagementError(c, err)
		return
	}
	common.ApiSuccess(c, state)
}

// PutRetailKeyLibrary commits one root action. Errors and audit entries contain identifiers only.
func PutRetailKeyLibrary(c *gin.Context) {
	if !common.IsRetailSite() {
		c.Status(http.StatusNotFound)
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 32<<10)
	var in model.RetailLibraryEdit
	if c.ShouldBindJSON(&in) != nil {
		retailPricingManagementError(c, retailprice.ErrConfig)
		return
	}
	id, err := model.EditRetailKeyLibrary(c.Request.Context(), in, c.GetInt("id"))
	switch err {
	case nil:
		model.InitChannelCache()
		model.RecordAuditLog(c, model.AuditLog{UserId: c.GetInt("id"), ActorRole: c.GetInt("role"), Action: "retail.key." + in.Action, Category: model.AuditCategoryOperation, Content: fmt.Sprintf("key #%d, channel #%d", id, in.Channel), Success: true})
		common.ApiSuccess(c, gin.H{"id": id})
	case model.ErrRetailLibraryConflict:
		c.JSON(http.StatusConflict, gin.H{"success": false, "message": "Key inventory changed. Reopen the form."})
	case model.ErrRetailLibraryUsed, model.ErrRetailLibraryUnavailable, model.ErrRetailSupplyGroupKey:
		common.ApiError(c, err)
	default:
		retailPricingManagementError(c, err)
	}
}
