package model

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	gormlogger "gorm.io/gorm/logger"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

func TestSystemTaskHistoryDatabaseMatrix(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			var dialector gorm.Dialector
			switch dialect {
			case "sqlite":
				dialector = sqlite.Open(":memory:")
			case "mysql":
				dsn := os.Getenv("TEST_MYSQL_DSN")
				if dsn == "" {
					t.Skip("TEST_MYSQL_DSN is not configured")
				}
				dialector = mysql.Open(dsn)
			case "postgres":
				dsn := os.Getenv("TEST_POSTGRES_DSN")
				if dsn == "" {
					t.Skip("TEST_POSTGRES_DSN is not configured")
				}
				dialector = postgres.Open(dsn)
			}
			db, err := gorm.Open(dialector, &gorm.Config{NamingStrategy: schema.NamingStrategy{TablePrefix: "history_test_"}})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			sqlDB.SetMaxOpenConns(1)
			previousDB := DB
			DB = db
			t.Cleanup(func() {
				DB = previousDB
				require.NoError(t, db.Migrator().DropTable(&SystemTask{}))
				require.NoError(t, sqlDB.Close())
			})
			require.NoError(t, db.AutoMigrate(&SystemTask{}))
			var version string
			versionQuery := "SELECT version()"
			if dialect == "sqlite" {
				versionQuery = "SELECT sqlite_version()"
			}
			require.NoError(t, db.Raw(versionQuery).Scan(&version).Error)
			t.Logf("database version: %s", version)

			tasks := []SystemTask{
				{TaskID: "model-failed-old", Type: SystemTaskTypeModelUpdate, Status: SystemTaskStatusFailed},
				{TaskID: "model-success", Type: SystemTaskTypeModelUpdate, Status: SystemTaskStatusSucceeded},
				{TaskID: "channel-failed", Type: SystemTaskTypeChannelTest, Status: SystemTaskStatusFailed},
				{TaskID: "model-failed-recent", Type: SystemTaskTypeModelUpdate, Status: SystemTaskStatusFailed},
				{TaskID: "model-latest", Type: SystemTaskTypeModelUpdate, Status: SystemTaskStatusSucceeded},
				{TaskID: "channel-pending", Type: SystemTaskTypeChannelTest, Status: SystemTaskStatusPending},
				{TaskID: "channel-running", Type: SystemTaskTypeChannelTest, Status: SystemTaskStatusRunning},
				{TaskID: "log-only-history", Type: SystemTaskTypeLogCleanup, Status: SystemTaskStatusSucceeded},
			}
			require.NoError(t, db.Create(&tasks).Error)
			filter := SystemTaskFilter{Scope: "history", Type: SystemTaskTypeModelUpdate, Status: SystemTaskStatusFailed}
			page, total, err := ListSystemTasks(filter, 1, 1)
			require.NoError(t, err)
			assert.EqualValues(t, 2, total)
			require.Len(t, page, 1)
			assert.Equal(t, "model-failed-old", page[0].TaskID)
			page, total, err = ListSystemTasks(filter, 2, 1)
			require.NoError(t, err)
			assert.EqualValues(t, 2, total)
			assert.Empty(t, page)

			active, total, err := ListSystemTasks(SystemTaskFilter{Scope: "active"}, 0, 20)
			require.NoError(t, err)
			assert.EqualValues(t, 2, total)
			require.Len(t, active, 2)
			assert.Equal(t, "channel-running", active[0].TaskID)
			assert.Equal(t, "channel-pending", active[1].TaskID)

			deleted, err := DeleteSystemTaskHistory(filter)
			require.NoError(t, err)
			assert.EqualValues(t, 2, deleted)
			_, total, err = ListSystemTasks(SystemTaskFilter{Scope: "history"}, 0, 20)
			require.NoError(t, err)
			assert.EqualValues(t, 4, total, "other types and statuses are untouched")

			deleted, err = DeleteSystemTaskHistory(SystemTaskFilter{Scope: "active", Status: SystemTaskStatusRunning})
			require.NoError(t, err)
			assert.Zero(t, deleted, "caller cannot override the terminal-status restriction")
			deleted, err = DeleteSystemTaskHistory(SystemTaskFilter{})
			require.NoError(t, err)
			assert.EqualValues(t, 2, deleted)
			remaining, total, err := ListSystemTasks(SystemTaskFilter{}, 0, 100)
			require.NoError(t, err)
			assert.EqualValues(t, 4, total)
			var remainingIDs []string
			for _, task := range remaining {
				remainingIDs = append(remainingIDs, task.TaskID)
			}
			assert.ElementsMatch(t, []string{"model-latest", "channel-pending", "channel-running", "log-only-history"}, remainingIDs)
			latest, err := GetLatestSystemTasks([]string{SystemTaskTypeModelUpdate, SystemTaskTypeChannelTest, SystemTaskTypeLogCleanup})
			require.NoError(t, err)
			assert.Equal(t, "model-latest", latest[SystemTaskTypeModelUpdate].TaskID)
			assert.Equal(t, "channel-running", latest[SystemTaskTypeChannelTest].TaskID)
			assert.Equal(t, "log-only-history", latest[SystemTaskTypeLogCleanup].TaskID)
			deleted, err = DeleteSystemTaskHistory(SystemTaskFilter{})
			require.NoError(t, err)
			assert.Zero(t, deleted, "repeated cleanup preserves the scheduler's latest runs")
		})
	}
}

