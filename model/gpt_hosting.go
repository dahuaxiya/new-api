package model

import (
	"errors"
	"os"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

var ErrHostingConflict = errors.New("hosting state conflicts with this operation")
var ErrHostingUnavailable = errors.New("hosting entitlement is unavailable")

// HostingPermanentEnd 是管理员显式选择的永久期限；零值或其他负数不能授予永久权益。
const HostingPermanentEnd int64 = -1

// GPTHostingGrant 是管理员发放的单个名额的权益；时间为 UTC Unix 秒，EndsAt 可为 HostingPermanentEnd。
type GPTHostingGrant struct {
	CreatedAt     int64  `json:"created_at" gorm:"autoCreateTime:milli"`
	SeatID        string `json:"seat_id" gorm:"type:varchar(36);primaryKey"`
	UserID        int    `json:"user_id" gorm:"index"`
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

// GPTHostingInventory 只登记管理员已经准备好的槽位；停用只阻止新分配，不撤销现有用户。
type GPTHostingInventory struct {
	Platform   string `json:"platform" gorm:"type:varchar(16)"`
	ID         string `json:"id" gorm:"type:varchar(36);primaryKey"`
	InstanceID string `json:"instance_id" gorm:"type:varchar(64);uniqueIndex:idx_hosting_inventory_slot,priority:1"`
	SlotID     string `json:"slot_id" gorm:"type:varchar(80);uniqueIndex:idx_hosting_inventory_slot,priority:2"`
	Enabled    bool   `json:"enabled"`
	UpdatedAt  int64  `json:"updated_at" gorm:"autoUpdateTime"`
}

// GPTHostingBinding 同时承担用户独占、槽位独占和官方身份独占；可空唯一列仅在清理确认后释放。
// OperationToken 是短期工作租约，BindingID 则在整个绑定生命周期不变，跨系统重试必须沿用它。
type GPTHostingBinding struct {
	IdentitySource string  `json:"identity_source" gorm:"type:varchar(24)"`
	SeatID         string  `json:"seat_id" gorm:"type:varchar(36);primaryKey"`
	Platform       string  `json:"platform" gorm:"type:varchar(16)"`
	UserID         int     `json:"-" gorm:"index"`
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
	// Migration fields keep the source route usable until the target account is
	// activated. A nullable target key gives each database a portable unique
	// constraint while allowing every non-migrating binding to remain NULL.
	MigrationState             string  `json:"-" gorm:"type:varchar(24)"`
	MigrationTargetKey         *string `json:"-" gorm:"type:varchar(160);uniqueIndex"`
	MigrationSourceInstanceID  string  `json:"-" gorm:"type:varchar(64)"`
	MigrationSourceSlotID      string  `json:"-" gorm:"type:varchar(80)"`
	MigrationSourceInventoryID string  `json:"-" gorm:"type:varchar(36)"`
	MigrationTargetInstanceID  string  `json:"-" gorm:"type:varchar(64)"`
	MigrationTargetSlotID      string  `json:"-" gorm:"type:varchar(80)"`
	MigrationTargetInventoryID string  `json:"-" gorm:"type:varchar(36)"`
	UpdatedAt                  int64   `json:"updated_at" gorm:"autoUpdateTime"`
}

// ValidAt 在每次绑定和调用时检查真实时间，不使用可滞后的客户端计时或用户缓存。
func (g GPTHostingGrant) ValidAt(now int64) bool {
	return g.Enabled && g.StartsAt > 0 && g.StartsAt <= now && (g.EndsAt == HostingPermanentEnd || now < g.EndsAt)
}

// GetHostingGrant 返回指定用户的权益；不存在与已到期由调用方分别展示。
func GetHostingGrant(userID int, seats ...string) (*GPTHostingGrant, error) {
	var g GPTHostingGrant
	err := hostingSeatScope(DB, userID, seats).First(&g).Error
	return &g, err
}

// GetHostingBinding 只以鉴权后的用户 ID 查询；不得将浏览器传来的 user_id 用于自助接口。
func GetHostingBinding(userID int, seats ...string) (*GPTHostingBinding, error) {
	var b GPTHostingBinding
	err := hostingSeatScope(DB, userID, seats).First(&b).Error
	return &b, err
}

// PutHostingGrant 发放或修改单名额并将新托管用户放入专属分组；旧开户用户必须先完成显式迁移。
// 免费专属分组对应月费托管，保留站内用量记录，不修改用户现有钱包余额。
func PutHostingGrant(input GPTHostingGrant) (*GPTHostingGrant, error) {
	return putHostingGrant(input, nil)
}

// putHostingGrant 在一个事务内建立权益和归属；接管参数只来自服务端预检，不能由浏览器声明。
func putHostingGrant(input GPTHostingGrant, takeover *HostingTakeover) (*GPTHostingGrant, error) {
	if input.RetentionDays < 0 || input.RetentionDays > 30 || input.UserID <= 0 || input.ActorID <= 0 || input.StartsAt <= 0 || input.StartsAt > 4102444800 || (input.EndsAt != HostingPermanentEnd && (input.EndsAt <= input.StartsAt || input.EndsAt > 4102444800)) || len([]rune(input.Note)) > 255 {
		return nil, ErrHostingConflict
	}
	vm2apiEnrollmentCommitMutex.Lock()
	defer vm2apiEnrollmentCommitMutex.Unlock()
	if input.SeatID == "" {
		input.SeatID = defaultHostingSeat(input.UserID)
	}
	var saved GPTHostingGrant
	var ratioJSON string
	err := DB.Transaction(func(tx *gorm.DB) error {
		return persistHostingGrant(tx, input, takeover, &saved, &ratioJSON)
	})
	if err != nil {
		return nil, err
	}
	if err := updateOptionMap("GroupRatio", ratioJSON); err != nil {
		return nil, err
	}
	if err := RefreshUserGroupCache(saved.UserID); err != nil {
		return nil, err
	}
	return &saved, nil
}

// persistHostingGrant 将名额、绑定和专属分组写入调用方事务，支付订单必须与权益一起提交。
// 调用方持有开户互斥锁；提交后再刷新内存与缓存，不能在事务内发布尚未提交的分组。
func persistHostingGrant(tx *gorm.DB, input GPTHostingGrant, takeover *HostingTakeover, saved *GPTHostingGrant, ratioJSON *string) error {
	var user User
	// 写入权益之前锁用户；SQLite 以写入占有，跨方言唯一索引兜住并发首次开通。
	if err := lockForUpdate(tx).First(&user, "id = ?", input.UserID).Error; err != nil {
		return err
	}
	if user.Role != common.RoleCommonUser || user.Status != common.UserStatusEnabled {
		return ErrHostingConflict
	}
	err := hostingSeatScope(tx, input.UserID, []string{input.SeatID}).First(saved).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		var count int64
		if err := tx.Model(&VM2APIEnrollment{}).Where("user_id = ?", user.Id).Count(&count).Error; err != nil {
			return err
		}
		var existingSeats int64
		if err := tx.Model(&GPTHostingGrant{}).Where("user_id = ?", user.Id).Count(&existingSeats).Error; err != nil {
			return err
		}
		if existingSeats >= 100 {
			return ErrHostingConflict
		}
		if takeover == nil && existingSeats == 0 && (count != 0 || strings.HasPrefix(user.Group, "acct_")) {
			return ErrHostingConflict
		}
		*saved = input
		saved.Group = "acct_" + strings.ReplaceAll(uuid.NewString(), "-", "")
		saved.Revision = 1
		if err := tx.Create(saved).Error; err != nil {
			return err
		}
		binding := GPTHostingBinding{SeatID: saved.SeatID, UserID: user.Id, BindingID: uuid.NewString(), State: "unbound"}
		if takeover != nil {
			ch, snapshot, err := legacyHostingChannel(tx, user.Id)
			if err != nil || snapshot != takeover.Snapshot || ch.Id != takeover.ChannelID {
				return ErrHostingConflict
			}
			inv := GPTHostingInventory{ID: uuid.NewString(), InstanceID: takeover.InstanceID, SlotID: takeover.SlotID, Enabled: false}
			if err := tx.Create(&inv).Error; err != nil {
				return ErrHostingConflict
			}
			binding.State, binding.InventoryID = "takeover", &inv.ID
			binding.IdentitySource = takeover.IdentitySource
			binding.InstanceID, binding.SlotID, binding.ChannelID = takeover.InstanceID, takeover.SlotID, ch.Id
			binding.AccountHash, binding.SubjectHash = &takeover.AccountHash, &takeover.SubjectHash
			binding.AccountLabel, binding.Plan = takeover.Label, takeover.Plan
			binding.KeyID, binding.PoolID, binding.LegacySnapshot = takeover.KeyID, takeover.PoolID, snapshot
		}
		if err := tx.Create(&binding).Error; err != nil {
			return err
		}
		if existingSeats == 0 {
			if err := tx.Model(&User{}).Where("id = ?", user.Id).Update("group", saved.Group).Error; err != nil {
				return err
			}
			// 旧令牌显式分组清空后跟随当前用户组，避免发放名额后仍落入公共渠道。
			if err := tx.Model(&Token{}).Where("user_id = ?", user.Id).Updates(map[string]any{"group": "", "auto_groups": "", "cross_group_retry": false, "hosting_seat_id": saved.SeatID}).Error; err != nil {
				return err
			}
		}
	} else if err != nil {
		return err
	} else {
		var b GPTHostingBinding
		if err := hostingSeatScope(tx, input.UserID, []string{input.SeatID}).First(&b).Error; err != nil {
			return err
		}
		// 未完成接管只能沿用原任务重试，禁止在远端结果未知时改期限或重新分配。
		if b.State == "takeover" || takeover != nil {
			return ErrHostingConflict
		}
		saved.StartsAt, saved.EndsAt, saved.Enabled = input.StartsAt, input.EndsAt, input.Enabled
		saved.ActorID, saved.Note = input.ActorID, input.Note
		saved.RetentionDays = input.RetentionDays
		saved.Revision++
		if err := tx.Save(saved).Error; err != nil {
			return err
		}
	}
	option := Option{Key: "GroupRatio", Value: ratio_setting.GroupRatio2JSONString()}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&option).Error; err != nil {
		return err
	}
	if err := lockForUpdate(tx).Where(Option{Key: "GroupRatio"}).First(&option).Error; err != nil {
		return err
	}
	ratios := map[string]float64{}
	if err := common.UnmarshalJsonStr(option.Value, &ratios); err != nil || ratios == nil {
		return ErrHostingConflict
	}
	ratios[saved.Group] = 0
	raw, err := common.Marshal(ratios)
	if err != nil {
		return err
	}
	*ratioJSON = string(raw)
	return tx.Model(&Option{}).Where(Option{Key: "GroupRatio"}).Update("value", *ratioJSON).Error
}

