package model

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

const (
	VM2APIEnrollmentPending              = "pending"
	VM2APIEnrollmentFailed               = "failed"
	VM2APIEnrollmentCompleted            = "completed"
	vm2apiEnrollmentLeaseSeconds   int64 = 180
	vm2apiEnrollmentFailureMessage       = "Enrollment could not be completed. Retry to continue."
)

var (
	ErrVM2APIEnrollmentConflict = errors.New("enrollment request conflicts with an existing reservation")
	ErrVM2APIEnrollmentBusy     = errors.New("enrollment request is already running")
	ErrVM2APIEnrollmentLease    = errors.New("enrollment lease is no longer valid")
	vm2apiEnrollmentIdentifier  = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	// 数据库行锁负责跨进程并发；本地锁还保证多个开户提交后的倍率缓存按提交顺序发布。
	vm2apiEnrollmentCommitMutex sync.Mutex
)

// VM2APIEnrollment 保留一次开户的幂等状态，唯一约束使用户名和实例槽位只能被一次开户占有。
// 密码仅保存已计算的哈希，成功后清除；租约令牌与哈希均不允许序列化给浏览器。
type VM2APIEnrollment struct {
	RequestID    string `json:"request_id" gorm:"type:varchar(36);primaryKey"`
	ActorID      int    `json:"actor_id" gorm:"not null;index"`
	Username     string `json:"username" gorm:"type:varchar(20);not null;uniqueIndex:idx_vm2api_enrollment_username"`
	DisplayName  string `json:"display_name" gorm:"type:varchar(20);not null"`
	PasswordHash string `json:"-" gorm:"type:varchar(256);not null"`
	InstanceID   string `json:"instance_id" gorm:"type:varchar(64);not null;uniqueIndex:idx_vm2api_enrollment_slot,priority:1"`
	SlotID       string `json:"slot_id" gorm:"type:varchar(80);not null;uniqueIndex:idx_vm2api_enrollment_slot,priority:2"`
	Group        string `json:"group" gorm:"type:varchar(64);not null;uniqueIndex:idx_vm2api_enrollment_group"`
	Quota        int    `json:"quota" gorm:"type:bigint;not null"`
	Status       string `json:"status" gorm:"type:varchar(16);not null;index"`
	Message      string `json:"message" gorm:"type:varchar(255);not null"`
	UserID       int    `json:"user_id" gorm:"index"`
	ChannelID    int    `json:"channel_id"`
	PoolID       string `json:"pool_id" gorm:"type:varchar(128)"`
	KeyID        string `json:"key_id" gorm:"type:varchar(128)"`
	LeaseToken   string `json:"-" gorm:"type:varchar(36);not null"`
	LeaseUntil   int64  `json:"-" gorm:"not null"`
	CreatedAt    int64  `json:"created_at" gorm:"autoCreateTime"`
	UpdatedAt    int64  `json:"updated_at" gorm:"autoUpdateTime"`
}

// VM2APISlotDraft 记录管理员创建槽位的请求，重试使用相同远端 ID，避免网络超时生成重复槽位。
type VM2APISlotDraft struct {
	RequestID  string `json:"request_id" gorm:"type:varchar(36);primaryKey"`
	ActorID    int    `json:"actor_id" gorm:"not null;index"`
	InstanceID string `json:"instance_id" gorm:"type:varchar(64);not null"`
	SlotID     string `json:"slot_id" gorm:"type:varchar(80);not null;uniqueIndex:idx_vm2api_draft_slot"`
	Name       string `json:"name" gorm:"type:varchar(80);not null"`
	ExitID     string `json:"exit_id" gorm:"type:varchar(128);not null"`
	LeaseToken string `json:"-" gorm:"type:varchar(36);not null"`
	LeaseUntil int64  `json:"-" gorm:"not null"`
	CreatedAt  int64  `json:"created_at" gorm:"autoCreateTime"`
	UpdatedAt  int64  `json:"updated_at" gorm:"autoUpdateTime"`
}

// canonicalVM2APIRequestID 只接收标准小写 UUID，防止不同字符串形式生成不同分组或槽位。
func canonicalVM2APIRequestID(id string) bool {
	parsed, err := uuid.Parse(id)
	return err == nil && parsed != uuid.Nil && parsed.String() == id
}

