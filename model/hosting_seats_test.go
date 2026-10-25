package model

import (
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// TestHostingSeatsDatabases 验证同用户多账号独立租约、路由、到期，以及跨用户和跨账号唯一性。
func TestHostingSeatsDatabases(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := useVM2APIEnrollmentDatabase(t, dialect)
			t.Setenv("GPT_HOSTING_ENABLED", "true")
			require.NoError(t, migrateDB())
			require.NoError(t, migrateDB())
			user := User{Username: "seats-owner", AffCode: "seats-owner", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Group: "default"}
			require.NoError(t, db.Create(&user).Error)
			keyA := Token{UserId: user.Id, Key: "existing-client-key", Status: 1}
			require.NoError(t, db.Create(&keyA).Error)
			now := time.Now().Unix()
			first, err := PutHostingGrant(GPTHostingGrant{UserID: user.Id, ActorID: 999, Enabled: true, StartsAt: now - 60, EndsAt: now + 3600})
			require.NoError(t, err)
			second, err := PutHostingGrant(GPTHostingGrant{SeatID: uuid.NewString(), UserID: user.Id, ActorID: 999, Enabled: true, StartsAt: now - 60, EndsAt: HostingPermanentEnd})
			require.NoError(t, err)
			require.NoError(t, db.First(&keyA, keyA.Id).Error)
			require.Equal(t, first.SeatID, keyA.HostingSeatID, "新增名额不能重定向既有 Key")
			keyB := Token{UserId: user.Id, Key: "second-client-key", Status: 1, HostingSeatID: second.SeatID}
			require.NoError(t, db.Create(&keyB).Error)
			_, err = SelectTokenHostingSeat(user.Id, "")
			require.ErrorIs(t, err, ErrHostingConflict)
			_, err = SelectTokenHostingSeat(user.Id+1, second.SeatID)
			require.ErrorIs(t, err, ErrHostingConflict)
			leases := []string{}
			channels := []int{}
			for i, seat := range []*GPTHostingGrant{first, second} {
				inv, e := PutHostingInventory("instance", []string{"gpt-slot", "claude-slot"}[i], true, []string{"codex", "claude"}[i])
				require.NoError(t, e)
				_, e = ReserveHostingSlot(user.Id, inv.ID, seat.SeatID)
				require.NoError(t, e)
				_, lease, e := ClaimHostingOperation(user.Id, seat.SeatID)
				require.NoError(t, e)
				leases = append(leases, lease)
				e = ClaimHostingIdentity(user.Id, lease, strings.Repeat([]string{"a", "c"}[i], 64), strings.Repeat([]string{"b", "d"}[i], 64), "masked-account", []string{"plus", "max"}[i], seat.SeatID)
				require.NoError(t, e)
				require.NoError(t, CompleteHostingChannel(user.Id, lease, "https://example.test", []string{"gpt-test", "claude-test"}[i], "upstream-key", "remote-key", "pool", seat.SeatID))
				_, channel, e := GetTokenHostingRoute(user.Id, []int{keyA.Id, keyB.Id}[i])
				require.NoError(t, e)
				channels = append(channels, channel)
			}
			require.NotEqual(t, channels[0], channels[1])
			var claudeChannel Channel
			require.NoError(t, db.First(&claudeChannel, channels[1]).Error)
			require.Equal(t, 14, claudeChannel.Type)
			require.Error(t, UpdateHostingOperation(user.Id, leases[0], map[string]any{"paused": true}, second.SeatID), "另一个名额租约不能修改当前账号")
			require.NoError(t, UpdateHostingOperation(user.Id, leases[0], map[string]any{"paused": true}, first.SeatID))
			_, _, err = GetTokenHostingRoute(user.Id, keyA.Id)
			require.ErrorIs(t, err, ErrHostingUnavailable)
			_, channel, err := GetTokenHostingRoute(user.Id, keyB.Id)
			require.NoError(t, err)
			require.Equal(t, channels[1], channel)
			first.EndsAt = now - 1
			first.StartsAt = now - 100
			_, err = PutHostingGrant(*first)
			require.NoError(t, err)
			require.NoError(t, UpdateHostingOperation(user.Id, leases[0], map[string]any{"state": "cleaning"}, first.SeatID))
			require.NoError(t, ClearHostingBinding(user.Id, leases[0], first.SeatID))
			_, channel, err = GetTokenHostingRoute(user.Id, keyB.Id)
			require.NoError(t, err)
			require.Equal(t, channels[1], channel)
			_, _, err = GetHostingRoute(user.Id, uuid.NewString())
			require.ErrorIs(t, err, ErrHostingUnavailable)
			// 运行时重新读取 Key 目标，缓存和长连接不能继续用改绑前的账号。
			require.NoError(t, db.Model(&keyB).Update("hosting_seat_id", first.SeatID).Error)
			_, _, err = GetTokenHostingRoute(user.Id, keyB.Id)
			require.ErrorIs(t, err, ErrHostingUnavailable)
			require.NoError(t, migrateDB())
			persisted, err := GetHostingGrant(user.Id, second.SeatID)
			require.NoError(t, err)
			require.Equal(t, int64(HostingPermanentEnd), persisted.EndsAt)
		})
	}
}

