package controller

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/Calcium-Ion/go-epay/epay"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/gptquota"
	"github.com/QuantumNous/new-api/service/vm2api"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// TestGPTHostingSessionBoundary 验证真实登录会话隔离、跨站拒绝、名额停用以及敏感动作所需证明。
func TestGPTHostingSessionBoundary(t *testing.T) {
	user, identity := setupSecurityEnrollmentTest(t)
	t.Setenv("GPT_HOSTING_ENABLED", "true")
	require.NoError(t, model.DB.AutoMigrate(&model.GPTHostingGrant{}, &model.GPTHostingBinding{}, &model.GPTHostingInventory{}))
	now := time.Now().Unix()
	require.NoError(t, model.DB.Create(&model.GPTHostingGrant{UserID: user.Id, StartsAt: now - 1, EndsAt: model.HostingPermanentEnd, Enabled: true, Group: "acct_fixture"}).Error)
	require.NoError(t, model.DB.Create(&model.GPTHostingBinding{UserID: user.Id, BindingID: uuid.NewString(), State: "unbound"}).Error)
	token, _, e := service.IssueAccessToken(identity)
	require.NoError(t, e)
	r := gin.New()
	r.GET("/self", middleware.UserAuth(), GetGPTHostingSelf)
	r.GET("/admin", middleware.RootAuth(), GetGPTHostingAdmin)
	r.POST("/login", middleware.UserAuth(), middleware.SessionCookieOriginGuard(), StartGPTHostingLogin)
	r.POST("/unbind", middleware.UserAuth(), middleware.SessionCookieOriginGuard(), UnbindGPTHosting)
	request := func(method, path, auth, origin string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "http://portal.test"+path, bytes.NewBufferString(`{}`))
		req.Header.Set("Content-Type", "application/json")
		if auth != "" {
			req.Header.Set("Authorization", "Bearer "+auth)
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	own := request("GET", "/self?user_id=999", token, "")
	require.Equal(t, 200, own.Code, own.Body.String())
	require.Contains(t, own.Body.String(), `"binding_id"`)
	for _, private := range []string{"account_hash", "subject_hash", "slot_id", "instance_id", "operation_token", "key_id"} {
		require.NotContains(t, own.Body.String(), `"`+private+`"`)
	}
	// 用户即使知道他人的名额编号，也不能读取其身份或授权状态。
	foreignSeat := uuid.NewString()
	require.NoError(t, model.DB.Create(&model.GPTHostingGrant{SeatID: foreignSeat, UserID: user.Id + 1, Group: "acct_other", Enabled: true, StartsAt: now - 1, EndsAt: -1}).Error)
	require.NoError(t, model.DB.Create(&model.GPTHostingBinding{SeatID: foreignSeat, UserID: user.Id + 1, BindingID: uuid.NewString(), State: "active", AccountLabel: "foreign-owner"}).Error)
	forbiddenSeat := request("GET", "/self?seat_id="+foreignSeat, token, "")
	require.Equal(t, 409, forbiddenSeat.Code)
	require.NotContains(t, forbiddenSeat.Body.String(), "foreign-owner")
	require.Equal(t, 401, request("GET", "/self", "", "").Code)
	require.Equal(t, 403, request("GET", "/admin", token, "").Code)
	require.Equal(t, 403, request("POST", "/login", token, "https://untrusted.test").Code)
	for _, action := range []string{"/login", "/unbind"} {
		denied := request("POST", action, token, "http://portal.test")
		require.NotEqual(t, http.StatusOK, denied.Code, denied.Body.String())
		var binding model.GPTHostingBinding
		require.NoError(t, model.DB.First(&binding, "user_id = ?", user.Id).Error)
		require.Equal(t, "unbound", binding.State, "missing proof must not reserve or clear")
	}
	require.NoError(t, model.DB.Model(&model.GPTHostingGrant{}).Where("user_id = ?", user.Id).Update("ends_at", now-1).Error)
	denied := request("POST", "/login", token, "http://portal.test")
	require.Contains(t, denied.Body.String(), "hosting_expired")
	require.Equal(t, common.RoleCommonUser, user.Role)
}

// TestUnifiedHostingAdminBoundary 验证旧绑定立即可见、错误归属不会回退，以及普通用户与无证明写入均被拒绝。
func TestUnifiedHostingAdminBoundary(t *testing.T) {
	actor, identity := setupSecurityEnrollmentTest(t)
	t.Setenv("GPT_HOSTING_ENABLED", "true")
	require.NoError(t, model.DB.AutoMigrate(&model.GPTHostingGrant{}, &model.GPTHostingBinding{}, &model.GPTHostingInventory{}, &model.VM2APIEnrollment{}))
	target := model.User{Username: "legacy-owner", AffCode: "legacy-owner", Group: "old-group", Role: common.RoleCommonUser, Status: 1, Quota: 345}
	require.NoError(t, model.DB.Create(&target).Error)
	cfg := gptquota.Config{PanelURL: "https://fixture.test/api/panel", Users: map[string]gptquota.Binding{strconv.Itoa(target.Id): {Username: target.Username, Group: target.Group, Slot: "slot-old"}}}
	registry, err := vm2api.New(vm2api.Config{Instances: []vm2api.InstanceConfig{{ID: "cc", Name: "CloudCone", BaseURL: "https://fixture.test", Username: "fixture", Password: "fixture-password", Models: []string{"gpt-test"}}}})
	require.NoError(t, err)
	oldCfg, oldRegistry, oldSvc := configuredQuota, onboardingRegistry, quotaService
	configuredQuota = func() (gptquota.Config, error) { return cfg, nil }
	onboardingRegistry = func() (*vm2api.Registry, error) { return registry, nil }
	quotaService = func() (*gptquota.Service, error) { return nil, errors.New("offline") }
	t.Cleanup(func() { configuredQuota, onboardingRegistry, quotaService = oldCfg, oldRegistry, oldSvc })
	token, _, err := service.IssueAccessToken(identity)
	require.NoError(t, err)
	r := gin.New()
	r.GET("/users", middleware.AdminAuth(), GetGPTHostingUsers)
	r.POST("/wallet", middleware.AdminAuth(), middleware.SessionCookieOriginGuard(), AdjustHostingUserWallet)
	r.POST("/grants", middleware.RootAuth(), middleware.SessionCookieOriginGuard(), PutGPTHostingGrant)
	r.POST("/takeover", middleware.RootAuth(), middleware.SessionCookieOriginGuard(), TakeoverGPTHosting)
	request := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "http://portal.test"+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Origin", "http://portal.test")
		req.Header.Set("Content-Type", "application/json")
		out := httptest.NewRecorder()
		r.ServeHTTP(out, req)
		return out
	}
	path := "/users?ids=" + strconv.Itoa(target.Id)
	require.Equal(t, 403, request("GET", path, "").Code)
	require.NoError(t, model.DB.Model(actor).Update("role", common.RoleRootUser).Error)
	require.NoError(t, model.PublishUserAuthCache(actor.Id))
	out := request("GET", path, "")
	require.Equal(t, 200, out.Code)
	require.Contains(t, out.Body.String(), `"status":"undated"`)
	require.Contains(t, out.Body.String(), `"instance_id":"cc"`)
	require.Contains(t, out.Body.String(), `"quota_unavailable":true`)
	require.NotContains(t, out.Body.String(), "fixture-password")
	require.NotContains(t, out.Body.String(), "fixture.test")
	for _, tc := range []struct{ path, body string }{{"/wallet", fmt.Sprintf(`{"id":%d,"action":"add_quota","mode":"add","value":99,"reason":"fixture"}`, target.Id)}, {"/takeover", fmt.Sprintf(`{"user_id":%d,"starts_at":1,"ends_at":-1,"enabled":true}`, target.Id)}, {"/grants", fmt.Sprintf(`{"user_id":%d,"starts_at":1,"ends_at":-1,"enabled":true}`, target.Id)}} {
		require.NotEqual(t, 200, request("POST", tc.path, tc.body).Code, "missing proof must not write")
	}
	saved, err := model.GetUserById(target.Id, false)
	require.NoError(t, err)
	require.Equal(t, 345, saved.Quota)
	var count int64
	require.NoError(t, model.DB.Model(&model.GPTHostingGrant{}).Count(&count).Error)
	require.Zero(t, count)
	cfg.Users["999"] = cfg.Users[strconv.Itoa(target.Id)]
	require.Contains(t, request("GET", path, "").Body.String(), `"status":"conflict"`)
	delete(cfg.Users, "999")
	require.NoError(t, model.DB.Model(&target).Update("group", "moved").Error)
	require.Contains(t, request("GET", path, "").Body.String(), `"status":"conflict"`)
	require.Equal(t, 400, request("GET", "/users?ids=01", "").Code)
}

