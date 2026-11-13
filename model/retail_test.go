package model

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/stretchr/testify/require"
)

// TestRetailDatabases 在真实三方言验证发布版升级、权限隔离、并发预留、退款与支付幂等。
func TestRetailDatabases(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := useVM2APIEnrollmentDatabase(t, dialect)
			require.NoError(t, migrateDB())
			oldRedis, oldBatch := common.RedisEnabled, common.BatchUpdateEnabled
			common.RedisEnabled, common.BatchUpdateEnabled = false, false
			t.Cleanup(func() { common.RedisEnabled, common.BatchUpdateEnabled = oldRedis, oldBatch })
			user := User{Username: "retail-customer", AffCode: "retail", Status: common.UserStatusEnabled, Group: "acct_12345678901234567890123456789012", Quota: 1000}
			require.NoError(t, db.Create(&user).Error)
			legacy := Token{UserId: user.Id, Key: "legacy-key", Status: 1, HostingSeatID: "existing-seat", ExpiredTime: -1, RemainQuota: 1000}
			require.NoError(t, db.Create(&legacy).Error)
			// 删除本次增量得到发布版 Token 结构，再升级两次，保留原 Key 和唯一索引。
			require.NoError(t, db.Migrator().DropColumn(&Token{}, "service_mode"))
			require.NoError(t, db.Migrator().DropTable(&WalletPayment{}, &RetailCharge{}, &RetailAttempt{}, &RetailCost{}))
			require.NoError(t, migrateDB())
			require.NoError(t, migrateDB())
			require.NoError(t, db.First(&legacy, legacy.Id).Error)
			require.Equal(t, "existing-seat", legacy.HostingSeatID)
			require.Empty(t, legacy.ServiceMode)
			require.Error(t, db.Create(&Token{Key: legacy.Key}).Error)
			require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1,"retail":1}`))
			cfg := filepath.Join(t.TempDir(), "retail.json")
			require.NoError(t, os.WriteFile(cfg, []byte(`{"enabled":true,"group":"retail","supplies":[{"channel_id":101,"kind":"subscription"},{"channel_id":102,"kind":"reseller"}]}`), 0600))
			t.Setenv("RETAIL_RELAY_CONFIG", cfg)
			for _, ch := range []Channel{{Id: 101, Type: 1, Name: "owner", Group: "retail", Status: 1, Key: "private-owner-key"}, {Id: 102, Type: 1, Name: "customer", Group: "retail,acct_private", Status: 1, Key: "private-customer-key"}} {
				require.NoError(t, db.Create(&ch).Error)
			}
			ids, err := RetailSupplyIDs("retail")
			require.NoError(t, err)
			require.Equal(t, []int{101}, ids)
			require.NoError(t, db.Model(&Channel{}).Where("id = ?", 101).Update("type", 3).Error)
			ids, err = RetailSupplyIDs("retail")
			require.NoError(t, err)
			require.Empty(t, ids, "未验收的适配器不能加入商业供货")
			require.NoError(t, db.Model(&Channel{}).Where("id = ?", 101).Update("type", 1).Error)
			_, err = NormalizeTokenService(user.Id, "retail", "existing-seat")
			require.Error(t, err)
			_, err = NormalizeTokenService(user.Id, "retail", "")
			require.NoError(t, err)
			token := Token{UserId: user.Id, Key: "retail-key", Status: 1, ServiceMode: "retail", ExpiredTime: -1, RemainQuota: 1000}
			require.NoError(t, db.Create(&token).Error)
			group, err := RetailTokenGroup(user.Id, token.Id)
			require.NoError(t, err)
			require.Equal(t, "retail", group)
			_, err = RetailTokenGroup(user.Id+1, token.Id)
			require.Error(t, err)
			require.NoError(t, db.Model(&token).Update("status", 2).Error)
			_, err = RetailTokenGroup(user.Id, token.Id)
			require.Error(t, err)
			require.NoError(t, db.Model(&token).Update("status", 1).Error)
			// 两笔各 700 的并发预留最多成功一笔；失败事务不留下账单或半边扣费。
			var wg sync.WaitGroup
			results := make(chan string, 2)
			for _, id := range []string{"request-a", "request-b"} {
				wg.Go(func() {
					if ReserveRetailCharge(id, user.Id, token.Id, 700, `{"price":1}`) == nil {
						results <- id
					}
				})
			}
			wg.Wait()
			close(results)
			winning := []string{}
			for id := range results {
				winning = append(winning, id)
			}
			require.Len(t, winning, 1)
			require.NoError(t, ChangeRetailCharge(winning[0], user.Id, token.Id, 200, "settled"))
			require.NoError(t, ChangeRetailCharge(winning[0], user.Id, token.Id, 200, "settled"))
			require.Error(t, ChangeRetailCharge(winning[0], user.Id, token.Id, 0, "refunded"))
			require.NoError(t, db.First(&user, user.Id).Error)
			require.Equal(t, 800, user.Quota)
			require.NoError(t, db.First(&token, token.Id).Error)
			require.Equal(t, 800, token.RemainQuota)
			require.Equal(t, 200, token.UsedQuota)
			require.NoError(t, ReserveRetailCharge("zero-delta", user.Id, token.Id, 100, "{}"))
			require.NoError(t, ChangeRetailCharge("zero-delta", user.Id, token.Id, 100, "settled"))
			var charge RetailCharge
			require.NoError(t, db.First(&charge, "id = ?", "zero-delta").Error)
			require.Equal(t, "settled", charge.Status)
			require.NoError(t, ReserveRetailCharge("crash-reservation", user.Id, token.Id, 100, "{}"))
			require.NoError(t, ChangeRetailCharge("crash-reservation", user.Id, token.Id, 0, "refunded"))
			require.NoError(t, ChangeRetailCharge("crash-reservation", user.Id, token.Id, 0, "refunded"))
			order, err := CreateWalletPayment(user.Id, "merchant-a", 1234, 500, 1)
			require.NoError(t, err)
			require.Error(t, ApplyWalletPayment(order.OrderID, "merchant-b", "trade-a", 1234, false))
			require.Error(t, ApplyWalletPayment(order.OrderID, "merchant-a", "trade-a", 1, false))
			for range 4 {
				wg.Go(func() { _ = ApplyWalletPayment(order.OrderID, "merchant-a", "trade-a", 1234, false) })
			}
			wg.Wait()
			require.NoError(t, ApplyWalletPayment(order.OrderID, "merchant-a", "trade-a", 1234, false))
			require.NoError(t, db.First(&user, user.Id).Error)
			require.Equal(t, 1200, user.Quota)
			other, err := CreateWalletPayment(user.Id, "merchant-a", 1234, 500, 1)
			require.NoError(t, err)
			require.Error(t, ApplyWalletPayment(other.OrderID, "merchant-a", "trade-a", 1234, false))
			require.Error(t, ManualCompleteTopUp(other.OrderID, "127.0.0.1"))
			_, err = GetWalletPayment(user.Id+1, order.OrderID)
			require.Error(t, err)
			require.NoError(t, ApplyWalletPayment(other.OrderID, "merchant-a", "trade-refund", 1234, true))
			require.Error(t, ApplyWalletPayment(other.OrderID, "merchant-a", "trade-refund", 1234, false))
			require.NoError(t, db.First(&user, user.Id).Error)
			require.Equal(t, 1200, user.Quota)
			// 崩溃留下的预留只有超过一天、金额一致且注明人工核对原因才允许退回。
			require.NoError(t, ReserveRetailCharge("manual-review", user.Id, token.Id, 100, "{}"))
			require.Error(t, RefundStaleRetailCharge("manual-review", 1, 100, "Confirmed no running request"))
			require.NoError(t, db.Model(&RetailCharge{}).Where("id = ?", "manual-review").Update("updated_at", time.Now().Unix()-90000).Error)
			require.Error(t, RefundStaleRetailCharge("manual-review", 1, 99, "Confirmed no running request"))
			require.NoError(t, RefundStaleRetailCharge("manual-review", 1, 100, "Confirmed no running request"))
			require.NoError(t, RefundStaleRetailCharge("manual-review", 1, 100, "Confirmed no running request"))
			attemptID, err := BeginRetailAttempt("request-cost", 101)
			require.NoError(t, err)
			require.NoError(t, RecordRetailAttemptUsage(attemptID, map[string]int{"input_tokens": 10}))
			require.NoError(t, FinishRetailAttempt(attemptID, 200, "completed"))
			cost := RetailCost{Reference: "invoice-attempt", Kind: "attempt", AttemptID: attemptID, ChannelID: 101, Currency: "USD", AmountMinor: 10, ActorID: 1}
			require.NoError(t, RecordRetailCost(cost))
			require.NoError(t, RecordRetailCost(cost))
			cost.Reference = "duplicate-attempt"
			require.Error(t, RecordRetailCost(cost))
			cost.AttemptID = 0
			require.Error(t, RecordRetailCost(cost))
			cost = RetailCost{Reference: "subscription-invoice", Kind: "subscription", ChannelID: 101, Currency: "USD", AmountMinor: 2000, Period: "2026-11", ActorID: 1}
			require.NoError(t, RecordRetailCost(cost))
			require.NoError(t, RecordRetailCost(cost))
			cost.ChannelID = 102
			require.Error(t, RecordRetailCost(cost))
			var attempt RetailAttempt
			require.NoError(t, db.First(&attempt, attemptID).Error)
			require.Equal(t, "confirmed", attempt.CostState)
			require.NoError(t, migrateDB())
			require.NoError(t, db.First(&user, user.Id).Error)
			require.Equal(t, 1200, user.Quota)
		})
	}
}