type legacyGPTHostingGrant struct {
	UserID        int    `json:"user_id" gorm:"primaryKey;autoIncrement:false"`
	StartsAt      int64  `json:"starts_at"`
	RetentionDays int    `json:"retention_days"`
	EndsAt        int64  `json:"ends_at"`
	Enabled       bool   `json:"enabled"`
	Group         string `json:"-" gorm:"type:varchar(64);uniqueIndex"`
	Revision      int64  `json:"revision"`
	ActorID       int    `json:"actor_id"`
	Note          string `json:"note" gorm:"type:varchar(255)"`
	UpdatedAt     int64  `json:"updated_at" gorm:"autoUpdateTime"`
}
type legacyGPTHostingBinding struct {
	IdentitySource string  `json:"identity_source" gorm:"type:varchar(24)"`
	UserID         int     `json:"-" gorm:"primaryKey;autoIncrement:false"`
	BindingID      string  `json:"-" gorm:"type:varchar(36);uniqueIndex"`
	InventoryID    *string `json:"-" gorm:"type:varchar(36);uniqueIndex"`
	InstanceID     string  `json:"-" gorm:"type:varchar(64)"`
	SlotID         string  `json:"-" gorm:"type:varchar(80)"`
	State          string  `json:"state" gorm:"type:varchar(24)"`
	AccountHash    *string `json:"-" gorm:"type:varchar(64);uniqueIndex"`
	SubjectHash    *string `json:"-" gorm:"type:varchar(64);uniqueIndex"`
	AccountLabel   string  `json:"account_label" gorm:"type:varchar(160)"`
	Plan           string  `json:"plan" gorm:"type:varchar(32)"`
	Paused         bool    `json:"paused"`
	ChannelID      int     `json:"-"`
	KeyID          string  `json:"-" gorm:"type:varchar(128)"`
	PoolID         string  `json:"-" gorm:"type:varchar(128)"`
	LegacySnapshot string  `json:"-" gorm:"type:text"`
	OperationToken string  `json:"-" gorm:"type:varchar(36)"`
	OperationUntil int64   `json:"-"`
	Generation     int64   `json:"-"`
	UpdatedAt      int64   `json:"updated_at" gorm:"autoUpdateTime"`
}

// TableName 对应升级前实际使用的单用户主键表。
func (legacyGPTHostingGrant) TableName() string { return "gpt_hosting_grants" }

// TableName 对应升级前实际使用的单用户绑定表。
func (legacyGPTHostingBinding) TableName() string { return "gpt_hosting_bindings" }