type testSystemTaskPayload struct {
	TargetTimestamp int64 `json:"target_timestamp"`
	BatchSize       int   `json:"batch_size"`
}

type testSystemTaskState struct {
	Total     int64 `json:"total"`
	Processed int64 `json:"processed"`
	Progress  int   `json:"progress"`
	Remaining int64 `json:"remaining"`
}

func createLegacyPendingSystemTask(t *testing.T, taskType string) *SystemTask {
	t.Helper()
	taskID, err := GenerateSystemTaskID()
	require.NoError(t, err)
	task := &SystemTask{
		TaskID: taskID,
		Type:   taskType,
		Status: SystemTaskStatusPending,
	}
	require.NoError(t, DB.Create(task).Error)
	return task
}

func TestSystemTaskCreateAndActiveLifecycle(t *testing.T) {
	truncateTables(t)

	payload := testSystemTaskPayload{TargetTimestamp: 1000, BatchSize: 100}
	state := testSystemTaskState{}
	task, err := CreateSystemTask(SystemTaskTypeLogCleanup, payload, state)
	require.NoError(t, err)
	require.NotNil(t, task.ActiveKey)
	assert.Equal(t, SystemTaskTypeLogCleanup, *task.ActiveKey)

	var decodedPayload testSystemTaskPayload
	require.NoError(t, task.DecodePayload(&decodedPayload))
	assert.Equal(t, payload, decodedPayload)

	activeTask, err := GetActiveSystemTask(SystemTaskTypeLogCleanup)
	require.NoError(t, err)
	require.NotNil(t, activeTask)
	assert.Equal(t, task.TaskID, activeTask.TaskID)

	runnerID := "runner-a"
	claimedTask, claimed, err := ClaimSystemTask(task.ID, SystemTaskTypeLogCleanup, runnerID, common.GetTimestamp()+60)
	require.NoError(t, err)
	require.True(t, claimed)

	err = FinishSystemTask(claimedTask.TaskID, runnerID, SystemTaskStatusSucceeded, map[string]int64{"deleted_count": 0}, "")
	require.NoError(t, err)

	finishedTask, err := GetSystemTaskByTaskID(task.TaskID)
	require.NoError(t, err)
	require.NotNil(t, finishedTask)
	assert.Nil(t, finishedTask.ActiveKey)

	activeTask, err = GetActiveSystemTask(SystemTaskTypeLogCleanup)
	require.NoError(t, err)
	require.Nil(t, activeTask)

	_, err = CreateSystemTask(SystemTaskTypeLogCleanup, payload, state)
	require.NoError(t, err)
}

func TestSystemTaskActiveKeyPreventsDuplicateActiveRun(t *testing.T) {
	truncateTables(t)

	payload := testSystemTaskPayload{TargetTimestamp: 1000, BatchSize: 100}
	task, err := CreateSystemTask(SystemTaskTypeLogCleanup, payload, testSystemTaskState{})
	require.NoError(t, err)
	_, err = CreateSystemTask(SystemTaskTypeLogCleanup, payload, testSystemTaskState{})
	require.Error(t, err)

	activeTask, err := GetActiveSystemTask(SystemTaskTypeLogCleanup)
	require.NoError(t, err)
	require.NotNil(t, activeTask)
	assert.Equal(t, task.TaskID, activeTask.TaskID)
}

