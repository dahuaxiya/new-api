package middleware

import (
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

// RetailStudioSession admits only authenticated same-origin browser requests.
// Unlike public relay CORS, wallet spending requires a trusted Origin even in local development.
func RetailStudioSession() gin.HandlerFunc {
	return func(c *gin.Context) {
		origin, valid := requestBrowserOrigin(c.Request)
		if !common.IsRetailSite() || c.GetBool("use_access_token") || !valid || !isAllowedSessionOrigin(c.Request, origin) || c.ContentType() != "application/json" {
			abortWithOpenAiMessage(c, http.StatusForbidden, "Studio requires a same-origin session")
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 256<<10)
		c.Next()
	}
}

// RetailStudioRelay resolves identity exclusively from the live login session,
// then enters the same distribution, wallet reservation and price guards as API calls.
func RetailStudioRelay(path string) gin.HandlerFunc {
	return func(c *gin.Context) {
		book, version, err := model.GetRetailPriceBook()
		if err != nil || !book.Enabled || version == 0 {
			abortWithOpenAiMessage(c, http.StatusForbidden, "retail_price_unavailable")
			return
		}
		token, err := model.RetailStudioToken(c.GetInt("id"), false)
		if err != nil {
			abortWithOpenAiMessage(c, http.StatusForbidden, "retail_product_not_allowed")
			return
		}
		group, err := model.RetailTokenGroup(c.GetInt("id"), token.Id)
		if err != nil || group == "" {
			abortWithOpenAiMessage(c, http.StatusForbidden, "retail_product_not_allowed")
			return
		}
		user, err := model.GetUserCache(c.GetInt("id"))
		if err != nil || user.Status != common.UserStatusEnabled {
			abortWithOpenAiMessage(c, http.StatusForbidden, "retail_product_not_allowed")
			return
		}
		user.WriteContext(c)
		if err := SetupContextForToken(c, token); err != nil {
			abortWithOpenAiMessage(c, http.StatusForbidden, "retail_product_not_allowed")
			return
		}
		c.Set("retail_request", true)
		common.SetContextKey(c, constant.ContextKeyUserGroup, group)
		common.SetContextKey(c, constant.ContextKeyUsingGroup, group)
		// Only fixed server route constants may select a relay protocol. Never take
		// an upstream URL, owner ID, channel or credential from the browser.
		c.Request.URL.Path = path
		c.Next()
	}
}
