package controller

import (
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/retailprice"
	"github.com/QuantumNous/new-api/setting/retail_setting"
	"github.com/gin-gonic/gin"
)

// GetRetailPricingAdmin returns the current immutable book to the root management boundary.
func GetRetailPricingAdmin(c *gin.Context) {
	book, version, err := model.GetRetailPriceBook()
	if err != nil {
		common.ApiError(c, common.NewMessage("Unable to load retail pricing"))
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"book": book, "version": version, "server_time": time.Now().Unix()}})
}

// PutRetailPricingAdmin publishes a complete reviewed book with optimistic concurrency protection.
func PutRetailPricingAdmin(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20)
	var input struct {
		ExpectedVersion int64            `json:"expected_version"`
		Book            retailprice.Book `json:"book"`
	}
	if c.ShouldBindJSON(&input) != nil {
		common.ApiError(c, common.NewMessage("Retail pricing configuration is invalid"))
		return
	}
	version, err := model.PublishRetailPriceBook(input.ExpectedVersion, input.Book, nil, c.GetInt("id"))
	if err != nil {
		retailPricingManagementError(c, err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": gin.H{"version": version}})
}

// ImportRetailRates replaces only procurement multipliers; official tariffs and sales policy remain immutable in prior versions.
func ImportRetailRates(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 128<<10)
	var input struct {
		ExpectedVersion int64              `json:"expected_version"`
		Rates           []retailprice.Rate `json:"rates"`
	}
	if c.ShouldBindJSON(&input) != nil || input.Rates == nil {
		common.ApiError(c, common.NewMessage("Retail pricing configuration is invalid"))
		return
	}
	version, err := model.PublishRetailPriceBook(input.ExpectedVersion, retailprice.Book{}, input.Rates, c.GetInt("id"))
	if err != nil {
		retailPricingManagementError(c, err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": gin.H{"version": version}})
}

// PreviewRetailPricing computes CNY selling schedules without publishing or granting any customer access.
func PreviewRetailPricing(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20)
	var input struct {
		Book retailprice.Book `json:"book"`
	}
	if c.ShouldBindJSON(&input) != nil {
		retailPricingManagementError(c, retailprice.ErrConfig)
		return
	}
	// Publication preserves server-managed mappings; preview must use the same
	// source so an older browser draft cannot show a manual price for a bound route.
	current, _, err := model.GetRetailPriceBook()
	if err != nil {
		retailPricingManagementError(c, err)
		return
	}
	input.Book.GroupBindings, input.Book.PublicRateSync = current.GroupBindings, current.PublicRateSync
	// Match publication: catalog edits cannot replace independently saved following economics.
	input.Book.SalePolicyManaged = current.SalePolicyManaged
	if current.SalePolicyManaged || (len(current.GroupBindings) > 0 && current.ReceiptRatio != "") {
		input.Book.ReceiptRatio, input.Book.MarginBPS, input.Book.FeeBPS, input.Book.BufferBPS = current.ReceiptRatio, current.MarginBPS, current.FeeBPS, current.BufferBPS
	}
	if input.Book.Validate(time.Now().Unix()) != nil {
		retailPricingManagementError(c, retailprice.ErrConfig)
		return
	}
	rows := []gin.H{}
	for _, product := range input.Book.Products {
		ids := []int{}
		for _, route := range product.Routes {
			ids = append(ids, route.ChannelID)
		}
		for _, official := range input.Book.Models {
			ceiling := retailprice.Prices{Input: "99999999", Output: "99999999", CacheRead: "99999999", CacheWrite: "99999999", CacheWrite1h: "99999999"}
			policy := retailprice.KeyPolicy{ProductID: product.ID, AcceptFloating: true, Caps: map[string]retailprice.Prices{official.ID: ceiling}, MaxRequestCNY: "99999999", DailyBudgetCNY: "99999999"}
			quote, err := input.Book.MakeQuote(0, product.UserGroups[0], policy, official.ID, 1, ids, time.Now().Unix())
			if err != nil {
				rows = append(rows, gin.H{"product": product.Name, "model": official.ID, "available": false})
				continue
			}
			rows = append(rows, gin.H{"product": product.Name, "model": official.ID, "available": true, "tiers": quote.Sale, "valid_until": quote.ValidUntil})
		}
	}
	c.JSON(200, gin.H{"success": true, "data": rows})
}

// retailPricingManagementError maps internal failure reasons to stable translated management messages.
func retailPricingManagementError(c *gin.Context, err error) {
	message := "Retail pricing configuration is invalid"
	switch err {
	case model.ErrRetailPriceConflict:
		message = "Retail pricing changed. Reload before saving."
	case retailprice.ErrAccess:
		message = "This product or API key is not available to this account"
	}
	common.ApiError(c, common.NewMessage(message))
}

// GetRetailKeyPricing checks ownership even for an unconfigured policy; numeric IDs grant no authority.
func GetRetailKeyPricing(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		retailPricingManagementError(c, retailprice.ErrAccess)
		return
	}
	policy, revision, err := model.GetRetailKeyPolicy(c.GetInt("id"), id)
	if err != nil {
		retailPricingManagementError(c, err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": gin.H{"policy": policy, "revision": revision}})
}

// PutRetailKeyPricing saves explicit user consent and CNY ceilings, never accepting a caller-supplied owner ID.
func PutRetailKeyPricing(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		retailPricingManagementError(c, retailprice.ErrAccess)
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 128<<10)
	var input struct {
		ExpectedRevision int64                 `json:"expected_revision"`
		Policy           retailprice.KeyPolicy `json:"policy"`
	}
	if c.ShouldBindJSON(&input) != nil {
		retailPricingManagementError(c, retailprice.ErrConfig)
		return
	}
	if err := model.PutRetailKeyPolicy(c.GetInt("id"), id, input.ExpectedRevision, input.Policy); err != nil {
		retailPricingManagementError(c, err)
		return
	}
	c.JSON(200, gin.H{"success": true})
}

// GetRetailCatalog exposes only authorized selling prices; procurement groups, ratios and channel IDs stay private.
func GetRetailCatalog(c *gin.Context) {
	book, version, err := model.GetRetailPriceBook()
	if err != nil {
		common.ApiError(c, common.NewMessage("Unable to load retail pricing"))
		return
	}
	products := []gin.H{}
	if version == 0 {
		c.JSON(200, gin.H{"success": true, "data": gin.H{"enabled": false, "version": version, "products": products}})
		return
	}
	var user model.User
	if err := model.DB.Select("id", "group").First(&user, c.GetInt("id")).Error; err != nil {
		retailPricingManagementError(c, retailprice.ErrAccess)
		return
	}
	cfg, err := retail_setting.Load()
	if err != nil {
		retailPricingManagementError(c, err)
		return
	}
	ids, err := model.RetailSupplyIDs(cfg.Group)
	if err != nil {
		retailPricingManagementError(c, err)
		return
	}
	for _, p := range book.Products {
		if !slices.Contains(p.UserGroups, user.Group) {
			continue
		}
		models := []gin.H{}
		for _, official := range book.Models {
			// Synthetic generous caps are only for displaying a price, never a persisted user consent.
			ceiling := retailprice.Prices{Input: "99999999", Output: "99999999", CacheRead: "99999999", CacheWrite: "99999999", CacheWrite1h: "99999999"}
			policy := retailprice.KeyPolicy{ProductID: p.ID, AcceptFloating: true, Caps: map[string]retailprice.Prices{official.ID: ceiling}, MaxRequestCNY: "99999999", DailyBudgetCNY: "99999999"}
			quote, err := book.MakeQuote(version, user.Group, policy, official.ID, 1, ids, time.Now().Unix())
			if err != nil {
				continue
			}
			models = append(models, gin.H{"id": official.ID, "kind": official.Kind, "official_model": official.OfficialModel, "currency": "CNY", "tiers": quote.Sale, "valid_until": quote.ValidUntil, "max_output": official.MaxOutput})
		}
		products = append(products, gin.H{"id": p.ID, "name": p.Name, "models": models})
	}
	c.JSON(200, gin.H{"success": true, "data": gin.H{"enabled": book.Enabled, "version": version, "products": products}})
}
