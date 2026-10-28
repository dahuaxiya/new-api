package model

import (
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// TestHostingMigrationLifecycle 验证迁移只在有效租约下写入，并且切换渠道后才释放旧槽位坐标。
func TestHostingMigrationLifecycle(t *testing.T) {
	db := useVM2APIEnrollmentDatabase(t, "sqlite")
	t.Setenv("GPT_HOSTING_ENABLED", "true")
	require.NoError(t, migrateDB())

	user := User{Username: "migration-owner", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Group: "default", AffCode: "migration-owner"}
	require.NoError(t, db.Create(&user).Error)
	now := time.Now().Unix()
	grant, err := PutHostingGrant(GPTHostingGrant{UserID: user.Id, ActorID: 99, StartsAt: now - 60, EndsAt: now + 3600, Enabled: true})
	require.NoError(t, err)
	baseURL := "https://old.example"
	channel := Channel{Name: "migration-route", Type: 1, Key: "old-upstream-key", BaseURL: &baseURL, Group: grant.Group, Models: "gpt-old", Status: common.ChannelStatusEnabled}
	require.NoError(t, db.Create(&channel).Error)
	require.NoError(t, channel.AddAbilities(db))
	sourceInventory, err := PutHostingInventory("source", "slot-old", true)
	require.NoError(t, err)
	targetInventory, err := PutHostingInventory("target", "slot-new", true)
	require.NoError(t, err)
	accountHash, subjectHash := strings.Repeat("a", 64), strings.Repeat("b", 64)
	binding, err := GetHostingBinding(user.Id, grant.SeatID)
	require.NoError(t, err)
	binding.BindingID = uuid.NewString()
	binding.Platform = "codex"
	binding.InventoryID = &sourceInventory.ID
	binding.InstanceID, binding.SlotID = sourceInventory.InstanceID, sourceInventory.SlotID
	binding.State = "active"
	binding.AccountHash, binding.SubjectHash = &accountHash, &subjectHash
	binding.ChannelID, binding.KeyID, binding.PoolID = channel.Id, "old-key-id", "old-pool"
	require.NoError(t, db.Save(binding).Error)

	_, err = BeginHostingMigration(user.Id, "invalid-lease", targetInventory.ID, targetInventory.InstanceID, targetInventory.SlotID, grant.SeatID)
	require.ErrorIs(t, err, ErrHostingConflict, "过期或伪造租约不能预留目标")

	claimed, lease, err := ClaimHostingOperation(user.Id, grant.SeatID)
	require.NoError(t, err)
	require.Equal(t, binding.BindingID, claimed.BindingID)
	require.NoError(t, db.Model(&GPTHostingBinding{}).Where("seat_id = ?", grant.SeatID).Update("state", "reserved").Error)
	_, err = BeginHostingMigration(user.Id, lease, targetInventory.ID, targetInventory.InstanceID, targetInventory.SlotID, grant.SeatID)
	require.ErrorIs(t, err, ErrHostingConflict, "未激活的源绑定不能迁移")
	require.NoError(t, db.Model(&GPTHostingBinding{}).Where("seat_id = ?", grant.SeatID).Update("state", "active").Error)

	// 平台不一致时必须在本地预留之前失败，源绑定仍保持 active。
	require.NoError(t, db.Model(&GPTHostingInventory{}).Where("id = ?", targetInventory.ID).Update("platform", "claude").Error)
	_, err = BeginHostingMigration(user.Id, lease, targetInventory.ID, targetInventory.InstanceID, targetInventory.SlotID, grant.SeatID)
	require.ErrorIs(t, err, ErrHostingConflict)
	require.NoError(t, db.Model(&GPTHostingInventory{}).Where("id = ?", targetInventory.ID).Update("platform", "codex").Error)

	// 目标键已被另一个未完成迁移占用时，不能靠重复请求抢槽。
	targetKey := targetInventory.InstanceID + "\x00" + targetInventory.SlotID
	otherSeat := uuid.NewString()
	otherKey := targetKey
	require.NoError(t, db.Create(&GPTHostingBinding{SeatID: otherSeat, UserID: user.Id, BindingID: uuid.NewString(), MigrationTargetKey: &otherKey}).Error)
	_, err = BeginHostingMigration(user.Id, lease, targetInventory.ID, targetInventory.InstanceID, targetInventory.SlotID, grant.SeatID)
	require.ErrorIs(t, err, ErrHostingConflict)
	require.NoError(t, db.Delete(&GPTHostingBinding{}, "seat_id = ?", otherSeat).Error)

	started, err := BeginHostingMigration(user.Id, lease, targetInventory.ID, targetInventory.InstanceID, targetInventory.SlotID, grant.SeatID)
	require.NoError(t, err)
	require.Equal(t, "reserving", started.MigrationState)
	require.Equal(t, sourceInventory.InstanceID, started.MigrationSourceInstanceID)
	require.Equal(t, sourceInventory.SlotID, started.MigrationSourceSlotID)
	require.Equal(t, targetInventory.ID, started.MigrationTargetInventoryID)
	require.NoError(t, AbortHostingMigration(user.Id, lease, grant.SeatID))
	aborted, err := GetHostingBinding(user.Id, grant.SeatID)
	require.NoError(t, err)
	require.Empty(t, aborted.MigrationState)
	require.Nil(t, aborted.MigrationTargetKey)
	started, err = BeginHostingMigration(user.Id, lease, targetInventory.ID, targetInventory.InstanceID, targetInventory.SlotID, grant.SeatID)
	require.NoError(t, err)
	require.NoError(t, UpdateHostingMigrationState(user.Id, lease, "prepared", grant.SeatID))

	newBaseURL := "https://new.example"
	switched, err := CompleteHostingMigration(user.Id, lease, newBaseURL, "gpt-new", "new-upstream-key", "new-key-id", "new-pool", grant.SeatID)
	require.NoError(t, err)
	require.Equal(t, "switched", switched.MigrationState)
	require.Equal(t, targetInventory.InstanceID, switched.InstanceID)
	require.Equal(t, targetInventory.SlotID, switched.SlotID)
	require.Equal(t, sourceInventory.InstanceID, switched.MigrationSourceInstanceID, "旧坐标要保留到远端释放确认")
	require.Equal(t, channel.Id, switched.ChannelID)

	var savedChannel Channel
	require.NoError(t, db.First(&savedChannel, channel.Id).Error)
	require.Equal(t, newBaseURL, valueOrEmpty(savedChannel.BaseURL))
	require.Equal(t, "new-upstream-key", savedChannel.Key)
	require.Equal(t, "gpt-new", savedChannel.Models)
	require.Equal(t, grant.Group, savedChannel.Group)

	require.NoError(t, FinalizeHostingMigration(user.Id, lease, grant.SeatID))
	final, err := GetHostingBinding(user.Id, grant.SeatID)
	require.NoError(t, err)
	require.Empty(t, final.MigrationState)
	require.Empty(t, final.MigrationSourceInstanceID)
	require.Empty(t, final.MigrationTargetInstanceID)
	require.Nil(t, final.MigrationTargetKey)
	require.NoError(t, CheckHostingAccess(user.Id, grant.SeatID))
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