func TestSystemTaskLockPreventsConcurrentClaim(t *testing.T) {
	truncateTables(t)

	payload := testSystemTaskPayload{TargetTimestamp: 1000, BatchSize: 100}
	task, err := CreateSystemTask(SystemTaskTypeLogCleanup, payload, testSystemTaskState{})
	require.NoError(t, err)
	secondTask := createLegacyPendingSystemTask(t, SystemTaskTypeLogCleanup)

	claimedTask, claimed, err := ClaimSystemTask(task.ID, SystemTaskTypeLogCleanup, "runner-a", common.GetTimestamp()+60)
	require.NoError(t, err)
	require.True(t, claimed)

	_, claimed, err = ClaimSystemTask(secondTask.ID, SystemTaskTypeLogCleanup, "runner-b", common.GetTimestamp()+60)
	require.NoError(t, err)
	require.False(t, claimed)

	assert.Equal(t, "runner-a", claimedTask.LockedBy)

	reloadedSecond, err := GetSystemTaskByTaskID(secondTask.TaskID)
	require.NoError(t, err)
	require.NotNil(t, reloadedSecond)
	assert.Equal(t, SystemTaskStatusPending, reloadedSecond.Status)
}

func TestExpiredSystemTaskLockFailsOldRunAndClaimsLegacyPendingRun(t *testing.T) {
	truncateTables(t)

	first, err := CreateSystemTask(SystemTaskTypeLogCleanup, nil, nil)
	require.NoError(t, err)
	_, claimed, err := ClaimSystemTask(first.ID, SystemTaskTypeLogCleanup, "runner-a", common.GetTimestamp()+60)
	require.NoError(t, err)
	require.True(t, claimed)

	require.NoError(t, DB.Model(&SystemTaskLock{}).
		Where("task_id = ?", first.TaskID).
		Update("locked_until", common.GetTimestamp()-1).Error)

	second := createLegacyPendingSystemTask(t, SystemTaskTypeLogCleanup)
	claimedTask, claimed, err := ClaimSystemTask(second.ID, SystemTaskTypeLogCleanup, "runner-b", common.GetTimestamp()+60)
	require.NoError(t, err)
	require.True(t, claimed)
	assert.Equal(t, second.TaskID, claimedTask.TaskID)
	assert.Equal(t, "runner-b", claimedTask.LockedBy)

	reloadedFirst, err := GetSystemTaskByTaskID(first.TaskID)
	require.NoError(t, err)
	require.NotNil(t, reloadedFirst)
	assert.Equal(t, SystemTaskStatusFailed, reloadedFirst.Status)
	assert.Equal(t, "task lease expired", reloadedFirst.Error)
	assert.Nil(t, reloadedFirst.ActiveKey)
}

func TestExpireStaleSystemTaskLockFailsOldRunAndAllowsNewRun(t *testing.T) {
	truncateTables(t)

	first, err := CreateSystemTask(SystemTaskTypeLogCleanup, nil, nil)
	require.NoError(t, err)
	_, claimed, err := ClaimSystemTask(first.ID, SystemTaskTypeLogCleanup, "runner-a", common.GetTimestamp()+60)
	require.NoError(t, err)
	require.True(t, claimed)

	require.NoError(t, DB.Model(&SystemTaskLock{}).
		Where("task_id = ?", first.TaskID).
		Update("locked_until", common.GetTimestamp()-1).Error)

	require.NoError(t, ExpireStaleSystemTaskLocks(common.GetTimestamp()))

	reloadedFirst, err := GetSystemTaskByTaskID(first.TaskID)
	require.NoError(t, err)
	require.NotNil(t, reloadedFirst)
	assert.Equal(t, SystemTaskStatusFailed, reloadedFirst.Status)
	assert.Equal(t, "task lease expired", reloadedFirst.Error)
	assert.Nil(t, reloadedFirst.ActiveKey)

	var lockCount int64
	require.NoError(t, DB.Model(&SystemTaskLock{}).Where("task_id = ?", first.TaskID).Count(&lockCount).Error)
	assert.Equal(t, int64(0), lockCount)

	second, err := CreateSystemTask(SystemTaskTypeLogCleanup, nil, nil)
	require.NoError(t, err)
	require.NotEqual(t, first.TaskID, second.TaskID)
}

func TestFindEarliestPendingSystemTasks(t *testing.T) {
	truncateTables(t)

	empty, err := FindEarliestPendingSystemTasks(nil)
	require.NoError(t, err)
	assert.Empty(t, empty)

	firstA, err := CreateSystemTask("type_a", nil, nil)
	require.NoError(t, err)
	ignoredB, err := CreateSystemTask("type_b", nil, nil)
	require.NoError(t, err)
	_, claimed, err := ClaimSystemTask(ignoredB.ID, "type_b", "runner-b", common.GetTimestamp()+60)
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, FinishSystemTask(ignoredB.TaskID, "runner-b", SystemTaskStatusFailed, nil, "failed"))
	firstB, err := CreateSystemTask("type_b", nil, nil)
	require.NoError(t, err)
	ignoredC, err := CreateSystemTask("type_c", nil, nil)
	require.NoError(t, err)
	_, claimed, err = ClaimSystemTask(ignoredC.ID, "type_c", "runner-c", common.GetTimestamp()+60)
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, FinishSystemTask(ignoredC.TaskID, "runner-c", SystemTaskStatusFailed, nil, "failed"))

	tasks, err := FindEarliestPendingSystemTasks([]string{"type_a", "type_b", "type_c", "missing"})
	require.NoError(t, err)
	require.Len(t, tasks, 2)
	assert.Equal(t, firstA.TaskID, tasks["type_a"].TaskID)
	assert.Equal(t, firstB.TaskID, tasks["type_b"].TaskID)
	assert.Nil(t, tasks["type_c"])
	assert.Nil(t, tasks["missing"])
}

