package router

import (
	"fmt"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestStudioRunUsesLoginAndPricing verifies the real router dispatches through the login, origin and price checks.
// The deliberately empty price book guarantees zero downstream traffic and zero wallet charges.
func TestStudioRunUsesLoginAndPricing(t *testing.T) {
	setupRelayRouterTestDB(t)
	t.Setenv("SITE_MODE", "retail")
	t.Setenv("STUDIO_HISTORY_DIR", t.TempDir())
	require.NoError(t, i18n.Init())
	require.NoError(t, model.DB.AutoMigrate(&model.UserSession{}, &model.RetailPriceState{}))
	user := model.User{Username: "studio-paid-routing", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1}
	require.NoError(t, model.DB.Create(&user).Error)
	login, err := service.CreateLoginSession(user.Id, "password", "127.0.0.1", "studio-test")
	require.NoError(t, err)
	engine := newAccessTokenScopeTestEngine(t)
	id := uuid.NewString()
	path := fmt.Sprintf("/api/retail/studio/runs/%s?owner=%d", id, user.Id)
	body := fmt.Sprintf(`{"record":{"id":"%s","mode":"text","product":"test","entries":[{"prompt":"test","model":"test"}]},"maxTokens":10240}`, id)
	request := func(method string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Origin", "http://example.com")
		req.Header.Set("Authorization", "Bearer "+login.AccessToken)
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		engine.ServeHTTP(rr, req)
		return rr
	}
	rr := request("POST")
	require.Equal(t, 202, rr.Code, rr.Body.String())
	require.Eventually(t, func() bool { return strings.Contains(request("GET").Body.String(), "retail_price_unavailable") }, 3*time.Second, 10*time.Millisecond)
}
