package model

import (
	"strings"
	"time"

	"github.com/QuantumNous/new-api/setting/retail_setting"
	"gorm.io/gorm"
)

// RefundStaleRetailCharge 在人工确认后释放至少 24 小时未结算的预留，审核信息与金额同事务保存。
// 调用方必须验证 root 会话和来源；管理员需先确认没有仍在执行的请求，不能作为自动超时退款任务。
func RefundStaleRetailCharge(id string, actorID, expectedReserved int, reason string) error {
	if actorID <= 0 || len(reason) < 5 || len(reason) > 500 {
		return ErrRetailSettlement
	}
	userID, tokenID := 0, 0
	var pricing RetailCharge
	if err := DB.Select("pricing_version").First(&pricing, "id = ?", id).Error; err != nil {
		return err
	}
	err := DB.Transaction(func(tx *gorm.DB) error {
		if pricing.PricingVersion > 0 {
			if _, err := lockRetailPriceState(tx); err != nil {
				return err
			}
		}
		var charge RetailCharge
		if err := lockForUpdate(tx).First(&charge, "id = ?", id).Error; err != nil {
			return err
		}
		if charge.Status == "refunded" && charge.ReviewedBy == actorID && charge.ReviewReason == reason {
			return nil
		}
		if charge.Status != "reserved" || charge.Reserved != expectedReserved || charge.UpdatedAt > time.Now().Unix()-86400 {
			return ErrRetailSettlement
		}
		if err := applyRetailDelta(tx, charge.UserID, charge.TokenID, -charge.Reserved); err != nil {
			return err
		}
		if charge.PricingVersion > 0 {
			if _, err := settleRetailPriceBudget(tx, charge, 0); err != nil {
				return err
			}
		}
		userID, tokenID = charge.UserID, charge.TokenID
		return tx.Model(&charge).Updates(map[string]any{"status": "refunded", "actual": 0, "reviewed_by": actorID, "review_reason": reason, "updated_at": time.Now().Unix()}).Error
	})
	if userID > 0 {
		invalidateRetailCaches(userID, tokenID)
	}
	return err
}

// RetailCost 保存供应商账单或订阅期间的真实支出，与客户销售价和 token 计数分离。
// 金额为币种最小单位；不同币种不能未经换汇直接求和。Reference 是操作者提供的唯一对账凭据。
type RetailCost struct {
	ID          int    `json:"id"`
	Reference   string `json:"reference" gorm:"type:varchar(100);uniqueIndex"`
	Kind        string `json:"kind" gorm:"type:varchar(24)"`
	AttemptID   int    `json:"attempt_id" gorm:"index"`
	ChannelID   int    `json:"channel_id"`
	Currency    string `json:"currency" gorm:"type:varchar(3)"`
	AmountMinor int64  `json:"amount_minor"`
	Period      string `json:"period" gorm:"type:varchar(40)"`
	ActorID     int    `json:"actor_id"`
	CreatedAt   int64  `json:"created_at"`
}

// RecordRetailCost 只追加已核实支出；重复凭据不能重复计入成本，失败尝试也可记录实际费用。
func RecordRetailCost(cost RetailCost) error {
	if cost.ActorID <= 0 || len(cost.Reference) < 3 || len(cost.Reference) > 100 || cost.AmountMinor < 0 || cost.AmountMinor > 1_000_000_000_000 || len(cost.Currency) != 3 {
		return ErrRetailSettlement
	}
	for _, r := range cost.Currency {
		if r < 'A' || r > 'Z' {
			return ErrRetailSettlement
		}
	}
	if cost.Kind != "attempt" && cost.Kind != "subscription" {
		return ErrRetailSettlement
	}
	if cost.Kind == "attempt" && (cost.AttemptID <= 0 || cost.ChannelID <= 0 || cost.Period != "") {
		return ErrRetailSettlement
	}
	if cost.Kind == "subscription" && (cost.AttemptID != 0 || cost.ChannelID <= 0 || strings.TrimSpace(cost.Period) == "" || len(cost.Period) > 40) {
		return ErrRetailSettlement
	}
	if cost.Kind == "subscription" {
		// 固定订阅成本只能归属于明确登记的自营供货，不能落到客户私有渠道。
		cfg, err := retail_setting.Load()
		if err != nil {
			return err
		}
		valid := false
		for _, supply := range cfg.Supplies {
			if supply.ChannelID == cost.ChannelID && supply.Kind == "subscription" {
				valid = true
			}
		}
		if !valid {
			return ErrRetailSettlement
		}
	}
	cost.ID = 0
	cost.CreatedAt = time.Now().Unix()
	return DB.Transaction(func(tx *gorm.DB) error {
		var previous RetailCost
		if err := tx.First(&previous, "reference = ?", cost.Reference).Error; err == nil {
			if previous.Kind == cost.Kind && previous.AttemptID == cost.AttemptID && previous.ChannelID == cost.ChannelID && previous.Currency == cost.Currency && previous.AmountMinor == cost.AmountMinor && previous.Period == cost.Period {
				return nil
			}
			return ErrRetailSettlement
		} else if err != gorm.ErrRecordNotFound {
			return err
		}
		if cost.Kind == "attempt" {
			var attempt RetailAttempt
			if err := lockForUpdate(tx).First(&attempt, cost.AttemptID).Error; err != nil {
				return err
			}
			if attempt.CostState == "confirmed" || attempt.ChannelID != cost.ChannelID {
				return ErrRetailSettlement
			}
			if err := tx.Model(&attempt).Update("cost_state", "confirmed").Error; err != nil {
				return err
			}
		}
		return tx.Create(&cost).Error
	})
}