func TestGetLatestSystemTask(t *testing.T) {
	truncateTables(t)

	latest, err := GetLatestSystemTask(SystemTaskTypeChannelTest)
	require.NoError(t, err)
	require.Nil(t, latest)

	first, err := CreateSystemTask(SystemTaskTypeChannelTest, nil, nil)
	require.NoError(t, err)

	runnerID := "runner-a"
	_, claimed, err := ClaimSystemTask(first.ID, SystemTaskTypeChannelTest, runnerID, common.GetTimestamp()+60)
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, FinishSystemTask(first.TaskID, runnerID, SystemTaskStatusSucceeded, nil, ""))

	second, err := CreateSystemTask(SystemTaskTypeChannelTest, nil, nil)
	require.NoError(t, err)

	latest, err = GetLatestSystemTask(SystemTaskTypeChannelTest)
	require.NoError(t, err)
	require.NotNil(t, latest)
	assert.Equal(t, second.TaskID, latest.TaskID)
}

func TestGetLatestSystemTasks(t *testing.T) {
	truncateTables(t)

	empty, err := GetLatestSystemTasks(nil)
	require.NoError(t, err)
	assert.Empty(t, empty)

	firstA, err := CreateSystemTask("type_a", nil, nil)
	require.NoError(t, err)
	firstB, err := CreateSystemTask("type_b", nil, nil)
	require.NoError(t, err)
	_, claimed, err := ClaimSystemTask(firstA.ID, "type_a", "runner-a", common.GetTimestamp()+60)
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, FinishSystemTask(firstA.TaskID, "runner-a", SystemTaskStatusSucceeded, nil, ""))
	secondA, err := CreateSystemTask("type_a", nil, nil)
	require.NoError(t, err)

	tasks, err := GetLatestSystemTasks([]string{"type_a", "type_b", "missing"})
	require.NoError(t, err)
	require.Len(t, tasks, 2)
	assert.NotEqual(t, firstA.TaskID, tasks["type_a"].TaskID)
	assert.Equal(t, secondA.TaskID, tasks["type_a"].TaskID)
	assert.Equal(t, firstB.TaskID, tasks["type_b"].TaskID)
	assert.Nil(t, tasks["missing"])
}

func TestRenewSystemTaskLock(t *testing.T) {
	truncateTables(t)

	task, err := CreateSystemTask(SystemTaskTypeLogCleanup, nil, nil)
	require.NoError(t, err)

	runnerID := "runner-a"
	_, claimed, err := ClaimSystemTask(task.ID, SystemTaskTypeLogCleanup, runnerID, common.GetTimestamp()+60)
	require.NoError(t, err)
	require.True(t, claimed)

	newLockUntil := common.GetTimestamp() + 600
	require.NoError(t, RenewSystemTaskLock(task.TaskID, runnerID, newLockUntil))

	var lock SystemTaskLock
	require.NoError(t, DB.Where("task_id = ?", task.TaskID).First(&lock).Error)
	assert.Equal(t, newLockUntil, lock.LockedUntil)

	// A different runner cannot renew a lease it does not hold.
	assert.ErrorIs(t, RenewSystemTaskLock(task.TaskID, "runner-b", common.GetTimestamp()+600), ErrSystemTaskLockLost)

	// After the task finishes it is no longer running, so renew fails.
	require.NoError(t, FinishSystemTask(task.TaskID, runnerID, SystemTaskStatusSucceeded, nil, ""))
	assert.ErrorIs(t, RenewSystemTaskLock(task.TaskID, runnerID, common.GetTimestamp()+600), ErrSystemTaskLockLost)
}

