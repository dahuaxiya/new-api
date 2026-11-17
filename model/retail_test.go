package model

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/retailprice"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
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

// TestRetailPricingDatabases verifies upgrade, CNY/USD rules, quote freezing and atomic budgets on real engines.
func TestRetailPricingDatabases(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := useVM2APIEnrollmentDatabase(t, dialect)
			require.NoError(t, migrateDB())
			// Reconstruct the latest release's charge columns, keep a real legacy reservation, then upgrade twice.
			user := User{Username: "price-user", AffCode: "price", Group: "default", Status: 1, Quota: 1000000}
			require.NoError(t, db.Create(&user).Error)
			t.Setenv("SITE_MODE", "retail")
			studio, err := RetailStudioToken(user.Id, true)
			require.NoError(t, err)
			reopened, err := RetailStudioToken(user.Id, true)
			require.NoError(t, err)
			assert.Equal(t, studio.Id, reopened.Id)
			_, err = ValidateUserToken(studio.Key)
			require.Error(t, err)
			token := Token{UserId: user.Id, Key: "price-key", ServiceMode: "retail", Status: 1, ExpiredTime: -1, RemainQuota: 1000000}
			require.NoError(t, db.Create(&token).Error)
			visible, err := GetAllUserTokens(user.Id, 0, 100)
			require.NoError(t, err)
			require.Len(t, visible, 1)
			assert.Equal(t, token.Id, visible[0].Id)
			count, err := CountUserTokens(user.Id)
			require.NoError(t, err)
			assert.EqualValues(t, 1, count)
			visible, count, err = SearchUserTokens(user.Id, "", "", 0, 100)
			require.NoError(t, err)
			assert.EqualValues(t, 1, count)
			require.Len(t, visible, 1)
			_, err = GetTokenByIds(studio.Id, user.Id)
			require.Error(t, err)
			deleted, err := BatchDeleteTokens([]int{studio.Id}, user.Id)
			require.NoError(t, err)
			assert.Zero(t, deleted)
			require.Error(t, DeleteTokenById(studio.Id, user.Id))
			keys, err := GetTokenKeysByIds([]int{studio.Id}, user.Id)
			require.NoError(t, err)
			assert.Empty(t, keys)
			require.NoError(t, ReserveRetailCharge("legacy-before-upgrade", user.Id, token.Id, 50, "{}"))
			for _, field := range []string{"pricing_version", "wallet_cny_per_unit", "reserved_cny", "actual_cny", "budget_id"} {
				require.NoError(t, db.Migrator().DropColumn(&RetailCharge{}, field))
			}
			require.NoError(t, db.Migrator().DropTable(&RetailPriceState{}, &RetailPriceVersion{}, &RetailKeyPricePolicy{}, &RetailPriceBudget{}))
			require.NoError(t, migrateDB())
			require.NoError(t, migrateDB())
			require.NoError(t, ChangeRetailCharge("legacy-before-upgrade", user.Id, token.Id, 0, "refunded"))
			require.Error(t, db.Create(&Token{Key: token.Key}).Error)
			cfg := filepath.Join(t.TempDir(), "retail.json")
			require.NoError(t, os.WriteFile(cfg, []byte(`{"enabled":true,"group":"retail","supplies":[{"channel_id":101,"kind":"reseller"}]}`), 0600))
			t.Setenv("RETAIL_RELAY_CONFIG", cfg)
			raw, err := os.ReadFile("../pkg/retailprice/testdata/book.json")
			require.NoError(t, err)
			var book retailprice.Book
			require.NoError(t, common.Unmarshal(raw, &book))
			now := time.Now().Unix()
			book.Models[0].VerifiedAt, book.Models[0].ValidUntil = now, now+86400
			book.Rates[0].VerifiedAt, book.Rates[0].ValidUntil = now, now+3600
			version, err := PublishRetailPriceBook(0, book, nil, 1)
			require.NoError(t, err)
			assert.Equal(t, int64(1), version)
			// Published pricebooks use their own products and tariffs. Removing
			// the internal supply group from legacy UI ratios must not block studio.
			previousGroups := ratio_setting.GroupRatio2JSONString()
			require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1,"gpt-normal":1}`))
			routed, routeErr := RetailTokenGroup(user.Id, studio.Id)
			require.NoError(t, routeErr)
			require.Equal(t, "retail", routed)
			_, routeErr = NormalizeTokenService(user.Id, "retail", "")
			require.NoError(t, routeErr)
			require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(previousGroups))

			_, err = PublishRetailPriceBook(0, book, nil, 1)
			require.ErrorIs(t, err, ErrRetailPriceConflict)
			policy := retailprice.KeyPolicy{ProductID: "standard", AcceptFloating: true, Caps: map[string]retailprice.Prices{"gpt-4o": {Input: "4.5", Output: "9", CacheRead: ".45", CacheWrite: "5.625", CacheWrite1h: "9"}}, MaxRequestCNY: "1", DailyBudgetCNY: "1"}
			// Decimal strings have one canonical syntax; shorthand and exponent forms are rejected.
			require.Error(t, policy.Validate())
			cap := policy.Caps["gpt-4o"]
			cap.CacheRead = "0.45"
			policy.Caps["gpt-4o"] = cap
			require.Error(t, PutRetailKeyPolicy(user.Id+1, token.Id, 0, policy))
			require.NoError(t, PutRetailKeyPolicy(user.Id, token.Id, 0, policy))
			quote, err := RetailQuote(user.Id, token.Id, "gpt-4o", 20, []int{101})
			require.NoError(t, err)
			require.NotNil(t, quote)
			assert.Equal(t, "3", quote.Sale[0].Prices.Input)
			assert.Equal(t, "6", quote.Sale[0].Prices.Output)
			amount, err := quote.Charge(100, [5]int{100, 10, 0, 0, 0})
			require.NoError(t, err)
			assert.Equal(t, "0.00036", amount.String())
			cnyBook := book
			cnyBook.Models = append([]retailprice.Model(nil), book.Models...)
			cnyBook.Models[0].Currency = "CNY"
			cnyQuote, err := cnyBook.MakeQuote(1, "default", policy, "gpt-4o", 20, []int{101}, now)
			require.NoError(t, err)
			assert.Equal(t, quote.Sale, cnyQuote.Sale, "CNY must not pass through an FX conversion")
			giftBook := book
			giftBook.ReceiptRatio = "0.9"
			giftBook.FeeBPS = 60
			factor, err := giftBook.SaleFactor("0.24")
			require.NoError(t, err)
			assert.True(t, factor.GreaterThan(decimal.RequireFromString("0.3")))
			_, err = quote.Charge(1001, [5]int{1001, 0, 0, 0, 0})
			require.Error(t, err)
			_, err = ReservePricedRetailCharge("frozen-quote", user.Id, token.Id, quote)
			require.NoError(t, err)
			rates := append([]retailprice.Rate(nil), book.Rates...)
			rates[0].Multiplier = "0.4"
			version, err = PublishRetailPriceBook(1, retailprice.Book{}, rates, 1)
			require.NoError(t, err)
			_, err = RetailQuote(user.Id, token.Id, "gpt-4o", 20, []int{101})
			require.ErrorIs(t, err, retailprice.ErrCap)
			quota, _, err := RetailCNYQuota(amount, quote.WalletCNYPerUnit)
			require.NoError(t, err)
			require.NoError(t, ChangeRetailCharge("frozen-quote", user.Id, token.Id, quota, "settled"))
			require.NoError(t, ChangeRetailCharge("frozen-quote", user.Id, token.Id, quota, "settled"))
			var charge RetailCharge
			require.NoError(t, db.First(&charge, "id = ?", "frozen-quote").Error)
			assert.Equal(t, "0.00036", charge.ActualCNY)
			version, err = PublishRetailPriceBook(version, book, nil, 1)
			require.NoError(t, err)
			policy.DailyBudgetCNY = "0.03"
			require.NoError(t, PutRetailKeyPolicy(user.Id, token.Id, 1, policy))
			var wg sync.WaitGroup
			results := make(chan string, 2)
			for _, id := range []string{"budget-a", "budget-b"} {
				wg.Go(func() {
					q, e := RetailQuote(user.Id, token.Id, "gpt-4o", 20, []int{101})
					if e == nil {
						if _, e = ReservePricedRetailCharge(id, user.Id, token.Id, q); e == nil {
							results <- id
						}
					}
				})
			}
			wg.Wait()
			close(results)
			winners := []string{}
			for id := range results {
				winners = append(winners, id)
			}
			require.Len(t, winners, 1)
			require.NoError(t, ChangeRetailCharge(winners[0], user.Id, token.Id, 0, "refunded"))
			require.NoError(t, ChangeRetailCharge(winners[0], user.Id, token.Id, 0, "refunded"))
			book.Enabled = false
			_, err = PublishRetailPriceBook(version, book, nil, 1)
			require.NoError(t, err)
			_, err = RetailQuote(user.Id, token.Id, "gpt-4o", 20, []int{101})
			require.Error(t, err, "pause must not fall back to legacy pricing")
			require.NoError(t, migrateDB())
			var versions int64
			require.NoError(t, db.Model(&RetailPriceVersion{}).Count(&versions).Error)
			assert.Equal(t, int64(4), versions)
			// Public call groups retain the same transaction and budget checks for
			// accounts outside the legacy group; there is no account reassignment.
			book.Enabled = true
			book.Products[0].Public = true
			book.Products[0].BindingGroup = "shared"
			book.Products[0].UserGroups = nil
			_, err = PublishRetailPriceBook(4, book, nil, 1)
			require.NoError(t, err)
			require.NoError(t, db.Model(&user).Update("group", "another").Error)
			policy.DailyBudgetCNY = "1"
			require.NoError(t, PutRetailKeyPolicy(user.Id, token.Id, 2, policy))
			publicQuote, err := RetailQuote(user.Id, token.Id, "gpt-4o", 20, []int{101})
			require.NoError(t, err)
			require.Equal(t, quote.Sale, publicQuote.Sale)
			_, err = ReservePricedRetailCharge("public-group", user.Id, token.Id, publicQuote)
			require.NoError(t, err)
			require.NoError(t, ChangeRetailCharge("public-group", user.Id, token.Id, quota, "settled"))
			var publicCharge RetailCharge
			require.NoError(t, db.First(&publicCharge, "id = ?", "public-group").Error)
			require.Equal(t, "0.00036", publicCharge.ActualCNY)
			// Group-only mode removes user pricing forms, but actual funds remain
			// atomic: quote, reserve and settle still debit the wallet, and an empty
			// wallet must fail before a charge or reservation ledger can commit.
			groupPolicy := retailprice.KeyPolicy{ProductID: policy.ProductID, GroupCurrent: true}
			require.NoError(t, PutRetailKeyPolicy(user.Id, token.Id, 3, groupPolicy))
			groupQuote, err := RetailQuote(user.Id, token.Id, "gpt-4o", 20, []int{101})
			require.NoError(t, err)
			_, err = ReservePricedRetailCharge("group-current", user.Id, token.Id, groupQuote)
			require.NoError(t, err)
			require.NoError(t, ChangeRetailCharge("group-current", user.Id, token.Id, quota, "settled"))
			var groupCharge RetailCharge
			require.NoError(t, db.First(&groupCharge, "id = ?", "group-current").Error)
			require.Equal(t, "0.00036", groupCharge.ActualCNY)
			require.NoError(t, db.Model(&user).Update("quota", 0).Error)
			_, err = ReservePricedRetailCharge("group-empty-wallet", user.Id, token.Id, groupQuote)
			require.Error(t, err)
			var failedCount int64
			require.NoError(t, db.Model(&RetailCharge{}).Where("id = ?", "group-empty-wallet").Count(&failedCount).Error)
			require.Zero(t, failedCount)

		})
	}
}

// TestRetailGroupBindingsDatabases protects saved assignments, exact costs, price caps and update races on real engines.
func TestRetailGroupBindingsDatabases(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := useVM2APIEnrollmentDatabase(t, dialect)
			require.NoError(t, migrateDB())
			cfg := filepath.Join(t.TempDir(), "retail.json")
			require.NoError(t, os.WriteFile(cfg, []byte(`{"enabled":true,"group":"retail","supplies":[{"channel_id":101,"kind":"reseller"}]}`), 0600))
			t.Setenv("RETAIL_RELAY_CONFIG", cfg)
			endpoint := "https://aixingxing.pro"
			require.NoError(t, db.Create(&Channel{Id: 101, Name: "Test credential", BaseURL: &endpoint, Group: "retail", Status: 1, Type: 1, Key: "synthetic-only"}).Error)
			now := time.Now().Unix()
			binding := retailprice.GroupBinding{LocalGroup: "default", ChannelID: 101, UpstreamGroup: "ChatGPT-pro稳定"}
			feed := &retailprice.PublicRateSnapshot{Source: "https://aixingxing.pro/api/public/rates", UpdatedAt: now, FetchedAt: now, Groups: []retailprice.PublicRate{{Group: binding.UpstreamGroup, Name: "Pro", Multiplier: "0.24"}}}
			ctx := context.Background()
			policy := retailprice.SalePolicy{ReceiptRatio: "1", MarginBPS: 2000}
			version, err := SaveRetailGroupBindings(ctx, 0, nil, false, nil, 1, policy)
			require.NoError(t, err)
			require.EqualValues(t, 1, version)
			// Saving profit policy before any binding or model must survive first publication.
			version, err = PublishRetailPriceBook(version, retailprice.Book{}, nil, 1)
			require.NoError(t, err)
			version, err = SaveRetailGroupBindings(ctx, version, []retailprice.GroupBinding{binding}, true, feed, 1)
			require.NoError(t, err)
			draft, version, err := GetRetailPriceBook()
			require.NoError(t, err)
			assert.False(t, draft.Enabled, "saving a binding cannot open sales")
			assert.Empty(t, draft.Models, "following economics need no catalog or file")
			assert.Equal(t, 2000, draft.MarginBPS)
			assert.True(t, draft.SalePolicyManaged)
			for _, invalid := range []retailprice.SalePolicy{
				{ReceiptRatio: "0"}, {ReceiptRatio: "1.1"},
				{ReceiptRatio: "1", MarginBPS: -1}, {ReceiptRatio: "1", MarginBPS: 9501},
				{ReceiptRatio: "1", FeeBPS: 5001}, {ReceiptRatio: "1", BufferBPS: 10001},
			} {
				_, err := SaveRetailGroupBindings(ctx, version, []retailprice.GroupBinding{binding}, true, feed, 1, invalid)
				require.ErrorIs(t, err, retailprice.ErrConfig)
			}
			assert.Equal(t, []retailprice.GroupBinding{binding}, draft.GroupBindings)
			require.NoError(t, migrateDB())
			require.NoError(t, migrateDB())
			bad := binding
			bad.ChannelID = 102
			_, err = SaveRetailGroupBindings(ctx, version, []retailprice.GroupBinding{bad}, true, feed, 1)
			require.Error(t, err)
			bad = binding
			bad.LocalGroup = "unknown"
			_, err = SaveRetailGroupBindings(ctx, version, []retailprice.GroupBinding{bad}, true, feed, 1)
			require.Error(t, err)
			bad = binding
			bad.LocalGroup = "existing"
			bad.UpstreamGroup = "other"
			_, err = SaveRetailGroupBindings(ctx, version, []retailprice.GroupBinding{binding, bad}, true, feed, 1)
			require.Error(t, err)
			raw, err := os.ReadFile("../pkg/retailprice/testdata/book.json")
			require.NoError(t, err)
			var book retailprice.Book
			require.NoError(t, common.Unmarshal(raw, &book))
			book.Models[0].VerifiedAt, book.Models[0].ValidUntil = now, now+86400
			book.Rates[0].VerifiedAt, book.Rates[0].ValidUntil = now, now+3600
			// The ordinary price editor must not be able to drop protected bindings or forge a public snapshot.
			book.MarginBPS = 0
			version, err = PublishRetailPriceBook(version, book, nil, 1)
			require.NoError(t, err)
			book, version, err = GetRetailPriceBook()
			require.NoError(t, err)
			require.Len(t, book.GroupBindings, 1)
			assert.Equal(t, 2000, book.MarginBPS, "catalog publication preserves following margin")
			key := retailprice.KeyPolicy{ProductID: "standard", AcceptFloating: true, Caps: map[string]retailprice.Prices{"gpt-4o": {Input: "5", Output: "10", CacheRead: "5", CacheWrite: "10", CacheWrite1h: "10"}}, MaxRequestCNY: "10", DailyBudgetCNY: "10"}
			quote, err := book.MakeQuote(version, "default", key, "gpt-4o", 100, []int{101}, now)
			require.NoError(t, err)
			assert.Equal(t, "3", quote.Sale[0].Prices.Input)
			// A margin-only save changes future quotes, while accepted quotes stay frozen.
			policy.MarginBPS = 2500
			version, err = SaveRetailGroupBindings(ctx, version, []retailprice.GroupBinding{binding}, true, feed, 1, policy)
			require.NoError(t, err)
			book, _, err = GetRetailPriceBook()
			require.NoError(t, err)
			repriced, err := book.MakeQuote(version, "default", key, "gpt-4o", 100, []int{101}, now)
			require.NoError(t, err)
			assert.Equal(t, "3.2", repriced.Sale[0].Prices.Input)
			assert.Equal(t, "3", quote.Sale[0].Prices.Input)
			// Same rates renew freshness without bumping price versions or breaking requests already admitted.
			renewal := *feed
			renewal.FetchedAt = now + 1
			same, err := SyncRetailGroupRates(ctx, version, &renewal, false)
			require.NoError(t, err)
			assert.Equal(t, version, same)
			renewed, _, err := GetRetailPriceBook()
			require.NoError(t, err)
			assert.Equal(t, 2500, renewed.MarginBPS, "feed refresh retains margin")
			// A source increase wins over the old manual rate and hits the user's accepted ceiling.
			raised := *feed
			raised.FetchedAt = now + 2
			raised.Groups = []retailprice.PublicRate{{Group: binding.UpstreamGroup, Name: "Pro", Multiplier: "0.8"}}
			version, err = SyncRetailGroupRates(ctx, version, &raised, false)
			require.NoError(t, err)
			book, _, err = GetRetailPriceBook()
			require.NoError(t, err)
			_, err = book.MakeQuote(version, "default", key, "gpt-4o", 100, []int{101}, now)
			require.ErrorIs(t, err, retailprice.ErrCap)
			assert.Equal(t, "3", quote.Sale[0].Prices.Input, "earlier customer quote stays frozen")
			// A failure is durable and cannot silently reuse either cached public data or manual costs.
			version, err = SyncRetailGroupRates(ctx, version, nil, true)
			require.NoError(t, err)
			book, _, err = GetRetailPriceBook()
			require.NoError(t, err)
			_, err = book.MakeQuote(version, "default", key, "gpt-4o", 100, []int{101}, now)
			require.ErrorIs(t, err, retailprice.ErrExpired)
			recovered := *feed
			recovered.FetchedAt = now + 3
			version, err = SyncRetailGroupRates(ctx, version, &recovered, false)
			require.NoError(t, err)
			book, _, err = GetRetailPriceBook()
			require.NoError(t, err)
			_, err = book.MakeQuote(version, "default", key, "gpt-4o", 100, []int{101}, now+301)
			require.ErrorIs(t, err, retailprice.ErrExpired)
			missing := recovered
			missing.FetchedAt = now + 4
			missing.Groups = []retailprice.PublicRate{{Group: "another", Name: "Another", Multiplier: "0.01"}}
			version, err = SyncRetailGroupRates(ctx, version, &missing, false)
			require.NoError(t, err)
			book, _, err = GetRetailPriceBook()
			require.NoError(t, err)
			_, err = book.MakeQuote(version, "default", key, "gpt-4o", 100, []int{101}, now)
			require.ErrorIs(t, err, retailprice.ErrExpired)
			_, err = SaveRetailGroupBindings(ctx, version, nil, false, nil, 1)
			require.Error(t, err, "in-use binding cannot be removed to restore manual prices")
			// Two editors see the same version: exactly one complete configuration may be committed.
			var wg sync.WaitGroup
			results := make(chan error, 2)
			for range 2 {
				wg.Go(func() {
					_, err := SaveRetailGroupBindings(ctx, version, []retailprice.GroupBinding{binding}, false, feed, 1)
					results <- err
				})
			}
			wg.Wait()
			close(results)
			success := 0
			for err := range results {
				if err == nil {
					success++
				} else {
					require.ErrorIs(t, err, ErrRetailPriceConflict)
				}
			}
			assert.Equal(t, 1, success)
			book, _, err = GetRetailPriceBook()
			require.NoError(t, err)
			assert.False(t, book.PublicRateSync.Enabled)
			assert.Equal(t, 2500, book.MarginBPS, "older clients omitting policy preserve saved economics")
			_, err = book.MakeQuote(version, "default", key, "gpt-4o", 100, []int{101}, now)
			require.ErrorIs(t, err, retailprice.ErrExpired)
		})
	}
}
