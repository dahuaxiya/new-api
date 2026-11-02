package model

import (
	"encoding/hex"
	"errors"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// HostingResetAttempt 按官方账号保存最后一次重置状态，跨解绑和迁移保留未确认结果。
// AccountHash 只供服务端定位，不能返回浏览器或写入日志。
type HostingResetAttempt struct {
	AccountHash string `gorm:"type:varchar(64);primaryKey"`
	OperationID string `gorm:"type:varchar(36)"`
	Status      string `gorm:"type:varchar(16);not null"`
	AttemptedAt int64  `gorm:"not null"`
}

const hostingResetUnknownSeconds = 24 * 60 * 60

// validHostingResetIdentity 只接受服务端保存的 SHA-256 账号标识和规范 UUID 操作编号。
// @param accountHash 服务端保存的账号摘要。
// @param operationID 与本次消费绑定的规范 UUID。
// @return 两项标识均有效时为 true。
func validHostingResetIdentity(accountHash, operationID string) bool {
	decoded, err := hex.DecodeString(accountHash)
	if err != nil || len(decoded) != 32 || len(accountHash) != 64 {
		return false
	}
	id, err := uuid.Parse(operationID)
	return err == nil && id != uuid.Nil && id.String() == operationID
}

// BeginHostingResetAttempt 在发送上游请求前持久化不确定状态；原子条件更新阻止并发或重启后的重复消耗。
// 未确认结果保留 24 小时，与 Claude 上游的保护期限一致；数据库失败时不能发起兑换。
// @param accountHash 跨名额和迁移保持不变的账号摘要。
// @param operationID 本次消费的唯一操作编号。
// @param now 本次尝试的服务端时间。
// @return 成功取得消费权时为空，否则返回冲突或数据库错误。
func BeginHostingResetAttempt(accountHash, operationID string, now time.Time) error {
	if !validHostingResetIdentity(accountHash, operationID) {
		return ErrHostingConflict
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&HostingResetAttempt{AccountHash: accountHash, Status: "idle"}).Error; err != nil {
			return err
		}
		result := tx.Model(&HostingResetAttempt{}).Where("account_hash = ? AND (status <> ? OR attempted_at <= ?)", accountHash, "pending", now.Unix()-hostingResetUnknownSeconds).
			Updates(map[string]any{"operation_id": operationID, "status": "pending", "attempted_at": now.Unix()})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrHostingConflict
		}
		return nil
	})
}

// FinishHostingResetAttempt 仅在远端明确确认已重置或未使用时解除保护；响应丢失必须保留 pending。
// @param accountHash 本次消费对应的账号摘要。
// @param operationID 取得消费权时使用的操作编号，旧操作不能覆盖新操作。
// @param outcome 只能是 reset 或 not_used。
// @return 操作编号和状态匹配并已更新时为空，否则返回错误。
func FinishHostingResetAttempt(accountHash, operationID, outcome string) error {
	if !validHostingResetIdentity(accountHash, operationID) || (outcome != "reset" && outcome != "not_used") {
		return ErrHostingConflict
	}
	result := DB.Model(&HostingResetAttempt{}).Where("account_hash = ? AND operation_id = ? AND status = ?", accountHash, operationID, "pending").Update("status", outcome)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrHostingConflict
	}
	return nil
}

// HostingResetBlockedUntil 返回仍未确认的保护截止时间；不存在记录时不阻止首次使用。
// @param accountHash 要查询的账号摘要。
// @param now 判断保护是否过期使用的服务端时间。
// @return 未确认保护的 Unix 截止时间，0 表示当前无需阻止；查询失败时返回错误。
func HostingResetBlockedUntil(accountHash string, now time.Time) (int64, error) {
	if _, err := hex.DecodeString(accountHash); err != nil || len(accountHash) != 64 {
		return 0, ErrHostingConflict
	}
	var attempt HostingResetAttempt
	err := DB.First(&attempt, "account_hash = ?", accountHash).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	until := attempt.AttemptedAt + hostingResetUnknownSeconds
	if attempt.Status == "pending" && until > now.Unix() {
		return until, nil
	}
	return 0, nil
}
