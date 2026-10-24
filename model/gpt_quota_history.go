package model

import (
	"context"
	"errors"
	"math"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const GPTQuotaHistoryDays = 30
const GPTQuotaHistoryBucketSeconds int64 = 300

// GPTQuotaHistory 仅保存官方百分比和时间；身份指纹不包含原始邮箱、凭据或会话。
// 每个绑定每五分钟最多一个点，保留最新真实读数，限制小磁盘实例的增长速度。
type GPTQuotaHistory struct {
	ID          int64    `json:"-" gorm:"primaryKey"`
	UserID      int      `json:"-" gorm:"uniqueIndex:idx_quota_history_bucket,priority:1"`
	Scope       string   `json:"-" gorm:"type:varchar(64);uniqueIndex:idx_quota_history_bucket,priority:2"`
	Bucket      int64    `json:"-" gorm:"uniqueIndex:idx_quota_history_bucket,priority:3;index:idx_quota_history_retention"`
	ObservedAt  int64    `json:"observed_at"`
	Segment     string   `json:"segment" gorm:"type:varchar(64)"`
	Remaining5h *float64 `json:"remaining_5h" gorm:"column:remaining_5h"`
	Remaining7d *float64 `json:"remaining_7d" gorm:"column:remaining_7d"`
	Reset5h     *int64   `json:"reset_5h" gorm:"column:reset_5h"`
	Reset7d     *int64   `json:"reset_7d" gorm:"column:reset_7d"`
}

// SaveGPTQuotaHistory 原子合并同一五分钟桶中的读数；并发采集和手动刷新不能倒退时间。
// observed_at 使用毫秒；缺失或已经到期的窗口必须由调用者保留为 nil。
func SaveGPTQuotaHistory(ctx context.Context, row GPTQuotaHistory, now time.Time) error {
	if row.UserID <= 0 || len(row.Scope) != 64 || len(row.Segment) != 64 || row.ObservedAt <= 0 ||
		row.ObservedAt > now.UnixMilli() || row.ObservedAt < now.Add(-10*time.Minute).UnixMilli() {
		return errors.New("invalid quota history observation")
	}
	for _, value := range []*float64{row.Remaining5h, row.Remaining7d} {
		if value != nil && (math.IsNaN(*value) || math.IsInf(*value, 0) || *value < 0 || *value > 100) {
			return errors.New("invalid quota history percentage")
		}
	}
	row.Bucket = row.ObservedAt / 1000 / GPTQuotaHistoryBucketSeconds * GPTQuotaHistoryBucketSeconds
	// 所有赋值先比较旧 observed_at，最后才更新 observed_at。MySQL 的赋值顺序
	// 与 PostgreSQL/SQLite 不同，因此不能让 GORM 按字段名重排此更新列表。
	updates := make(clause.Set, 0, 6)
	for _, field := range []struct {
		name  string
		value any
	}{
		{"segment", row.Segment}, {"remaining_5h", row.Remaining5h}, {"remaining_7d", row.Remaining7d},
		{"reset_5h", row.Reset5h}, {"reset_7d", row.Reset7d},
	} {
		updates = append(updates, clause.Assignment{Column: clause.Column{Name: field.name}, Value: gorm.Expr(
			"CASE WHEN gpt_quota_histories.observed_at < ? THEN ? ELSE gpt_quota_histories."+field.name+" END", row.ObservedAt, field.value)})
	}
	updates = append(updates, clause.Assignment{Column: clause.Column{Name: "observed_at"}, Value: gorm.Expr(
		"CASE WHEN gpt_quota_histories.observed_at < ? THEN ? ELSE gpt_quota_histories.observed_at END", row.ObservedAt, row.ObservedAt)})
	return DB.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "user_id"}, {Name: "scope"}, {Name: "bucket"}}, DoUpdates: updates,
	}).Create(&row).Error
}

// GetGPTQuotaHistory 只接受服务端生成的当前归属范围，并固定时间窗口与最大结果数。
// 不允许按槽位单独查历史，避免槽位重新分配后读到前一位用户的记录。
func GetGPTQuotaHistory(ctx context.Context, userID int, scope string, hours int, now time.Time) ([]GPTQuotaHistory, error) {
	rows := make([]GPTQuotaHistory, 0)
	if userID <= 0 || len(scope) != 64 || (hours != 24 && hours != 168 && hours != 720) {
		return nil, errors.New("invalid quota history range")
	}
	since := now.Add(-time.Duration(hours) * time.Hour)
	err := DB.WithContext(ctx).Where("user_id = ? AND scope = ? AND bucket >= ? AND observed_at >= ? AND observed_at <= ?",
		userID, scope, since.Unix()/GPTQuotaHistoryBucketSeconds*GPTQuotaHistoryBucketSeconds, since.UnixMilli(), now.UnixMilli()).
		Order("bucket ASC").Limit(GPTQuotaHistoryDays*24*12 + 1).Find(&rows).Error
	return rows, err
}

// PruneGPTQuotaHistory 分批删除超过保留期的记录，不清理其他业务表，也不执行阻塞式 VACUUM。
func PruneGPTQuotaHistory(ctx context.Context, now time.Time) error {
	var ids []int64
	if err := DB.WithContext(ctx).Model(&GPTQuotaHistory{}).Where("bucket < ?", now.Add(-GPTQuotaHistoryDays*24*time.Hour).Unix()).
		Order("bucket ASC").Limit(1000).Pluck("id", &ids).Error; err != nil || len(ids) == 0 {
		return err
	}
	return DB.WithContext(ctx).Where("id IN ?", ids).Delete(&GPTQuotaHistory{}).Error
}