// PutHostingInventory 只登记通过远端空槽检查的目标，已有记录不会因重新同步而失去占用信息。
func PutHostingInventory(instance, slot string, enabled bool, platforms ...string) (*GPTHostingInventory, error) {
	if !validVM2APIIdentifier(instance, 64) || !validVM2APIIdentifier(slot, 80) {
		return nil, ErrHostingConflict
	}
	platform := "codex"
	if len(platforms) > 0 {
		platform = platforms[0]
	}
	if platform != "codex" && platform != "claude" {
		return nil, ErrHostingConflict
	}
	row := GPTHostingInventory{Platform: platform, ID: uuid.NewString(), InstanceID: instance, SlotID: slot, Enabled: enabled}
	if err := DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
		return nil, err
	}
	if err := DB.Where("instance_id = ? AND slot_id = ?", instance, slot).First(&row).Error; err != nil {
		return nil, err
	}
	if err := DB.Model(&row).Updates(map[string]any{"enabled": enabled, "platform": platform}).Error; err != nil {
		return nil, err
	}
	row.Enabled = enabled
	return &row, nil
}

// ListHostingInventory 返回管理员资源清单，空槽的实时资格由远端独立校验。
func ListHostingInventory() ([]GPTHostingInventory, error) {
	rows := []GPTHostingInventory{}
	err := DB.Order("instance_id, slot_id").Find(&rows).Error
	return rows, err
}