// TestHostingSeatsUpgradeDatabases 以发布版结构建立旧表，升级两次后验证账号、期限、Key 以及唯一约束保持。
func TestHostingSeatsUpgradeDatabases(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := useVM2APIEnrollmentDatabase(t, dialect)
			require.NoError(t, db.AutoMigrate(&legacyGPTHostingGrant{}, &legacyGPTHostingBinding{}, &Token{}))
			now := time.Now().Unix()
			hash := strings.Repeat("a", 64)
			subject := strings.Repeat("b", 64)
			inventory := uuid.NewString()
			bindingID := uuid.NewString()
			old := legacyGPTHostingGrant{UserID: 77, ActorID: 1, Group: "acct_existing", Enabled: true, StartsAt: now - 10, EndsAt: HostingPermanentEnd, Revision: 9}
			require.NoError(t, db.Create(&old).Error)
			require.NoError(t, db.Create(&legacyGPTHostingBinding{UserID: 77, BindingID: bindingID, InventoryID: &inventory, State: "active", AccountHash: &hash, SubjectHash: &subject, ChannelID: 123, KeyID: "old-key", Generation: 20}).Error)
			key := Token{UserId: 77, Key: "must-stay-the-same", Status: 1}
			require.NoError(t, db.Create(&key).Error)
			require.NoError(t, migrateDB())
			require.NoError(t, migrateDB())
			g, err := GetHostingGrant(77)
			require.NoError(t, err)
			require.Equal(t, old.EndsAt, g.EndsAt)
			require.Equal(t, old.Group, g.Group)
			b, err := GetHostingBinding(77)
			require.NoError(t, err)
			require.Equal(t, bindingID, b.BindingID)
			require.Equal(t, 123, b.ChannelID)
			require.Equal(t, int64(20), b.Generation)
			require.NoError(t, db.First(&key, key.Id).Error)
			require.Equal(t, "must-stay-the-same", key.Key)
			require.Equal(t, g.SeatID, key.HostingSeatID)
			// 重启不能从旧表恢复已经解绑的账号，也不能覆盖用户后来为 Key 选择的目标。
			require.NoError(t, db.Model(b).Updates(map[string]any{"state": "unbound", "inventory_id": nil, "account_hash": nil, "subject_hash": nil}).Error)
			require.NoError(t, db.Model(&key).Update("hosting_seat_id", "later-selection").Error)
			require.NoError(t, migrateDB())
			b, err = GetHostingBinding(77)
			require.NoError(t, err)
			require.Equal(t, "unbound", b.State)
			require.NoError(t, db.First(&key, key.Id).Error)
			require.Equal(t, "later-selection", key.HostingSeatID)
			// 新表自身仍约束同槽与同一官方账号唯一性。
			require.NoError(t, db.Create(&GPTHostingBinding{SeatID: uuid.NewString(), UserID: 78, BindingID: uuid.NewString(), InventoryID: &inventory, AccountHash: &hash, SubjectHash: &subject}).Error)
			require.Error(t, db.Create(&GPTHostingBinding{SeatID: uuid.NewString(), UserID: 79, BindingID: uuid.NewString(), InventoryID: &inventory}).Error)
			require.Error(t, db.Create(&GPTHostingBinding{SeatID: uuid.NewString(), UserID: 79, BindingID: uuid.NewString(), AccountHash: &hash}).Error)
		})
	}
}

// TestHostingDisabledKeepsOrdinaryKeys 验证未启用托管的部署不需要托管表，但不能接收显式账号目标。
func TestHostingDisabledKeepsOrdinaryKeys(t *testing.T) {
	t.Setenv("GPT_HOSTING_ENABLED", "false")
	seat, err := SelectTokenHostingSeat(7, "")
	require.NoError(t, err)
	require.Empty(t, seat)
	_, err = SelectTokenHostingSeat(7, "explicit-seat")
	require.ErrorIs(t, err, ErrHostingConflict)
	group, channel, err := GetTokenHostingRoute(7, 9)
	require.NoError(t, err)
	require.Empty(t, group)
	require.Zero(t, channel)
}
