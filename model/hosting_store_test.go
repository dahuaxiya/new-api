package model

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// TestHostingStoreDatabases 验证实际三方言的初始化、升级、库存竞争和支付事务，不连接生产库。
func TestHostingStoreDatabases(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := useVM2APIEnrollmentDatabase(t, dialect)
			require.NoError(t, migrateDB())
			plans, err := ListHostingPlans()
			require.NoError(t, err)
			require.Len(t, plans, 3)
			require.Equal(t, int64(4000), plans[0].PriceCents)
			require.False(t, plans[0].Enabled)
			user := User{Username: "store-owner", AffCode: "store-owner", Role: common.RoleCommonUser, Status: 1, Group: "default", Quota: 12345}
			require.NoError(t, db.Create(&user).Error)
			token := Token{UserId: user.Id, Key: "unchanged-client-key", Status: 1}
			require.NoError(t, db.Create(&token).Error)
			// 当前发布版结构不含商城两表；已有名额、Key 和余额必须在升级后保留。
			grant, err := PutHostingGrant(GPTHostingGrant{UserID: user.Id, ActorID: 1, StartsAt: time.Now().Unix() - 10, EndsAt: time.Now().Unix() + 86400, Enabled: true, RetentionDays: 7})
			require.NoError(t, err)
			require.NoError(t, db.Migrator().DropTable(&HostingOrder{}, &HostingPlan{}))
			require.NoError(t, migrateDB())
			require.NoError(t, migrateDB())
			old, err := GetHostingGrant(user.Id, grant.SeatID)
			require.NoError(t, err)
			require.Equal(t, grant.EndsAt, old.EndsAt)
			require.NoError(t, db.First(&token, token.Id).Error)
			require.Equal(t, "unchanged-client-key", token.Key)
			require.NoError(t, db.First(&user, user.Id).Error)
			require.Equal(t, 12345, user.Quota)
			plan := HostingPlan{ID: "monthly", Days: 30, PriceCents: 4000, Enabled: true}
			require.NoError(t, SaveHostingPlan(plan))
			require.NoError(t, migrateDB())
			plans, err = ListHostingPlans()
			require.NoError(t, err)
			require.True(t, plans[0].Enabled)
			inventory, err := PutHostingInventory("cc", "purchase-slot", true, "codex")
			require.NoError(t, err)
			request := uuid.NewString()
			order, err := CreateHostingOrder(user.Id, request, "monthly", "", "codex", "wxpay", "merchant", []string{inventory.ID})
			require.NoError(t, err)
			retry, err := CreateHostingOrder(user.Id, request, "monthly", "", "codex", "wxpay", "merchant", nil)
			require.NoError(t, err)
			require.Equal(t, order.ID, retry.ID)
			_, err = CreateHostingOrder(user.Id, request, "yearly", "", "codex", "wxpay", "merchant", nil)
			require.Error(t, err)
			_, err = GetHostingOrder(user.Id+1, order.ID)
			require.Error(t, err)
			// 改价不改变订单，未验金额、商户、方式的回调不能授予权益。
			plan.PriceCents = 5000
			require.NoError(t, SaveHostingPlan(plan))
			for _, bad := range []struct {
				merchant, method string
				cents            int64
			}{{"foreign", "wxpay", 4000}, {"merchant", "alipay", 4000}, {"merchant", "wxpay", 1}} {
				_, err = CompleteHostingOrder(order.ID, bad.merchant, bad.method, "trade-one", bad.cents)
				require.Error(t, err)
			}
			_, err = GetHostingGrant(user.Id, order.SeatID)
			require.Error(t, err)
			_, err = ReserveHostingSlot(user.Id, inventory.ID, grant.SeatID)
			require.ErrorIs(t, err, ErrHostingSoldOut)
			enrollment := newVM2APIEnrollmentInput(t, "competing-enroll", "cc", "purchase-slot")
			_, err = ReserveVM2APIEnrollment(enrollment)
			require.ErrorIs(t, err, ErrHostingSoldOut)
			// 并发重复到账只有一份权益，原用户余额和已有 Key 均保持。
			results := make(chan error, 6)
			var wg sync.WaitGroup
			for range 6 {
				wg.Go(func() { _, e := CompleteHostingOrder(order.ID, "merchant", "wxpay", "trade-one", 4000); results <- e })
			}
			wg.Wait()
			close(results)
			for e := range results {
				require.NoError(t, e)
			}
			paid, err := GetHostingOrder(user.Id, order.ID)
			require.NoError(t, err)
			require.Equal(t, "paid", paid.Status)
			purchased, err := GetHostingGrant(user.Id, order.SeatID)
			require.NoError(t, err)
			require.Equal(t, int64(30*86400), purchased.EndsAt-purchased.StartsAt)
			binding, err := GetHostingBinding(user.Id, order.SeatID)
			require.NoError(t, err)
			require.Equal(t, "reserved", binding.State)
			require.Equal(t, inventory.ID, *binding.InventoryID)
			// 续费从原到期日累计；再次回调不再延长。套餐当前价格生效。
			renewal, err := CreateHostingOrder(user.Id, uuid.NewString(), "monthly", purchased.SeatID, "codex", "alipay", "merchant", nil)
			require.NoError(t, err)
			require.Equal(t, int64(5000), renewal.PriceCents)
			_, err = CompleteHostingOrder(renewal.ID, "merchant", "alipay", "trade-one", 5000)
			require.Error(t, err)
			unchanged, err := GetHostingGrant(user.Id, purchased.SeatID)
			require.NoError(t, err)
			require.Equal(t, purchased.EndsAt, unchanged.EndsAt)
			renewed, err := CompleteHostingOrder(renewal.ID, "merchant", "alipay", "trade-two", 5000)
			require.NoError(t, err)
			require.Equal(t, purchased.EndsAt+30*86400, renewed.EndsAt)
			_, err = CompleteHostingOrder(renewal.ID, "merchant", "alipay", "trade-two", 5000)
			require.NoError(t, err)
			// 过期但仍保留账号的名额，从已验签付款的接收时间开始。
			require.NoError(t, db.Model(purchased).Update("ends_at", time.Now().Unix()-1).Error)
			expired, err := CreateHostingOrder(user.Id, uuid.NewString(), "monthly", purchased.SeatID, "codex", "wxpay", "merchant", nil)
			require.NoError(t, err)
			expired, err = CompleteHostingOrder(expired.ID, "merchant", "wxpay", "trade-three", 5000)
			require.NoError(t, err)
			require.Equal(t, expired.PaidAt+30*86400, expired.EndsAt)
			// 付款等待期间暂停名额，保留到账证据供管理员核对，不能覆盖暂停状态。
			paused, err := CreateHostingOrder(user.Id, uuid.NewString(), "monthly", purchased.SeatID, "codex", "wxpay", "merchant", nil)
			require.NoError(t, err)
			require.NoError(t, db.Model(binding).Update("paused", true).Error)
			review, err := CompleteHostingOrder(paused.ID, "merchant", "wxpay", "trade-four", 5000)
			require.NoError(t, err)
			require.Equal(t, "paid_review", review.Status)
			unchanged, err = GetHostingGrant(user.Id, purchased.SeatID)
			require.NoError(t, err)
			require.Equal(t, expired.EndsAt, unchanged.EndsAt)
			// 超时释放占位，新用户可抢占；旧回调即便真实到账也不得占用后来者的库存。
			inv, err := PutHostingInventory("cc", "race-slot", true, "claude")
			require.NoError(t, err)
			late, err := CreateHostingOrder(user.Id, uuid.NewString(), "monthly", "", "claude", "wxpay", "merchant", []string{inv.ID})
			require.NoError(t, err)
			require.NoError(t, db.Model(late).Update("expires_at", time.Now().Unix()-1).Error)
			var owners []User
			for i := range 2 {
				u := User{Username: fmt.Sprintf("racer-%d", i), AffCode: fmt.Sprintf("race-%d", i), Role: common.RoleCommonUser, Status: 1, Group: "default"}
				require.NoError(t, db.Create(&u).Error)
				owners = append(owners, u)
			}
			race := make(chan error, 2)
			for _, u := range owners {
				wg.Go(func() {
					_, e := CreateHostingOrder(u.Id, uuid.NewString(), "monthly", "", "claude", "wxpay", "merchant", []string{inv.ID})
					race <- e
				})
			}
			wg.Wait()
			close(race)
			success := 0
			for e := range race {
				if e == nil {
					success++
				} else {
					require.ErrorIs(t, e, ErrHostingSoldOut)
				}
			}
			require.Equal(t, 1, success)
			late, err = CompleteHostingOrder(late.ID, "merchant", "wxpay", "late-trade", 5000)
			require.NoError(t, err)
			require.Equal(t, "paid_review", late.Status)
			_, err = GetHostingGrant(user.Id, late.SeatID)
			require.Error(t, err)
			// 重复迁移保留真实订单，并证明请求幂等键和渠道流水唯一约束仍然存在。
			require.NoError(t, migrateDB())
			require.NoError(t, migrateDB())
			copyOrder := *paid
			copyOrder.ID = "duplicate-id"
			copyOrder.ProviderTrade = nil
			require.Error(t, db.Create(&copyOrder).Error)
			copyOrder.RequestID = uuid.NewString()
			copyOrder.ProviderTrade = paid.ProviderTrade
			require.Error(t, db.Create(&copyOrder).Error)
			persisted, err := GetHostingOrder(user.Id, paid.ID)
			require.NoError(t, err)
			require.Equal(t, "paid", persisted.Status)
		})
	}
}
