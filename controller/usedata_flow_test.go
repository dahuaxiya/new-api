package controller

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type flowQuotaResponse struct {
	Success bool                  `json:"success"`
	Message string                `json:"message"`
	Data    []model.FlowQuotaData `json:"data"`
}

func setupFlowControllerTestDB(t *testing.T) {
	t.Helper()
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Token{}, &model.QuotaData{}))
	require.NoError(t, model.DB.Create(&model.Channel{Id: 1, Name: "east"}).Error)
	require.NoError(t, model.DB.Create(&model.Token{Id: 11, UserId: 1, Key: "sk-primary", Name: "primary"}).Error)
	require.NoError(t, model.DB.Create(&model.Token{Id: 22, UserId: 2, Key: "sk-backup", Name: "backup"}).Error)
	require.NoError(t, model.DB.Create(&model.QuotaData{
		UserID:    1,
		Username:  "alice",
		NodeName:  "node-a",
		TokenID:   11,
		UseGroup:  "default",
		ChannelID: 1,
		ModelName: "gpt-a",
		CreatedAt: 1100,
		Count:     2,
		Quota:     100,
		TokenUsed: 40,
	}).Error)
	require.NoError(t, model.DB.Create(&model.QuotaData{
		UserID:    2,
		Username:  "bob",
		NodeName:  "node-b",
		TokenID:   22,
		UseGroup:  "vip",
		ChannelID: 1,
		ModelName: "gpt-b",
		CreatedAt: 1200,
		Count:     1,
		Quota:     70,
		TokenUsed: 30,
	}).Error)
}

func decodeFlowQuotaResponse(t *testing.T, recorder *httptest.ResponseRecorder) flowQuotaResponse {
	t.Helper()
	require.Equal(t, http.StatusOK, recorder.Code)
	var payload flowQuotaResponse
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &payload))
	require.True(t, payload.Success, payload.Message)
	return payload
}

func TestGetAllFlowQuotaDatesUsesAdminDimensions(t *testing.T) {
	setupFlowControllerTestDB(t)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Set("role", common.RoleAdminUser)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/data/flow?start_timestamp=1000&end_timestamp=2000&username=bob", nil)

	GetAllFlowQuotaDates(ctx)

	payload := decodeFlowQuotaResponse(t, recorder)
	require.Len(t, payload.Data, 1)
	require.Equal(t, "bob", payload.Data[0].Username)
	require.Equal(t, "vip", payload.Data[0].UseGroup)
	require.Equal(t, "east", payload.Data[0].ChannelName)
	require.Empty(t, payload.Data[0].TokenName)
	require.Empty(t, payload.Data[0].NodeName)
}

func TestGetAllFlowQuotaDatesUsesRootDimensions(t *testing.T) {
	setupFlowControllerTestDB(t)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Set("role", common.RoleRootUser)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/data/flow?start_timestamp=1000&end_timestamp=2000&username=alice", nil)

	GetAllFlowQuotaDates(ctx)

	payload := decodeFlowQuotaResponse(t, recorder)
	require.Len(t, payload.Data, 1)
	require.Equal(t, "alice", payload.Data[0].Username)
	require.Equal(t, "node-a", payload.Data[0].NodeName)
	require.Equal(t, "primary", payload.Data[0].TokenName)
	require.Equal(t, "default", payload.Data[0].UseGroup)
	require.Equal(t, "east", payload.Data[0].ChannelName)
}

func TestGetUserFlowQuotaDatesRestrictsToAuthenticatedUser(t *testing.T) {
	setupFlowControllerTestDB(t)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Set("id", 1)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/data/flow/self?start_timestamp=1000&end_timestamp=2000", nil)

	GetUserFlowQuotaDates(ctx)

	payload := decodeFlowQuotaResponse(t, recorder)
	require.Len(t, payload.Data, 1)
	require.Empty(t, payload.Data[0].Username)
	require.Equal(t, "primary", payload.Data[0].TokenName)
	require.Equal(t, "default", payload.Data[0].UseGroup)
	require.Empty(t, payload.Data[0].ChannelName)
}

func TestGetUserFlowQuotaDatesRejectsInvalidTimeRange(t *testing.T) {
	setupFlowControllerTestDB(t)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Set("id", 1)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/data/flow/self?start_timestamp=bad&end_timestamp=2000", nil)

	GetUserFlowQuotaDates(ctx)

	require.Equal(t, http.StatusOK, recorder.Code)
	var payload flowQuotaResponse
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &payload))
	require.False(t, payload.Success)
	require.Equal(t, "invalid start_timestamp", payload.Message)
}

