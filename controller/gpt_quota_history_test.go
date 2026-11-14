package controller

import (
	"context"
	"crypto/sha256"
	"fmt"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/gptquota"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// TestGPTQuotaHistoryIsolation 通过真实会话和面板边界验证无页面采集、去重、
// 已过期窗口保留未知、查询范围限制，以及新用户/解绑后不能取得旧槽位历史。
func TestGPTQuotaHistoryIsolation(t *testing.T) {
	user, identity := setupSecurityEnrollmentTest(t)
	require.NoError(t, model.DB.AutoMigrate(&model.VM2APIEnrollment{}, &model.GPTQuotaHistory{}, &model.SystemTask{}, &model.SystemTaskLock{}))
	now := time.Now().UTC()
	var reads atomic.Int32
	panel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/panel/login" {
			_, _ = w.Write([]byte(`{"token":"fixture"}`))
			return
		}
		reads.Add(1)
		body, _ := common.Marshal(gin.H{"ok": true, "data": gin.H{"vm": gin.H{"id": "slot-a", "org_uuid": "private-account", "email": "private@example.test", "codex_usage": gin.H{
			"snapshot": gin.H{"updated_at": now.Format(time.RFC3339Nano)},
			"windows":  []gin.H{{"id": "5h", "used_percent": 90, "reset_at": now.Add(-time.Hour).Format(time.RFC3339Nano)}, {"id": "7d", "used_percent": 32, "reset_at": now.Add(time.Hour).Format(time.RFC3339Nano)}},
		}}}})
		_, _ = w.Write(body)
	}))
	defer panel.Close()
	config := gptquota.Config{PanelURL: panel.URL + "/api/panel", Login: map[string]string{"username": "test", "password": "test"}, Users: map[string]gptquota.Binding{
		strconv.Itoa(user.Id): {Username: user.Username, Group: user.Group, Slot: "slot-a", Label: "Fixture"},
	}}
	svc, err := gptquota.New(config)
	require.NoError(t, err)
	previous := quotaService
	quotaService = func() (*gptquota.Service, error) { return svc, nil }
	t.Cleanup(func() { quotaService = previous })
	// 调用真正的后台批次而不是 HTTP 页面，证明采集不依赖用户访问。
	t.Setenv("SITE_MODE", "hosting")
	t.Setenv("GPT_QUOTA_CONFIG", "fixture")
	first := runQuotaHistoryTask(t, nil, "history-first", false)
	require.Equal(t, model.SystemTaskStatusSucceeded, first.Status)
	// 第二轮先扫描到末尾并归零，第三轮才重新读取同一个绑定。
	second := runQuotaHistoryTask(t, first, "history-wrap", false)
	third := runQuotaHistoryTask(t, second, "history-again", false)
	require.Equal(t, model.SystemTaskStatusSucceeded, third.Status)
	var count int64
	require.NoError(t, model.DB.Model(&model.GPTQuotaHistory{}).Count(&count).Error)
	require.EqualValues(t, 1, count)
	router := gin.New()
	router.GET("/history", middleware.DisableCache(), middleware.UserAuth(), GetGPTQuotaHistorySelf)
	token, _, err := service.IssueAccessToken(identity)
	require.NoError(t, err)
	for _, tc := range []struct {
		suffix, token, hint string
		status              int
	}{
		{"?hours=24", token, "", 200}, {"?hours=168", token, "", 200}, {"?hours=720", token, "", 200},
		{"?hours=24&user_id=99", token, "", 400}, {"?hours=24&hours=168", token, "", 400},
		{"?slot=slot-a", token, "", 400}, {"?hours=721", token, "", 400}, {"", token, "999", 401}, {"", "", "", 401},
	} {
		req := httptest.NewRequest("GET", "/history"+tc.suffix, nil)
		req.Header.Set("Authorization", "Bearer "+tc.token)
		req.Header.Set("New-Api-User", tc.hint)
		out := httptest.NewRecorder()
		router.ServeHTTP(out, req)
		require.Equal(t, tc.status, out.Code, out.Body.String())
		if tc.status == 200 {
			require.Contains(t, out.Body.String(), `"remaining_7d":68`)
			require.Contains(t, out.Body.String(), `"remaining_5h":null`)
			for _, secret := range []string{"slot-a", "private@example.test", "private-account", `"scope"`, `"user_id"`} {
				require.NotContains(t, out.Body.String(), secret)
			}
			require.Contains(t, out.Header().Get("Cache-Control"), "no-store")
		}
	}
	require.EqualValues(t, 2, reads.Load(), "history queries must not contact upstream")
	// 同一槽位的新用户从空历史开始，不能查询前一位用户的记录。
	other := model.User{Username: "history-other", AffCode: "history-other", Group: user.Group, Status: common.UserStatusEnabled}
	require.NoError(t, model.DB.Create(&other).Error)
	config.Users[strconv.Itoa(other.Id)] = gptquota.Binding{Username: other.Username, Group: other.Group, Slot: "slot-a"}
	newService, err := gptquota.New(config)
	require.NoError(t, err)
	_, scope, err := quotaHistoryScope(newService, &other)
	require.NoError(t, err)
	rows, err := model.GetGPTQuotaHistory(context.Background(), other.Id, scope, 720, time.Now())
	require.NoError(t, err)
	require.Empty(t, rows)
	// 换绑到另一槽位会换历史范围；当前窗口不会连接旧账号的曲线。
	config.Users[strconv.Itoa(user.Id)] = gptquota.Binding{Username: user.Username, Group: user.Group, Slot: "slot-b"}
	svc, err = gptquota.New(config)
	require.NoError(t, err)
	req := httptest.NewRequest("GET", "/history", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	out := httptest.NewRecorder()
	router.ServeHTTP(out, req)
	require.Equal(t, 200, out.Code)
	require.Contains(t, out.Body.String(), `"points":[]`)
	// 即使存储中存在记录，撤销会话仍须先于历史查询生效。
	_, err = model.RevokeUserSession(user.Id, identity.SessionID, "quota history test")
	require.NoError(t, err)
	out = httptest.NewRecorder()
	router.ServeHTTP(out, req)
	require.Equal(t, 401, out.Code)
	require.False(t, strings.Contains(out.Body.String(), "remaining_7d"))
}

