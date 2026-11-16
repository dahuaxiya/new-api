package controller

import (
	"bytes"
	"context"
	"fmt"
	"io"
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
	"github.com/QuantumNous/new-api/pkg/retailprice"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
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
	for _, priced := range []bool{false, true} {
		for _, scenario := range []struct {
			name, path, body, response string
			format                     types.RelayFormat
			channelType                int
			charged                    bool
		}{
			{"image", "/v1/images/generations", `{"model":"gpt-4o","prompt":"a tree","n":1,"size":"1024x1024","quality":"medium","output_format":"png"}`, `{"data":[{"b64_json":"iVBORw0KGgo="}],"usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12,"input_tokens_details":{"text_tokens":10,"image_tokens":0}}}`, types.RelayFormatOpenAIImage, 1, true},
			{"image_missing_usage", "/v1/images/generations", `{"model":"gpt-4o","prompt":"a tree","n":1}`, `{"data":[{"b64_json":"iVBORw0KGgo="}]}`, types.RelayFormatOpenAIImage, 1, false},
			{"image_media_input", "/v1/images/generations", `{"model":"gpt-4o","prompt":"a tree","n":1}`, `{"data":[{"b64_json":"iVBORw0KGgo="}],"usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12,"input_tokens_details":{"text_tokens":9,"image_tokens":1}}}`, types.RelayFormatOpenAIImage, 1, false},
			{"image_no_payload", "/v1/images/generations", `{"model":"gpt-4o","prompt":"a tree","n":1}`, `{"data":[{"revised_prompt":"tree"}],"usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12,"input_tokens_details":{"text_tokens":10,"image_tokens":0}}}`, types.RelayFormatOpenAIImage, 1, false},
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
			t.Run(fmt.Sprintf("%s/guarded_%t", scenario.name, priced), func(t *testing.T) {
				if scenario.format == types.RelayFormatOpenAIImage && !priced {
					t.Skip("image calls require a published price contract")
				}
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
				require.NoError(t, model.DB.AutoMigrate(&model.Channel{}, &model.Ability{}, &model.GPTHostingBinding{}, &model.RetailCharge{}, &model.RetailAttempt{}, &model.RetailPriceState{}, &model.RetailPriceVersion{}, &model.RetailKeyPricePolicy{}, &model.RetailPriceBudget{}))
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
				if priced {
					if !scenario.charged && wantStatus == 200 {
						wantChargeState = "reserved"
					}
					if scenario.name == "retry_429" || scenario.name == "retry_limit" {
						wantHits, wantStatus, wantChargeState = 1, 429, "refunded"
					}
				}
				var hits atomic.Int32
				var publishDuringCall func() error
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					hit := hits.Add(1)
					// Publish after dispatch but before returning usage: settlement must
					// retain the accepted rate while the next request sees the increase.
					if hit == 1 && publishDuringCall != nil {
						if err := publishDuringCall(); err != nil {
							http.Error(w, "test price publication failed", 500)
							return
						}
					}
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
				if priced {
					raw, err := os.ReadFile("../pkg/retailprice/testdata/book.json")
					require.NoError(t, err)
					var book retailprice.Book
					require.NoError(t, common.Unmarshal(raw, &book))
					now := time.Now().Unix()
					book.Models[0].VerifiedAt, book.Models[0].ValidUntil = now, now+86400
					book.Rates[0].VerifiedAt, book.Rates[0].ValidUntil = now, now+3600
					if scenario.format == types.RelayFormatOpenAIImage {
						book.Models[0].Kind = "image"
					}
					book.Products[0].UserGroups = []string{user.Group}
					book.Products[0].Routes[0].ChannelID = channel.Id
					second := book.Products[0].Routes[0]
					second.ChannelID = backup.Id
					book.Products[0].Routes = append(book.Products[0].Routes, second)
					_, err = model.PublishRetailPriceBook(0, book, nil, user.Id)
					require.NoError(t, err)
					cap := retailprice.Prices{Input: "100", Output: "100", CacheRead: "100", CacheWrite: "100", CacheWrite1h: "100"}
					if scenario.name == "chat" {
						cap.Input = "4" // Accepted input price is 3; the raised price is 5 CNY / 1M.
						publishDuringCall = func() error {
							book.Rates[0].Multiplier = "0.4"
							_, err := model.PublishRetailPriceBook(1, retailprice.Book{}, book.Rates, user.Id)
							return err
						}
					}
					require.NoError(t, model.PutRetailKeyPolicy(user.Id, token.Id, 0, retailprice.KeyPolicy{ProductID: "standard", AcceptFloating: true, Caps: map[string]retailprice.Prices{"gpt-4o": cap}, MaxRequestCNY: "1", DailyBudgetCNY: "1"}))
				}
				engine := gin.New()
				engine.Use(middleware.BodyStorageCleanup())
				var acceptedRequests atomic.Int32
				engine.POST(scenario.path, middleware.TokenAuth(), middleware.Distribute(), func(c *gin.Context) {
					requestID := "retail-http-" + scenario.name
					if index := acceptedRequests.Add(1); index > 1 {
						requestID = fmt.Sprintf("%s-%d", requestID, index)
					}
					c.Set(common.RequestIdKey, requestID)
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
				if scenario.charged && wantStatus == 200 {
					expected := 18
					if priced {
						expected = 21
					}
					require.Equal(t, expected, charge.Actual, "10 输入和 2 输出按冻结测试售价只结算一次")
				} else {
					require.Zero(t, charge.Actual)
				}
				require.NoError(t, model.DB.First(user, user.Id).Error)
				if wantChargeState == "reserved" {
					require.Equal(t, 100000-charge.Reserved, user.Quota)
				} else {
					require.Equal(t, 100000-charge.Actual, user.Quota)
				}
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
				if scenario.name == "image" {
					for _, extra := range []string{`,"n":2`, `,"image":"https://example.com/input.png"`, `,"quality":"ultra"`, `,"stream":true`, `,"model":"other"`} {
						bad := httptest.NewRequest("POST", scenario.path, strings.NewReader(strings.TrimSuffix(scenario.body, "}")+extra+"}"))
						bad.Header.Set("Authorization", "Bearer sk-"+token.Key)
						bad.Header.Set("Content-Type", "application/json")
						w := httptest.NewRecorder()
						engine.ServeHTTP(w, bad)
						require.GreaterOrEqual(t, w.Code, 400, w.Body.String())
						require.Equal(t, wantHits, hits.Load(), "invalid image input must not reach upstream")
					}
				}
				// 状态化上下文必须在接触上游前拒绝，不增加尝试或账单。
				req = httptest.NewRequest("POST", scenario.path, strings.NewReader(strings.TrimSuffix(scenario.body, "}")+`,"previous_response_id":"other-account"}`))
				req.Header.Set("Authorization", "Bearer sk-"+token.Key)
				req.Header.Set("Content-Type", "application/json")
				recorder = httptest.NewRecorder()
				engine.ServeHTTP(recorder, req)
				require.Equal(t, 400, recorder.Code)
				require.Equal(t, wantHits, hits.Load())
				if priced && scenario.name == "chat" {
					// Exercise the public HTTP contract, including the selection stage
					// that runs before the relay handler reserves wallet money.
					for _, step := range []string{"cap", "accept_new_rate", "expired"} {
						if step == "accept_new_rate" {
							policy, revision, err := model.GetRetailKeyPolicy(user.Id, token.Id)
							require.NoError(t, err)
							cap := policy.Caps["gpt-4o"]
							cap.Input = "6"
							policy.Caps["gpt-4o"] = cap
							require.NoError(t, model.PutRetailKeyPolicy(user.Id, token.Id, revision, policy))
						}
						if step == "expired" {
							book, version, err := model.GetRetailPriceBook()
							require.NoError(t, err)
							book.Rates[0].VerifiedAt = time.Now().Add(-2 * time.Hour).Unix()
							book.Rates[0].ValidUntil = time.Now().Add(-time.Hour).Unix()
							_, err = model.PublishRetailPriceBook(version, retailprice.Book{}, book.Rates, user.Id)
							require.NoError(t, err)
						}
						req = httptest.NewRequest("POST", scenario.path, strings.NewReader(scenario.body))
						req.Header.Set("Authorization", "Bearer sk-"+token.Key)
						req.Header.Set("Content-Type", "application/json")
						recorder = httptest.NewRecorder()
						engine.ServeHTTP(recorder, req)
						wantQuota := 100000 - 21
						switch step {
						case "cap":
							require.Equal(t, 403, recorder.Code, recorder.Body.String())
							require.Contains(t, recorder.Body.String(), "retail_price_cap_exceeded")
							require.EqualValues(t, 1, hits.Load())
						case "accept_new_rate":
							require.Equal(t, 200, recorder.Code, recorder.Body.String())
							require.EqualValues(t, 2, hits.Load())
							wantQuota -= 35 // The second call uses 0.4 / (1 - 20%) instead of 0.24.
						case "expired":
							require.Equal(t, 403, recorder.Code, recorder.Body.String())
							require.Contains(t, recorder.Body.String(), "retail_price_expired")
							require.EqualValues(t, 2, hits.Load())
							wantQuota -= 35
						}
						require.NoError(t, model.DB.First(user, user.Id).Error)
						require.Equal(t, wantQuota, user.Quota, step)
					}
				}
			})
		}
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

// TestRetailPricingManagement uses real login sessions to exercise root access, ownership, CSRF and version conflicts.
func TestRetailPricingManagement(t *testing.T) {
	user, identity := setupSecurityEnrollmentTest(t)
	t.Setenv("SITE_MODE", "retail")
	oldGroups := ratio_setting.GroupRatio2JSONString()
	t.Cleanup(func() { _ = ratio_setting.UpdateGroupRatioByJSONString(oldGroups) })
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1,"retail":1}`))
	oldSecure, oldBatch := common.SessionCookieSecure, common.BatchUpdateEnabled
	common.SessionCookieSecure, common.BatchUpdateEnabled = true, false
	t.Cleanup(func() { common.SessionCookieSecure, common.BatchUpdateEnabled = oldSecure, oldBatch })
	require.NoError(t, model.DB.AutoMigrate(&model.Token{}, &model.Channel{}, &model.Ability{}, &model.GPTHostingBinding{}, &model.RetailPriceState{}, &model.RetailPriceVersion{}, &model.RetailKeyPricePolicy{}, &model.RetailPriceBudget{}))
	token := model.Token{UserId: user.Id, Key: strings.Repeat("k", 48), Name: "owner", ServiceMode: model.TokenServiceRetail, Status: common.TokenStatusEnabled, ExpiredTime: -1}
	require.NoError(t, model.DB.Create(&token).Error)
	channel := model.Channel{Id: 101, Name: "synthetic", Type: 1, Status: 1, Group: "retail", Models: "gpt-4o", Key: "test-only"}
	require.NoError(t, model.DB.Create(&channel).Error)
	require.NoError(t, model.DB.Create(&model.Ability{ChannelId: 101, Model: "gpt-4o", Group: "retail", Enabled: true}).Error)
	config := filepath.Join(t.TempDir(), "retail.json")
	require.NoError(t, os.WriteFile(config, []byte(`{"enabled":true,"group":"retail","supplies":[{"channel_id":101,"kind":"reseller"}]}`), 0600))
	t.Setenv("RETAIL_RELAY_CONFIG", config)
	raw, err := os.ReadFile("../pkg/retailprice/testdata/book.json")
	require.NoError(t, err)
	var book retailprice.Book
	require.NoError(t, common.Unmarshal(raw, &book))
	now := time.Now().Unix()
	book.Models[0].VerifiedAt, book.Models[0].ValidUntil = now, now+86400
	book.Rates[0].VerifiedAt, book.Rates[0].ValidUntil = now, now+3600
	access, _, err := service.IssueAccessToken(identity)
	require.NoError(t, err)
	engine := gin.New()
	admin := engine.Group("/api/retail/admin", middleware.RootAuth(), middleware.DisableCache())
	admin.GET("/pricing", GetRetailPricingAdmin)
	admin.GET("/pricing/public-rates", GetRetailPublicRates)
	admin.GET("/pricing/group-bindings", GetRetailGroupBindings)
	admin.PUT("/pricing/group-bindings", middleware.SessionCookieOriginGuard(), PutRetailGroupBindings)
	admin.POST("/pricing/group-bindings/sync", middleware.SessionCookieOriginGuard(), SyncRetailGroupBindings)
	admin.PUT("/pricing", middleware.SessionCookieOriginGuard(), PutRetailPricingAdmin)
	admin.POST("/pricing/rates", middleware.SessionCookieOriginGuard(), ImportRetailRates)
	admin.POST("/pricing/preview", middleware.SessionCookieOriginGuard(), PreviewRetailPricing)
	users := engine.Group("/api/retail", middleware.UserAuth(), middleware.DisableCache())
	studio := engine.Group("/api/retail/studio", middleware.UserAuth(), middleware.RetailStudioSession())
	studio.POST("/session", OpenRetailStudio)
	studio.POST("/chat/completions", middleware.RetailStudioRelay("/v1/chat/completions"), func(c *gin.Context) {
		c.JSON(200, gin.H{"token_id": c.GetInt("token_id"), "retail": c.GetBool("retail_request")})
	})
	users.GET("/catalog", GetRetailCatalog)
	users.GET("/keys/:id/pricing", GetRetailKeyPricing)
	users.PUT("/keys/:id/pricing", middleware.SessionCookieOriginGuard(), PutRetailKeyPricing)
	request := func(method, path, origin string, body any) *httptest.ResponseRecorder {
		t.Helper()
		payload, err := common.Marshal(body)
		require.NoError(t, err)
		r := httptest.NewRequest(method, "https://example.com"+path, bytes.NewReader(payload))
		r.Header.Set("Authorization", "Bearer "+access)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, r)
		return w
	}
	publish := gin.H{"expected_version": 0, "book": book}
	w := request("PUT", "/api/retail/admin/pricing", "https://example.com", publish)
	require.Equal(t, http.StatusForbidden, w.Code, "regular user cannot publish tariffs")
	w = request("GET", "/api/retail/admin/pricing/public-rates", "https://example.com", nil)
	require.Equal(t, http.StatusForbidden, w.Code, "public source preview is a root-only management feature")
	for _, action := range []struct{ method, path string }{{"GET", "/api/retail/admin/pricing/group-bindings"}, {"PUT", "/api/retail/admin/pricing/group-bindings"}, {"POST", "/api/retail/admin/pricing/group-bindings/sync"}} {
		w = request(action.method, action.path, "https://example.com", gin.H{})
		require.Equal(t, http.StatusForbidden, w.Code, "binding operations require root")
	}
	require.NoError(t, model.DB.Model(user).Update("role", common.RoleRootUser).Error)
	require.NoError(t, model.PublishUserAuthCache(user.Id))
	w = request("PUT", "/api/retail/admin/pricing", "https://attacker.example", publish)
	require.Equal(t, http.StatusForbidden, w.Code)
	w = request("PUT", "/api/retail/admin/pricing", "https://example.com", publish)
	require.Contains(t, w.Body.String(), `"success":true`, w.Body.String())
	w = request("PUT", "/api/retail/admin/pricing", "https://example.com", publish)
	require.Contains(t, w.Body.String(), `"success":false`, "stale editor cannot overwrite publication")
	book.Rates[0].Multiplier = "0.4"
	w = request("POST", "/api/retail/admin/pricing/rates", "https://example.com", gin.H{"expected_version": 1, "rates": book.Rates})
	require.Contains(t, w.Body.String(), `"success":true`, w.Body.String())
	stored, version, err := model.GetRetailPriceBook()
	require.NoError(t, err)
	require.Equal(t, int64(2), version)
	require.Equal(t, book.Models, stored.Models, "rate import cannot rewrite official catalog")
	// The public feed is independent of the price book, even when it uses the same group ID.
	oldTransport := retailPublicRatesClient.Transport
	t.Cleanup(func() { retailPublicRatesClient.Transport = oldTransport })
	retailPublicRatesClient.Transport = retailPublicRatesTestTransport(func(r *http.Request) (*http.Response, error) {
		require.Equal(t, retailPublicRatesURL, r.URL.String())
		require.Empty(t, r.Header.Get("Authorization"))
		require.Empty(t, r.Header.Get("Cookie"))
		body := `{"success":true,"server":"https://aixingxing.pro","updated_at":"2026-01-01T00:00:00Z","quota_per_unit":1,"groups":[{"group":"supplier/standard","name":"Public only","rate":0.99000001}]}`
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json; charset=utf-8"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	w = request("GET", "/api/retail/admin/pricing/public-rates?url=https://attacker.example", "https://example.com", nil)
	require.Contains(t, w.Body.String(), `"multiplier":"0.99000001"`)
	require.NotContains(t, w.Body.String(), "verified_at")
	require.NotContains(t, w.Body.String(), "quota_per_unit")
	afterRead, afterVersion, err := model.GetRetailPriceBook()
	require.NoError(t, err)
	require.Equal(t, version, afterVersion)
	require.Equal(t, stored, afterRead, "reading must not change rates, routes, wallet conversion or enabled state")
	keyPath := fmt.Sprintf("/api/retail/keys/%d/pricing", token.Id)
	w = request("GET", keyPath, "https://example.com", nil)
	require.Contains(t, w.Body.String(), `"success":true`)
	cap := retailprice.Prices{Input: "100", Output: "100", CacheRead: "100", CacheWrite: "100", CacheWrite1h: "100"}
	policy := retailprice.KeyPolicy{ProductID: "standard", AcceptFloating: true, Caps: map[string]retailprice.Prices{"gpt-4o": cap}, MaxRequestCNY: "10", DailyBudgetCNY: "10"}
	w = request("PUT", keyPath, "https://example.com", gin.H{"expected_revision": 0, "policy": policy})
	require.Contains(t, w.Body.String(), `"success":true`, w.Body.String())
	w = request("PUT", keyPath, "https://attacker.example", gin.H{"expected_revision": 1, "policy": policy})
	require.Equal(t, http.StatusForbidden, w.Code)
	require.NoError(t, model.DB.Model(&token).Update("user_id", user.Id+1).Error)
	for _, method := range []string{"GET", "PUT"} {
		w = request(method, keyPath, "https://example.com", gin.H{"expected_revision": 1, "policy": policy})
		require.Contains(t, w.Body.String(), `"success":false`, "even root cannot edit another user's key through the self API")
	}
	// A browser session creates one accounting identity, but never a bearer key.
	for _, origin := range []string{"", "https://attacker.example", "null"} {
		w = request("POST", "/api/retail/studio/session", origin, gin.H{})
		require.Equal(t, 403, w.Code)
	}
	w = request("POST", "/api/retail/studio/session", "https://example.com", gin.H{"user_id": user.Id + 1})
	require.Contains(t, w.Body.String(), `"success":true`)
	require.NotContains(t, w.Body.String(), `"key":`)
	studioToken, err := model.RetailStudioToken(user.Id, false)
	require.NoError(t, err)
	_, err = model.ValidateUserToken(studioToken.Key)
	require.Error(t, err, "accounting identity cannot authenticate as a bearer credential")
	w = request("POST", "/api/retail/studio/session", "https://example.com", gin.H{})
	require.Contains(t, w.Body.String(), fmt.Sprintf(`"token_id":%d`, studioToken.Id))
	_, err = model.RetailStudioToken(user.Id+1, false)
	require.Error(t, err, "client-supplied owner cannot create another account's identity")
	w = request("POST", "/api/retail/studio/chat/completions", "https://example.com", gin.H{})
	require.Equal(t, 200, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), `"retail":true`)
	require.NoError(t, model.DB.Model(studioToken).Update("status", common.TokenStatusDisabled).Error)
	w = request("POST", "/api/retail/studio/session", "https://example.com", gin.H{})
	require.Contains(t, w.Body.String(), fmt.Sprintf(`"token_id":%d`, studioToken.Id), "setup must not replace disabled identities")
	w = request("POST", "/api/retail/studio/chat/completions", "https://example.com", gin.H{})
	require.Equal(t, 403, w.Code)
	stored.Enabled = false
	_, err = model.PublishRetailPriceBook(2, stored, nil, user.Id)
	require.NoError(t, err)
	w = request("GET", "/api/retail/catalog", "https://example.com", nil)
	require.Contains(t, w.Body.String(), `"enabled":false`)
	require.Contains(t, w.Body.String(), `"input":"5"`)
	for _, private := range []string{"channel_id", "multiplier", "receipt_ratio", "supplier/standard", "margin_bps"} {
		require.NotContains(t, w.Body.String(), private)
	}
	// Persist via the real management route, ignoring any browser-supplied price.
	require.NoError(t, model.DB.Model(&channel).Update("base_url", "https://aixingxing.pro").Error)
	retailPublicRatesClient.Transport = retailPublicRatesTestTransport(func(r *http.Request) (*http.Response, error) {
		require.Empty(t, r.Header.Get("Authorization"))
		body := fmt.Sprintf(`{"success":true,"server":"https://aixingxing.pro","updated_at":%q,"groups":[{"group":"Pro","name":"Pro","rate":0.24}]}`, time.Now().UTC().Format(time.RFC3339))
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	bindingInput := gin.H{"expected_version": 3, "enabled": true, "sale_policy": gin.H{"receipt_ratio": "1", "margin_bps": 2000, "fee_bps": 0, "buffer_bps": 0}, "bindings": []gin.H{{"local_group": "default", "channel_id": 101, "upstream_group": "Pro", "multiplier": "0.00001"}}}
	w = request("PUT", "/api/retail/admin/pricing/group-bindings", "https://attacker.example", bindingInput)
	require.Equal(t, 403, w.Code)
	w = request("PUT", "/api/retail/admin/pricing/group-bindings", "https://example.com", bindingInput)
	require.Contains(t, w.Body.String(), `"success":true`, w.Body.String())
	w = request("GET", "/api/retail/admin/pricing/group-bindings", "https://example.com", nil)
	require.Contains(t, w.Body.String(), `"local_group":"default"`)
	require.Contains(t, w.Body.String(), `"multiplier":"0.24"`)
	require.Contains(t, w.Body.String(), `"sale_policy":{"receipt_ratio":"1","margin_bps":2000,"fee_bps":0,"buffer_bps":0}`)
	require.NotContains(t, w.Body.String(), "test-only")
	bound, _, err := model.GetRetailPriceBook()
	require.NoError(t, err)
	require.False(t, bound.Enabled, "binding must not reopen disabled sales")
	// Exercise the actual quote path, then reject a changed destination before any I/O.
	bound.Enabled = true
	_, err = model.PublishRetailPriceBook(4, bound, nil, user.Id)
	require.NoError(t, err)
	require.NoError(t, model.DB.Model(&token).Update("user_id", user.Id).Error)
	quote, err := model.RetailQuote(user.Id, token.Id, "gpt-4o", 20, []int{101})
	require.NoError(t, err)
	require.Equal(t, "3", quote.Sale[0].Prices.Input, "bound 0.24 cost must replace the manual 0.4 cost")
	book.MarginBPS = 0 // An old catalog draft cannot preview a different margin from publication.
	w = request("POST", "/api/retail/admin/pricing/preview", "https://example.com", gin.H{"book": book})
	require.Contains(t, w.Body.String(), `"input":"3"`, "preview must use saved bindings even when the browser has an older manual draft")
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	for _, endpoint := range []string{"https://another-supplier.example/v1/chat/completions", "http://aixingxing.pro/v1/chat/completions", "https://aixingxing.pro:8443/v1/chat/completions"} {
		r, err := http.NewRequest("POST", endpoint, strings.NewReader(`{"model":"gpt-4o"}`))
		require.NoError(t, err)
		info := &relaycommon.RelayInfo{RetailQuote: quote, ChannelMeta: &relaycommon.ChannelMeta{ChannelId: 101}}
		require.ErrorIs(t, service.GuardRetailUpstream(c, r, info), retailprice.ErrAccess)
	}
	retailPublicRatesClient.Transport = retailPublicRatesTestTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 403, Header: http.Header{"Content-Type": {"text/html"}}, Body: io.NopCloser(strings.NewReader("private upstream error")), Request: r}, nil
	})
	w = request("POST", "/api/retail/admin/pricing/group-bindings/sync", "https://example.com", gin.H{"expected_version": 5})
	require.Contains(t, w.Body.String(), `"success":false`)
	require.NotContains(t, w.Body.String(), "private upstream error")
	bound, _, err = model.GetRetailPriceBook()
	require.NoError(t, err)
	require.NotEmpty(t, bound.PublicRateSync.LastError)
	require.NoError(t, model.DB.Model(user).Update("status", common.UserStatusDisabled).Error)
	require.NoError(t, model.PublishUserAuthCache(user.Id))
	w = request("GET", "/api/retail/admin/pricing", "https://example.com", nil)
	require.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())
}

type retailPublicRatesTestTransport func(*http.Request) (*http.Response, error)

// RoundTrip replaces only the HTTP boundary; authentication, validation and publication remain real.
func (f retailPublicRatesTestTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

// TestRetailPublicRates rejects malformed snapshots as a whole, preserving exact decimals and source timestamps.
func TestRetailPublicRates(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	valid := `{"success":true,"server":"https://aixingxing.pro","updated_at":"2026-01-01T00:00:00Z","groups":[{"group":"生图","name":"生图","rate":0.05000001}]}`
	rows, updated, ok := parseRetailPublicRates([]byte(valid), now)
	require.True(t, ok)
	require.Equal(t, "0.05000001", rows[0].Multiplier)
	require.Less(t, updated, now.Unix(), "reading an old snapshot must not relabel it as newly updated")
	for _, scenario := range []struct{ name, body string }{
		{"failed", strings.Replace(valid, `"success":true`, `"success":false`, 1)},
		{"wrong_server", strings.Replace(valid, `https://aixingxing.pro`, `https://other.example`, 1)},
		{"missing_time", strings.Replace(valid, `2026-01-01T00:00:00Z`, ``, 1)},
		{"future_time", strings.Replace(valid, `2026-01-01T00:00:00Z`, `2027-01-01T00:00:00Z`, 1)},
		{"empty_group", strings.Replace(valid, `"group":"生图"`, `"group":""`, 1)},
		{"duplicate_group", strings.Replace(valid, `}]}`, `},{"group":"生图","name":"alias","rate":0.1}]}`, 1)},
		{"no_groups", `{"success":true,"server":"https://aixingxing.pro","updated_at":"2026-01-01T00:00:00Z","groups":[]}`},
		{"trailing_json", valid + `{}`},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			rows, _, ok := parseRetailPublicRates([]byte(scenario.body), now)
			require.False(t, ok)
			require.Nil(t, rows)
		})
	}
	for _, rate := range []string{"0", "-0.1", "100.00000001", "null", `"0.1"`, "1e99", "0.123456789", "{}"} {
		t.Run("invalid_rate_"+rate, func(t *testing.T) {
			rows, _, ok := parseRetailPublicRates([]byte(strings.Replace(valid, "0.05000001", rate, 1)), now)
			require.False(t, ok)
			require.Nil(t, rows)
		})
	}
	oldTransport := retailPublicRatesClient.Transport
	t.Cleanup(func() { retailPublicRatesClient.Transport = oldTransport })
	for _, scenario := range []struct {
		name, contentType, body string
		status                  int
		err                     error
	}{
		{"redirect", "application/json", valid, 302, nil},
		{"upstream_403", "application/json", "private diagnostics", 403, nil},
		{"html", "text/html", "<script>alert(1)</script>", 200, nil},
		{"oversize", "application/json", strings.Repeat(" ", 256<<10) + valid, 200, nil},
		{"bad_json", "application/json", "broken", 200, nil},
		{"cancelled", "", "", 0, context.Canceled},
		{"timeout", "", "", 0, context.DeadlineExceeded},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			calls := 0
			retailPublicRatesClient.Transport = retailPublicRatesTestTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if scenario.err != nil {
					return nil, scenario.err
				}
				return &http.Response{StatusCode: scenario.status, Header: http.Header{"Content-Type": {scenario.contentType}, "Location": {"https://attacker.example"}}, Body: io.NopCloser(strings.NewReader(scenario.body)), Request: r}, nil
			})
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest("GET", "/api/retail/admin/pricing/public-rates", nil)
			GetRetailPublicRates(c)
			require.Contains(t, w.Body.String(), `"success":false`)
			require.NotContains(t, w.Body.String(), `"data":`)
			require.NotContains(t, w.Body.String(), "private diagnostics")
			require.Equal(t, 1, calls, "no redirect or automatic retry")
		})
	}
}