// validVM2APIIdentifier 限制数据库标识的长度与字符；标识不能被拼成路径、查询或多个分组。
func validVM2APIIdentifier(value string, maximum int) bool {
	return len(value) > 0 && len(value) <= maximum && vm2apiEnrollmentIdentifier.MatchString(value)
}

// ReserveVM2APIEnrollment 原子预留用户名与槽位，重放必须属于同一管理员且元数据完全相同。
// 调用方先校验原始密码再传入哈希；重复请求的密码验证由 controller 完成，不比较随机盐哈希。
func ReserveVM2APIEnrollment(input *VM2APIEnrollment) (*VM2APIEnrollment, error) {
	if input == nil || !canonicalVM2APIRequestID(input.RequestID) || input.ActorID <= 0 ||
		!validVM2APIIdentifier(input.Username, UserNameMaxLength) || utf8.RuneCountInString(input.DisplayName) > 20 ||
		!utf8.ValidString(input.DisplayName) || !validVM2APIIdentifier(input.InstanceID, 64) || !validVM2APIIdentifier(input.SlotID, 80) ||
		input.Quota < 0 || common.ValidateWalletQuota(input.Quota) != nil || len(input.PasswordHash) > 256 ||
		!(strings.HasPrefix(input.PasswordHash, "$argon2id$") || strings.HasPrefix(input.PasswordHash, "$2a$") || strings.HasPrefix(input.PasswordHash, "$2b$")) {
		return nil, ErrVM2APIEnrollmentConflict
	}
	var saved VM2APIEnrollment
	err := DB.Where("request_id = ?", input.RequestID).First(&saved).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		// 包括软删除用户：users.username 的唯一约束不会随软删除释放。
		var existing int64
		if err := DB.Unscoped().Model(&User{}).Where("username = ?", input.Username).Count(&existing).Error; err != nil {
			return nil, err
		}
		if existing != 0 {
			return nil, ErrVM2APIEnrollmentConflict
		}
		candidate := VM2APIEnrollment{
			RequestID: input.RequestID, ActorID: input.ActorID, Username: input.Username, DisplayName: input.DisplayName,
			PasswordHash: input.PasswordHash, InstanceID: input.InstanceID, SlotID: input.SlotID,
			Group: "acct_" + strings.ReplaceAll(input.RequestID, "-", ""), Quota: input.Quota, Status: VM2APIEnrollmentPending,
		}
		// 三种数据库对冲突写入的 RowsAffected 不同，统一读回 request_id 验证真实归属。
		// 此写入包含密码哈希；即使运维开启 GORM Debug，也不能把参数值输出到 SQL 日志。
		if err := DB.Session(&gorm.Session{Logger: logger.Discard}).Transaction(func(tx *gorm.DB) error {
			// 旧开户入口与自助商城共享库存锁，不能在订单付款期间抢走预留的槽位。
			if tx.Migrator().HasTable(&GPTHostingInventory{}) {
				var inventory GPTHostingInventory
				err := lockForUpdate(tx).Where("instance_id = ? AND slot_id = ?", input.InstanceID, input.SlotID).First(&inventory).Error
				if err == nil {
					if err := checkHostingInventoryHold(tx, inventory.ID, "", time.Now().Unix()); err != nil {
						return err
					}
					var used int64
					if err := tx.Model(&GPTHostingBinding{}).Where("inventory_id = ?", inventory.ID).Count(&used).Error; err != nil {
						return err
					}
					if used != 0 {
						return ErrVM2APIEnrollmentConflict
					}
				} else if !errors.Is(err, gorm.ErrRecordNotFound) {
					return err
				}
			}
			return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&candidate).Error
		}); err != nil {
			return nil, err
		}
		err = DB.Where("request_id = ?", input.RequestID).First(&saved).Error
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrVM2APIEnrollmentConflict
	}
	if err != nil {
		return nil, err
	}
	if saved.ActorID != input.ActorID || saved.Username != input.Username || saved.DisplayName != input.DisplayName ||
		saved.InstanceID != input.InstanceID || saved.SlotID != input.SlotID || saved.Quota != input.Quota {
		return nil, ErrVM2APIEnrollmentConflict
	}
	return &saved, nil
}

