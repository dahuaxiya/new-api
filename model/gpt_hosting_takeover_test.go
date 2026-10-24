package model

import (
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
)

// TestHostingTakeoverDatabases 验证旧用户接管的原子性、持久化恢复、配置变化与身份唯一约束。
func TestHostingTakeoverDatabases(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := useVM2APIEnrollmentDatabase(t, dialect)
			t.Setenv("GPT_HOSTING_ENABLED", "true")
			require.NoError(t, migrateDB())
			// 代表上一版绑定表升级：新字段必须能追加，已有用户、余额和下游令牌保持原值。
			require.NoError(t, db.Migrator().DropColumn(&GPTHostingBinding{}, "LegacySnapshot"))
			require.NoError(t, db.Migrator().DropColumn(&GPTHostingBinding{}, "IdentitySource"))
			user := User{Username: "takeover", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Group: "old-dedicated", AffCode: "takeover", Quota: 1234}
			require.NoError(t, db.Create(&user).Error)
			base := "http://127.0.0.1:8787"
			ch := Channel{Name: "old-route", Key: "fixture-old-key", BaseURL: &base, Group: user.Group, Models: "gpt-test", Status: 1, Type: 1}
			require.NoError(t, db.Create(&ch).Error)
			require.NoError(t, ch.AddAbilities(db))
			token := Token{UserId: user.Id, Key: strings.Repeat("z", 48), Name: "client", Group: user.Group}
			require.NoError(t, db.Create(&token).Error)
			require.NoError(t, migrateDB())
			require.NoError(t, migrateDB())
			// 无期限旧用户也参与服务端筛选，页大小不影响总数。
			rows, total, err := SearchUsersWithHosting("", "", nil, nil, 0, 1, &HostingUserFilter{Status: "undated", LegacyUserIDs: []int{user.Id}})
			require.NoError(t, err)
			require.EqualValues(t, 1, total)
			require.Len(t, rows, 1)
			_, fingerprint, err := LegacyHostingChannel(user.Id)
			require.NoError(t, err)
			now := time.Now().Unix()
			grant := GPTHostingGrant{UserID: user.Id, ActorID: 99, StartsAt: now - 1, EndsAt: now + 3600, Enabled: true, RetentionDays: 7}
			intent := HostingTakeover{IdentitySource: "legacy_binding", InstanceID: "cc", SlotID: "old-slot", ChannelID: ch.Id, Snapshot: fingerprint, AccountHash: strings.Repeat("a", 64), SubjectHash: strings.Repeat("b", 64), Label: "a***@test", Plan: "plus", KeyID: "old-key-id", PoolID: "old-pool"}
			bad := intent
			bad.Snapshot = strings.Repeat("0", 64)
			_, err = BeginHostingTakeover(grant, bad)
			require.Error(t, err)
			var count int64
			require.NoError(t, db.Model(&GPTHostingGrant{}).Count(&count).Error)
			require.Zero(t, count, "预检快照变化不能半开通")
			_, err = BeginHostingTakeover(grant, intent)
			require.NoError(t, err)
			b, err := GetHostingBinding(user.Id)
			require.NoError(t, err)
			require.Equal(t, "takeover", b.State)
			require.Equal(t, "legacy_binding", b.IdentitySource, "管理员沿用旧绑定不能标成新官方登录")
			_, total, err = SearchUsersWithHosting("", "", nil, nil, 0, 1, &HostingUserFilter{Status: "takeover"})
			require.NoError(t, err)
			require.EqualValues(t, 1, total)
			_, total, err = SearchUsersWithHosting("", "", nil, nil, 0, 1, &HostingUserFilter{Status: "undated", LegacyUserIDs: []int{user.Id}})
			require.NoError(t, err)
			require.Zero(t, total, "已接管用户不能重复归入旧期限")
			require.ErrorIs(t, CheckHostingAccess(user.Id), ErrHostingUnavailable, "接管未完成时不回退旧路由")
			changed, err := ExpireHostingBinding(user.Id, now+999999)
			require.NoError(t, err)
			require.False(t, changed, "未知远端结果不能自动清理历史账号")
			_, err = PutHostingGrant(grant)
			require.Error(t, err, "只能重试原接管任务")
			_, lease, err := ClaimHostingOperation(user.Id)
			require.NoError(t, err)
			require.NoError(t, db.Model(&Channel{}).Where("id = ?", ch.Id).Update("models", "changed").Error)
			require.Error(t, CompleteHostingChannel(user.Id, lease, base, "gpt-test", "fixture-old-key", "old-key-id", "old-pool"), "预检之后的路由修改必须阻止提交")
			require.NoError(t, db.Model(&Channel{}).Where("id = ?", ch.Id).Update("models", ch.Models).Error)
			require.NoError(t, CompleteHostingChannel(user.Id, lease, base, "gpt-test", "fixture-old-key", "old-key-id", "old-pool"))
			ReleaseHostingOperation(user.Id, lease)
			_, total, err = SearchUsersWithHosting("", "", nil, nil, 0, 1, &HostingUserFilter{Status: "expiring"})
			require.NoError(t, err)
			require.EqualValues(t, 1, total)
			_, route, err := GetHostingRoute(user.Id)
			require.NoError(t, err)
			require.Equal(t, ch.Id, route)
			var preserved User
			require.NoError(t, db.First(&preserved, user.Id).Error)
			require.Equal(t, 1234, preserved.Quota)
			var saved Token
			require.NoError(t, db.First(&saved, token.Id).Error)
			require.Equal(t, token.Key, saved.Key)
			require.Empty(t, saved.Group)
			grant.EndsAt = now - 1
			grant.StartsAt = now - 100
			_, err = PutHostingGrant(grant)
			require.NoError(t, err)
			require.ErrorIs(t, CheckHostingAccess(user.Id), ErrHostingUnavailable)
			_, total, err = SearchUsersWithHosting("", "", nil, nil, 0, 1, &HostingUserFilter{Status: "expired"})
			require.NoError(t, err)
			require.EqualValues(t, 1, total)
		})
	}
}
