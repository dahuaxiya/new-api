package controller

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// TestRetailSupplyIsolation 验证旧 Key 不能借助分组、pin 或会话亲和绕过商业账本。
// 未设置配置保留旧路由；停售仍隔离供货；损坏配置须拒绝选路。
func TestRetailSupplyIsolation(t *testing.T) {
	i18n.Init()
	for _, scenario := range []struct {
		name, config         string
		pin, affinity, allow bool
	}{
		{"unset_legacy_unchanged", "", false, false, true},
		{"group_isolated", `{"enabled":true,"group":"retail","supplies":[{"channel_id":101,"kind":"reseller"}]}`, false, false, false},
		{"pin_isolated", `{"enabled":true,"group":"retail","supplies":[{"channel_id":101,"kind":"reseller"}]}`, true, false, false},
		{"affinity_isolated", `{"enabled":true,"group":"retail","supplies":[{"channel_id":101,"kind":"reseller"}]}`, false, true, false},
		{"disabled_isolated", `{"enabled":false,"supplies":[{"channel_id":101,"kind":"reseller"}]}`, false, false, false},
		{"malformed_fails_closed", `{"enabled":`, false, false, false},
		{"disabled_invalid_fails_closed", `{"enabled":false,"supplies":[{"channel_id":0,"kind":"reseller"}]}`, false, false, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			user, token := setupResponsesWSRequestTest(t)
			t.Setenv("GPT_HOSTING_ENABLED", "false")
			t.Setenv("RETAIL_RELAY_CONFIG", "")
			if scenario.config != "" {
				path := filepath.Join(t.TempDir(), "retail.json")
				require.NoError(t, os.WriteFile(path, []byte(scenario.config), 0600))
				t.Setenv("RETAIL_RELAY_CONFIG", path)
			}
			require.NoError(t, model.DB.AutoMigrate(&model.Channel{}, &model.Ability{}))
			channel := model.Channel{Id: 101, Name: "commercial", Type: 1, Status: 1, Key: "test-only", Group: "default", Models: "gpt-4o"}
			require.NoError(t, model.DB.Create(&channel).Error)
			require.NoError(t, model.DB.Create(&model.Ability{ChannelId: channel.Id, Model: "gpt-4o", Group: "default", Enabled: true}).Error)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
			c.Set("id", user.Id)
			c.Set("token_id", token.Id)
			common.SetContextKey(c, constant.ContextKeyUsingGroup, "default")
			common.SetContextKey(c, constant.ContextKeyUserGroup, "default")
			if scenario.pin {
				service.GetChannelConstraints(c).AddPin(dto.ChannelPin{ChannelId: channel.Id, Source: dto.PinSourceToken})
			}
			if scenario.affinity {
				setting := operation_setting.GetChannelAffinitySetting()
				previous := *setting
				t.Cleanup(func() { *setting = previous })
				*setting = operation_setting.ChannelAffinitySetting{Enabled: true, SessionMode: "prefer", DefaultTTLSeconds: 60, Rules: []operation_setting.ChannelAffinityRule{{Name: t.Name(), ModelRegex: []string{"^gpt-4o$"}, IncludeRuleName: true, KeySources: []operation_setting.ChannelAffinityKeySource{{Type: "request_header", Key: "X-Test-Session"}}}}}
				c.Request.Header.Set("X-Test-Session", t.Name())
				_, _ = service.GetPreferredChannelByAffinity(c, "gpt-4o", "default")
				service.RecordChannelAffinity(c, channel.Id)
				preferred, found := service.GetPreferredChannelByAffinity(c, "gpt-4o", "default")
				require.True(t, found, "先证明亲和缓存确实命中供货，避免空缓存导致假通过")
				require.Equal(t, channel.Id, preferred)
			}
			selected, _, err := service.SelectChannelForRequest(c, "gpt-4o", &service.RetryParam{Ctx: c, TokenGroup: "default", ModelName: "gpt-4o"})
			if scenario.allow {
				require.Nil(t, err)
				require.Equal(t, channel.Id, selected.Id)
			} else {
				require.NotNil(t, err)
				require.Nil(t, selected)
			}
		})
	}
}