// TestHostingLoginRejectsLegacyReference 验证 root 保留的历史归属不能经登录恢复接口伪装成已验证的新登录。
func TestHostingLoginRejectsLegacyReference(t *testing.T) {
	binding := &model.GPTHostingBinding{BindingID: "binding", SlotID: "slot", State: "prepared"}
	state := vm2api.HostedState{BindingID: "binding", VMID: "slot", State: "prepared", IdentitySource: "legacy_binding"}
	require.ErrorIs(t, finishHosting(context.Background(), binding, "lease", state), model.ErrHostingConflict)
}

// TestPermanentHostingSummary 确保永久名额仍按开始时间、暂停和绑定阶段展示，不误报到期。
func TestPermanentHostingSummary(t *testing.T) {
	user, _ := setupSecurityEnrollmentTest(t)
	require.NoError(t, model.DB.AutoMigrate(&model.GPTHostingGrant{}, &model.GPTHostingBinding{}))
	now := time.Now().Unix()
	grant := model.GPTHostingGrant{UserID: user.Id, StartsAt: now - 1, EndsAt: model.HostingPermanentEnd, Enabled: true, Group: "acct_permanent"}
	binding := model.GPTHostingBinding{UserID: user.Id, BindingID: uuid.NewString(), State: "active"}
	require.NoError(t, model.DB.Create(&grant).Error)
	require.NoError(t, model.DB.Create(&binding).Error)
	for _, status := range []string{"active", "disabled", "scheduled", "awaiting_login"} {
		grant.StartsAt, binding.Paused, binding.State = now-1, status == "disabled", "active"
		if status == "scheduled" {
			grant.StartsAt = now + 3600
		}
		if status == "awaiting_login" {
			binding.State = "unbound"
		}
		require.NoError(t, model.DB.Save(&grant).Error)
		require.NoError(t, model.DB.Save(&binding).Error)
		require.Equal(t, status, hostingSummary(user).Status)
	}
}

