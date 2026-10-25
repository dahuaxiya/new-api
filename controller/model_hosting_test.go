package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestListModelsHostingRoute 验证旧 Key 重绑后的模型发现、模型限制及失效权益，禁止回退旧组或其他用户的组。
func TestListModelsHostingRoute(t *testing.T) {
	for _, tc := range []struct {
		name, tokenGroup, state                  string
		grant, enabled, paused, expired, limited bool
		wantStatus                               int
		wantModels                               []string
	}{
		{"legacy without grant", "", "", false, false, false, false, false, 200, []string{"old-model"}},
		{"rebound inherited key", "", "active", true, true, false, false, false, 200, []string{"hosted-a", "hosted-b"}},
		{"stale explicit group", "other", "active", true, true, false, false, false, 200, []string{"hosted-a", "hosted-b"}},
		{"auto cannot widen", "auto", "active", true, true, false, false, false, 200, []string{"hosted-a", "hosted-b"}},
		{"token restriction retained", "", "active", true, true, false, false, true, 200, []string{"hosted-a"}},
		{"expired", "", "active", true, true, false, true, false, 403, nil},
		{"disabled", "", "active", true, false, false, false, false, 403, nil},
		{"paused", "", "active", true, true, true, false, false, 403, nil},
		{"unbound", "", "unbound", true, true, false, false, false, 403, nil},
		{"missing binding", "", "", true, true, false, false, false, 403, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("GPT_HOSTING_ENABLED", "true")
			withSelfUseModeEnabled(t)
			db := setupModelListControllerTestDB(t)
			require.NoError(t, db.AutoMigrate(&model.GPTHostingGrant{}, &model.GPTHostingBinding{}))
			require.NoError(t, db.Create(&[]model.Ability{
				{Group: "old", Model: "old-model", ChannelId: 2, Enabled: true},
				{Group: "other", Model: "other-user-model", ChannelId: 3, Enabled: true},
				{Group: "acct_owned", Model: "hosted-a", ChannelId: 1, Enabled: true},
				{Group: "acct_owned", Model: "hosted-b", ChannelId: 1, Enabled: true},
			}).Error)
			if tc.grant {
				end := model.HostingPermanentEnd
				if tc.expired {
					end = time.Now().Unix() - 1
				}
				require.NoError(t, db.Create(&model.GPTHostingGrant{UserID: 12, Group: "acct_owned", Enabled: tc.enabled, StartsAt: 1, EndsAt: end}).Error)
				if tc.state != "" {
					inventory := "owned-slot"
					require.NoError(t, db.Create(&model.GPTHostingBinding{UserID: 12, BindingID: "binding", InventoryID: &inventory, State: tc.state, Paused: tc.paused, ChannelID: 1}).Error)
				}
			}
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodGet, "/v1/models?user_id=999", nil)
			ctx.Set("id", 12)
			common.SetContextKey(ctx, constant.ContextKeyUserGroup, "old")
			common.SetContextKey(ctx, constant.ContextKeyTokenGroup, tc.tokenGroup)
			common.SetContextKey(ctx, constant.ContextKeyTokenAutoGroups, []string{"other", "old"})
			common.SetContextKey(ctx, constant.ContextKeyTokenModelLimitEnabled, tc.limited)
			common.SetContextKey(ctx, constant.ContextKeyTokenModelLimit, map[string]bool{"hosted-a": true, "other-user-model": true})
			ListModels(ctx, constant.ChannelTypeOpenAI)
			require.Equal(t, tc.wantStatus, recorder.Code, recorder.Body.String())
			if tc.wantStatus != http.StatusOK {
				assert.Contains(t, recorder.Body.String(), "hosting_unavailable")
				assert.NotContains(t, recorder.Body.String(), "old-model")
				return
			}
			payload := decodeListModelsPayload(t, recorder)
			ids := make([]string, 0, len(payload.Data))
			for _, item := range payload.Data {
				ids = append(ids, item.Id)
			}
			assert.ElementsMatch(t, tc.wantModels, ids)
		})
	}
}
