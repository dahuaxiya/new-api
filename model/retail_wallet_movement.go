package model

import (
	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"time"
)

// RetailWalletMovement 保存原业务单据没有逐笔记录的变动；不保存支付账号或兑换码。
type RetailWalletMovement struct {
	ID        int64  `gorm:"primaryKey"`
	UserID    int    `gorm:"index:idx_wallet_owner_time,priority:1"`
	CreatedAt int64  `gorm:"index:idx_wallet_owner_time,priority:2"`
	Kind      string `gorm:"type:varchar(24)"`
	Reference string `gorm:"type:varchar(255)"`
	Delta     int64
	Balance   int64
}

// recordRetailMovement 仅供持有用户余额写锁的业务事务调用；插入失败必须回滚原操作。
func recordRetailMovement(tx *gorm.DB, owner int, kind, reference string, delta, balance int) error {
	return tx.Create(&RetailWalletMovement{UserID: owner, CreatedAt: time.Now().Unix(), Kind: kind, Reference: reference, Delta: int64(delta), Balance: int64(balance)}).Error
}

// creditOrderWithReceipt 固化其他支付通道实际到账额度，不能用订单金额和事后汇率推算。
func creditOrderWithReceipt(tx *gorm.DB, order *TopUp, quota int, fields map[string]any) error {
	if err := creditTopUpQuota(tx, order.UserId, quota, fields); err != nil {
		return err
	}
	if !common.IsRetailSite() {
		return nil
	}
	var user User
	if err := tx.Select("quota").First(&user, order.UserId).Error; err != nil {
		return err
	}
	return recordRetailMovement(tx, order.UserId, "other_topup", order.TradeNo, quota, user.Quota)
}