// TestHostingStoreCallbackBoundary 使用真实签名和数据库验证回调验签、金额口径、重复通知及用户归属。
func TestHostingStoreCallbackBoundary(t *testing.T) {
	oldSecure := common.SessionCookieSecure
	common.SessionCookieSecure = true
	t.Cleanup(func() { common.SessionCookieSecure = oldSecure })
	oldOptions, oldRatios := common.OptionMap, ratio_setting.GroupRatio2JSONString()
	common.OptionMap = map[string]string{}
	t.Cleanup(func() { common.OptionMap = oldOptions; _ = ratio_setting.UpdateGroupRatioByJSONString(oldRatios) })
	user, identity := setupSecurityEnrollmentTest(t)
	t.Setenv("GPT_HOSTING_ENABLED", "true")
	require.NoError(t, model.DB.AutoMigrate(&model.HostingPlan{}, &model.HostingOrder{}, &model.GPTHostingGrant{}, &model.GPTHostingBinding{}, &model.GPTHostingInventory{}, &model.VM2APIEnrollment{}, &model.Token{}))
	require.NoError(t, model.MigrateHostingStore(model.DB))
	oldAddress, oldID, oldKey, oldMethods := operation_setting.PayAddress, operation_setting.EpayId, operation_setting.EpayKey, operation_setting.PayMethods
	t.Cleanup(func() {
		operation_setting.PayAddress, operation_setting.EpayId, operation_setting.EpayKey, operation_setting.PayMethods = oldAddress, oldID, oldKey, oldMethods
	})
	operation_setting.PayAddress = "https://payment.test"
	operation_setting.EpayId = "merchant"
	operation_setting.EpayKey = "test-only-signing-secret"
	operation_setting.PayMethods = []map[string]string{{"type": "wxpay"}, {"type": "alipay"}}
	require.NoError(t, model.SaveHostingPlan(model.HostingPlan{ID: "monthly", Days: 30, PriceCents: 4000, Enabled: true}))
	inv, err := model.PutHostingInventory("cc", "signed-callback", true)
	require.NoError(t, err)
	order, err := model.CreateHostingOrder(user.Id, uuid.NewString(), "monthly", "", "codex", "wxpay", "merchant", []string{inv.ID})
	require.NoError(t, err)
	r := gin.New()
	r.GET("/notify", HostingPaymentNotify)
	r.POST("/notify", HostingPaymentNotify)
	r.GET("/orders", middleware.UserAuth(), GetHostingOrders)
	r.GET("/admin", middleware.RootAuth(), GetHostingStoreAdmin)
	r.POST("/pay", middleware.UserAuth(), middleware.SessionCookieOriginGuard(), RequestHostingPayment)
	token, _, err := service.IssueAccessToken(identity)
	require.NoError(t, err)
	base := map[string]string{"pid": "merchant", "trade_no": "provider-transaction", "out_trade_no": order.ID, "type": "wxpay", "name": "Hosting 30 days", "money": "40.00", "trade_status": "TRADE_SUCCESS"}
	call := func(method string, params map[string]string, duplicate bool) *httptest.ResponseRecorder {
		signed, err := epay.GenerateParamsWithError(params, operation_setting.EpayKey)
		require.NoError(t, err)
		values := url.Values{}
		for k, v := range signed {
			values.Set(k, v)
		}
		if duplicate {
			values.Add("money", "0.01")
		}
		target := "http://portal.test/notify"
		var body *strings.Reader
		if method == "GET" {
			target += "?" + values.Encode()
			body = strings.NewReader("")
		} else {
			body = strings.NewReader(values.Encode())
		}
		req := httptest.NewRequest(method, target, body)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	for _, tc := range []struct{ key, value string }{{"money", "0.01"}, {"money", "40.001"}, {"money", "4e1"}, {"money", "-40"}, {"pid", "foreign"}, {"type", "alipay"}, {"trade_status", "WAIT_BUYER_PAY"}} {
		t.Run(tc.key+tc.value, func(t *testing.T) {
			params := maps.Clone(base)
			params[tc.key] = tc.value
			require.Equal(t, 400, call("GET", params, false).Code)
		})
	}
	require.Equal(t, 400, call("POST", maps.Clone(base), true).Code)
	tampered, _ := epay.GenerateParamsWithError(maps.Clone(base), "wrong-key")
	v := url.Values{}
	for k, val := range tampered {
		v.Set(k, val)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "http://portal.test/notify?"+v.Encode(), nil))
	require.Equal(t, 400, w.Code)
	_, err = model.GetHostingGrant(user.Id, order.SeatID)
	require.Error(t, err)
	for _, method := range []string{"POST", "GET", "POST"} {
		response := call(method, maps.Clone(base), false)
		require.Equal(t, 200, response.Code, response.Body.String())
		require.Equal(t, "success", response.Body.String())
	}
	grant, err := model.GetHostingGrant(user.Id, order.SeatID)
	require.NoError(t, err)
	require.Equal(t, int64(30*86400), grant.EndsAt-grant.StartsAt)
	// 现有已绑定名额续费不需要空闲槽位；即使客户端伪造价格与天数，也只签发服务端快照。
	confirmPaymentComplianceForTest(t)
	oldServer, oldCallback := system_setting.ServerAddress, operation_setting.CustomCallbackAddress
	system_setting.ServerAddress = "https://portal.test"
	operation_setting.CustomCallbackAddress = ""
	t.Cleanup(func() {
		system_setting.ServerAddress = oldServer
		operation_setting.CustomCallbackAddress = oldCallback
	})
	raw, _ := common.Marshal(map[string]any{"request_id": uuid.NewString(), "plan_id": "monthly", "seat_id": grant.SeatID, "platform": "codex", "method": "wxpay", "price_cents": 1, "days": 99999})
	purchaseRequest := httptest.NewRequest("POST", "http://portal.test/pay", bytes.NewReader(raw))
	purchaseRequest.Header.Set("Authorization", "Bearer "+token)
	purchaseRequest.Header.Set("Origin", "http://portal.test")
	purchaseRequest.Header.Set("Content-Type", "application/json")
	purchaseResponse := httptest.NewRecorder()
	r.ServeHTTP(purchaseResponse, purchaseRequest)
	require.Equal(t, 200, purchaseResponse.Code, purchaseResponse.Body.String())
	var checkout struct {
		Success bool `json:"success"`
		Data    struct {
			URL    string            `json:"url"`
			Params map[string]string `json:"params"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(purchaseResponse.Body.Bytes(), &checkout))
	require.True(t, checkout.Success, purchaseResponse.Body.String())
	require.Equal(t, "40.00", checkout.Data.Params["money"])
	require.Equal(t, "https://portal.test/api/gpt-hosting/payment/notify", checkout.Data.Params["notify_url"])
	require.Equal(t, "https://portal.test/account/gpt", checkout.Data.Params["return_url"])
	// 有会话的订单查询仍忽略外部 user_id；普通用户不能读取管理视图或跨站发起付款。
	foreign := model.HostingOrder{ID: "other-user-order", UserID: user.Id + 1, RequestUser: user.Id + 1, RequestID: uuid.NewString(), Status: "paid"}
	require.NoError(t, model.DB.Create(&foreign).Error)
	for _, tc := range []struct {
		method, path, origin string
		code                 int
	}{{"GET", "/orders?user_id=999", "", 200}, {"GET", "/admin", "", 403}, {"POST", "/pay", "https://foreign.test", 403}} {
		req := httptest.NewRequest(tc.method, "http://portal.test"+tc.path, strings.NewReader(`{}`))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		if tc.origin != "" {
			req.Header.Set("Origin", tc.origin)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		require.Equal(t, tc.code, w.Code, w.Body.String())
		require.NotContains(t, w.Body.String(), foreign.ID)
		for _, private := range []string{"merchant", "provider_trade", "inventory_id", "request_id"} {
			require.NotContains(t, w.Body.String(), `"`+private+`"`)
		}
	}
}

// TestHostingPlanProofBoundary 验证套餐改价需要 root 及目标绑定的一次性证明，其他账号动作的证明不能替代。
func TestHostingPlanProofBoundary(t *testing.T) {
	user, identity := setupSecurityEnrollmentTest(t)
	t.Setenv("GPT_HOSTING_ENABLED", "true")
	require.NoError(t, model.DB.AutoMigrate(&model.HostingPlan{}))
	require.NoError(t, model.MigrateHostingStore(model.DB))
	body := `{"id":"monthly","days":30,"price_cents":4000,"enabled":true}`
	operation := service.VerificationOperation{Scope: service.VerificationScopeGPTHosting, Context: []byte(`{"action":"store-plan","binding_id":"monthly"}`)}
	binding, err := service.BindVerificationOperation(operation)
	require.NoError(t, err)
	proof, err := service.CompleteSecurityVerification(identity, binding, service.VerificationMethodPassword)
	require.NoError(t, err)
	request := func(role int, token string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("POST", "http://portal.test/plan", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Request.Header.Set("X-Security-Proof", token)
		c.Set("id", user.Id)
		c.Set("role", role)
		c.Set("session_id", identity.SessionID)
		c.Set("auth_version", identity.UserAuthVersion)
		c.Set("session_version", identity.SessionVersion)
		PutHostingStorePlan(c)
		return w
	}
	require.Equal(t, 403, request(common.RoleCommonUser, proof.ProofToken).Code)
	// 生产身份中 root 角色也要从数据库确认；模拟授权提升后保持同一会话。
	require.NoError(t, model.DB.Model(user).Update("role", common.RoleRootUser).Error)
	require.NotEqual(t, 200, request(common.RoleRootUser, "").Code)
	response := request(common.RoleRootUser, proof.ProofToken)
	require.Equal(t, 200, response.Code, response.Body.String())
	require.NotEqual(t, 200, request(common.RoleRootUser, proof.ProofToken).Code)
	for _, context := range []string{`{"action":"store-plan","binding_id":"arbitrary"}`, `{"action":"login","binding_id":"monthly"}`} {
		_, err := service.BindVerificationOperation(service.VerificationOperation{Scope: service.VerificationScopeGPTHosting, Context: []byte(context)})
		require.Error(t, err)
	}
}