// GetVM2APIEnrollment 按请求和发起管理员读取记录；跨管理员访问与不存在使用相同结果。
func GetVM2APIEnrollment(requestID string, actorID int) (*VM2APIEnrollment, error) {
	var job VM2APIEnrollment
	err := DB.Where("request_id = ? AND actor_id = ?", requestID, actorID).First(&job).Error
	return &job, err
}

// GetVM2APIEnrollmentForUser 只返回已提交开户的绑定，失败或尚未完成的预留不能提供用户额度。
func GetVM2APIEnrollmentForUser(userID int) (*VM2APIEnrollment, error) {
	var job VM2APIEnrollment
	if userID <= 0 {
		return nil, gorm.ErrRecordNotFound
	}
	err := DB.Where("user_id = ? AND status = ?", userID, VM2APIEnrollmentCompleted).First(&job).Error
	return &job, err
}

// IsVM2APISlotAssigned 将未完成预留也视为占有；数据库异常返回错误，不能误判为可分配。
func IsVM2APISlotAssigned(instanceID, slotID string) (bool, error) {
	var count int64
	err := DB.Model(&VM2APIEnrollment{}).Where("instance_id = ? AND slot_id = ?", instanceID, slotID).Count(&count).Error
	return count != 0, err
}

// ClaimVM2APIEnrollment 通过单条条件更新取得 180 秒租约；已完成请求直接返回，令牌为空。
// 过期租约可接管，但旧工作器的完成或失败写入会被令牌比较拒绝。
func ClaimVM2APIEnrollment(requestID string, actorID int) (*VM2APIEnrollment, string, error) {
	job, err := GetVM2APIEnrollment(requestID, actorID)
	if err != nil || job.Status == VM2APIEnrollmentCompleted {
		return job, "", err
	}
	now, token := time.Now().Unix(), uuid.NewString()
	result := DB.Model(&VM2APIEnrollment{}).Where("request_id = ? AND actor_id = ? AND status IN ? AND lease_until <= ?", requestID, actorID, []string{VM2APIEnrollmentPending, VM2APIEnrollmentFailed}, now).
		Updates(map[string]any{"status": VM2APIEnrollmentPending, "message": "", "lease_token": token, "lease_until": now + vm2apiEnrollmentLeaseSeconds})
	if result.Error != nil {
		return nil, "", result.Error
	}
	if result.RowsAffected != 1 {
		job, err = GetVM2APIEnrollment(requestID, actorID)
		if err == nil && job.Status == VM2APIEnrollmentCompleted {
			return job, "", nil
		}
		return nil, "", ErrVM2APIEnrollmentBusy
	}
	job, err = GetVM2APIEnrollment(requestID, actorID)
	return job, token, err
}

// FailVM2APIEnrollment 仅释放当前未过期租约，预留仍保留以支持同一请求重试。
// 原始异常可能带有上游 Key 或登录信息，因此数据库中只存固定安全文案，不保存调用方正文。
func FailVM2APIEnrollment(requestID, lease, message string) error {
	if lease == "" {
		return ErrVM2APIEnrollmentLease
	}
	result := DB.Model(&VM2APIEnrollment{}).Where("request_id = ? AND lease_token = ? AND lease_until > ? AND status = ?", requestID, lease, time.Now().Unix(), VM2APIEnrollmentPending).
		Updates(map[string]any{"status": VM2APIEnrollmentFailed, "message": vm2apiEnrollmentFailureMessage, "lease_token": "", "lease_until": 0})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrVM2APIEnrollmentLease
	}
	return nil
}