// ReserveHostingSlot 用数据库唯一约束抢占预置槽位；失败不会自动换号重复创建远端资源。
func ReserveHostingSlot(userID int, inventoryID string, seats ...string) (*GPTHostingBinding, error) {
	err := DB.Transaction(func(tx *gorm.DB) error {
		now := time.Now().Unix()
		grant := hostingSeatScope(tx.Model(&GPTHostingGrant{}), userID, seats).Where("enabled = ? AND starts_at <= ? AND (ends_at = ? OR ends_at > ?)", true, now, HostingPermanentEnd, now).Update("revision", gorm.Expr("revision + 1"))
		if grant.Error != nil {
			return grant.Error
		}
		if grant.RowsAffected != 1 {
			return ErrHostingUnavailable
		}
		var inv GPTHostingInventory
		if err := lockForUpdate(tx).First(&inv, "id = ? AND enabled = ?", inventoryID, true).Error; err != nil {
			return err
		}
		// 待支付订单短期占用库存，管理员赠送名额也不能抢走已进入付款流程的槽位。
		if err := checkHostingInventoryHold(tx, inv.ID, "", now); err != nil {
			return err
		}
		seatID := ""
		if len(seats) > 0 {
			seatID = seats[0]
		}
		platform := inv.Platform
		if platform == "" {
			platform = "codex"
		}
		if err := reserveSeatPlatform(tx, userID, seatID, platform); err != nil {
			return err
		}
		var legacy int64
		if err := tx.Model(&VM2APIEnrollment{}).Where("instance_id = ? AND slot_id = ?", inv.InstanceID, inv.SlotID).Count(&legacy).Error; err != nil {
			return err
		}
		if legacy != 0 {
			return ErrHostingConflict
		}
		r := hostingSeatScope(tx.Model(&GPTHostingBinding{}), userID, seats).Where("state = ? AND inventory_id IS NULL", "unbound").Updates(map[string]any{
			"binding_id": uuid.NewString(), "inventory_id": inv.ID, "instance_id": inv.InstanceID, "slot_id": inv.SlotID, "state": "reserved", "paused": false, "generation": gorm.Expr("generation + 1"),
		})
		if r.Error != nil {
			return ErrHostingConflict
		}
		if r.RowsAffected != 1 {
			return ErrHostingConflict
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return GetHostingBinding(userID, seats...)
}

// ClaimHostingOperation 独占一个用户的跨系统动作；代数随接管递增，远端拒绝旧工作器迟到的写入。
func ClaimHostingOperation(userID int, seats ...string) (*GPTHostingBinding, string, error) {
	now, token := time.Now().Unix(), uuid.NewString()
	r := hostingSeatScope(DB.Model(&GPTHostingBinding{}), userID, seats).Where("operation_until <= ?", now).Updates(map[string]any{"operation_token": token, "operation_until": now + 120, "generation": gorm.Expr("generation + 1")})
	if r.Error != nil {
		return nil, "", r.Error
	}
	if r.RowsAffected != 1 {
		return nil, "", ErrHostingConflict
	}
	b, err := GetHostingBinding(userID, seats...)
	return b, token, err
}

// UpdateHostingOperation 只有当前租约拥有者能提交阶段；未知远端结果必须保留绑定等待同编号重试。
func UpdateHostingOperation(userID int, token string, changes map[string]any, seats ...string) error {
	changes["operation_until"] = gorm.Expr("operation_until + 1")
	r := hostingSeatScope(DB.Model(&GPTHostingBinding{}), userID, seats).Where("operation_token = ? AND operation_until > ?", token, time.Now().Unix()).Updates(changes)
	if r.Error != nil {
		return ErrHostingConflict
	}
	if r.RowsAffected != 1 {
		return ErrHostingConflict
	}
	return nil
}

// ReleaseHostingOperation 不清理任何账号资源，只释放匹配的短期工作锁，过期旧请求不会影响接管者。
func ReleaseHostingOperation(userID int, token string, seats ...string) {
	hostingSeatScope(DB.Model(&GPTHostingBinding{}), userID, seats).Where("operation_token = ?", token).Updates(map[string]any{"operation_token": "", "operation_until": 0})
}

// BeginHostingMigration records the target before touching the remote slot.
// The source binding remains active, so a failed reservation or OAuth attempt
// never routes requests to an unverified account. The nullable target key is
// protected by a unique index, which closes the cross-request target race on
// SQLite, MySQL, and PostgreSQL alike.
func BeginHostingMigration(userID int, token, targetInventoryID, targetInstance, targetSlot string, seats ...string) (*GPTHostingBinding, error) {
	if !validVM2APIIdentifier(targetInstance, 64) || !validVM2APIIdentifier(targetSlot, 80) || targetInventoryID == "" {
		return nil, ErrHostingConflict
	}
	targetKey := targetInstance + "\x00" + targetSlot
	err := DB.Transaction(func(tx *gorm.DB) error {
		var binding GPTHostingBinding
		if err := hostingSeatScope(lockForUpdate(tx), userID, seats).First(&binding, "operation_token = ? AND operation_until > ?", token, time.Now().Unix()).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrHostingConflict
			}
			return err
		}
		if binding.State != "active" || binding.InventoryID == nil || binding.ChannelID == 0 || binding.AccountHash == nil || binding.SubjectHash == nil || binding.MigrationState != "" {
			return ErrHostingConflict
		}
		var inventory GPTHostingInventory
		if err := lockForUpdate(tx).First(&inventory, "id = ? AND instance_id = ? AND slot_id = ? AND enabled = ?", targetInventoryID, targetInstance, targetSlot, true).Error; err != nil {
			return err
		}
		if inventory.Platform != "" && inventory.Platform != binding.Platform {
			return ErrHostingConflict
		}
		var used int64
		if err := tx.Model(&GPTHostingBinding{}).Where("(inventory_id = ? OR migration_target_key = ?) AND seat_id <> ?", targetInventoryID, targetKey, binding.SeatID).Count(&used).Error; err != nil {
			return err
		}
		if used != 0 {
			return ErrHostingConflict
		}
		changes := map[string]any{
			"migration_state":              "reserving",
			"migration_target_key":         targetKey,
			"migration_source_instance_id": binding.InstanceID,
			"migration_source_slot_id":     binding.SlotID,
			"migration_source_inventory_id": func() string {
				if binding.InventoryID == nil {
					return ""
				}
				return *binding.InventoryID
			}(),
			"migration_target_instance_id":  targetInstance,
			"migration_target_slot_id":      targetSlot,
			"migration_target_inventory_id": targetInventoryID,
		}
		if err := tx.Model(&binding).Updates(changes).Error; err != nil {
			return ErrHostingConflict
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return GetHostingBinding(userID, seats...)
}

// UpdateHostingMigrationState advances the durable migration state under the
// same short operation lease used by all hosted actions. It deliberately does
// not clear the target identity, allowing retries after a lost HTTP response.
func UpdateHostingMigrationState(userID int, token, state string, seats ...string) error {
	if state != "reserving" && state != "reserved" && state != "authorizing" && state != "prepared" && state != "switched" {
		return ErrHostingConflict
	}
	return UpdateHostingOperation(userID, token, map[string]any{"migration_state": state}, seats...)
}

// AbortHostingMigration clears only the central migration marker. The caller
// must have already confirmed that the target remote slot was released.
func AbortHostingMigration(userID int, token string, seats ...string) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		var binding GPTHostingBinding
		if err := hostingSeatScope(lockForUpdate(tx), userID, seats).First(&binding, "operation_token = ? AND operation_until > ?", token, time.Now().Unix()).Error; err != nil {
			return err
		}
		if binding.MigrationState == "" || binding.MigrationState == "switched" {
			return ErrHostingConflict
		}
		return tx.Model(&binding).Updates(map[string]any{
			"migration_state": "", "migration_target_key": nil,
			"migration_source_instance_id": "", "migration_source_slot_id": "", "migration_source_inventory_id": "",
			"migration_target_instance_id": "", "migration_target_slot_id": "", "migration_target_inventory_id": "",
		}).Error
	})
}

// CompleteHostingMigration switches the channel and binding in one database
// transaction after the target remote slot has been activated. The old source
// coordinates stay on the binding until the caller confirms source cleanup.
func CompleteHostingMigration(userID int, token, baseURL, models, key, keyID, poolID string, seats ...string) (*GPTHostingBinding, error) {
	if key == "" || keyID == "" || models == "" || baseURL == "" {
		return nil, ErrHostingConflict
	}
	var saved GPTHostingBinding
	err := DB.Session(&gorm.Session{Logger: logger.Discard}).Transaction(func(tx *gorm.DB) error {
		now := time.Now().Unix()
		var binding GPTHostingBinding
		if err := hostingSeatScope(lockForUpdate(tx), userID, seats).First(&binding, "operation_token = ? AND operation_until > ?", token, now).Error; err != nil {
			return err
		}
		if binding.MigrationState != "prepared" || binding.ChannelID == 0 || binding.AccountHash == nil || binding.SubjectHash == nil || binding.MigrationTargetInventoryID == "" {
			return ErrHostingConflict
		}
		var grant GPTHostingGrant
		if err := hostingSeatScope(lockForUpdate(tx), userID, seats).First(&grant).Error; err != nil {
			return err
		}
		if !grant.ValidAt(now) {
			return ErrHostingUnavailable
		}
		channelType := 1
		if binding.Platform == "claude" {
			channelType = 14
		}
		if err := tx.Model(&Channel{}).Where("id = ?", binding.ChannelID).Updates(map[string]any{
			"type": channelType, "key": key, "status": common.ChannelStatusEnabled,
			"base_url": baseURL, "models": models, "group": grant.Group,
		}).Error; err != nil {
			return err
		}
		if err := tx.Where("channel_id = ?", binding.ChannelID).Delete(&Ability{}).Error; err != nil {
			return err
		}
		var channel Channel
		if err := tx.First(&channel, binding.ChannelID).Error; err != nil {
			return err
		}
		if err := channel.AddAbilities(tx); err != nil {
			return err
		}
		inventoryID := binding.MigrationTargetInventoryID
		changes := map[string]any{
			"inventory_id": inventoryID, "instance_id": binding.MigrationTargetInstanceID, "slot_id": binding.MigrationTargetSlotID,
			"state": "active", "channel_id": binding.ChannelID, "key_id": keyID, "pool_id": poolID, "migration_state": "switched",
			"generation": gorm.Expr("generation + 1"),
		}
		if err := tx.Model(&binding).Updates(changes).Error; err != nil {
			return err
		}
		if err := tx.First(&saved, "seat_id = ?", binding.SeatID).Error; err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &saved, nil
}

// FinalizeHostingMigration forgets the source coordinates only after the
// source vm2api has confirmed release. Routing already uses the target when
// this function is called, so a failed cleanup can be retried safely.
func FinalizeHostingMigration(userID int, token string, seats ...string) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		var binding GPTHostingBinding
		if err := hostingSeatScope(lockForUpdate(tx), userID, seats).First(&binding, "operation_token = ? AND operation_until > ?", token, time.Now().Unix()).Error; err != nil {
			return err
		}
		if binding.MigrationState != "switched" {
			return ErrHostingConflict
		}
		return tx.Model(&binding).Updates(map[string]any{
			"migration_state": "", "migration_target_key": nil,
			"migration_source_instance_id": "", "migration_source_slot_id": "", "migration_source_inventory_id": "",
			"migration_target_instance_id": "", "migration_target_slot_id": "", "migration_target_inventory_id": "",
		}).Error
	})
}

