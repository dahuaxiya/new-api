package model

import (
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

const PaymentProviderAlipayOfficial = "alipay_official"

var ErrWalletPayment = common.NewMessage("Wallet payment could not be verified")

// WalletPayment 固定订单金额和入账额度，后续改兑换比例不影响已签发订单。
// 独立表避免其他支付通道或后台通用补单绕过官方支付宝的核验。
type WalletPayment struct {
	OrderID    string  `json:"order_id" gorm:"primaryKey;type:varchar(40)"`
	UserID     int     `json:"-" gorm:"index"`
	Merchant   string  `json:"-" gorm:"type:varchar(120);index"`
	TradeKey   *string `json:"-" gorm:"type:varchar(160);uniqueIndex"`
	PriceCents int64   `json:"price_cents"`
	Quota      int     `json:"quota"`
	Status     string  `json:"status" gorm:"type:varchar(24);index"`
	CreatedAt  int64   `json:"created_at" gorm:"index"`
	ExpiresAt  int64   `json:"expires_at"`
}

// CreateWalletPayment 在同一事务建立核验订单和钱包历史；浏览器只负责跳转，不能入账。
func CreateWalletPayment(userID int, merchant string, cents int64, quota int, displayAmount int64) (*WalletPayment, error) {
	if userID <= 0 || merchant == "" || cents < 1 || cents > 10000000 || quota <= 0 || quota > common.MaxWalletQuota {
		return nil, ErrWalletPayment
	}
	if err := ValidateTopUpQuotaCapacity(userID, quota); err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	order := &WalletPayment{OrderID: "WP" + uuid.NewString(), UserID: userID, Merchant: merchant, PriceCents: cents, Quota: quota, Status: "pending", CreatedAt: now, ExpiresAt: now + 1800}
	err := DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(order).Error; err != nil {
			return err
		}
		return tx.Create(&TopUp{UserId: userID, Amount: displayAmount, Money: float64(cents) / 100, TradeNo: order.OrderID, PaymentMethod: PaymentProviderAlipayOfficial, PaymentProvider: PaymentProviderAlipayOfficial, CreateTime: now, Status: common.TopUpStatusPending}).Error
	})
	return order, err
}

// ApplyWalletPayment 仅接受已验签的商户与金额；交易号唯一、订单锁和余额条件更新共同防重复入账。
// 退款通知转人工核对，不用余额扣回猜测退款结果；退款先到时禁止后来支付通知自动入账。
func ApplyWalletPayment(id, merchant, trade string, cents int64, review bool) error {
	if id == "" || merchant == "" || trade == "" || len(trade) > 160 {
		return ErrWalletPayment
	}
	userID := 0
	err := DB.Transaction(func(tx *gorm.DB) error {
		var order WalletPayment
		if err := lockForUpdate(tx).First(&order, "order_id = ?", id).Error; err != nil {
			return ErrWalletPayment
		}
		if order.Merchant != merchant || order.PriceCents != cents || order.TradeKey != nil && *order.TradeKey != trade {
			return ErrWalletPayment
		}
		if review {
			if err := tx.Model(&order).Updates(map[string]any{"status": "paid_review", "trade_key": trade}).Error; err != nil {
				return err
			}
			return tx.Model(&TopUp{}).Where("trade_no = ? AND payment_provider = ?", id, PaymentProviderAlipayOfficial).Update("status", "paid_review").Error
		}
		if order.Status == "paid" {
			return nil
		}
		if order.Status != "pending" {
			return ErrWalletPayment
		}
		// 唯一流水先写入，后面的金额更新失败时由事务一起回滚。
		if err := tx.Model(&order).Updates(map[string]any{"status": "paid", "trade_key": trade}).Error; err != nil {
			return err
		}
		if err := creditTopUpQuota(tx, order.UserID, order.Quota, nil); err != nil {
			return err
		}
		result := tx.Model(&TopUp{}).Where("trade_no = ? AND payment_provider = ? AND status = ?", id, PaymentProviderAlipayOfficial, common.TopUpStatusPending).Updates(map[string]any{"status": common.TopUpStatusSuccess, "complete_time": time.Now().Unix()})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrWalletPayment
		}
		userID = order.UserID
		return nil
	})
	if err == nil && userID != 0 {
		_ = invalidateUserCache(userID)
	}
	return err
}

// GetWalletPayment 限定订单所有者，查询接口不得根据客户端订单号跨用户操作。
func GetWalletPayment(userID int, id string) (*WalletPayment, error) {
	var order WalletPayment
	err := DB.First(&order, "order_id = ? AND user_id = ?", id, userID).Error
	return &order, err
}

// PendingWalletPayments 分页扫描七日内未决订单；关闭新销售后仍需要处理已付款订单。
func PendingWalletPayments(merchant, cursor string) ([]WalletPayment, error) {
	var orders []WalletPayment
	err := DB.Where("merchant = ? AND status = ? AND order_id > ? AND created_at > ?", merchant, "pending", cursor, time.Now().Unix()-7*86400).Order("order_id").Limit(25).Find(&orders).Error
	return orders, err
}