func TestFinishSystemTaskRetainsExecutor(t *testing.T) {
	truncateTables(t)

	task, err := CreateSystemTask(SystemTaskTypeLogCleanup, nil, nil)
	require.NoError(t, err)

	runnerID := "node-1-abc123"
	_, claimed, err := ClaimSystemTask(task.ID, SystemTaskTypeLogCleanup, runnerID, common.GetTimestamp()+60)
	require.NoError(t, err)
	require.True(t, claimed)

	require.NoError(t, FinishSystemTask(task.TaskID, runnerID, SystemTaskStatusSucceeded, nil, ""))

	reloaded, err := GetSystemTaskByTaskID(task.TaskID)
	require.NoError(t, err)
	require.NotNil(t, reloaded)
	assert.Equal(t, SystemTaskStatusSucceeded, reloaded.Status)
	assert.Equal(t, runnerID, reloaded.LockedBy, "executor-of-record must be retained for history")

	var lockCount int64
	require.NoError(t, DB.Model(&SystemTaskLock{}).Where("task_id = ?", task.TaskID).Count(&lockCount).Error)
	assert.Equal(t, int64(0), lockCount)
}

func TestSystemTaskUpdatesRequireCurrentLock(t *testing.T) {
	truncateTables(t)

	task, err := CreateSystemTask(SystemTaskTypeLogCleanup, nil, nil)
	require.NoError(t, err)

	runnerID := "runner-a"
	_, claimed, err := ClaimSystemTask(task.ID, SystemTaskTypeLogCleanup, runnerID, common.GetTimestamp()+60)
	require.NoError(t, err)
	require.True(t, claimed)

	require.NoError(t, DB.Model(&SystemTaskLock{}).
		Where("task_id = ?", task.TaskID).
		Updates(map[string]any{"locked_by": "runner-b"}).Error)

	assert.ErrorIs(t, UpdateSystemTaskState(task.TaskID, runnerID, testSystemTaskState{Progress: 10}), ErrSystemTaskLockLost)
	assert.ErrorIs(t, FinishSystemTask(task.TaskID, runnerID, SystemTaskStatusSucceeded, nil, ""), ErrSystemTaskLockLost)
}

func TestSystemTaskUpdatesRequireUnexpiredLock(t *testing.T) {
	truncateTables(t)

	task, err := CreateSystemTask(SystemTaskTypeLogCleanup, nil, nil)
	require.NoError(t, err)

	runnerID := "runner-a"
	_, claimed, err := ClaimSystemTask(task.ID, SystemTaskTypeLogCleanup, runnerID, common.GetTimestamp()+60)
	require.NoError(t, err)
	require.True(t, claimed)

	require.NoError(t, DB.Model(&SystemTaskLock{}).
		Where("task_id = ?", task.TaskID).
		Update("locked_until", common.GetTimestamp()-1).Error)

	assert.ErrorIs(t, UpdateSystemTaskState(task.TaskID, runnerID, testSystemTaskState{Progress: 10}), ErrSystemTaskLockLost)
	assert.ErrorIs(t, FinishSystemTask(task.TaskID, runnerID, SystemTaskStatusSucceeded, nil, ""), ErrSystemTaskLockLost)

	reloaded, err := GetSystemTaskByTaskID(task.TaskID)
	require.NoError(t, err)
	require.NotNil(t, reloaded)
	assert.Equal(t, SystemTaskStatusRunning, reloaded.Status)
	assert.Empty(t, reloaded.State)
}

func TestUpdateSystemTaskStateIdenticalPayloadDoesNotLoseLock(t *testing.T) {
	// SQLite reports matched rows for unchanged UPDATEs, so this case passed
	// even before the fix. The MySQL regression is covered by
	// TestUpdateSystemTaskStateIdenticalPayloadDoesNotLoseLockConfiguredDatabases.
	truncateTables(t)
	runUpdateSystemTaskStateIdenticalPayloadKeepsLock(t, SystemTaskTypeLogCleanup)
}

func runUpdateSystemTaskStateIdenticalPayloadKeepsLock(t *testing.T, taskType string) {
	t.Helper()
	// Two persists in the same second so MySQL's unchanged-row UPDATE returns 0.

	task, err := CreateSystemTask(taskType, nil, nil)
	require.NoError(t, err)

	runnerID := "runner-a"
	_, claimed, err := ClaimSystemTask(task.ID, taskType, runnerID, common.GetTimestamp()+60)
	require.NoError(t, err)
	require.True(t, claimed)

	state := testSystemTaskState{Total: 10, Processed: 10, Progress: 100, Remaining: 0}
	require.NoError(t, UpdateSystemTaskState(task.TaskID, runnerID, state))
	require.NoError(t, UpdateSystemTaskState(task.TaskID, runnerID, state), "identical state persist must not be treated as lock loss")

	require.NoError(t, FinishSystemTask(task.TaskID, runnerID, SystemTaskStatusSucceeded, map[string]int64{"deleted_count": 10}, ""))
	finished, err := GetSystemTaskByTaskID(task.TaskID)
	require.NoError(t, err)
	require.NotNil(t, finished)
	assert.Equal(t, SystemTaskStatusSucceeded, finished.Status)
}

