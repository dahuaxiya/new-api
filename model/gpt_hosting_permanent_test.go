package model

import (
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
)

// TestPermanentHostingDatabases 验证永久名额在真实三库中可分配、调用、筛选、改期和解绑；未完成登录仍有回收期限。
func TestPermanentHostingDatabases(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := useVM2APIEnrollmentDatabase(t, dialect)
			t.Setenv("GPT_HOSTING_ENABLED", "true")
			require.NoError(t, migrateDB())
			u := User{Username: "permanent", AffCode: "permanent", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Group: "default", Quota: 42}
			require.NoError(t, db.Create(&u).Error)
			now := time.Now().Unix()
			input := GPTHostingGrant{UserID: u.Id, ActorID: 99, StartsAt: now - 100, EndsAt: HostingPermanentEnd, Enabled: true, RetentionDays: 7}
			for _, invalid := range []int64{0, -2, now - 100, 4102444801} {
				bad := input
				bad.EndsAt = invalid
				_, err := PutHostingGrant(bad)
				require.ErrorIs(t, err, ErrHostingConflict, "未设置和非法期限不能变成永久")
			}
			grant, err := PutHostingGrant(input)
			require.NoError(t, err)
			require.True(t, grant.ValidAt(4102444801), "永久权益不能是伪装的远期日期")
			require.False(t, grant.ValidAt(input.StartsAt-1))
			inv, err := PutHostingInventory("fixture", "permanent-slot", true)
			require.NoError(t, err)
			_, err = ReserveHostingSlot(u.Id, inv.ID)
			require.NoError(t, err)
			_, lease, err := ClaimHostingOperation(u.Id)
			require.NoError(t, err)
			require.NoError(t, ClaimHostingIdentity(u.Id, lease, strings.Repeat("a", 64), strings.Repeat("b", 64), "fixture", "plus"))
			require.NoError(t, CompleteHostingChannel(u.Id, lease, "https://fixture.test", "gpt-test", "fixture-key", "key-id", "pool-id"))
			ReleaseHostingOperation(u.Id, lease)
			require.NoError(t, CheckHostingAccess(u.Id))
			before, err := GetHostingBinding(u.Id)
			require.NoError(t, err)
			changed, err := ExpireHostingBinding(u.Id, 4102444801)
			require.NoError(t, err)
			require.False(t, changed)
			for _, status := range []string{"active", "expired", "expiring", "awaiting_login", "scheduled"} {
				q, e := (HostingUserFilter{Status: status}).Apply(db.Model(&User{}), db)
				require.NoError(t, e)
				var count int64
				require.NoError(t, q.Count(&count).Error)
				if status == "active" {
					require.EqualValues(t, 1, count)
				} else {
					require.Zero(t, count, status)
				}
			}
			// 永久仅免除到期，不绕过管理员停用、预约开始时间或用户暂停。
			for _, status := range []string{"disabled", "scheduled", "awaiting_login"} {
				input.Enabled, input.StartsAt = status != "disabled", now-100
				if status == "scheduled" {
					input.StartsAt = now + 3600
				}
				_, err = PutHostingGrant(input)
				require.NoError(t, err)
				if status == "awaiting_login" {
					require.NoError(t, db.Model(before).Update("state", "reserved").Error)
				}
				require.ErrorIs(t, CheckHostingAccess(u.Id), ErrHostingUnavailable)
				q, e := (HostingUserFilter{Status: status}).Apply(db.Model(&User{}), db)
				require.NoError(t, e)
				var count int64
				require.NoError(t, q.Count(&count).Error)
				require.EqualValues(t, 1, count, status)
			}
			require.NoError(t, db.Model(before).Updates(map[string]any{"state": "active", "paused": true}).Error)
			require.ErrorIs(t, CheckHostingAccess(u.Id), ErrHostingUnavailable)
			require.NoError(t, db.Model(before).Update("paused", false).Error)
			input.EndsAt, input.RetentionDays = now-1, 0
			_, err = PutHostingGrant(input)
			require.NoError(t, err)
			require.ErrorIs(t, CheckHostingAccess(u.Id), ErrHostingUnavailable)
			// 在清理器运行前重新设为永久，应保留原渠道、账号和 Key。
			input.EndsAt = HostingPermanentEnd
			_, err = PutHostingGrant(input)
			require.NoError(t, err)
			changed, err = ExpireHostingBinding(u.Id, now)
			require.NoError(t, err)
			require.False(t, changed)
			after, err := GetHostingBinding(u.Id)
			require.NoError(t, err)
			require.Equal(t, before.BindingID, after.BindingID)
			require.Equal(t, before.ChannelID, after.ChannelID)
			require.Equal(t, before.KeyID, after.KeyID)
			_, lease, err = ClaimHostingOperation(u.Id)
			require.NoError(t, err)
			require.NoError(t, UpdateHostingOperation(u.Id, lease, map[string]any{"state": "cleaning"}))
			require.NoError(t, ClearHostingBinding(u.Id, lease))
			ReleaseHostingOperation(u.Id, lease)
			_, err = ReserveHostingSlot(u.Id, inv.ID)
			require.NoError(t, err)
			require.NoError(t, db.Model(&GPTHostingBinding{}).Where("user_id = ?", u.Id).Update("updated_at", now-901).Error)
			changed, err = ExpireHostingBinding(u.Id, now)
			require.NoError(t, err)
			require.True(t, changed, "永久名额也须释放放弃的首次登录预约")
		})
	}
}