// runQuotaHistoryTask 建立真实租约并运行一页采集；subprocess=true 时使用独立 OS 进程，模拟原进程已退出。
func runQuotaHistoryTask(t *testing.T, previous *model.SystemTask, runner string, subprocess bool) *model.SystemTask {
	t.Helper()
	handler := quotaHistoryTaskHandler{}
	state, err := handler.ResumeState(previous)
	require.NoError(t, err)
	task, err := model.CreateSystemTask(handler.Type(), nil, state)
	require.NoError(t, err)
	if subprocess {
		executable, err := os.Executable()
		require.NoError(t, err)
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, executable, "-test.run=^TestQuotaHistoryProcessWorker$")
		command.Env = append(os.Environ(), "QUOTA_HISTORY_WORKER=1", "QUOTA_HISTORY_TASK="+task.TaskID, "QUOTA_HISTORY_RUNNER="+runner)
		output, err := command.CombinedOutput()
		require.NoError(t, err, string(output))
	} else {
		claimed, ok, err := model.ClaimSystemTask(task.ID, task.Type, runner, time.Now().Unix()+60)
		require.NoError(t, err)
		require.True(t, ok)
		handler.Run(t.Context(), claimed, runner)
	}
	task, err = model.GetSystemTaskByTaskID(task.TaskID)
	require.NoError(t, err)
	return task
}

// quotaHistoryTestDB 连接本轮专用库并恢复包级状态；SQLite 使用文件以支持两个进程接续读取。
func quotaHistoryTestDB(t *testing.T, dialect, dsn string) *gorm.DB {
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
		t.Fatalf("unsupported database: %s", dialect)
	}
	db, err := gorm.Open(driver, &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	previous, previousType := model.DB, common.MainDatabaseType()
	model.DB = db
	common.SetMainDatabaseType(common.DatabaseType(dialect))
	t.Cleanup(func() {
		model.DB = previous
		common.SetMainDatabaseType(previousType)
		require.NoError(t, sqlDB.Close())
	})
	return db
}

// quotaHistoryTestService 只连接父进程提供的本地假面板，保留真实快照解析和历史归属检查。
func quotaHistoryTestService(t *testing.T) {
	t.Helper()
	svc, err := gptquota.New(gptquota.Config{
		Instances: map[string]gptquota.BackendConfig{"fixture": {PanelURL: os.Getenv("QUOTA_HISTORY_PANEL") + "/api/panel", Login: map[string]string{"username": "fixture", "password": "fixture"}}},
		Users:     map[string]gptquota.Binding{"203": {InstanceID: "fixture", Username: "quota-legacy", Group: "default", Slot: "legacy"}},
	})
	require.NoError(t, err)
	previous := quotaService
	quotaService = func() (*gptquota.Service, error) { return svc, nil }
	t.Cleanup(func() { quotaService = previous })
}