// ClaimHostingIdentity 跨实例独占两个明确的官方标识；重新授权必须仍属于同一主体和账号。
func ClaimHostingIdentity(userID int, token, accountHash, subjectHash, label, plan string, seats ...string) error {
	if len(accountHash) != 64 || len(subjectHash) != 64 || len(label) > 160 || len(plan) > 32 {
		return ErrHostingConflict
	}
	b, err := GetHostingBinding(userID, seats...)
	if err != nil {
		return err
	}
	if (b.AccountHash != nil && *b.AccountHash != accountHash) || (b.SubjectHash != nil && *b.SubjectHash != subjectHash) {
		return ErrHostingConflict
	}
	return UpdateHostingOperation(userID, token, map[string]any{"account_hash": accountHash, "subject_hash": subjectHash, "account_label": label, "plan": plan, "state": "prepared", "identity_source": "official"}, seats...)
}

// CompleteHostingChannel 将用户固定到自己的渠道；上游凭据仅来自受信实例，永不输出到用户响应。
func CompleteHostingChannel(userID int, token, baseURL, models, key, keyID, poolID string, seats ...string) error {
	if key == "" || keyID == "" || models == "" {
		return ErrHostingConflict
	}
	return DB.Session(&gorm.Session{Logger: logger.Discard}).Transaction(func(tx *gorm.DB) error {
		now := time.Now().Unix()
		r := hostingSeatScope(tx.Model(&GPTHostingBinding{}), userID, seats).Where("operation_token = ? AND operation_until > ?", token, now).Update("generation", gorm.Expr("generation + 1"))
		if r.Error != nil {
			return r.Error
		}
		if r.RowsAffected != 1 {
			return ErrHostingConflict
		}
		var b GPTHostingBinding
		var g GPTHostingGrant
		if err := hostingSeatScope(lockForUpdate(tx), userID, seats).First(&b).Error; err != nil {
			return err
		}
		if err := hostingSeatScope(lockForUpdate(tx), userID, seats).First(&g).Error; err != nil {
			return err
		}
		if !g.ValidAt(now) || b.AccountHash == nil {
			return ErrHostingUnavailable
		}
		ch := Channel{Name: "hosted-" + b.Platform + "-" + b.BindingID, Type: 1, Key: key, Status: common.ChannelStatusEnabled, BaseURL: &baseURL, Models: models, Group: g.Group, CreatedTime: now}
		if b.Platform == "claude" {
			ch.Type = 14
		}
		if b.LegacySnapshot != "" && b.State == "takeover" {
			var old Channel
			if err := lockForUpdate(tx).First(&old, b.ChannelID).Error; err != nil {
				return err
			}
			if hostingChannelFingerprint(old) != b.LegacySnapshot {
				return ErrHostingConflict
			}
		}
		if b.ChannelID != 0 {
			ch.Id = b.ChannelID
			if err := tx.Model(&Channel{}).Where("id = ?", ch.Id).Updates(map[string]any{"type": ch.Type, "key": key, "status": ch.Status, "base_url": baseURL, "models": models, "group": g.Group}).Error; err != nil {
				return err
			}
		} else if err := tx.Create(&ch).Error; err != nil {
			return err
		}
		if err := tx.Where("channel_id = ?", ch.Id).Delete(&Ability{}).Error; err != nil {
			return err
		}
		if err := ch.AddAbilities(tx); err != nil {
			return err
		}
		return hostingSeatScope(tx.Model(&GPTHostingBinding{}), userID, seats).Updates(map[string]any{"channel_id": ch.Id, "key_id": keyID, "pool_id": poolID, "state": "active"}).Error
	})
}