// TestUserQuotaBreakdownDatabaseMatrix 验证三种真实数据库上的汇总、历史未知维度及跨用户隔离。
func TestUserQuotaBreakdownDatabaseMatrix(t *testing.T) {
	for _, tc := range []struct {
		kind, env string
		typ       common.DatabaseType
	}{
		{"sqlite", "", common.DatabaseTypeSQLite},
		{"mysql", "AUDIT_MYSQL_DSN", common.DatabaseTypeMySQL},
		{"postgres", "AUDIT_POSTGRES_DSN", common.DatabaseTypePostgreSQL},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			dsn := os.Getenv(tc.env)
			if tc.env != "" && dsn == "" {
				t.Skip("dedicated database not configured")
			}
			db, _ := newAuditTestDatabase(t, tc.kind, dsn)
			useLogExportDatabases(t, db, db, tc.typ, tc.typ)
			require.NoError(t, db.AutoMigrate(&model.QuotaData{}, &model.Token{}))
			tokens := []model.Token{
				{Id: 11, UserId: 1, Key: "fixture-secret-a", Name: "production"},
				{Id: 12, UserId: 1, Key: "fixture-secret-b", Name: "production"},
				{Id: 13, UserId: 1, Key: "fixture-secret-c", Name: "deleted"},
				{Id: 22, UserId: 2, Key: "fixture-secret-other", Name: "other-user"},
			}
			require.NoError(t, db.Create(&tokens).Error)
			require.NoError(t, db.Delete(&model.Token{}, 13).Error)
			hour := time.Now().Unix() / 3600 * 3600
			rows := []model.QuotaData{
				{UserID: 1, CreatedAt: hour, TokenID: 11, ModelName: "model-a", UseGroup: "default", ChannelID: 1, Count: 1, Quota: 1, TokenUsed: 10},
				{UserID: 1, CreatedAt: hour, TokenID: 11, ModelName: "model-a", UseGroup: "default", ChannelID: 2, Count: 2, Quota: 2, TokenUsed: 20},
				{UserID: 1, CreatedAt: hour, TokenID: 12, ModelName: "model-a", UseGroup: "default", Count: 1, Quota: 4, TokenUsed: 30},
				{UserID: 1, CreatedAt: hour - 3600, TokenID: 13, ModelName: "free-model", UseGroup: "vip", Count: 1, Quota: 0, TokenUsed: 40},
				{UserID: 1, CreatedAt: hour, ModelName: "legacy", Count: 1, Quota: 8, TokenUsed: 50},
				{UserID: 2, CreatedAt: hour, TokenID: 22, ModelName: "private", UseGroup: "vip", Count: 99, Quota: 999, TokenUsed: 999},
				{UserID: 1, CreatedAt: hour - 7200, TokenID: 11, ModelName: "out-of-range", Count: 99, Quota: 999},
			}
			require.NoError(t, db.Create(&rows).Error)
			query := fmt.Sprintf("start_timestamp=%d&end_timestamp=%d", hour-3600, hour)
			// 通过真实 handler 和模型查询验证；伪造用户名及用户 ID 也不能改变 self 归属。
			payload := callLogHandler(t, GetUserQuotaDates, 1, common.RoleRootUser, query+"&breakdown=true&username=other&user_id=2")[0]
			require.Equal(t, true, payload["success"])
			encoded, err := common.Marshal(payload["data"])
			require.NoError(t, err)
			var data []model.UserQuotaBreakdown
			require.NoError(t, common.Unmarshal(encoded, &data))
			require.Equal(t, []model.UserQuotaBreakdown{
				{CreatedAt: hour - 3600, TokenID: 13, ModelName: "free-model", UseGroup: "vip", Count: 1, Quota: 0, TokenUsed: 40},
				{CreatedAt: hour, ModelName: "legacy", Count: 1, Quota: 8, TokenUsed: 50},
				{CreatedAt: hour, TokenID: 11, TokenName: "production", ModelName: "model-a", UseGroup: "default", Count: 3, Quota: 3, TokenUsed: 30},
				{CreatedAt: hour, TokenID: 12, TokenName: "production", ModelName: "model-a", UseGroup: "default", Count: 1, Quota: 4, TokenUsed: 30},
			}, data)
			for _, value := range []string{"fixture-secret", "other-user", "private", "channel_id", "user_id"} {
				require.NotContains(t, string(encoded), value)
			}
			legacy := callLogHandler(t, GetUserQuotaDates, 1, common.RoleCommonUser, query)[0]
			require.Equal(t, true, legacy["success"])
			require.Len(t, legacy["data"], 3, "without breakdown the original model/time aggregation must stay compatible")
			invalid := callLogHandler(t, GetUserQuotaDates, 1, common.RoleCommonUser, "breakdown=true&start_timestamp=bad&end_timestamp=2000")[0]
			require.Equal(t, false, invalid["success"])
			wide := callLogHandler(t, GetUserQuotaDates, 1, common.RoleRootUser, fmt.Sprintf("breakdown=true&start_timestamp=%d&end_timestamp=%d", hour-32*86400, hour))[0]
			require.Equal(t, false, wide["success"], "even root cannot request an unbounded personal breakdown")
		})
	}
}