// TestQuotaHistoryProcessWorker 在新的进程里读取共享检查点并执行，不依赖原进程的配置对象或内存缓存。
func TestQuotaHistoryProcessWorker(t *testing.T) {
	if os.Getenv("QUOTA_HISTORY_WORKER") != "1" {
		t.Skip("subprocess entry")
	}
	quotaHistoryTestDB(t, os.Getenv("QUOTA_HISTORY_DIALECT"), os.Getenv("QUOTA_HISTORY_DSN"))
	quotaHistoryTestService(t)
	task, err := model.GetSystemTaskByTaskID(os.Getenv("QUOTA_HISTORY_TASK"))
	require.NoError(t, err)
	require.NotNil(t, task)
	runner := os.Getenv("QUOTA_HISTORY_RUNNER")
	task, ok, err := model.ClaimSystemTask(task.ID, task.Type, runner, time.Now().Unix()+60)
	require.NoError(t, err)
	require.True(t, ok)
	quotaHistoryTaskHandler{}.Run(t.Context(), task, runner)
}

// TestQuotaHistoryTaskDatabaseMatrix 验证多名额跨页、跨进程恢复、坏记录继续、旧历史清理及取消/丢锁后不再访问上游。
func TestQuotaHistoryTaskDatabaseMatrix(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			dsn := os.Getenv("TEST_SYSTEM_TASK_" + strings.ToUpper(dialect) + "_DSN")
			if dialect == "sqlite" {
				dsn = filepath.Join(t.TempDir(), "quota.db") + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_txlock=immediate"
			}
			if dsn == "" {
				t.Skip("dedicated matrix DSN not configured")
			}
			t.Setenv("SITE_MODE", "hosting")
			t.Setenv("GPT_HOSTING_ENABLED", "true")
			t.Setenv("GPT_QUOTA_CONFIG", "fixture")
			t.Setenv("QUOTA_HISTORY_DIALECT", dialect)
			t.Setenv("QUOTA_HISTORY_DSN", dsn)
			db := quotaHistoryTestDB(t, dialect, dsn)
			require.NoError(t, db.AutoMigrate(&model.User{}, &model.GPTHostingGrant{}, &model.GPTHostingBinding{}, &model.VM2APIEnrollment{}, &model.GPTQuotaHistory{}, &model.SystemTask{}, &model.SystemTaskLock{}))
			// 这些矩阵 DSN 必须指向专用空库；测试只删除自己创建的固定夹具。
			t.Cleanup(func() {
				require.NoError(t, db.Where("user_id IN ?", []int{201, 202, 203}).Delete(&model.GPTQuotaHistory{}).Error)
				require.NoError(t, db.Where("user_id = ?", 201).Delete(&model.GPTHostingBinding{}).Error)
				require.NoError(t, db.Where("user_id = ?", 201).Delete(&model.GPTHostingGrant{}).Error)
				require.NoError(t, db.Where("id IN ?", []int{201, 202, 203}).Delete(&model.User{}).Error)
				require.NoError(t, db.Where("type = ?", "gpt_quota_history").Delete(&model.SystemTaskLock{}).Error)
				require.NoError(t, db.Where("type = ?", "gpt_quota_history").Delete(&model.SystemTask{}).Error)
			})
			var version string
			query := "SELECT version()"
			if dialect == "sqlite" {
				query = "SELECT sqlite_version()"
			}
			require.NoError(t, db.Raw(query).Scan(&version).Error)
			t.Logf("database version: %s", version)
			var reads atomic.Int32
			var cancelDuringRead atomic.Pointer[context.CancelFunc]
			now := time.Now().UTC()
			panel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/api/panel/login" {
					fmt.Fprint(w, `{"token":"fixture"}`)
					return
				}
				slot, ok := strings.CutPrefix(r.URL.Path, "/api/panel/vms/")
				if !ok || r.Method != "GET" {
					http.Error(w, "unexpected request", 400)
					return
				}
				reads.Add(1)
				if cancel := cancelDuringRead.Swap(nil); cancel != nil {
					(*cancel)()
					<-r.Context().Done()
					return
				}
				body, _ := common.Marshal(gin.H{"ok": true, "data": gin.H{"vm": gin.H{"id": slot, "org_uuid": slot, "codex_usage": gin.H{
					"snapshot": gin.H{"updated_at": now.Format(time.RFC3339Nano)}, "windows": []gin.H{{"id": "7d", "used_percent": 25, "reset_at": now.Add(time.Hour).Format(time.RFC3339Nano)}},
				}}}})
				_, _ = w.Write(body)
			}))
			defer panel.Close()
			t.Setenv("QUOTA_HISTORY_PANEL", panel.URL)
			quotaHistoryTestService(t)
			require.NoError(t, db.Create(&[]model.User{
				{Id: 201, Username: "quota-owner", AffCode: "quota-owner", Group: "default", Status: common.UserStatusEnabled},
				{Id: 202, Username: "quota-disabled", AffCode: "quota-disabled", Group: "default", Status: common.UserStatusDisabled},
				{Id: 203, Username: "quota-legacy", AffCode: "quota-legacy", Group: "default", Status: common.UserStatusEnabled},
			}).Error)
			for i := 1; i <= 53; i++ {
				seat := fmt.Sprintf("seat-%03d", i)
				hash := fmt.Sprintf("%x", sha256.Sum256([]byte("openai-account\x00"+seat)))
				instance := "fixture"
				if i == 1 {
					instance = "missing"
				}
				require.NoError(t, db.Create(&model.GPTHostingGrant{SeatID: seat, UserID: 201, Group: seat, EndsAt: model.HostingPermanentEnd}).Error)
				require.NoError(t, db.Create(&model.GPTHostingBinding{SeatID: seat, BindingID: seat, UserID: 201, State: "active", InstanceID: instance, SlotID: seat, InventoryID: &seat, AccountHash: &hash, Platform: "codex"}).Error)
			}
			expired := model.GPTQuotaHistory{UserID: 201, Scope: strings.Repeat("a", 64), Bucket: now.Add(-31 * 24 * time.Hour).Unix(), Segment: strings.Repeat("b", 64)}
			require.NoError(t, db.Create(&expired).Error)
			var previous *model.SystemTask
			for page, want := range []struct {
				user                      int
				seat                      string
				processed, failed, points int64
			}{
				{201, "seat-050", 50, 1, 49}, {203, "", 4, 0, 53}, {0, "", 0, 0, 53},
			} {
				previous = runQuotaHistoryTask(t, previous, fmt.Sprintf("quota-process-%d", page), true)
				var checkpoint quotaHistoryCheckpoint
				require.NoError(t, previous.DecodeState(&checkpoint))
				assert.Equal(t, want.user, checkpoint.UserID)
				assert.Equal(t, want.seat, checkpoint.SeatID)
				var result map[string]int64
				require.NoError(t, common.UnmarshalJsonStr(previous.Result, &result))
				assert.Equal(t, want.processed, result["processed"])
				assert.Equal(t, want.failed, result["failed"])
				var points int64
				require.NoError(t, db.Model(&model.GPTQuotaHistory{}).Count(&points).Error)
				assert.Equal(t, want.points, points, "each process continues the previous page and old history is pruned")
			}
			assert.EqualValues(t, 53, reads.Load(), "completed pages must not be read again in the replacement process")
			// 在第二个名额的上游读取期间取消，已提交的第一条保留，未完成的第二条不能被跳过。
			interrupted, err := model.CreateSystemTask("gpt_quota_history", nil, nil)
			require.NoError(t, err)
			interrupted, claimed, err := model.ClaimSystemTask(interrupted.ID, interrupted.Type, "interrupt-owner", time.Now().Unix()+60)
			require.NoError(t, err)
			require.True(t, claimed)
			activeContext, cancelActive := context.WithCancel(t.Context())
			cancelDuringRead.Store(&cancelActive)
			quotaHistoryTaskHandler{}.Run(activeContext, interrupted, "interrupt-owner")
			cancelActive()
			interrupted, err = model.GetSystemTaskByTaskID(interrupted.TaskID)
			require.NoError(t, err)
			assert.JSONEq(t, `{"user_id":201,"seat_id":"seat-001"}`, interrupted.State)
			assert.Equal(t, model.SystemTaskStatusFailed, interrupted.Status)
			assert.EqualValues(t, 54, reads.Load(), "cancel must stop before reading the next seat")

			for _, mode := range []string{"cancel", "lost-lease", "retail"} {
				task, err := model.CreateSystemTask("gpt_quota_history", nil, nil)
				require.NoError(t, err)
				task, claimed, err := model.ClaimSystemTask(task.ID, task.Type, "old-owner", time.Now().Unix()+60)
				require.NoError(t, err)
				require.True(t, claimed)
				ctx, cancel := context.WithCancel(t.Context())
				switch mode {
				case "cancel":
					cancel()
				case "lost-lease":
					require.NoError(t, model.ReleaseSystemTaskLock(task.TaskID, "old-owner"))
				case "retail":
					t.Setenv("SITE_MODE", "retail")
				}
				quotaHistoryTaskHandler{}.Run(ctx, task, "old-owner")
				cancel()
				assert.EqualValues(t, 54, reads.Load(), mode+" must not reach the panel")
				if mode == "lost-lease" {
					require.NoError(t, model.MarkSystemTaskLeaseExpired(task.TaskID))
				}
			}
		})
	}
}