// openSystemTaskMatrixDB 连接独立测试库；子进程使用同一 DSN，禁止用表前缀绕过真实租约 SQL。
func openSystemTaskMatrixDB(t *testing.T, dialect, dsn string) *gorm.DB {
	t.Helper()
	var driver gorm.Dialector
	switch dialect {
	case "sqlite":
		driver = sqlite.Open(dsn)
	case "mysql":
		driver = mysql.Open(dsn)
	case "postgres":
		driver = postgres.Open(dsn)
	default:
		t.Fatalf("unsupported test dialect %q", dialect)
	}
	db, err := gorm.Open(driver, &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	previous := DB
	DB = db
	t.Cleanup(func() { DB = previous; require.NoError(t, sqlDB.Close()) })
	return db
}

// TestSystemTaskProcessWorker 是独立 OS 进程的测试入口；管道构成确定性屏障，不靠 sleep 制造竞争。
func TestSystemTaskProcessWorker(t *testing.T) {
	if os.Getenv("SYSTEM_TASK_WORKER") != "1" {
		t.Skip("subprocess helper")
	}
	openSystemTaskMatrixDB(t, os.Getenv("SYSTEM_TASK_DIALECT"), os.Getenv("SYSTEM_TASK_DSN"))
	task, err := GetSystemTaskByTaskID(os.Getenv("SYSTEM_TASK_ID"))
	require.NoError(t, err)
	require.NotNil(t, task)
	fmt.Println("READY")
	scanner := bufio.NewScanner(os.Stdin)
	require.True(t, scanner.Scan())
	runner := fmt.Sprintf("process-%d", os.Getpid())
	claimedTask, claimed, err := ClaimSystemTask(task.ID, task.Type, runner, common.GetTimestamp()+60)
	require.NoError(t, err)
	if !claimed {
		fmt.Println("CLAIM:false")
		return
	}
	if os.Getenv("SYSTEM_TASK_FINISH") == "1" {
		assert.JSONEq(t, `{"cursor":"page-50"}`, claimedTask.State)
		require.NoError(t, FinishSystemTask(task.TaskID, runner, SystemTaskStatusSucceeded, nil, ""))
		fmt.Println("CLAIM:true")
		return
	}
	require.NoError(t, UpdateSystemTaskState(task.TaskID, runner, map[string]string{"cursor": "page-50"}))
	fmt.Println("CLAIM:true")
	// 胜者保留租约直到父进程杀死它，模拟未执行退出清理的崩溃。
	scanner.Scan()
}

type taskTestProcess struct {
	cmd    *exec.Cmd
	input  io.WriteCloser
	output *bufio.Scanner
	stderr bytes.Buffer
}

// startTaskTestProcess 启动真实测试子进程；只经环境传递 DSN，输出失败信息不包含连接凭据。
func startTaskTestProcess(t *testing.T, dialect, dsn, taskID string, finish bool) *taskTestProcess {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	executable, err := os.Executable()
	require.NoError(t, err)
	worker := &taskTestProcess{cmd: exec.CommandContext(ctx, executable, "-test.run=^TestSystemTaskProcessWorker$")}
	worker.cmd.Env = append(os.Environ(), "SYSTEM_TASK_WORKER=1", "SYSTEM_TASK_DIALECT="+dialect, "SYSTEM_TASK_DSN="+dsn, "SYSTEM_TASK_ID="+taskID)
	if finish {
		worker.cmd.Env = append(worker.cmd.Env, "SYSTEM_TASK_FINISH=1")
	}
	worker.input, err = worker.cmd.StdinPipe()
	require.NoError(t, err)
	output, err := worker.cmd.StdoutPipe()
	require.NoError(t, err)
	worker.output = bufio.NewScanner(output)
	worker.cmd.Stderr = &worker.stderr
	require.NoError(t, worker.cmd.Start())
	t.Cleanup(func() { _ = worker.input.Close(); _ = worker.cmd.Process.Kill() })
	worker.read(t, "READY")
	return worker
}

// read 跳过测试框架输出直到得到屏障消息；进程超时会关闭管道，不会永久挂住测试。
func (p *taskTestProcess) read(t *testing.T, prefix string) string {
	t.Helper()
	for p.output.Scan() {
		if line := p.output.Text(); strings.HasPrefix(line, prefix) {
			return line
		}
	}
	t.Fatalf("worker exited before %s: %s", prefix, p.stderr.String())
	return ""
}

// TestSystemTaskLeaseDatabaseMatrix 用三种真实数据库验证竞争领取、崩溃接管、旧持有者拒写和续租清理竞态。
func TestSystemTaskLeaseDatabaseMatrix(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			dsn := os.Getenv("TEST_SYSTEM_TASK_" + strings.ToUpper(dialect) + "_DSN")
			if dialect == "sqlite" {
				dsn = filepath.Join(t.TempDir(), "tasks.db") + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
			}
			if dsn == "" {
				t.Skip("dedicated matrix DSN not configured")
			}
			db := openSystemTaskMatrixDB(t, dialect, dsn)
			require.NoError(t, db.AutoMigrate(&SystemTask{}, &SystemTaskLock{}))
			kind := "lease_matrix_" + common.GetRandomString(12)
			t.Cleanup(func() {
				require.NoError(t, db.Where("type = ?", kind).Delete(&SystemTaskLock{}).Error)
				require.NoError(t, db.Where("type = ?", kind).Delete(&SystemTask{}).Error)
			})
			var version string
			query := "SELECT version()"
			if dialect == "sqlite" {
				query = "SELECT sqlite_version()"
			}
			require.NoError(t, db.Raw(query).Scan(&version).Error)
			t.Logf("database version: %s", version)
			task, err := CreateSystemTask(kind, nil, nil)
			require.NoError(t, err)
			a := startTaskTestProcess(t, dialect, dsn, task.TaskID, false)
			b := startTaskTestProcess(t, dialect, dsn, task.TaskID, false)
			_, err = io.WriteString(a.input, "claim\n")
			require.NoError(t, err)
			_, err = io.WriteString(b.input, "claim\n")
			require.NoError(t, err)
			aWon := a.read(t, "CLAIM:") == "CLAIM:true"
			bWon := b.read(t, "CLAIM:") == "CLAIM:true"
			require.NotEqual(t, aWon, bWon, "exactly one process must own the task")
			winner, loser := a, b
			if bWon {
				winner, loser = b, a
			}
			require.NoError(t, loser.cmd.Wait())
			require.NoError(t, winner.cmd.Process.Kill())
			require.Error(t, winner.cmd.Wait())
			crashed, err := GetSystemTaskByTaskID(task.TaskID)
			require.NoError(t, err)
			require.JSONEq(t, `{"cursor":"page-50"}`, crashed.State)
			require.NoError(t, db.Model(&SystemTaskLock{}).Where("task_id = ?", task.TaskID).Update("locked_until", common.GetTimestamp()-1).Error)
			require.NoError(t, ExpireStaleSystemTaskLocks(common.GetTimestamp()))
			require.ErrorIs(t, UpdateSystemTaskState(task.TaskID, crashed.LockedBy, map[string]string{"cursor": "stale"}), ErrSystemTaskLockLost)
			require.ErrorIs(t, FinishSystemTask(task.TaskID, crashed.LockedBy, SystemTaskStatusSucceeded, nil, ""), ErrSystemTaskLockLost)
			crashed, err = GetSystemTaskByTaskID(task.TaskID)
			require.NoError(t, err)
			assert.Equal(t, SystemTaskStatusFailed, crashed.Status)
			var checkpoint map[string]string
			require.NoError(t, crashed.DecodeState(&checkpoint))
			next, err := CreateSystemTask(kind, nil, checkpoint)
			require.NoError(t, err)
			replacement := startTaskTestProcess(t, dialect, dsn, next.TaskID, true)
			_, err = io.WriteString(replacement.input, "claim\n")
			require.NoError(t, err)
			assert.Equal(t, "CLAIM:true", replacement.read(t, "CLAIM:"))
			require.NoError(t, replacement.cmd.Wait())
			next, err = GetSystemTaskByTaskID(next.TaskID)
			require.NoError(t, err)
			assert.Equal(t, SystemTaskStatusSucceeded, next.Status)

			// 用未来的清理截止时间稳定模拟 SELECT 之后续租；不等待时钟或依赖线程调度。
			live, err := CreateSystemTask(kind, nil, nil)
			require.NoError(t, err)
			now := common.GetTimestamp()
			_, claimed, err := ClaimSystemTask(live.ID, kind, "renewed-owner", now+60)
			require.NoError(t, err)
			require.True(t, claimed)
			renewed := false
			callback := "test:renew_after_scan"
			require.NoError(t, db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
				if tx.Statement.Table == "system_task_locks" && !renewed {
					renewed = true
					tx.AddError(RenewSystemTaskLock(live.TaskID, "renewed-owner", now+200))
				}
			}))
			err = ExpireStaleSystemTaskLocks(now + 90)
			require.NoError(t, db.Callback().Query().Remove(callback))
			require.NoError(t, err)
			require.True(t, renewed)
			live, err = GetSystemTaskByTaskID(live.TaskID)
			require.NoError(t, err)
			assert.Equal(t, SystemTaskStatusRunning, live.Status, "renewed task must not be failed by stale scan")
			require.NoError(t, UpdateSystemTaskState(live.TaskID, "renewed-owner", map[string]string{"cursor": "still-live"}))
			require.NoError(t, FinishSystemTask(live.TaskID, "renewed-owner", SystemTaskStatusSucceeded, nil, ""))
		})
	}
}