// ClearHostingBinding 仅在远端已停止进程并清除授权后调用；释放身份和槽位，不恢复公共组访问。
func ClearHostingBinding(userID int, token string, seats ...string) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		var binding GPTHostingBinding
		if err := hostingSeatScope(lockForUpdate(tx), userID, seats).First(&binding, "operation_token = ? AND operation_until > ? AND state = ?", token, time.Now().Unix(), "cleaning").Error; err != nil {
			return err
		}
		// 远端已确认清理，门户同时删除可用凭据和能力；原渠道编号可留给再次绑定复用。
		if binding.ChannelID != 0 {
			if err := tx.Model(&Channel{}).Where("id = ?", binding.ChannelID).Updates(map[string]any{"key": "", "status": common.ChannelStatusManuallyDisabled}).Error; err != nil {
				return err
			}
			if err := tx.Where("channel_id = ?", binding.ChannelID).Delete(&Ability{}).Error; err != nil {
				return err
			}
		}
		return hostingSeatScope(tx.Model(&GPTHostingBinding{}), userID, seats).Updates(map[string]any{"inventory_id": nil, "account_hash": nil, "subject_hash": nil, "instance_id": "", "slot_id": "", "state": "unbound", "identity_source": "", "account_label": "", "plan": "", "key_id": "", "pool_id": "", "paused": false}).Error
	})
}

