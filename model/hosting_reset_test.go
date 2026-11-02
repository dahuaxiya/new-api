package model

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// TestHostingResetAttempts 验证重置防重状态能跨重启保留，旧操作不能解除新操作的保护。
func TestHostingResetAttempts(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := useVM2APIEnrollmentDatabase(t, dialect)
			require.NoError(t, migrateDB())
			require.True(t, db.Migrator().HasTable(&HostingResetAttempt{}))
			require.NoError(t, migrateDB())
			// 模拟上一版本升级，确保新增防重表不会改动已有用户记录。
			require.NoError(t, db.Migrator().DropTable(&HostingResetAttempt{}))
			legacy := User{Username: "reset-legacy", AffCode: "reset-legacy", Quota: 12345}
			require.NoError(t, db.Create(&legacy).Error)
			require.NoError(t, migrateDB())
			require.NoError(t, migrateDB())
			var preserved User
			require.NoError(t, db.First(&preserved, legacy.Id).Error)
			require.Equal(t, legacy.Quota, preserved.Quota)
			account, now := strings.Repeat("a", 64), time.Now().UTC().Truncate(time.Second)
			first := uuid.NewString()
			require.NoError(t, BeginHostingResetAttempt(account, first, now))
			until, err := HostingResetBlockedUntil(account, now)
			require.NoError(t, err)
			require.Equal(t, now.Unix()+hostingResetUnknownSeconds, until)
			require.ErrorIs(t, BeginHostingResetAttempt(account, uuid.NewString(), now.Add(time.Hour)), ErrHostingConflict)
			require.ErrorIs(t, FinishHostingResetAttempt(account, uuid.NewString(), "not_used"), ErrHostingConflict)
			require.NoError(t, FinishHostingResetAttempt(account, first, "not_used"))
			require.NoError(t, BeginHostingResetAttempt(account, first, now.Add(2*time.Hour)))
			second := uuid.NewString()
			require.NoError(t, BeginHostingResetAttempt(account, second, now.Add(26*time.Hour)))
			require.ErrorIs(t, FinishHostingResetAttempt(account, first, "reset"), ErrHostingConflict)
			require.NoError(t, FinishHostingResetAttempt(account, second, "reset"))
			until, err = HostingResetBlockedUntil(account, now.Add(26*time.Hour))
			require.NoError(t, err)
			require.Zero(t, until)
		})
	}
}

// TestHostingResetConcurrentClaims 验证并发请求最多有一个成功取得同一账号的消费权。
func TestHostingResetConcurrentClaims(t *testing.T) {
	db := useVM2APIEnrollmentDatabase(t, "sqlite")
	require.NoError(t, db.AutoMigrate(&HostingResetAttempt{}))
	account, now := strings.Repeat("b", 64), time.Now()
	const requests = 8
	start := make(chan struct{})
	results := make(chan error, requests)
	var wait sync.WaitGroup
	for i := 0; i < requests; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			results <- BeginHostingResetAttempt(account, uuid.NewString(), now)
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		}
	}
	require.Equal(t, 1, success)
	until, err := HostingResetBlockedUntil(account, now)
	require.NoError(t, err)
	require.Greater(t, until, now.Unix())
}
