package controller

import (
	"context"
	"fmt"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	gormlogger "gorm.io/gorm/logger"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestSystemTaskListFiltersAndPaginationResponse(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	previousDB := model.DB
	model.DB = db
	t.Cleanup(func() {
		model.DB = previousDB
		require.NoError(t, sqlDB.Close())
	})
	require.NoError(t, db.AutoMigrate(&model.SystemTask{}))
	require.NoError(t, db.Create(&[]model.SystemTask{
		{TaskID: "older", Type: model.SystemTaskTypeModelUpdate, Status: model.SystemTaskStatusFailed},
		{TaskID: "newer", Type: model.SystemTaskTypeModelUpdate, Status: model.SystemTaskStatusFailed},
		{TaskID: "other-status", Type: model.SystemTaskTypeModelUpdate, Status: model.SystemTaskStatusSucceeded},
		{TaskID: "other-type", Type: model.SystemTaskTypeChannelTest, Status: model.SystemTaskStatusFailed},
	}).Error)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/system-task/list?scope=history&type=model_update&status=failed&offset=1&limit=1", nil)
	ListSystemTasks(c)
	require.Equal(t, http.StatusOK, recorder.Code)
	var response struct {
		Success bool                       `json:"success"`
		Total   int64                      `json:"total"`
		Data    []model.SystemTaskResponse `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	assert.True(t, response.Success)
	assert.EqualValues(t, 2, response.Total)
	require.Len(t, response.Data, 1)
	assert.Equal(t, "older", response.Data[0].TaskID)
}

func TestSystemTaskInvalidFiltersAreRejected(t *testing.T) {
	for _, tc := range []struct {
		query   string
		handler gin.HandlerFunc
	}{
		{"?scope=invalid", ListSystemTasks},
		{"?status=unknown", ListSystemTasks},
		{"?offset=-1", ListSystemTasks},
		{"?limit=invalid", ListSystemTasks},
		{"?status=unknown", DeleteSystemTaskHistory},
		{"?scope=invalid", DeleteSystemTaskHistory},
	} {
		t.Run(tc.query, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodGet, "/"+tc.query, nil)
			tc.handler(c)
			assert.Equal(t, http.StatusBadRequest, recorder.Code)
			assert.JSONEq(t, `{"success":false,"message":"invalid system task filters"}`, recorder.Body.String())
		})
	}
}

// TestSiteTaskMaintenancePagination 验证坏记录不阻塞后续名额，跨任务继续下一页且完整扫描后回到起点。
func TestSiteTaskMaintenancePagination(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			t.Setenv("SITE_MODE", "hosting")
			t.Setenv("GPT_HOSTING_ENABLED", "true")
			var driver gorm.Dialector
			dsn := os.Getenv("TEST_SYSTEM_TASK_" + strings.ToUpper(dialect) + "_DSN")
			switch dialect {
			case "sqlite":
				driver = sqlite.Open(":memory:")
			case "mysql":
				if dsn == "" {
					t.Skip("dedicated matrix DSN not configured")
				}
				driver = mysql.Open(dsn)
			case "postgres":
				if dsn == "" {
					t.Skip("dedicated matrix DSN not configured")
				}
				driver = postgres.Open(dsn)
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
			require.NoError(t, db.AutoMigrate(&model.SystemTask{}, &model.SystemTaskLock{}, &model.GPTHostingBinding{}, &model.GPTHostingGrant{}))
			prefix := "page-" + common.GetRandomString(8) + "-"
			t.Cleanup(func() {
				require.NoError(t, db.Where("seat_id LIKE ?", prefix+"%").Delete(&model.GPTHostingBinding{}).Error)
				require.NoError(t, db.Where("seat_id LIKE ?", prefix+"%").Delete(&model.GPTHostingGrant{}).Error)
				require.NoError(t, db.Where("type = ?", siteTaskHosting).Delete(&model.SystemTaskLock{}).Error)
				require.NoError(t, db.Where("type = ?", siteTaskHosting).Delete(&model.SystemTask{}).Error)
			})
			for i := 1; i <= 53; i++ {
				seat := fmt.Sprintf("%s%03d", prefix, i)
				require.NoError(t, db.Create(&model.GPTHostingBinding{SeatID: seat, BindingID: seat, UserID: i, State: "reserved", UpdatedAt: time.Now().Unix()}).Error)
				// 第一条缺少权益，模拟历史坏数据；其余记录可以正常检查，无需访问真实账号服务。
				if i > 1 {
					require.NoError(t, db.Create(&model.GPTHostingGrant{SeatID: seat, Group: seat, UserID: i, EndsAt: model.HostingPermanentEnd}).Error)
				}
			}
			handler := siteTaskHandler{kind: siteTaskHosting}
			var previousTask *model.SystemTask
			for page, want := range []struct {
				cursor            string
				processed, failed int
			}{{prefix + "050", 50, 1}, {prefix + "053", 3, 0}, {"", 0, 0}} {
				state, err := handler.ResumeState(previousTask)
				require.NoError(t, err)
				task, err := model.CreateSystemTask(handler.Type(), nil, state)
				require.NoError(t, err)
				runner := fmt.Sprintf("page-runner-%d", page)
				task, claimed, err := model.ClaimSystemTask(task.ID, task.Type, runner, time.Now().Unix()+60)
				require.NoError(t, err)
				require.True(t, claimed)
				handler.Run(context.Background(), task, runner)
				previousTask, err = model.GetSystemTaskByTaskID(task.TaskID)
				require.NoError(t, err)
				var checkpoint siteTaskCheckpoint
				require.NoError(t, previousTask.DecodeState(&checkpoint))
				assert.Equal(t, want.cursor, checkpoint.Cursor)
				var result map[string]int
				require.NoError(t, common.UnmarshalJsonStr(previousTask.Result, &result))
				assert.Equal(t, want.processed, result["processed"])
				assert.Equal(t, want.failed, result["failed"])
			}
			// 取消或持有者失效时，连本地权益 revision 都不能推进，更不能开始下一项外部操作。
			for _, cancelled := range []bool{true, false} {
				task, err := model.CreateSystemTask(handler.Type(), nil, siteTaskCheckpoint{})
				require.NoError(t, err)
				task, claimed, err := model.ClaimSystemTask(task.ID, task.Type, "old-owner", time.Now().Unix()+60)
				require.NoError(t, err)
				require.True(t, claimed)
				ctx, cancel := context.WithCancel(context.Background())
				if cancelled {
					cancel()
				} else {
					require.NoError(t, db.Model(&model.SystemTaskLock{}).Where("task_id = ?", task.TaskID).Update("locked_by", "new-owner").Error)
				}
				handler.Run(ctx, task, "old-owner")
				cancel()
				var grant model.GPTHostingGrant
				require.NoError(t, db.First(&grant, "seat_id = ?", prefix+"002").Error)
				assert.EqualValues(t, 1, grant.Revision, "cancelled/lost owner must not run maintenance")
				if !cancelled {
					require.NoError(t, db.Model(&model.SystemTaskLock{}).Where("task_id = ?", task.TaskID).Update("locked_until", time.Now().Unix()-1).Error)
					require.NoError(t, model.ExpireStaleSystemTaskLocks(time.Now().Unix()))
				}
			}
			t.Setenv("SITE_MODE", "retail")
			assert.False(t, handler.Enabled(), "retail must never process hosting accounts")
			assert.False(t, (siteTaskHandler{kind: siteTaskHostingPayment}).Enabled())
		})
	}
}