// TestRetailHTTPRelay 从客户 Key 走真实鉴权、选路、HTTP 上游和结算，覆盖三种同步协议及缺失用量。
func TestRetailHTTPRelay(t *testing.T) {
	t.Setenv("SITE_MODE", "retail")
	oldTimeout := constant.StreamingTimeout
	oldRetry := common.RetryTimes
	constant.StreamingTimeout = 10
	common.RetryTimes = 3
	t.Cleanup(func() { constant.StreamingTimeout, common.RetryTimes = oldTimeout, oldRetry })
	for _, scenario := range []struct {
		name, path, body, response string
		format                     types.RelayFormat
		channelType                int
		charged                    bool
	}{
		{"chat", "/v1/chat/completions", `{"model":"gpt-4o","messages":[{"role":"user","content":"hello"}],"max_tokens":20}`, `{"id":"chat-1","object":"chat.completion","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`, types.RelayFormatOpenAI, 1, true},
		{"responses", "/v1/responses", `{"model":"gpt-4o","input":"hello","max_output_tokens":20}`, `{"id":"resp-1","object":"response","status":"completed","model":"gpt-4o","output":[{"id":"msg-1","type":"message","role":"assistant","content":[{"type":"output_text","text":"OK"}]}],"usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12}}`, types.RelayFormatOpenAIResponses, 1, true},
		{"claude", "/v1/messages", `{"model":"gpt-4o","messages":[{"role":"user","content":"hello"}],"max_tokens":20}`, `{"id":"msg-1","type":"message","role":"assistant","model":"gpt-4o","content":[{"type":"text","text":"OK"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":2}}`, types.RelayFormatClaude, 14, true},
		{"missing_usage", "/v1/chat/completions", `{"model":"gpt-4o","messages":[{"role":"user","content":"hello"}],"max_tokens":20}`, `{"id":"chat-1","object":"chat.completion","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}]}`, types.RelayFormatOpenAI, 1, false},
		{"retry_429", "/v1/chat/completions", `{"model":"gpt-4o","messages":[{"role":"user","content":"hello"}],"max_tokens":20}`, `{"id":"chat-1","object":"chat.completion","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`, types.RelayFormatOpenAI, 1, true},
		{"retry_limit", "/v1/chat/completions", `{"model":"gpt-4o","messages":[{"role":"user","content":"hello"}],"max_tokens":20}`, `{}`, types.RelayFormatOpenAI, 1, false},
		{"no_retry_500", "/v1/chat/completions", `{"model":"gpt-4o","messages":[{"role":"user","content":"hello"}],"max_tokens":20}`, `{}`, types.RelayFormatOpenAI, 1, false},
		{"responses_missing_usage", "/v1/responses", `{"model":"gpt-4o","input":"hello"}`, `{"id":"resp-1","object":"response","status":"completed","model":"gpt-4o","output":[{"type":"web_search_call","id":"search-1","status":"completed"}]}`, types.RelayFormatOpenAIResponses, 1, false},
		{"chat_stream", "/v1/chat/completions", `{"model":"gpt-4o","messages":[{"role":"user","content":"hello"}],"max_tokens":20,"stream":true}`, "data: {\"id\":\"chat-1\",\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"OK\"},\"finish_reason\":\"stop\"}]}\n\ndata: {\"id\":\"chat-1\",\"model\":\"gpt-4o\",\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2,\"total_tokens\":12}}\n\ndata: [DONE]\n\n", types.RelayFormatOpenAI, 1, true},
		{"chat_stream_missing_usage", "/v1/chat/completions", `{"model":"gpt-4o","messages":[{"role":"user","content":"hello"}],"max_tokens":20,"stream":true}`, "data: {\"id\":\"chat-1\",\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"OK\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", types.RelayFormatOpenAI, 1, false},
		{"responses_stream", "/v1/responses", `{"model":"gpt-4o","input":"hello","stream":true}`, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"OK\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"model\":\"gpt-4o\",\"status\":\"completed\",\"usage\":{\"input_tokens\":10,\"output_tokens\":2,\"total_tokens\":12}}}\n\n", types.RelayFormatOpenAIResponses, 1, true},
		{"responses_stream_interrupted", "/v1/responses", `{"model":"gpt-4o","input":"hello","stream":true}`, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"OK\"}\n\n", types.RelayFormatOpenAIResponses, 1, false},
		{"responses_stream_estimated", "/v1/responses", `{"model":"gpt-4o","input":"hello","stream":true}`, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"OK\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"model\":\"gpt-4o\",\"status\":\"completed\",\"usage\":{\"input_tokens\":10,\"output_tokens\":0,\"total_tokens\":10}}}\n\n", types.RelayFormatOpenAIResponses, 1, false},
		{"claude_stream", "/v1/messages", `{"model":"gpt-4o","messages":[{"role":"user","content":"hello"}],"max_tokens":20,"stream":true}`, "data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg-1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"gpt-4o\",\"content\":[],\"usage\":{\"input_tokens\":10,\"output_tokens\":0}}}\n\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"OK\"}}\n\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}\n\ndata: {\"type\":\"message_stop\"}\n\n", types.RelayFormatClaude, 14, true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			i18n.Init()
			user, token := setupResponsesWSRequestTest(t)
			oldBatch, oldLogs, oldCount := common.BatchUpdateEnabled, common.LogConsumeEnabled, constant.CountToken
			common.BatchUpdateEnabled, common.LogConsumeEnabled, constant.CountToken = false, true, false
			t.Cleanup(func() {
				common.BatchUpdateEnabled, common.LogConsumeEnabled, constant.CountToken = oldBatch, oldLogs, oldCount
			})
			oldModels := ratio_setting.ModelRatio2JSONString()
			t.Cleanup(func() { _ = ratio_setting.UpdateModelRatioByJSONString(oldModels) })
			require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"gpt-4o":1}`))
			oldGroups := ratio_setting.GroupRatio2JSONString()
			t.Cleanup(func() { _ = ratio_setting.UpdateGroupRatioByJSONString(oldGroups) })
			require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1,"retail":1}`))
			require.NoError(t, model.DB.AutoMigrate(&model.Channel{}, &model.Ability{}, &model.GPTHostingBinding{}, &model.RetailCharge{}, &model.RetailAttempt{}))
			require.NoError(t, model.LOG_DB.AutoMigrate(&model.Log{}))
			require.NoError(t, model.DB.Model(user).Update("quota", 100000).Error)
			require.NoError(t, model.DB.Model(token).Updates(map[string]any{"remain_quota": 100000, "service_mode": "retail"}).Error)
			wantHits, wantStatus, wantChargeState := int32(1), 200, "settled"
			switch scenario.name {
			case "retry_429":
				wantHits = 2
			case "retry_limit":
				wantHits, wantStatus, wantChargeState = 2, 429, "refunded"
			case "no_retry_500":
				wantStatus, wantChargeState = 500, "refunded"
			}
			var hits atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hit := hits.Add(1)
				w.Header().Set("Content-Type", "application/json")
				if scenario.name == "retry_limit" || scenario.name == "retry_429" && hit == 1 {
					w.WriteHeader(429)
					_, _ = fmt.Fprint(w, `{"error":{"message":"rate limit","type":"rate_limit_error"}}`)
					return
				}
				if scenario.name == "no_retry_500" {
					w.WriteHeader(500)
					_, _ = fmt.Fprint(w, `{"error":{"message":"upstream failed","type":"server_error"}}`)
					return
				}
				if strings.Contains(scenario.body, `"stream":true`) {
					w.Header().Set("Content-Type", "text/event-stream")
				}
				_, _ = fmt.Fprint(w, scenario.response)
			}))
			t.Cleanup(upstream.Close)
			channel := model.Channel{Name: "retail-upstream", Key: "test-upstream-key", Status: 1, Type: scenario.channelType, Group: "retail", Models: "gpt-4o", BaseURL: &upstream.URL, Priority: common.GetPointer(int64(10))}
			require.NoError(t, model.DB.Create(&channel).Error)
			require.NoError(t, model.DB.Create(&model.Ability{ChannelId: channel.Id, Model: "gpt-4o", Group: "retail", Enabled: true, Priority: channel.Priority}).Error)
			backup := channel
			backup.Id, backup.Priority = 0, common.GetPointer(int64(0))
			require.NoError(t, model.DB.Create(&backup).Error)
			require.NoError(t, model.DB.Create(&model.Ability{ChannelId: backup.Id, Model: "gpt-4o", Group: "retail", Enabled: true, Priority: backup.Priority}).Error)
			// 未登记渠道即使优先级更高也不能命中，覆盖普通选路和重试的白名单约束。
			private := channel
			private.Id, private.Priority, private.BaseURL = 0, common.GetPointer(int64(100)), common.GetPointer("http://127.0.0.1:1")
			require.NoError(t, model.DB.Create(&private).Error)
			require.NoError(t, model.DB.Create(&model.Ability{ChannelId: private.Id, Model: "gpt-4o", Group: "retail", Enabled: true, Priority: private.Priority}).Error)
			cfg := filepath.Join(t.TempDir(), "retail.json")
			require.NoError(t, os.WriteFile(cfg, []byte(fmt.Sprintf(`{"enabled":true,"group":"retail","supplies":[{"channel_id":%d,"kind":"reseller"},{"channel_id":%d,"kind":"subscription"}]}`, channel.Id, backup.Id)), 0600))
			t.Setenv("RETAIL_RELAY_CONFIG", cfg)
			engine := gin.New()
			engine.Use(middleware.BodyStorageCleanup())
			engine.POST(scenario.path, middleware.TokenAuth(), middleware.Distribute(), func(c *gin.Context) {
				c.Set(common.RequestIdKey, "retail-http-"+scenario.name)
				common.SetContextKey(c, constant.ContextKeyRequestStartTime, time.Now())
				Relay(c, scenario.format)
			})
			req := httptest.NewRequest("POST", scenario.path, strings.NewReader(scenario.body))
			req.Header.Set("Authorization", "Bearer sk-"+token.Key)
			req.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, req)
			require.Equal(t, wantStatus, recorder.Code, recorder.Body.String())
			require.Equal(t, wantHits, hits.Load())
			var charge model.RetailCharge
			require.NoError(t, model.DB.First(&charge, "id = ?", "retail-http-"+scenario.name).Error)
			require.Equal(t, wantChargeState, charge.Status)
			if scenario.charged {
				require.Equal(t, 18, charge.Actual, "10 输入和 2 输出按固定测试售价只结算一次")
			} else {
				require.Zero(t, charge.Actual)
			}
			require.NoError(t, model.DB.First(user, user.Id).Error)
			require.Equal(t, 100000-charge.Actual, user.Quota)
			var attempts []model.RetailAttempt
			require.NoError(t, model.DB.Where("request_id = ?", charge.ID).Order("id").Find(&attempts).Error)
			require.Len(t, attempts, int(wantHits))
			attempt := attempts[len(attempts)-1]
			if wantHits == 2 {
				require.Equal(t, "failed", attempts[0].State)
				require.Equal(t, backup.Id, attempt.ChannelID, "限流只重试备用供货渠道")
			}
			if strings.HasSuffix(scenario.name, "interrupted") {
				require.Equal(t, "interrupted", attempt.State)
			} else if wantStatus != 200 {
				require.Equal(t, "failed", attempt.State)
			} else {
				require.Equal(t, "completed", attempt.State)
			}
			require.Equal(t, "unknown", attempt.CostState)
			// 状态化上下文必须在接触上游前拒绝，不增加尝试或账单。
			req = httptest.NewRequest("POST", scenario.path, strings.NewReader(strings.TrimSuffix(scenario.body, "}")+`,"previous_response_id":"other-account"}`))
			req.Header.Set("Authorization", "Bearer sk-"+token.Key)
			req.Header.Set("Content-Type", "application/json")
			recorder = httptest.NewRecorder()
			engine.ServeHTTP(recorder, req)
			require.Equal(t, 400, recorder.Code)
			require.Equal(t, wantHits, hits.Load())
		})
	}
}

