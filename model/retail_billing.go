package model

import (
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

var ErrRetailQuota = common.NewMessage("Retail balance or API key quota is insufficient")
var ErrRetailSettlement = common.NewMessage("Retail settlement requires review")

// RetailCharge 是钱包和 Key 同事务的持久化凭据；进程退出后保留预留，供管理员核对。
// 请求 ID 由服务器生成，只用于防重复结算，不承诺客户端请求重放去重。
type RetailCharge struct {
	PricingVersion   int64  `json:"pricing_version"`
	WalletCNYPerUnit string `json:"wallet_cny_per_unit" gorm:"type:varchar(40)"`
	ReservedCNY      string `json:"reserved_cny" gorm:"type:varchar(40)"`
	ActualCNY        string `json:"actual_cny" gorm:"type:varchar(40)"`
	BudgetID         string `json:"-" gorm:"type:varchar(64)"`
	ReviewedBy       int    `json:"reviewed_by"`
	ReviewReason     string `json:"review_reason" gorm:"type:text"`
	ID               string `json:"id" gorm:"primaryKey;type:varchar(80)"`
	UserID           int    `json:"user_id" gorm:"index"`
	TokenID          int    `json:"token_id"`
	Reserved         int    `json:"reserved"`
	Actual           int    `json:"actual"`
	Status           string `json:"status" gorm:"type:varchar(24);index"`
	PriceSnapshot    string `json:"-" gorm:"type:text"`
	CreatedAt        int64  `json:"created_at" gorm:"index"`
	UpdatedAt        int64  `json:"updated_at"`
}

// ReserveRetailCharge 同时检查并预留钱包及 Key 额度，任何一步失败均回滚。
// 不使用异步批量余额写回，避免进程退出丢失预留来源。
func ReserveRetailCharge(id string, userID, tokenID, quota int, snapshot string) error {
	if id == "" || len(id) > 80 || userID <= 0 || tokenID <= 0 || quota < 0 || quota > common.MaxWalletQuota {
		return ErrRetailQuota
	}
	err := DB.Transaction(func(tx *gorm.DB) error {
		now := time.Now().Unix()
		charge := RetailCharge{ID: id, UserID: userID, TokenID: tokenID, Reserved: quota, Status: "reserved", PriceSnapshot: snapshot, CreatedAt: now, UpdatedAt: now}
		if err := tx.Create(&charge).Error; err != nil {
			return err
		}
		return applyRetailDelta(tx, userID, tokenID, quota)
	})
	invalidateRetailCaches(userID, tokenID)
	return err
}

// applyRetailDelta 在调用方事务中调整余额；正数预扣必须满足余额及 Key 限额，负数只用于已持久化预留退款。
func applyRetailDelta(tx *gorm.DB, userID, tokenID, delta int) error {
	if delta == 0 {
		return nil
	}
	wallet := tx.Model(&User{}).Where("id = ?", userID)
	if delta > 0 {
		wallet = wallet.Where("quota >= ?", delta)
	} else {
		wallet = wallet.Where("quota <= ?", common.MaxWalletQuota+delta)
	}
	result := wallet.Update("quota", gorm.Expr("quota - ?", delta))
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrRetailQuota
	}
	token := tx.Unscoped().Model(&Token{}).Where("id = ? AND user_id = ?", tokenID, userID)
	if delta > 0 {
		token = token.Where("unlimited_quota = ? OR remain_quota >= ?", true, delta)
	}
	result = token.Updates(map[string]any{"remain_quota": gorm.Expr("remain_quota - ?", delta), "used_quota": gorm.Expr("used_quota + ?", delta), "accessed_time": time.Now().Unix()})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrRetailQuota
	}
	return nil
}

// ChangeRetailCharge 追加预留或完成结算；状态锁保证重复完成、退款竞争最多一次影响余额。
// actual 大于可扣额度时保留预留并报告待核对，不制造负余额或把失败结算误当退款。
func ChangeRetailCharge(id string, userID, tokenID, target int, finalStatus string) error {
	if target < 0 || target > common.MaxWalletQuota || (finalStatus != "" && finalStatus != "settled" && finalStatus != "refunded") || finalStatus == "refunded" && target != 0 {
		return ErrRetailSettlement
	}
	var pricing RetailCharge
	if err := DB.Select("pricing_version").First(&pricing, "id = ? AND user_id = ? AND token_id = ?", id, userID, tokenID).Error; err != nil {
		return err
	}
	err := DB.Transaction(func(tx *gorm.DB) error {
		if pricing.PricingVersion > 0 {
			if _, err := lockRetailPriceState(tx); err != nil {
				return err
			}
		}
		var charge RetailCharge
		if err := lockForUpdate(tx).First(&charge, "id = ? AND user_id = ? AND token_id = ?", id, userID, tokenID).Error; err != nil {
			return err
		}
		if charge.Status != "reserved" {
			if charge.Status == finalStatus && charge.Actual == target {
				return nil
			}
			return ErrRetailSettlement
		}
		if finalStatus == "" && target <= charge.Reserved {
			return nil
		}
		actualCNY := ""
		if charge.PricingVersion > 0 {
			// A frozen quote never silently expands its monetary reservation after execution.
			if finalStatus == "" {
				return ErrRetailSettlement
			}
			var err error
			actualCNY, err = settleRetailPriceBudget(tx, charge, target)
			if err != nil {
				return err
			}
		}
		if err := applyRetailDelta(tx, userID, tokenID, target-charge.Reserved); err != nil {
			return err
		}
		updates := map[string]any{"updated_at": time.Now().Unix()}
		if finalStatus == "" {
			updates["reserved"] = target
		} else {
			updates["status"] = finalStatus
			updates["actual"] = target
			if charge.PricingVersion > 0 {
				updates["actual_cny"] = actualCNY
			}
		}
		return tx.Model(&charge).Updates(updates).Error
	})
	invalidateRetailCaches(userID, tokenID)
	return err
}

// invalidateRetailCaches 金额以数据库事务为准；删除缓存让后续读者重新载入，不向缓存重复加减金额。
func invalidateRetailCaches(userID, tokenID int) {
	_ = invalidateUserCache(userID)
	var token Token
	if DB.Unscoped().Select("id", "key").First(&token, tokenID).Error == nil {
		_ = invalidateTokenCacheForMutation(token.Key)
	}
}