// CompleteVM2APIEnrollment 在单事务内创建普通用户、专属渠道、能力、倍率与完成绑定。
// baseURL/models/Key 必须来自服务端配置与 vm2api 客户端；调用方提交成功后刷新渠道缓存。
// 已计算的密码哈希直接写入，不调用会重新哈希或发放注册赠额的 Insert/FinishInsert。
func CompleteVM2APIEnrollment(requestID, lease, baseURL, models, upstreamKey, poolID, keyID string) (*VM2APIEnrollment, error) {
	u, err := url.Parse(baseURL)
	if lease == "" || err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		(u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "::1"))) ||
		len(upstreamKey) == 0 || len(upstreamKey) > 4096 || !validVM2APIIdentifier(poolID, 128) || !validVM2APIIdentifier(keyID, 128) || len(models) == 0 || len(models) > 8192 {
		return nil, ErrVM2APIEnrollmentConflict
	}
	for modelName := range strings.SplitSeq(models, ",") {
		if len(modelName) == 0 || len(modelName) > 255 || strings.TrimSpace(modelName) != modelName || strings.ContainsAny(modelName, "\r\n\x00|") {
			return nil, ErrVM2APIEnrollmentConflict
		}
	}
	vm2apiEnrollmentCommitMutex.Lock()
	defer vm2apiEnrollmentCommitMutex.Unlock()
	var completed VM2APIEnrollment
	var ratioJSON string
	// 整个事务包含密码哈希与可用上游 Key，禁用 SQL 参数日志；调用方只报告固定错误。
	err = DB.Session(&gorm.Session{Logger: logger.Discard}).Transaction(func(tx *gorm.DB) error {
		now := time.Now().Unix()
		// 先 CAS 写入获得 SQLite 的写锁，也阻止其他数据库在事务中接管租约。
		// 必须实际改变值：MySQL 默认只统计变更行，写入相同秒数会误报租约失效。
		claim := tx.Model(&VM2APIEnrollment{}).Where("request_id = ? AND lease_token = ? AND lease_until > ? AND status = ?", requestID, lease, now, VM2APIEnrollmentPending).
			Update("lease_until", gorm.Expr("lease_until + ?", 1))
		if claim.Error != nil {
			return claim.Error
		}
		if claim.RowsAffected != 1 {
			return ErrVM2APIEnrollmentLease
		}
		if err := lockForUpdate(tx).Where("request_id = ?", requestID).First(&completed).Error; err != nil {
			return err
		}
		settings, err := common.Marshal(dto.UserSetting{SidebarModules: generateDefaultSidebarConfigForRole(common.RoleCommonUser)})
		if err != nil {
			return err
		}
		user := User{Username: completed.Username, Password: completed.PasswordHash, DisplayName: completed.DisplayName,
			Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Group: completed.Group, Quota: completed.Quota,
			AffCode: strings.ReplaceAll(uuid.NewString(), "-", ""), Setting: string(settings), AuthVersion: 1}
		if err := tx.Create(&user).Error; err != nil {
			return err
		}
		channel := Channel{Name: "vm2api-" + completed.Username, Type: 1, Key: upstreamKey, Status: common.ChannelStatusEnabled,
			BaseURL: &baseURL, Models: models, Group: completed.Group, CreatedTime: now}
		if err := tx.Create(&channel).Error; err != nil {
			return err
		}
		if err := channel.AddAbilities(tx); err != nil {
			return err
		}
		// 先确保倍率配置行存在，再锁行合并，防止并发开户覆盖另一个已完成分组。
		option := Option{Key: "GroupRatio", Value: ratio_setting.GroupRatio2JSONString()}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&option).Error; err != nil {
			return err
		}
		if err := lockForUpdate(tx).Where(Option{Key: "GroupRatio"}).First(&option).Error; err != nil {
			return err
		}
		ratios := map[string]float64{}
		if err := common.UnmarshalJsonStr(option.Value, &ratios); err != nil || ratios == nil {
			return ErrVM2APIEnrollmentConflict
		}
		if _, exists := ratios[completed.Group]; exists {
			return ErrVM2APIEnrollmentConflict
		}
		ratios[completed.Group] = 1
		raw, err := common.Marshal(ratios)
		if err != nil {
			return err
		}
		ratioJSON = string(raw)
		if err := tx.Model(&Option{}).Where(Option{Key: "GroupRatio"}).Update("value", ratioJSON).Error; err != nil {
			return err
		}
		completed.UserID, completed.ChannelID = user.Id, channel.Id
		completed.PoolID, completed.KeyID = poolID, keyID
		completed.Status, completed.Message, completed.PasswordHash = VM2APIEnrollmentCompleted, "", ""
		completed.LeaseToken, completed.LeaseUntil = "", 0
		return tx.Save(&completed).Error
	})
	if err != nil {
		return nil, err
	}
	// 只有事务提交成功才发布内存倍率，避免数据库回滚后用户看见尚不存在的分组。
	if err := updateOptionMap("GroupRatio", ratioJSON); err != nil {
		return &completed, err
	}
	return &completed, nil
}