// TestRetailAccountingBoundary 验证真实会话权限、同秒分页和人工退款的来源及金额约束。
func TestRetailAccountingBoundary(t *testing.T) {
	user, identity := setupSecurityEnrollmentTest(t)
	oldSecure, oldOrigins := common.SessionCookieSecure, common.SessionCookieTrustedURLs
	common.SessionCookieSecure, common.SessionCookieTrustedURLs = true, nil
	t.Cleanup(func() { common.SessionCookieSecure, common.SessionCookieTrustedURLs = oldSecure, oldOrigins })
	require.NoError(t, model.DB.AutoMigrate(&model.Token{}, &model.RetailCharge{}, &model.RetailAttempt{}, &model.RetailCost{}))
	accessToken, _, err := service.IssueAccessToken(identity)
	require.NoError(t, err)
	engine := gin.New()
	root := engine.Group("/api/retail/admin", middleware.RootAuth())
	root.GET("/accounting", RetailAccounting)
	root.POST("/charges/:id/refund", middleware.SessionCookieOriginGuard(), RefundRetailReservation)
	request := func(method, path, body, origin, bearer string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://portal.test"+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", origin)
		if bearer != "" {
			r.Header.Set("Authorization", "Bearer "+bearer)
		}
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, r)
		return w
	}
	path := "/api/retail/admin/accounting"
	require.Equal(t, 401, request("GET", path, "", "", "").Code)
	require.Equal(t, 403, request("GET", path, "", "", accessToken).Code)
	require.NoError(t, model.DB.Model(user).Update("role", common.RoleRootUser).Error)
	require.NoError(t, model.PublishUserAuthCache(user.Id))
	created := time.Now().Unix() - 100
	for index := range 105 {
		require.NoError(t, model.DB.Create(&model.RetailCharge{ID: fmt.Sprintf("page-%03d", index), UserID: user.Id, Status: "settled", CreatedAt: created}).Error)
	}
	var first, second struct {
		Success bool
		Data    struct {
			Charges []model.RetailCharge
			Next    struct {
				Before int64  `json:"charge_before"`
				ID     string `json:"charge_id"`
			}
		}
	}
	w := request("GET", path, "", "", accessToken)
	require.Equal(t, 200, w.Code, w.Body.String())
	require.NoError(t, common.Unmarshal(w.Body.Bytes(), &first))
	require.True(t, first.Success)
	require.Len(t, first.Data.Charges, 100)
	w = request("GET", fmt.Sprintf("%s?charge_before=%d&charge_id=%s", path, first.Data.Next.Before, first.Data.Next.ID), "", "", accessToken)
	require.NoError(t, common.Unmarshal(w.Body.Bytes(), &second))
	require.Len(t, second.Data.Charges, 5)
	require.Equal(t, "page-004", second.Data.Charges[0].ID)

	token := model.Token{UserId: user.Id, Key: "review-key", ServiceMode: "retail", RemainQuota: 1000, Status: 1}
	require.NoError(t, model.DB.Create(&token).Error)
	require.NoError(t, model.DB.Model(user).Update("quota", 1000).Error)
	require.NoError(t, model.ReserveRetailCharge("review-old", user.Id, token.Id, 100, "{}"))
	require.NoError(t, model.DB.Model(&model.RetailCharge{}).Where("id = ?", "review-old").Update("updated_at", time.Now().Unix()-90000).Error)
	refundPath := "/api/retail/admin/charges/review-old/refund"
	body := `{"expected_reserved":100,"reason":"已确认没有在途请求"}`
	require.Equal(t, 403, request("POST", refundPath, body, "https://foreign.test", accessToken).Code)
	w = request("POST", refundPath, strings.Replace(body, ":100", ":99", 1), "http://portal.test", accessToken)
	require.Contains(t, w.Body.String(), `"success":false`)
	w = request("POST", refundPath, body, "http://portal.test", accessToken)
	require.Contains(t, w.Body.String(), `"success":true`, w.Body.String())
	w = request("POST", refundPath, body, "http://portal.test", accessToken)
	require.Contains(t, w.Body.String(), `"success":true`)
	require.NoError(t, model.DB.First(user, user.Id).Error)
	require.Equal(t, 1000, user.Quota, "重复审核不能重复退回余额")
}

