package controller

import (
	"errors"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/QuantumNous/new-api/setting/retail_setting"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// RetailSupplies 提供 root 供货状态与站点地址；复用实际选路白名单，不探测上游、不返回凭据。
// eligible 只表示通过本站选路资格校验，不证明上游额度、网络或真实模型调用可用。
func RetailSupplies(c *gin.Context) {
	cfg, err := model.RetailSupplyConfig(model.DB)
	if err != nil {
		common.ApiError(c, retail_setting.ErrUnavailable)
		return
	}
	allowed := []int{}
	if cfg.Enabled {
		allowed, err = model.RetailSupplyIDs(cfg.Group)
		if err != nil {
			common.ApiError(c, retail_setting.ErrUnavailable)
			return
		}
	}
	state := "ready"
	switch {
	case !cfg.Enabled:
		state = "disabled"
	case common.RedisEnabled || common.BatchUpdateEnabled:
		state = "unsupported_runtime"
	case !ratio_setting.ContainsGroupRatio(cfg.Group):
		state = "group_missing"
	case len(allowed) == 0:
		state = "no_eligible_supply"
	}
	items := []gin.H{}
	for _, supply := range cfg.Supplies {
		// 用安全白名单构造响应，渠道结构还有其他敏感字段，不能直接序列化。
		item := gin.H{"channel_id": supply.ChannelID, "kind": supply.Kind, "name": "", "status": "missing"}
		channel, err := model.GetChannelById(supply.ChannelID, false)
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			common.ApiError(c, retail_setting.ErrUnavailable)
			return
		}
		if err == nil {
			item["name"] = channel.Name
			item["site_id"] = channel.RetailSiteID
			if supply.Kind == "reseller" {
				item["base_url"] = channel.GetBaseURL()
				item["models"] = channel.Models
			}
			switch {
			case channel.Status != common.ChannelStatusEnabled:
				item["status"] = "disabled"
			case !cfg.Enabled:
				item["status"] = "not_evaluated"
			case slices.Contains(allowed, channel.Id):
				item["status"] = "eligible"
			default:
				item["status"] = "excluded"
			}
		}
		items = append(items, item)
	}
	c.JSON(200, gin.H{"success": true, "data": gin.H{"state": state, "group": cfg.Group, "items": items}})
}

// RetailAccounting 返回限量账务视图；入口只允许 root 会话，不包含客户请求正文或供货密钥。
func RetailAccounting(c *gin.Context) {
	// 三类记录使用独立游标。时间戳配合主键翻页，避免同一秒超过 100 笔时漏单。
	before, err := strconv.ParseInt(c.DefaultQuery("charge_before", "0"), 10, 64)
	if err != nil || before < 0 {
		common.ApiError(c, model.ErrRetailSettlement)
		return
	}
	afterID := c.Query("charge_id")
	if before == 0 {
		before = time.Now().Unix() + 1
	}
	attemptsBefore, err := strconv.Atoi(c.DefaultQuery("attempt_before", "0"))
	if err != nil || attemptsBefore < 0 {
		common.ApiError(c, model.ErrRetailSettlement)
		return
	}
	costsBefore, err := strconv.Atoi(c.DefaultQuery("cost_before", "0"))
	if err != nil || costsBefore < 0 {
		common.ApiError(c, model.ErrRetailSettlement)
		return
	}
	charges := []model.RetailCharge{}
	attempts := []model.RetailAttempt{}
	costs := []model.RetailCost{}
	chargeQuery := model.DB.Where("created_at < ? OR (created_at = ? AND id < ?)", before, before, afterID)
	attemptQuery, costQuery := model.DB, model.DB
	if attemptsBefore > 0 {
		attemptQuery = attemptQuery.Where("id < ?", attemptsBefore)
	}
	if costsBefore > 0 {
		costQuery = costQuery.Where("id < ?", costsBefore)
	}
	if id := c.Query("request_id"); id != "" {
		chargeQuery = model.DB.Where("id = ?", id)
		attemptQuery = attemptQuery.Where("request_id = ?", id)
		costQuery = costQuery.Where("kind = ? AND attempt_id IN (?)", "attempt", model.DB.Model(&model.RetailAttempt{}).Select("id").Where("request_id = ?", id))
	}
	if err := chargeQuery.Order("created_at DESC, id DESC").Limit(100).Find(&charges).Error; err != nil {
		common.ApiError(c, model.ErrRetailSettlement)
		return
	}
	if err := attemptQuery.Order("id DESC").Limit(100).Find(&attempts).Error; err != nil {
		common.ApiError(c, model.ErrRetailSettlement)
		return
	}
	if err := costQuery.Order("id DESC").Limit(100).Find(&costs).Error; err != nil {
		common.ApiError(c, model.ErrRetailSettlement)
		return
	}
	next := gin.H{}
	if len(charges) == 100 {
		last := charges[len(charges)-1]
		next["charge_before"] = last.CreatedAt
		next["charge_id"] = last.ID
	}
	if len(attempts) == 100 {
		next["attempt_before"] = attempts[len(attempts)-1].ID
	}
	if len(costs) == 100 {
		next["cost_before"] = costs[len(costs)-1].ID
	}
	c.JSON(200, gin.H{"success": true, "data": gin.H{"charges": charges, "attempts": attempts, "costs": costs, "next": next}})
}

