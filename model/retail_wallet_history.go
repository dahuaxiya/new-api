package model

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

// saveRetailBalanceSnapshot must run in the same transaction as the quota update;
// the destination and column are chosen exclusively by the caller, never by a request.
func saveRetailBalanceSnapshot(tx *gorm.DB, record any, userID int, column string) error {
	var owner User
	if err := lockForUpdate(tx).Select("quota").First(&owner, userID).Error; err != nil {
		return err
	}
	return tx.Model(record).UpdateColumn(column, owner.Quota).Error
}

// reserveRetailWithBalance records the actual wallet result only after both wallet and API-key reservations succeed.
func reserveRetailWithBalance(tx *gorm.DB, charge *RetailCharge) error {
	if err := applyRetailDelta(tx, charge.UserID, charge.TokenID, charge.Reserved); err != nil {
		return err
	}
	return saveRetailBalanceSnapshot(tx, charge, charge.UserID, "held_balance")
}

// RetailWalletEvent 只暴露账务凭据与数额，历史余额未知时保持 nil。
type RetailWalletEvent struct {
	Reference string `json:"reference"`
	Kind      string `json:"kind"`
	At        int64  `json:"at"`
	Delta     int64  `json:"delta"`
	Actual    *int64 `json:"actual"`
	After     *int64 `json:"after" gorm:"column:balance_after"`
	Source    int    `json:"source"`
	EventKey  string `json:"key"`
}
type RetailWalletHistory struct {
	Available    int                 `json:"available"`
	Pending      int64               `json:"pending"`
	Today        int64               `json:"today"`
	Events       []RetailWalletEvent `json:"events"`
	More         bool                `json:"more"`
	QuotaPerUnit float64             `json:"quota_per_unit"`
}

// ReadRetailWalletHistory 按上海自然日读取，翻页键含来源和单据号，避免同秒交易被跳过。
// userID 必须来自会话；所有分支按单据所有者过滤，历史余额不得根据当前余额倒推。
func ReadRetailWalletHistory(ctx context.Context, userID, days int, now time.Time, before int64, source int, key string) (RetailWalletHistory, error) {
	result := RetailWalletHistory{Events: []RetailWalletEvent{}, QuotaPerUnit: common.QuotaPerUnit}
	if userID <= 0 || (days != 1 && days != 7 && days != 30) || before < 0 || source < 0 || source > 5 || len(key) > 255 {
		return result, errors.New("invalid wallet history query")
	}
	local := now.In(time.FixedZone("CST", 8*60*60))
	midnight := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, local.Location())
	since := midnight.AddDate(0, 0, 1-days).Unix()
	opts := &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead}
	if DB.Dialector.Name() == "sqlite" {
		opts = &sql.TxOptions{}
	}
	err := DB.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		var user User
		if err := db.Select("id", "quota").First(&user, userID).Error; err != nil {
			return err
		}
		result.Available = user.Quota
		if err := db.Model(&RetailCharge{}).Where("user_id = ? AND status = ?", userID, "reserved").Select("COALESCE(SUM(reserved),0)").Scan(&result.Pending).Error; err != nil {
			return err
		}
		if err := db.Model(&RetailCharge{}).Where("user_id = ? AND status = ? AND updated_at >= ? AND updated_at < ?", userID, "settled", midnight.Unix(), midnight.AddDate(0, 0, 1).Unix()).Select("COALESCE(SUM(actual),0)").Scan(&result.Today).Error; err != nil {
			return err
		}
		// 固定 SQL 片段，类型转换只取决于受支持的方言。客户端输入全部绑定，绝不拼入 SQL。
		query := `SELECT * FROM (
 SELECT id AS reference, CASE WHEN held_balance IS NULL AND pricing_version = 0 THEN 'hold_total' ELSE 'hold' END AS kind,created_at AS at,-COALESCE(initial_hold,reserved) AS delta,NULL AS actual,held_balance AS balance_after,1 AS source,id AS event_key FROM retail_charges WHERE user_id=? AND created_at>=? AND COALESCE(initial_hold,reserved)>0
 UNION ALL SELECT id,CASE WHEN status='refunded' THEN 'release' ELSE 'settle' END,updated_at,reserved-actual,actual,final_balance,5,id FROM retail_charges WHERE user_id=? AND updated_at>=? AND status IN ('refunded','settled')
 UNION ALL SELECT wp.order_id,'topup',tu.complete_time,wp.quota,NULL,wp.balance_after,2,wp.order_id FROM wallet_payments wp JOIN top_ups tu ON tu.trade_no=wp.order_id AND tu.user_id=wp.user_id WHERE wp.user_id=? AND tu.complete_time>=? AND tu.payment_provider=?
 UNION ALL SELECT CAST(id AS TEXT),'redeem',redeemed_time,quota,NULL,balance_after,3,CAST(id AS TEXT) FROM redemptions WHERE used_user_id=? AND redeemed_time>=? AND status=?
 UNION ALL SELECT reference,kind,created_at,delta,NULL,balance,4,CAST(id AS TEXT) FROM retail_wallet_movements WHERE user_id=? AND created_at>=?
 ) w`
		if db.Dialector.Name() == "mysql" {
			// 旧表与新版校验表可能采用不同的排序规则；UNION 与后续游标比较统一按标识的二进制顺序。
			query = strings.NewReplacer(" AS TEXT)", " AS CHAR)", "id AS reference", "CONVERT(id USING utf8mb4) COLLATE utf8mb4_bin AS reference", "END AS kind", "END COLLATE utf8mb4_bin AS kind", "id AS event_key", "CONVERT(id USING utf8mb4) COLLATE utf8mb4_bin AS event_key").Replace(query)
		}
		args := []any{userID, since, userID, since, userID, since, PaymentProviderAlipayOfficial, userID, since, common.RedemptionCodeStatusUsed, userID, since}
		if before > 0 {
			query += " WHERE (at < ? OR (at = ? AND (source < ? OR (source = ? AND event_key < ?))))"
			args = append(args, before, before, source, source, key)
		}
		const limit = 100
		if err := db.Raw(query+" ORDER BY at DESC, source DESC, event_key DESC LIMIT 101", args...).Scan(&result.Events).Error; err != nil {
			return err
		}
		if len(result.Events) > limit {
			result.More = true
			result.Events = result.Events[:limit]
		}
		return nil
	}, opts)
	return result, err
}