// TestRetailSupplyOverview 验证供货视图复用路由资格、停售不冒充故障，并且不返回敏感渠道字段。
func TestRetailSupplyOverview(t *testing.T) {
	user, identity := setupSecurityEnrollmentTest(t)
	require.NoError(t, model.DB.Model(user).Update("role", common.RoleRootUser).Error)
	require.NoError(t, model.DB.AutoMigrate(&model.Channel{}, &model.GPTHostingBinding{}))
	oldBatch := common.BatchUpdateEnabled
	common.BatchUpdateEnabled = false
	t.Cleanup(func() { common.BatchUpdateEnabled = oldBatch })
	groups := ratio_setting.GetGroupRatioCopy()
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1,"retail":1}`))
	t.Cleanup(func() { raw, _ := common.Marshal(groups); _ = ratio_setting.UpdateGroupRatioByJSONString(string(raw)) })
	for _, channel := range []model.Channel{
		{Id: 101, Name: "Owned", Type: 1, Status: 1, Group: "retail", Key: "secret-key", BaseURL: &[]string{"https://private-upstream.test"}[0]},
		{Id: 102, Name: "Disabled", Type: 14, Status: 2, Group: "retail", Key: "secret-key"},
		{Id: 103, Name: "Customer", Type: 1, Status: 1, Group: "retail,acct_private", Key: "secret-key"},
	} {
		require.NoError(t, model.DB.Create(&channel).Error)
	}
	path := filepath.Join(t.TempDir(), "retail.json")
	t.Setenv("RETAIL_RELAY_CONFIG", path)
	config := `{"enabled":true,"group":"retail","supplies":[{"channel_id":101,"kind":"subscription"},{"channel_id":102,"kind":"reseller"},{"channel_id":103,"kind":"subscription"},{"channel_id":104,"kind":"reseller"}]}`
	require.NoError(t, os.WriteFile(path, []byte(config), 0600))
	token, _, err := service.IssueAccessToken(identity)
	require.NoError(t, err)
	r := gin.New()
	r.GET("/supplies", middleware.RootAuth(), RetailSupplies)
	request := func(auth string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/supplies", nil)
		if auth != "" {
			req.Header.Set("Authorization", "Bearer "+auth)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	require.Equal(t, 401, request("").Code)
	require.NoError(t, model.DB.Model(user).Update("role", common.RoleCommonUser).Error)
	require.Equal(t, 403, request(token).Code)
	require.NoError(t, model.DB.Model(user).Update("role", common.RoleRootUser).Error)
	result := request(token)
	require.Equal(t, 200, result.Code)
	require.Contains(t, result.Body.String(), `"state":"ready"`)
	for _, status := range []string{"eligible", "disabled", "excluded", "missing"} {
		require.Contains(t, result.Body.String(), `"status":"`+status+`"`)
	}
	for _, private := range []string{"secret-key", "private-upstream", "acct_private", "base_url"} {
		require.NotContains(t, result.Body.String(), private)
	}
	require.NoError(t, os.WriteFile(path, []byte(strings.Replace(config, `"enabled":true`, `"enabled":false`, 1)), 0600))
	result = request(token)
	require.Contains(t, result.Body.String(), `"state":"disabled"`)
	require.Contains(t, result.Body.String(), `"status":"not_evaluated"`)
	require.NotContains(t, result.Body.String(), `"status":"eligible"`)
	require.NoError(t, os.WriteFile(path, []byte(config), 0600))
	common.BatchUpdateEnabled = true
	require.Contains(t, request(token).Body.String(), `"state":"unsupported_runtime"`)
	require.NoError(t, os.WriteFile(path, []byte(`{`), 0600))
	require.Contains(t, request(token).Body.String(), `"success":false`)
}