// ReserveVM2APISlotDraft 按请求 UUID 固定远端槽位名，重放不能切换实例、出口或显示名。
func ReserveVM2APISlotDraft(input *VM2APISlotDraft) (*VM2APISlotDraft, error) {
	if input == nil || !canonicalVM2APIRequestID(input.RequestID) || input.ActorID <= 0 || !validVM2APIIdentifier(input.InstanceID, 64) ||
		len(input.Name) == 0 || utf8.RuneCountInString(input.Name) > 80 || !utf8.ValidString(input.Name) || strings.ContainsAny(input.Name, "\r\n\x00") ||
		(len(input.ExitID) > 0 && !validVM2APIIdentifier(input.ExitID, 128)) {
		return nil, ErrVM2APIEnrollmentConflict
	}
	candidate := VM2APISlotDraft{RequestID: input.RequestID, ActorID: input.ActorID, InstanceID: input.InstanceID,
		SlotID: "na-" + input.RequestID, Name: input.Name, ExitID: input.ExitID}
	if err := DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&candidate).Error; err != nil {
		return nil, err
	}
	var saved VM2APISlotDraft
	if err := DB.Where("request_id = ?", input.RequestID).First(&saved).Error; err != nil {
		return nil, err
	}
	if saved.ActorID != input.ActorID || saved.InstanceID != input.InstanceID || saved.Name != input.Name || saved.ExitID != input.ExitID {
		return nil, ErrVM2APIEnrollmentConflict
	}
	return &saved, nil
}

// CanUseVM2APISlotDraft 限定本页面创建的槽位只能由创建管理员继续开户或登录。
// 没有草稿的既有槽位沿用原管理权限；读取失败不能视为没有草稿而放行。
func CanUseVM2APISlotDraft(instanceID, slotID string, actorID int) (bool, error) {
	if actorID <= 0 {
		return false, nil
	}
	var draft VM2APISlotDraft
	err := DB.Where("instance_id = ? AND slot_id = ?", instanceID, slotID).First(&draft).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return draft.ActorID == actorID, nil
}

// GetVM2APISlotDraft 限定创建管理员读取槽位草稿，不能根据别人的请求 ID 接管登录流程。
func GetVM2APISlotDraft(requestID string, actorID int) (*VM2APISlotDraft, error) {
	var draft VM2APISlotDraft
	err := DB.Where("request_id = ? AND actor_id = ?", requestID, actorID).First(&draft).Error
	return &draft, err
}

// ClaimVM2APISlotDraft 为创建槽位取得 180 秒租约，防止重复点击或并发重试重复操作远端。
func ClaimVM2APISlotDraft(requestID string, actorID int) (*VM2APISlotDraft, string, error) {
	if _, err := GetVM2APISlotDraft(requestID, actorID); err != nil {
		return nil, "", err
	}
	now, token := time.Now().Unix(), uuid.NewString()
	result := DB.Model(&VM2APISlotDraft{}).Where("request_id = ? AND actor_id = ? AND lease_until <= ?", requestID, actorID, now).
		Updates(map[string]any{"lease_token": token, "lease_until": now + vm2apiEnrollmentLeaseSeconds})
	if result.Error != nil {
		return nil, "", result.Error
	}
	if result.RowsAffected != 1 {
		return nil, "", ErrVM2APIEnrollmentBusy
	}
	draft, err := GetVM2APISlotDraft(requestID, actorID)
	return draft, token, err
}

// ReleaseVM2APISlotDraft 只释放当前有效租约，过期工作器不能清除新工作器的锁。
func ReleaseVM2APISlotDraft(requestID, lease string) error {
	if lease == "" {
		return ErrVM2APIEnrollmentLease
	}
	result := DB.Model(&VM2APISlotDraft{}).Where("request_id = ? AND lease_token = ? AND lease_until > ?", requestID, lease, time.Now().Unix()).
		Updates(map[string]any{"lease_token": "", "lease_until": 0})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrVM2APIEnrollmentLease
	}
	return nil
}