// RefundRetailReservation 只释放已人工核实的旧预留，金额必须匹配页面读取的原值。
func RefundRetailReservation(c *gin.Context) {
	var input struct {
		ExpectedReserved int    `json:"expected_reserved"`
		Reason           string `json:"reason"`
	}
	if c.ShouldBindJSON(&input) != nil {
		common.ApiError(c, model.ErrRetailSettlement)
		return
	}
	if err := model.RefundStaleRetailCharge(c.Param("id"), c.GetInt("id"), input.ExpectedReserved, input.Reason); err != nil {
		common.ApiError(c, err)
		return
	}
	c.JSON(200, gin.H{"success": true})
}

// PutRetailCost 登记供应商实际账单或订阅期间支出；客户端不能指定操作者和记录时间。
func PutRetailCost(c *gin.Context) {
	var cost model.RetailCost
	if c.ShouldBindJSON(&cost) != nil {
		common.ApiError(c, model.ErrRetailSettlement)
		return
	}
	cost.ActorID = c.GetInt("id")
	if err := model.RecordRetailCost(cost); err != nil {
		common.ApiError(c, err)
		return
	}
	c.JSON(200, gin.H{"success": true})
}

// CreateRetailSupplyGroup adds an explicitly owned supplier credential under an existing site.
// The root-only route also requires same-origin session writes and does not return the submitted Key.
func CreateRetailSupplyGroup(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 32<<10)
	var input struct {
		SiteID int    `json:"site_id"`
		Name   string `json:"name"`
		Key    string `json:"key"`
		Models string `json:"models"`
	}
	if c.ShouldBindJSON(&input) != nil {
		common.ApiError(c, retail_setting.ErrUnavailable)
		return
	}
	// A legacy console may create a disabled draft, but may no longer store a second copy of a Key on it.
	if input.Key != "" {
		common.ApiError(c, retail_setting.ErrUnavailable)
		return
	}
	id, err := model.CreateRetailSupplyGroup(c.Request.Context(), input.SiteID, input.Name, input.Key, input.Models)
	if err != nil {
		if errors.Is(err, model.ErrRetailSupplyGroupName) || errors.Is(err, model.ErrRetailSupplyGroupKey) {
			common.ApiError(c, err)
			return
		}
		common.ApiError(c, retail_setting.ErrUnavailable)
		return
	}
	model.InitChannelCache()
	c.JSON(200, gin.H{"success": true, "data": gin.H{"channel_id": id}})
}