// TestSystemTaskRetentionDatabaseMatrix 验证三库有限批次清理、恢复检查点保护和扫描后任务状态变化的重新检查。
func TestSystemTaskRetentionDatabaseMatrix(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			dsn := os.Getenv("TEST_SYSTEM_TASK_" + strings.ToUpper(dialect) + "_DSN")
			if dialect == "sqlite" {
				dsn = filepath.Join(t.TempDir(), "retention.db")
			}
			if dsn == "" {
				t.Skip("dedicated matrix DSN not configured")
			}
			db := openSystemTaskMatrixDB(t, dialect, dsn)
			require.NoError(t, db.AutoMigrate(&SystemTask{}, &SystemTaskLock{}))
			prefix := "retention-" + common.GetRandomString(8)
			now := time.Now()
			old := now.Add(-31 * 24 * time.Hour).Unix()
			rows := make([]SystemTask, 0, 1003)
			for i := range 1003 {
				rows = append(rows, SystemTask{TaskID: fmt.Sprintf("%s-%04d", prefix, i), Type: prefix, Status: SystemTaskStatusSucceeded, UpdatedAt: old})
			}
			require.NoError(t, db.CreateInBatches(&rows, 100).Error)
			active := prefix + "-protected"
			preserved := []SystemTask{
				{TaskID: prefix + "-checkpoint", Type: prefix, Status: SystemTaskStatusFailed, State: `{"cursor":"last-page"}`, UpdatedAt: old},
				{TaskID: prefix + "-running", Type: prefix + "-running", Status: SystemTaskStatusRunning, UpdatedAt: old},
				{TaskID: prefix + "-pending", Type: prefix + "-pending", Status: SystemTaskStatusPending, UpdatedAt: old},
				{TaskID: prefix + "-recent-1", Type: prefix + "-recent", Status: SystemTaskStatusSucceeded, UpdatedAt: now.Unix()},
				{TaskID: prefix + "-recent-2", Type: prefix + "-recent", Status: SystemTaskStatusSucceeded, UpdatedAt: now.Unix()},
				{TaskID: prefix + "-active-key", Type: prefix + "-running", Status: SystemTaskStatusFailed, ActiveKey: &active, UpdatedAt: old},
			}
			require.NoError(t, db.Create(&preserved).Error)
			t.Cleanup(func() { require.NoError(t, db.Where("task_id LIKE ?", prefix+"%").Delete(&SystemTask{}).Error) })
			// 在清理候选查询结束后刷新一条旧记录，重现另一个执行者刚更新记录的窗口。
			refreshed := false
			callback := "test:retention_refresh"
			require.NoError(t, db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
				if _, hasLimit := tx.Statement.Clauses["LIMIT"]; tx.Statement.Table == "system_tasks" && hasLimit && !refreshed {
					refreshed = true
					tx.AddError(db.Model(&SystemTask{}).Where("id = ?", rows[0].ID).Update("updated_at", now.Unix()).Error)
				}
			}))
			deleted, err := PruneSystemTaskHistory(t.Context(), now)
			require.NoError(t, db.Callback().Query().Remove(callback))
			require.NoError(t, err)
			require.True(t, refreshed)
			assert.EqualValues(t, 999, deleted, "1000 candidates minus the concurrently refreshed row")
			deleted, err = PruneSystemTaskHistory(t.Context(), now)
			require.NoError(t, err)
			assert.EqualValues(t, 3, deleted)
			deleted, err = PruneSystemTaskHistory(t.Context(), now)
			require.NoError(t, err)
			assert.Zero(t, deleted)
			for _, expected := range preserved {
				got, err := GetSystemTaskByTaskID(expected.TaskID)
				require.NoError(t, err)
				require.NotNil(t, got)
				assert.Equal(t, expected.Status, got.Status)
				assert.Equal(t, expected.State, got.State)
			}
			var remaining int64
			require.NoError(t, db.Model(&SystemTask{}).Where("task_id LIKE ?", prefix+"%").Count(&remaining).Error)
			assert.EqualValues(t, 7, remaining)
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			deleted, err = PruneSystemTaskHistory(ctx, now)
			require.Error(t, err)
			assert.Zero(t, deleted)
		})
	}
}
