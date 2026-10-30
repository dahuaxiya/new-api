package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type pricingHostingResponse struct {
	Success     bool               `json:"success"`
	Data        []model.Pricing    `json:"data"`
	UsableGroup map[string]string  `json:"usable_group"`
	GroupRatio  map[string]float64 `json:"group_ratio"`
	AutoGroups  []string           `json:"auto_groups"`
}

// pricingHostingRequest 模拟鉴权中间件设置的身份；查询参数不能决定用户身份。
func pricingHostingRequest(t *testing.T, userID, role int) (int, pricingHostingResponse) {
	t.Helper()
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/pricing?user_id=3", nil)
	if userID > 0 {
		ctx.Set("id", userID)
		ctx.Set("role", role)
	}
	GetPricing(ctx)
	var result pricingHostingResponse
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &result))
	return recorder.Code, result
}

// TestPricingHostingVisibility 验证管理目录、有效名额、跨账号分组裁剪及共享缓存不被污染。
func TestPricingHostingVisibility(t *testing.T) {
	t.Setenv("GPT_HOSTING_ENABLED", "true")
	db := setupModelListControllerTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.GPTHostingGrant{}, &model.GPTHostingBinding{}, &model.UserSession{}, &model.UserAccessToken{}, &model.Option{}, &model.AuditLog{}))
	oldGroups := setting.UserUsableGroups2JSONString()
	oldRatios, err := common.Marshal(ratio_setting.GetGroupRatioCopy())
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(oldGroups))
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(string(oldRatios)))
		model.InvalidatePricingCache()
	})
	require.NoError(t, setting.UpdateUserUsableGroupsByJSONString(`{}`))
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"acct_a":1.25,"acct_c":1.5,"acct_b":2,"legacy":0.7}`))

	users := []model.User{
		{Id: 1, Username: "root-pricing", AffCode: "root-pricing", Group: "default", Role: common.RoleRootUser, Status: common.UserStatusEnabled},
		{Id: 2, Username: "owner-pricing", AffCode: "owner-pricing", Group: "legacy", Role: common.RoleCommonUser, Status: common.UserStatusEnabled},
		{Id: 3, Username: "other-pricing", AffCode: "other-pricing", Group: "other", Role: common.RoleCommonUser, Status: common.UserStatusEnabled},
		{Id: 4, Username: "ordinary-pricing", AffCode: "ordinary-pricing", Group: "legacy", Role: common.RoleCommonUser, Status: common.UserStatusEnabled},
	}
	require.NoError(t, db.Create(&users).Error)
	channels := []model.Channel{
		{Id: 1, Name: "owned-a", Type: 1, Status: common.ChannelStatusEnabled},
		{Id: 2, Name: "owned-c", Type: 1, Status: common.ChannelStatusEnabled},
		{Id: 3, Name: "foreign", Type: 1, Status: common.ChannelStatusEnabled},
		{Id: 4, Name: "legacy", Type: 1, Status: common.ChannelStatusEnabled},
		{Id: 5, Name: "same-group-foreign", Type: 1, Status: common.ChannelStatusEnabled},
		{Id: 6, Name: "expired", Type: 1, Status: common.ChannelStatusEnabled},
		{Id: 7, Name: "paused", Type: 1, Status: common.ChannelStatusEnabled},
		{Id: 8, Name: "disabled", Type: 1, Status: common.ChannelStatusManuallyDisabled},
	}
	require.NoError(t, db.Create(&channels).Error)
	abilities := []model.Ability{
		{Group: "acct_a", Model: "shared-model", ChannelId: 1, Enabled: true},
		{Group: "acct_a", Model: "own-model", ChannelId: 1, Enabled: true},
		{Group: "acct_c", Model: "second-model", ChannelId: 2, Enabled: true},
		{Group: "acct_b", Model: "shared-model", ChannelId: 3, Enabled: true},
		{Group: "acct_b", Model: "foreign-model", ChannelId: 3, Enabled: true},
		{Group: "legacy", Model: "legacy-model", ChannelId: 4, Enabled: true},
		{Group: "acct_a", Model: "wrong-channel-model", ChannelId: 5, Enabled: true},
		{Group: "acct_expired", Model: "expired-model", ChannelId: 6, Enabled: true},
		{Group: "acct_paused", Model: "paused-model", ChannelId: 7, Enabled: true},
		{Group: "acct_disabled", Model: "disabled-model", ChannelId: 8, Enabled: true},
	}
	require.NoError(t, db.Create(&abilities).Error)
	now := time.Now().Unix()
	grants := []model.GPTHostingGrant{
		{SeatID: "seat-a", UserID: 2, Group: "acct_a", StartsAt: now - 100, EndsAt: -1, Enabled: true},
		{SeatID: "seat-c", UserID: 2, Group: "acct_c", StartsAt: now - 100, EndsAt: now + 3600, Enabled: true},
		{SeatID: "seat-expired", UserID: 2, Group: "acct_expired", StartsAt: now - 100, EndsAt: now - 1, Enabled: true},
		{SeatID: "seat-paused", UserID: 2, Group: "acct_paused", StartsAt: now - 100, EndsAt: -1, Enabled: true},
		{SeatID: "seat-unbound", UserID: 2, Group: "acct_unbound", StartsAt: now - 100, EndsAt: -1, Enabled: true},
		{SeatID: "seat-disabled", UserID: 2, Group: "acct_disabled", StartsAt: now - 100, EndsAt: -1, Enabled: true},
		{SeatID: "seat-b", UserID: 3, Group: "acct_b", StartsAt: now - 100, EndsAt: -1, Enabled: true},
	}
	require.NoError(t, db.Create(&grants).Error)
	for i, seat := range []struct {
		id      string
		userID  int
		channel int
		paused  bool
	}{
		{"seat-a", 2, 1, false}, {"seat-c", 2, 2, false}, {"seat-expired", 2, 6, false},
		{"seat-paused", 2, 7, true}, {"seat-disabled", 2, 8, false}, {"seat-b", 3, 3, false},
	} {
		inventory := seat.id + "-inventory"
		binding := model.GPTHostingBinding{SeatID: seat.id, UserID: seat.userID, BindingID: seat.id + "-binding", InventoryID: &inventory, State: "active", Paused: seat.paused, ChannelID: seat.channel}
		require.NoError(t, db.Create(&binding).Error, "binding %d", i)
	}
	model.InvalidatePricingCache()

	status, owner := pricingHostingRequest(t, 2, common.RoleCommonUser)
	require.Equal(t, http.StatusOK, status)
	ownerModels := pricingByModelName(owner.Data)
	assert.ElementsMatch(t, []string{"shared-model", "own-model", "second-model"}, pricingNames(owner.Data))
	assert.Equal(t, []string{"acct_a"}, ownerModels["shared-model"].EnableGroup)
	assert.NotContains(t, owner.UsableGroup, "acct_b")
	assert.NotContains(t, owner.GroupRatio, "acct_b")
	assert.InDelta(t, 1.25, owner.GroupRatio["acct_a"], 0.001)
	assert.Empty(t, owner.AutoGroups)

	_, other := pricingHostingRequest(t, 3, common.RoleCommonUser)
	assert.ElementsMatch(t, []string{"shared-model", "foreign-model"}, pricingNames(other.Data))
	assert.Equal(t, []string{"acct_b"}, pricingByModelName(other.Data)["shared-model"].EnableGroup)
	_, ordinary := pricingHostingRequest(t, 4, common.RoleCommonUser)
	assert.Equal(t, []string{"legacy-model"}, pricingNames(ordinary.Data))
	_, anonymous := pricingHostingRequest(t, 0, 0)
	assert.Empty(t, anonymous.Data)

	_, admin := pricingHostingRequest(t, 1, common.RoleRootUser)
	assert.Contains(t, pricingNames(admin.Data), "foreign-model")
	assert.Contains(t, pricingNames(admin.Data), "wrong-channel-model")
	assert.ElementsMatch(t, []string{"acct_a", "acct_b"}, pricingByModelName(admin.Data)["shared-model"].EnableGroup)
	assert.Contains(t, admin.UsableGroup, "acct_b")

	// 通过真实会话与路由中间件验证，客户端参数不能替代服务器鉴权结果。
	oldSecret := common.SessionSecret
	common.SessionSecret = "pricing-test-session-secret"
	t.Cleanup(func() { common.SessionSecret = oldSecret })
	require.NoError(t, model.EnsureLegacyAccessTokenRetireAt(now))
	ownerSession, err := service.CreateLoginSession(2, "password", "127.0.0.1", "pricing-test")
	require.NoError(t, err)
	rootSession, err := service.CreateLoginSession(1, "password", "127.0.0.1", "pricing-test")
	require.NoError(t, err)
	router := gin.New()
	router.GET("/api/pricing", middleware.TryUserAuth(), GetPricing)
	for _, tc := range []struct {
		name   string
		token  string
		models []string
	}{
		{"owner", ownerSession.AccessToken, []string{"shared-model", "own-model", "second-model"}},
		{"root", rootSession.AccessToken, pricingNames(admin.Data)},
	} {
		t.Run("session_"+tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/api/pricing?user_id=3&role=100", nil)
			request.Header.Set("Authorization", "Bearer "+tc.token)
			router.ServeHTTP(recorder, request)
			require.Equal(t, http.StatusOK, recorder.Code)
			var response pricingHostingResponse
			require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
			assert.ElementsMatch(t, tc.models, pricingNames(response.Data))
		})
	}

	// 所有名额失效后，不能因为用户原有 legacy 分组而继续展示旧渠道。
	require.NoError(t, db.Model(&model.GPTHostingGrant{}).Where("user_id = ?", 2).Update("enabled", false).Error)
	_, expired := pricingHostingRequest(t, 2, common.RoleCommonUser)
	assert.Empty(t, expired.Data)
	assert.Empty(t, expired.UsableGroup)

	// 权益表读取失败必须拒绝查询，不能静默回退到 legacy。
	require.NoError(t, db.Migrator().DropTable(&model.GPTHostingGrant{}))
	status, failed := pricingHostingRequest(t, 2, common.RoleCommonUser)
	assert.Equal(t, http.StatusInternalServerError, status)
	assert.False(t, failed.Success)
}

// pricingNames 返回响应中的模型名，供测试比较可见范围，不依赖数据库的返回顺序。
func pricingNames(pricing []model.Pricing) []string {
	names := make([]string, 0, len(pricing))
	for _, item := range pricing {
		names = append(names, item.ModelName)
	}
	return names
}
