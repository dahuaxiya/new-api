package model

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// TestGPTHostingDatabases 验证真实三库迁移、单名额、槽位/身份唯一性及期限对请求的约束。
func TestGPTHostingDatabases(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := useVM2APIEnrollmentDatabase(t, dialect)
			t.Setenv("GPT_HOSTING_ENABLED", "true")
			require.NoError(t, migrateDB())
			// 模拟上一发行版升级，保留真实用户、渠道和余额后再重复执行迁移。
			require.NoError(t, db.Migrator().DropTable(&GPTHostingBinding{}, &GPTHostingInventory{}, &GPTHostingGrant{}))
			users := []User{{Username: "host-a", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Group: "default", AffCode: "ha", Quota: 42}, {Username: "host-b", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Group: "default", AffCode: "hb", Quota: 77}}
			for i := range users {
				require.NoError(t, db.Create(&users[i]).Error)
			}
			require.NoError(t, migrateDB())
			require.NoError(t, migrateDB())
			now := time.Now().Unix()
			for _, u := range users {
				created, err := PutHostingGrant(GPTHostingGrant{UserID: u.Id, ActorID: 999, StartsAt: now - 10, EndsAt: now + 3600, Enabled: true})
				require.NoError(t, err)
				require.Equal(t, float64(1), ratio_setting.GetGroupRatio(created.Group))
			}
			a, b := users[0].Id, users[1].Id
			require.ErrorIs(t, CheckHostingAccess(a), ErrHostingUnavailable, "有名额但未绑定不能请求公共渠道")
			inventory, err := PutHostingInventory("vps-a", "prebuilt-1", true)
			require.NoError(t, err)
			first, err := ReserveHostingSlot(a, inventory.ID)
			require.NoError(t, err)
			_, err = ReserveHostingSlot(b, inventory.ID)
			require.Error(t, err, "一个槽位不能属于两个用户")
			_, err = ReserveHostingSlot(a, inventory.ID)
			require.Error(t, err, "重复点击不能再分配")
			claimed, lease, err := ClaimHostingOperation(a)
			require.NoError(t, err)
			require.Equal(t, first.BindingID, claimed.BindingID)
			_, _, err = ClaimHostingOperation(a)
			require.Error(t, err, "同一用户操作不能并发")
			require.NoError(t, ClaimHostingIdentity(a, lease, strings.Repeat("a", 64), strings.Repeat("b", 64), "a***@example.com", "plus"))
			require.Error(t, ClaimHostingIdentity(a, lease, strings.Repeat("c", 64), strings.Repeat("b", 64), "other", "plus"), "重新授权不能更换账号")
			require.NoError(t, CompleteHostingChannel(a, lease, "https://example.com", "gpt-test", "test-upstream-key", "key_test", "pool_test"))
			require.NoError(t, CheckHostingAccess(a))
			group, channelID, routeErr := GetHostingRoute(a)
			require.NoError(t, routeErr)
			require.NotEmpty(t, group)
			require.NotZero(t, channelID)
			require.NoError(t, UpdateHostingOperation(a, lease, map[string]any{"paused": true}))
			require.ErrorIs(t, CheckHostingAccess(a), ErrHostingUnavailable)
			require.NoError(t, UpdateHostingOperation(a, lease, map[string]any{"paused": false}))
			ReleaseHostingOperation(a, lease)
			inv2, err := PutHostingInventory("vps-b", "prebuilt-2", true)
			require.NoError(t, err)
			_, err = ReserveHostingSlot(b, inv2.ID)
			require.NoError(t, err)
			_, leaseB, err := ClaimHostingOperation(b)
			require.NoError(t, err)
			require.Error(t, ClaimHostingIdentity(b, leaseB, strings.Repeat("a", 64), strings.Repeat("d", 64), "same", "plus"), "官方账号跨实例唯一")
			require.Error(t, ClaimHostingIdentity(b, leaseB, strings.Repeat("d", 64), strings.Repeat("b", 64), "same-sub", "plus"), "同一主体不能用不同工作区绕过")
			ReleaseHostingOperation(b, leaseB)
			before, _ := GetHostingGrant(a)
			renewed, err := PutHostingGrant(GPTHostingGrant{UserID: a, ActorID: 999, StartsAt: now - 10, EndsAt: now + 7200, Enabled: true})
			require.NoError(t, err)
			require.Equal(t, before.Group, renewed.Group)
			require.Equal(t, float64(1), ratio_setting.GetGroupRatio(renewed.Group), "续期不能恢复为零倍率")
			require.Equal(t, 0.75, ratio_setting.GetGroupRatio("existing"), "不能覆盖其他分组倍率")
			expired, err := PutHostingGrant(GPTHostingGrant{UserID: a, ActorID: 999, StartsAt: now - 3600, EndsAt: now - 1, Enabled: true})
			require.NoError(t, err)
			require.False(t, expired.ValidAt(now))
			require.ErrorIs(t, CheckHostingAccess(a), ErrHostingUnavailable)
			_, lease, err = ClaimHostingOperation(a)
			require.NoError(t, err)
			require.Error(t, UpdateHostingOperation(a, uuid.NewString(), map[string]any{"state": "active"}), "旧租约不能写入")
			require.NoError(t, UpdateHostingOperation(a, lease, map[string]any{"state": "cleaning"}))
			require.NoError(t, ClearHostingBinding(a, lease))
			ReleaseHostingOperation(a, lease)
			cleared, _ := GetHostingBinding(a)
			require.Nil(t, cleared.InventoryID)
			require.Nil(t, cleared.AccountHash)
			var clearedChannel Channel
			require.NoError(t, db.First(&clearedChannel, cleared.ChannelID).Error)
			require.Empty(t, clearedChannel.Key)
			require.Equal(t, common.ChannelStatusManuallyDisabled, clearedChannel.Status)
			unchanged, _ := GetHostingGrant(a)
			require.Equal(t, expired.EndsAt, unchanged.EndsAt, "解绑不能重置期限")
			_, err = ReserveHostingSlot(a, inventory.ID)
			require.ErrorIs(t, err, ErrHostingUnavailable)
			var user User
			require.NoError(t, db.First(&user, a).Error)
			require.Equal(t, 42, user.Quota, "开通和续期不修改旧余额")
			// 同时预约同一空槽必须只有一个获胜者，不能依赖进程内的串行点击。
			for _, id := range []int{a, b} {
				_, e := PutHostingGrant(GPTHostingGrant{UserID: id, ActorID: 999, StartsAt: now - 10, EndsAt: now + 3600, Enabled: true, RetentionDays: 7})
				require.NoError(t, e)
				require.NoError(t, db.Model(&GPTHostingBinding{}).Where("user_id = ?", id).Updates(map[string]any{"inventory_id": nil, "state": "unbound", "operation_until": 0}).Error)
			}
			results := make(chan error, 2)
			var wg sync.WaitGroup
			for _, id := range []int{a, b} {
				wg.Go(func() { _, e := ReserveHostingSlot(id, inventory.ID); results <- e })
			}
			wg.Wait()
			close(results)
			success := 0
			for err := range results {
				if err == nil {
					success++
				}
			}
			require.Equal(t, 1, success)
			// 保留期结束才进入清理；续期后旧清理条件必须失效。
			require.NoError(t, db.Model(&GPTHostingBinding{}).Where("user_id = ?", a).Update("state", "active").Error)
			require.NoError(t, db.Model(&GPTHostingGrant{}).Where("user_id = ?", a).Updates(map[string]any{"ends_at": now - 1, "retention_days": 7}).Error)
			changed, e := ExpireHostingBinding(a, now)
			require.NoError(t, e)
			require.False(t, changed)
			changed, e = ExpireHostingBinding(a, now+7*86400)
			require.NoError(t, e)
			require.True(t, changed)
		})
	}
}