// ExpireHostingBinding 在同一事务中核对最新期限并占有清理；续费与回收的竞争不能清理新续期账号。
func ExpireHostingBinding(userID int, now int64, seats ...string) (bool, error) {
	changed := false
	err := DB.Transaction(func(tx *gorm.DB) error {
		// 与名额更新使用相同锁序；写 revision 也为 SQLite 获取写锁。
		if err := hostingSeatScope(tx.Model(&GPTHostingGrant{}), userID, seats).Update("revision", gorm.Expr("revision + 1")).Error; err != nil {
			return err
		}
		var g GPTHostingGrant
		var b GPTHostingBinding
		if err := hostingSeatScope(lockForUpdate(tx), userID, seats).First(&g).Error; err != nil {
			return err
		}
		if err := hostingSeatScope(lockForUpdate(tx), userID, seats).First(&b).Error; err != nil {
			return err
		}
		if b.State == "unbound" || b.State == "cleaning" || b.State == "takeover" || b.OperationUntil > now {
			return nil
		}
		// 未完成的初次登录预约只保留十五分钟；已有身份的重登失败不能释放他人账号归属。
		abandoned := b.AccountHash == nil && (b.State == "reserved" || b.State == "authorizing") && b.UpdatedAt < now-900
		// 永久权益不按时间回收，但放弃的首次登录仍须释放预约，避免无限占槽。
		if (g.EndsAt == HostingPermanentEnd || now < g.EndsAt+int64(g.RetentionDays)*86400) && !abandoned {
			return nil
		}
		changed = true
		return tx.Model(&b).Update("state", "cleaning").Error
	})
	return changed, err
}

// GetHostingRoute 返回当前有效的专属路由；即使用户或令牌缓存残留公共组，也不能路由到其他账号。
func GetHostingRoute(userID int, seats ...string) (string, int, error) {
	if os.Getenv("GPT_HOSTING_ENABLED") != "true" {
		return "", 0, nil
	}
	g, err := GetHostingGrant(userID, seats...)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if len(seats) > 0 && seats[0] != "" {
			return "", 0, ErrHostingUnavailable
		}
		return "", 0, nil
	}
	if err != nil {
		return "", 0, err
	}
	if !g.ValidAt(time.Now().Unix()) {
		return "", 0, ErrHostingUnavailable
	}
	b, err := GetHostingBinding(userID, seats...)
	if err != nil {
		return "", 0, err
	}
	if b.State != "active" || b.Paused || b.InventoryID == nil || b.ChannelID == 0 {
		return "", 0, ErrHostingUnavailable
	}
	return g.Group, b.ChannelID, nil
}

// CheckHostingAccess 每个实际请求从数据库读取权益；没有托管权益的历史用户保持既有行为。
func CheckHostingAccess(userID int, seats ...string) error {
	_, _, err := GetHostingRoute(userID, seats...)
	return err
}
