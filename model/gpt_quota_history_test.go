package model

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestGPTQuotaHistoryDatabases 验证真实三种数据库的新建、旧数据升级及重复迁移，
// 并验证并发去重、较旧读数不能覆盖新读数、归属过滤和有限批次的过期清理。
func TestGPTQuotaHistoryDatabases(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := useVM2APIEnrollmentDatabase(t, dialect)
			ctx := context.Background()
			// 此版本仅添加独立表，既有用户和开户表使用上一发布版本未变更的结构。
			require.NoError(t, db.AutoMigrate(&User{}, &VM2APIEnrollment{}))
			user := User{Username: "history-owner", Group: "default", Status: 1, Quota: 123}
			require.NoError(t, db.Create(&user).Error)
			for range 2 {
				require.NoError(t, db.AutoMigrate(&GPTQuotaHistory{}))
			}
			now := time.Now().UTC().Truncate(5 * time.Minute).Add(4 * time.Minute)
			percent := 65.0
			scope := strings.Repeat("a", 64)
			row := GPTQuotaHistory{UserID: user.Id, Scope: scope, Segment: strings.Repeat("b", 64), ObservedAt: now.Add(-time.Minute).UnixMilli(), Remaining7d: &percent}
			require.NoError(t, SaveGPTQuotaHistory(ctx, row, now))
			newer := row
			newPercent := 61.0
			newer.ObservedAt, newer.Remaining7d = now.UnixMilli(), &newPercent
			require.NoError(t, SaveGPTQuotaHistory(ctx, newer, now))
			var wg sync.WaitGroup
			errors := make(chan error, 6)
			for range 6 {
				wg.Go(func() { errors <- SaveGPTQuotaHistory(ctx, row, now) })
			}
			wg.Wait()
			close(errors)
			for err := range errors {
				require.NoError(t, err)
			}
			rows, err := GetGPTQuotaHistory(ctx, user.Id, scope, 24, now)
			require.NoError(t, err)
			require.Len(t, rows, 1)
			require.Equal(t, newer.ObservedAt, rows[0].ObservedAt)
			require.Equal(t, 61.0, *rows[0].Remaining7d)
			require.Nil(t, rows[0].Remaining5h)
			for range 2 {
				require.NoError(t, db.AutoMigrate(&GPTQuotaHistory{}))
			}
			var preserved User
			require.NoError(t, db.First(&preserved, user.Id).Error)
			require.Equal(t, 123, preserved.Quota)
			require.True(t, db.Migrator().HasIndex(&GPTQuotaHistory{}, "idx_quota_history_bucket"))
			require.True(t, db.Migrator().HasIndex(&VM2APIEnrollment{}, "idx_vm2api_enrollment_slot"))
			other, err := GetGPTQuotaHistory(ctx, user.Id+1, scope, 720, now)
			require.NoError(t, err)
			require.Empty(t, other)
			other, err = GetGPTQuotaHistory(ctx, user.Id, strings.Repeat("c", 64), 720, now)
			require.NoError(t, err)
			require.Empty(t, other)
			// 完整零值有意义；缺失值保持 NULL。历史点不能伪造未来观察时间。
			zero := 0.0
			newer.ObservedAt += 1
			newer.Remaining5h, newer.Remaining7d = &zero, nil
			require.NoError(t, SaveGPTQuotaHistory(ctx, newer, now.Add(time.Millisecond)))
			rows, err = GetGPTQuotaHistory(ctx, user.Id, scope, 24, now.Add(time.Millisecond))
			require.NoError(t, err)
			require.Equal(t, 0.0, *rows[0].Remaining5h)
			require.Nil(t, rows[0].Remaining7d)
			invalid := row
			invalid.ObservedAt = now.Add(time.Hour).UnixMilli()
			require.Error(t, SaveGPTQuotaHistory(ctx, invalid, now))
			invalid.ObservedAt = now.Add(-time.Hour).UnixMilli()
			require.Error(t, SaveGPTQuotaHistory(ctx, invalid, now))
			_, err = GetGPTQuotaHistory(ctx, user.Id, scope, 99999, now)
			require.Error(t, err)
			require.NoError(t, PruneGPTQuotaHistory(ctx, now.Add(31*24*time.Hour)))
			var count int64
			require.NoError(t, db.Model(&GPTQuotaHistory{}).Count(&count).Error)
			require.Zero(t, count)
			// 独立空库也须可首次创建及重复创建，而不是依赖旧表迁移顺序。
			require.NoError(t, db.Migrator().DropTable(&GPTQuotaHistory{}))
			for range 2 {
				require.NoError(t, db.AutoMigrate(&GPTQuotaHistory{}))
			}
			require.NoError(t, SaveGPTQuotaHistory(ctx, row, now))
		})
	}
}
