package controller

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"mime/multipart"
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
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
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
				var replayBody atomic.Value
				var publishDuringCall func() error
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					hit := hits.Add(1)
					if hit >= 2 && (scenario.name == "responses" || scenario.name == "responses_stream") {
						body, readErr := io.ReadAll(r.Body)
						if readErr != nil {
							http.Error(w, "test upstream could not read replay", 500)
							return
						}
						replayBody.Store(string(body))
					}
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
				if scenario.name == "responses" || scenario.name == "responses_stream" {
					require.Contains(t, recorder.Body.String(), "previous_response_not_found")
					// 只给服务端引用时无法核对归属，即使请求沿用同一 Key 也不能从共享账号取回历史。
					for _, extra := range []string{`"conversation":{"id":"conv_other"}`, `"input":[{"type":"item_reference","id":"item_other"}]`} {
						ref := httptest.NewRequest("POST", scenario.path, strings.NewReader(`{"model":"gpt-4o",`+extra+"}"))
						ref.Header = req.Header.Clone()
						w := httptest.NewRecorder()
						engine.ServeHTTP(w, ref)
						require.Equal(t, 400, w.Code, w.Body.String())
						require.Equal(t, wantHits, hits.Load())
					}

					// 第二轮同时带客户端保存的历史和含同名属性的函数定义；模拟上游必须收到原值。
					kind := "reasoning"
					if scenario.name == "responses_stream" {
						kind = "compaction"
					}
					replay := fmt.Sprintf(`{"model":"gpt-4o","max_output_tokens":20,"stream":%t,
						"input":[
							{"type":%q,"encrypted_content":"client-replay","summary":[]},
							{"type":"custom_tool_call","id":"ctc_1","namespace":"functions","call_id":"call_1","name":"ticket_tool","input":"ticket","status":"completed"},
							{"type":"custom_tool_call_output","call_id":"call_1","output":[{"type":"input_text","text":%q}]},
							{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"open_page","url":"https://example.com/"}},
							{"role":"user","content":[{"type":"input_text","text":"Repeat the ticket from the tool result."},{"type":"input_image","detail":"low","image_url":"data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=="}]}],
						"tools":[{"type":"namespace","name":"functions","tools":[{"type":"custom","name":"ticket_tool"}]},{"type":"function","name":"lookup","parameters":{"type":"object","properties":{"conversation":{"type":"string"}}}}]}`, scenario.name == "responses_stream", kind, uuid.NewString())
					req = httptest.NewRequest("POST", scenario.path, strings.NewReader(replay))
					req.Header.Set("Authorization", "Bearer sk-"+token.Key)
					req.Header.Set("Content-Type", "application/json")
					w := httptest.NewRecorder()
					engine.ServeHTTP(w, req)
					require.Equal(t, http.StatusOK, w.Code, w.Body.String())
					require.EqualValues(t, 2, hits.Load())
					for _, path := range []string{"input", "tools"} {
						require.JSONEq(t, gjson.Get(replay, path).Raw, gjson.Get(replayBody.Load().(string), path).Raw, path)
					}
					var follow model.RetailCharge
					require.NoError(t, model.DB.First(&follow, "id = ?", "retail-http-"+scenario.name+"-2").Error)
					require.Equal(t, "settled", follow.Status)
					require.Equal(t, charge.Actual, follow.Actual)
					if priced {
						// 同一 Key、同一获准渠道和凭据允许增量续聊，检查真实出站的引用及结算。
						refBody := fmt.Sprintf(`{"model":"gpt-4o","previous_response_id":"resp-1","input":"next","max_output_tokens":20,"stream":%t}`, scenario.name == "responses_stream")
						refReq := httptest.NewRequest("POST", scenario.path, strings.NewReader(refBody))
						refReq.Header = req.Header.Clone()
						w = httptest.NewRecorder()
						engine.ServeHTTP(w, refReq)
						require.Equal(t, 200, w.Code, w.Body.String())
						require.EqualValues(t, 3, hits.Load())
						require.Equal(t, "resp-1", gjson.Get(replayBody.Load().(string), "previous_response_id").String())

						// 历史搜索只作为上下文。声明新搜索、未完成的动作及无报价媒体必须在触达上游前拒绝。
						for _, rejected := range []string{
							strings.Replace(replay, `"type":"custom","name":"ticket_tool"`, `"type":"web_search"`, 1),
							strings.Replace(replay, `"ws_1","status":"completed"`, `"ws_1","status":"in_progress"`, 1),
							strings.Replace(replay, `"type":"open_page"`, `"type":"unknown"`, 1),
							strings.Replace(replay, `"type":"input_text","text"`, `"type":"input_audio","audio"`, 1),
						} {
							bad := httptest.NewRequest(http.MethodPost, scenario.path, strings.NewReader(rejected))
							bad.Header = req.Header.Clone()
							denied := httptest.NewRecorder()
							engine.ServeHTTP(denied, bad)
							require.Equal(t, http.StatusForbidden, denied.Code, denied.Body.String())
							require.Contains(t, denied.Body.String(), "retail_price_configuration_invalid")
							require.EqualValues(t, 3, hits.Load())
						}

						// 另一个 Key 即使属于同一用户名下，也不能查询本 Key 在共享账号里的历史。
						other := model.Token{UserId: user.Id, Key: common.GetUUID(), Status: common.TokenStatusEnabled, ExpiredTime: -1, UnlimitedQuota: true, ServiceMode: "retail"}
						require.NoError(t, model.DB.Create(&other).Error)
						foreign := httptest.NewRequest("POST", scenario.path, strings.NewReader(refBody))
						foreign.Header = refReq.Header.Clone()
						foreign.Header.Set("Authorization", "Bearer sk-"+other.Key)
						w = httptest.NewRecorder()
						engine.ServeHTTP(w, foreign)
						require.Equal(t, 400, w.Code, w.Body.String())
						require.Contains(t, w.Body.String(), "previous_response_not_found")
						require.EqualValues(t, 3, hits.Load())
						// 渠道号不变但供货凭据已轮换，旧回复也不能发给更换后的账号。
						require.NoError(t, model.DB.Model(&channel).Update("key", "rotated-test-key").Error)
						rotated := httptest.NewRequest("POST", scenario.path, strings.NewReader(refBody))
						rotated.Header = refReq.Header.Clone()
						w = httptest.NewRecorder()
						engine.ServeHTTP(w, rotated)
						require.Equal(t, 400, w.Code, w.Body.String())
						require.Contains(t, w.Body.String(), "previous_response_not_found")
						require.EqualValues(t, 3, hits.Load())
					}
				}
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
	for _, private := range []string{"secret-key", "private-upstream", "acct_private"} {
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
	admin.PUT("/pricing/site-rates", middleware.SessionCookieOriginGuard(), PutRetailSiteRates)
	admin.PUT("/pricing/group-bindings", middleware.SessionCookieOriginGuard(), PutRetailGroupBindings)
	admin.POST("/pricing/group-bindings/sync", middleware.SessionCookieOriginGuard(), SyncRetailGroupBindings)
	admin.PUT("/pricing", middleware.SessionCookieOriginGuard(), PutRetailPricingAdmin)
	admin.POST("/pricing/rates", middleware.SessionCookieOriginGuard(), ImportRetailRates)
	admin.POST("/pricing/preview", middleware.SessionCookieOriginGuard(), PreviewRetailPricing)
	admin.POST("/pricing/official-draft", middleware.SessionCookieOriginGuard(), GenerateRetailOfficialDraft)
	users := engine.Group("/api/retail", middleware.UserAuth(), middleware.DisableCache())
	studio := engine.Group("/api/retail/studio", middleware.UserAuth(), middleware.RetailStudioSession())
	studio.POST("/session", OpenRetailStudio)
	studio.POST("/chat/completions", middleware.RetailStudioRelay("/v1/chat/completions"), func(c *gin.Context) {
		c.JSON(200, gin.H{"token_id": c.GetInt("token_id"), "retail": c.GetBool("retail_request")})
	})
	engine.POST("/api/token/", middleware.UserAuth(), middleware.SessionCookieOriginGuard(), AddToken)
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
	w = request("POST", "/api/retail/admin/pricing/official-draft", "https://example.com", gin.H{"expected_version": 0})
	require.Equal(t, http.StatusForbidden, w.Code, "official proposals are root-only")
	for _, action := range []struct{ method, path string }{{"GET", "/api/retail/admin/pricing/group-bindings"}, {"PUT", "/api/retail/admin/pricing/group-bindings"}, {"POST", "/api/retail/admin/pricing/group-bindings/sync"}} {
		w = request(action.method, action.path, "https://example.com", gin.H{})
		require.Equal(t, http.StatusForbidden, w.Code, "binding operations require root")
	}
	require.NoError(t, model.DB.Model(user).Update("role", common.RoleRootUser).Error)
	require.NoError(t, model.PublishUserAuthCache(user.Id))
	emptyPublication := request("PUT", "/api/retail/admin/pricing", "https://example.com", gin.H{"expected_version": 0, "book": retailprice.Book{}})
	require.Contains(t, emptyPublication.Body.String(), `"success":false`, "empty setup must not be reported as a published catalog")
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
	// The integrated form creates a credential and publishes its mapping in one HTTP write.
	retailPublicRatesClient.Transport = retailPublicRatesTestTransport(func(r *http.Request) (*http.Response, error) {
		body := fmt.Sprintf(`{"success":true,"server":"https://aixingxing.pro","updated_at":%q,"groups":[{"group":"Pro","name":"Pro","rate":0.24}]}`, time.Now().UTC().Format(time.RFC3339))
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	_, currentVersion, err := model.GetRetailPriceBook()
	require.NoError(t, err)
	integrated := gin.H{
		"expected_version": currentVersion, "enabled": true,
		"bindings":    []gin.H{{"local_group": "default", "channel_id": 101, "upstream_group": "Pro"}, {"local_group": "default", "channel_id": -1, "upstream_group": "Pro"}},
		"sale_policy": gin.H{"receipt_ratio": "1", "margin_bps": 2500, "fee_bps": 0, "buffer_bps": 0},
		"supplies":    []gin.H{{"id": -1, "site_id": 101, "name": "Pro dedicated", "key": "new-group-test-only-key", "models": "gpt-4o"}},
	}
	w = request("PUT", "/api/retail/admin/pricing/group-bindings", "https://attacker.example", integrated)
	require.Equal(t, http.StatusForbidden, w.Code)
	w = request("PUT", "/api/retail/admin/pricing/group-bindings", "https://example.com", integrated)
	require.Contains(t, w.Body.String(), `"success":true`, w.Body.String())
	require.NotContains(t, w.Body.String(), "new-group-test-only-key")
	w = request("GET", "/api/retail/admin/pricing/group-bindings", "https://example.com", nil)
	require.Contains(t, w.Body.String(), `"name":"Pro dedicated"`)
	require.Contains(t, w.Body.String(), `"has_key":true`)
	require.Contains(t, w.Body.String(), `"margin_bps":2500`)
	require.NotContains(t, w.Body.String(), "new-group-test-only-key")
	w = request("PUT", "/api/retail/admin/pricing/group-bindings", "https://example.com", integrated)
	require.Contains(t, w.Body.String(), `"success":false`, "retrying a committed draft must not create another channel")
	var createdCount int64
	require.NoError(t, model.DB.Model(&model.Channel{}).Where("name = ?", "Pro dedicated").Count(&createdCount).Error)
	require.EqualValues(t, 1, createdCount)
	// Generating official prices is a proposal only: preserve the committed book,
	// economics, existing manual products and credentials until explicit publication.
	require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", 101).Update("models", "gpt-6.1-sol,gpt-image-2.6-sunburst,unknown-alias").Error)
	// Production channel edits update routing abilities along with the configured model names.
	publishedChannel, err := model.GetChannelById(101, false)
	require.NoError(t, err)
	require.NoError(t, publishedChannel.UpdateAbilities(nil))
	beforeProposal, proposalVersion, err := model.GetRetailPriceBook()
	require.NoError(t, err)
	w = request("POST", "/api/retail/admin/pricing/official-draft", "https://attacker.example", gin.H{"expected_version": proposalVersion})
	require.Equal(t, http.StatusForbidden, w.Code)
	w = request("POST", "/api/retail/admin/pricing/official-draft", "https://example.com", gin.H{"expected_version": proposalVersion - 1})
	require.Contains(t, w.Body.String(), `"success":false`)
	w = request("POST", "/api/retail/admin/pricing/official-draft", "https://example.com", gin.H{"expected_version": proposalVersion})
	require.Contains(t, w.Body.String(), `"success":true`, w.Body.String())
	var proposal struct {
		Data struct {
			Book retailprice.Book `json:"book"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(w.Body.Bytes(), &proposal))
	require.Len(t, proposal.Data.Book.Models, len(beforeProposal.Models)+2)
	require.Equal(t, beforeProposal.Products[0], proposal.Data.Book.Products[0])
	require.Equal(t, beforeProposal.MarginBPS, proposal.Data.Book.MarginBPS)
	require.Contains(t, w.Body.String(), "unknown-alias")
	require.NotContains(t, w.Body.String(), "new-group-test-only-key")
	afterProposal, afterVersion, err := model.GetRetailPriceBook()
	require.NoError(t, err)
	require.Equal(t, proposalVersion, afterVersion)
	require.Equal(t, beforeProposal, afterProposal)
	// Public call groups are shared catalog choices. Account membership remains
	// independent, while private plans and key ownership keep their restrictions.
	w = request("PUT", "/api/retail/admin/pricing", "https://example.com", gin.H{"expected_version": proposalVersion, "book": proposal.Data.Book})
	require.Contains(t, w.Body.String(), `"success":true`, w.Body.String())
	require.NoError(t, model.DB.Model(user).Update("group", "another").Error)
	require.NoError(t, model.PublishUserAuthCache(user.Id))
	w = request("GET", "/api/retail/catalog", "https://example.com", nil)
	require.Contains(t, w.Body.String(), `"id":"gpt-6.1-sol"`, w.Body.String())
	require.NotContains(t, w.Body.String(), `"id":"standard"`)
	for _, privateField := range []string{"binding_group", "upstream_group", "channel_id", "user_groups"} {
		require.NotContains(t, w.Body.String(), privateField)
	}
	publicProduct := proposal.Data.Book.Products[len(proposal.Data.Book.Products)-1]
	publicPolicy := retailprice.KeyPolicy{ProductID: publicProduct.ID, AcceptFloating: true, Caps: map[string]retailprice.Prices{"gpt-6.1-sol": cap}, MaxRequestCNY: "10", DailyBudgetCNY: "10"}
	require.NoError(t, model.DB.Model(&token).Update("user_id", user.Id+1).Error)
	w = request("PUT", keyPath, "https://example.com", gin.H{"expected_revision": 1, "policy": publicPolicy})
	require.Contains(t, w.Body.String(), `"success":false`, "public access never bypasses key ownership")
	require.NoError(t, model.DB.Model(&token).Update("user_id", user.Id).Error)
	w = request("PUT", keyPath, "https://example.com", gin.H{"expected_revision": 1, "policy": publicPolicy})
	require.Contains(t, w.Body.String(), `"success":true`, w.Body.String())
	w = request("PUT", keyPath, "https://example.com", gin.H{"expected_revision": 2, "policy": policy})
	require.Contains(t, w.Body.String(), `"success":false`, "private plan remains unavailable")
	// Creation and policy share one commit, including public access for ordinary
	// users. Forged ownership and studio-only budget semantics cannot cross this API.
	require.NoError(t, model.DB.Model(user).Update("role", common.RoleCommonUser).Error)
	require.NoError(t, model.PublishUserAuthCache(user.Id))
	createInput := gin.H{"name": "public-create", "service_mode": "retail", "unlimited_quota": true, "expired_time": -1, "retail_policy": publicPolicy, "user_id": user.Id + 1}
	w = request("POST", "/api/token/", "https://attacker.example", createInput)
	require.Equal(t, http.StatusForbidden, w.Code)
	w = request("POST", "/api/token/", "https://example.com", createInput)
	require.Contains(t, w.Body.String(), `"success":true`, w.Body.String())
	require.NotContains(t, w.Body.String(), `"key":`, "creation must not disclose usable credentials")
	var created model.Token
	require.NoError(t, model.DB.Where("name = ?", "public-create").First(&created).Error)
	require.Equal(t, user.Id, created.UserId)
	savedPolicy, savedRevision, err := model.GetRetailKeyPolicy(user.Id, created.Id)
	require.NoError(t, err)
	require.EqualValues(t, 1, savedRevision)
	require.Equal(t, publicPolicy, savedPolicy)
	var account model.User
	require.NoError(t, model.DB.First(&account, user.Id).Error)
	require.Equal(t, "another", account.Group, "selecting a call group cannot change account membership")
	for _, scenario := range []string{"private", "unknown", "no-consent", "zero-budget", "studio", "foreign-model"} {
		t.Run("atomic-key-"+scenario, func(t *testing.T) {
			bad := publicPolicy
			switch scenario {
			case "private":
				bad.ProductID = "standard"
			case "unknown":
				bad.ProductID = "missing"
			case "no-consent":
				bad.AcceptFloating = false
			case "zero-budget":
				bad.DailyBudgetCNY = "0"
			case "studio":
				bad.StudioCurrent = true
			case "foreign-model":
				bad.Caps = map[string]retailprice.Prices{"foreign-model": cap}
			}
			var before, after int64
			require.NoError(t, model.DB.Model(&model.Token{}).Count(&before).Error)
			w = request("POST", "/api/token/", "https://example.com", gin.H{"name": "rejected-" + scenario, "service_mode": "retail", "unlimited_quota": true, "retail_policy": bad})
			require.Contains(t, w.Body.String(), `"success":false`, w.Body.String())
			require.NoError(t, model.DB.Model(&model.Token{}).Count(&after).Error)
			require.Equal(t, before, after, "rejected policy must roll back the new token")
		})
	}
	// Group-only creation and switching use the same ownership, permission and
	// optimistic-lock boundaries without requiring customer prices or budgets.
	groupInput := gin.H{"name": "group-only", "service_mode": "retail", "unlimited_quota": true, "expired_time": -1, "retail_product_id": publicProduct.ID}
	w = request("POST", "/api/token/", "https://attacker.example", groupInput)
	require.Equal(t, http.StatusForbidden, w.Code)
	w = request("POST", "/api/token/", "https://example.com", groupInput)
	require.Contains(t, w.Body.String(), `"success":true`, w.Body.String())
	var groupKey model.Token
	require.NoError(t, model.DB.Where("name = ?", "group-only").First(&groupKey).Error)
	simple := retailprice.KeyPolicy{ProductID: publicProduct.ID, GroupCurrent: true}
	savedPolicy, savedRevision, err = model.GetRetailKeyPolicy(user.Id, groupKey.Id)
	require.NoError(t, err)
	require.Equal(t, simple, savedPolicy)
	require.EqualValues(t, 1, savedRevision)
	// Saving a group also replaces an older explicit policy in one transaction.
	w = request("PUT", keyPath, "https://example.com", gin.H{"expected_revision": 2, "product_id": publicProduct.ID})
	require.Contains(t, w.Body.String(), `"success":true`, w.Body.String())
	savedPolicy, savedRevision, err = model.GetRetailKeyPolicy(user.Id, token.Id)
	require.NoError(t, err)
	require.Equal(t, simple, savedPolicy)
	require.EqualValues(t, 3, savedRevision)
	w = request("PUT", keyPath, "https://example.com", gin.H{"expected_revision": 2, "product_id": publicProduct.ID})
	require.Contains(t, w.Body.String(), `"success":false`, "stale group selection must not overwrite")
	w = request("PUT", keyPath, "https://attacker.example", gin.H{"expected_revision": 3, "product_id": publicProduct.ID})
	require.Equal(t, http.StatusForbidden, w.Code)
	for _, productID := range []string{"standard", "missing", ""} {
		var before, after int64
		require.NoError(t, model.DB.Model(&model.Token{}).Count(&before).Error)
		w = request("POST", "/api/token/", "https://example.com", gin.H{"name": "denied-group", "service_mode": "retail", "retail_product_id": productID})
		require.Contains(t, w.Body.String(), `"success":false`)
		require.NoError(t, model.DB.Model(&model.Token{}).Count(&after).Error)
		require.Equal(t, before, after)
		w = request("PUT", keyPath, "https://example.com", gin.H{"expected_revision": 3, "product_id": productID})
		require.Contains(t, w.Body.String(), `"success":false`)
	}
	w = request("POST", "/api/token/", "https://example.com", gin.H{"name": "mixed", "retail_product_id": publicProduct.ID, "retail_policy": publicPolicy})
	require.Contains(t, w.Body.String(), `"success":false`)
	w = request("PUT", keyPath, "https://example.com", gin.H{"expected_revision": 3, "product_id": publicProduct.ID, "policy": publicPolicy})
	require.Contains(t, w.Body.String(), `"success":false`)
	require.NoError(t, model.DB.Model(&token).Update("user_id", user.Id+1).Error)
	w = request("PUT", keyPath, "https://example.com", gin.H{"expected_revision": 3, "product_id": publicProduct.ID})
	require.Contains(t, w.Body.String(), `"success":false`)
	require.NoError(t, model.DB.Model(&token).Update("user_id", user.Id).Error)
	require.NoError(t, model.DB.First(&account, user.Id).Error)
	require.NoError(t, model.DB.Model(user).Update("role", common.RoleRootUser).Error)
	require.NoError(t, model.PublishUserAuthCache(user.Id))
	// The same public group must lose the model in preview, catalog and request pricing on a changed alias or a disabled Key.
	current, currentVersion, err := model.GetRetailPriceBook()
	require.NoError(t, err)
	checkUnavailable := func() {
		t.Helper()
		preview := request("POST", "/api/retail/admin/pricing/preview", "https://example.com", gin.H{"book": current})
		var response struct {
			Success bool
			Data    []struct {
				Product, Model string
				Available      bool
			}
		}
		require.NoError(t, common.Unmarshal(preview.Body.Bytes(), &response))
		require.True(t, response.Success, preview.Body.String())
		found := false
		for _, row := range response.Data {
			if row.Product == publicProduct.Name && row.Model == "gpt-6.1-sol" {
				found = true
				require.False(t, row.Available)
			}
		}
		require.True(t, found)
		catalog := request("GET", "/api/retail/catalog", "https://example.com", nil)
		require.Contains(t, catalog.Body.String(), `"success":true`)
		require.NotContains(t, catalog.Body.String(), `"id":"gpt-6.1-sol"`)
		_, err := model.RetailQuote(user.Id, groupKey.Id, "gpt-6.1-sol", 1, []int{101})
		require.ErrorIs(t, err, retailprice.ErrExpired)
	}
	require.NoError(t, model.DB.Model(&channel).Update("model_mapping", `{"gpt-6.1-sol":"supplier-alias"}`).Error)
	checkUnavailable()
	require.NoError(t, model.DB.Model(&channel).Update("model_mapping", "{}").Error)
	retailPublicRatesClient.Transport = retailPublicRatesTestTransport(func(*http.Request) (*http.Response, error) { return nil, io.ErrUnexpectedEOF })
	off := false
	edits := []model.RetailSupplyEdit{{ID: 101, SiteID: 101, Name: publishedChannel.Name, Models: publishedChannel.Models, Enabled: &off}}
	payload := gin.H{"expected_version": currentVersion, "bindings": current.GroupBindings, "enabled": true, "supplies": edits}
	edits[0].Name += " changed"
	w = request("PUT", "/api/retail/admin/pricing/group-bindings", "https://example.com", payload)
	require.Contains(t, w.Body.String(), `"success":false`)
	edits[0].Name = publishedChannel.Name
	w = request("PUT", "/api/retail/admin/pricing/group-bindings", "https://example.com", payload)
	require.Contains(t, w.Body.String(), `"success":true`, w.Body.String())
	checkUnavailable()
	after, newVersion, err := model.GetRetailPriceBook()
	require.NoError(t, err)
	require.Equal(t, current, after)
	require.Equal(t, currentVersion+1, newVersion)
	w = request("PUT", "/api/retail/admin/pricing/group-bindings", "https://example.com", payload)
	require.Contains(t, w.Body.String(), `"success":false`)

	// 从旧版星行迁移到单站冻结时，即使接口故障也应保留同一份可信快照。后台不可继续刷新该站。
	feedPath := "/api/retail/admin/pricing/site-rates"
	requestFeed := func(version int64, follow *bool) *httptest.ResponseRecorder {
		input := gin.H{"expected_version": version, "site_id": 101, "url": retailPublicRatesURL}
		if follow != nil {
			input["follow"] = *follow
		}
		return request("PUT", feedPath, "https://example.com", input)
	}
	w = requestFeed(newVersion, &off)
	require.Contains(t, w.Body.String(), `"success":true`, w.Body.String())
	migrated, v, err := model.GetRetailPriceBook()
	require.NoError(t, err)
	require.False(t, migrated.SiteFeeds[101].Follow)
	require.Equal(t, migrated.PublicRateSync.Snapshot, migrated.SiteFeeds[101].Snapshot)
	require.NoError(t, syncRetailSiteRates(context.Background()))
	currentRate := "0.36"
	retailPublicRatesClient.Transport = retailPublicRatesTestTransport(func(r *http.Request) (*http.Response, error) {
		body := fmt.Sprintf(`{"success":true,"server":"https://aixingxing.pro","updated_at":%q,"groups":[{"group":"Pro","name":"Pro","rate":%s}]}`, time.Now().UTC().Format(time.RFC3339), currentRate)
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	w = requestFeed(v, nil)
	require.Contains(t, w.Body.String(), `"success":true`, w.Body.String())
	manual, v, err := model.GetRetailPriceBook()
	require.NoError(t, err)
	require.False(t, manual.SiteFeeds[101].Follow)
	on := true
	w = requestFeed(v, &on)
	require.Contains(t, w.Body.String(), `"success":true`, w.Body.String())
	currentRate = "0.45"
	require.NoError(t, syncRetailSiteRates(context.Background()))
	updated, v, err := model.GetRetailPriceBook()
	require.NoError(t, err)
	value, err := updated.BoundRate(retailprice.GroupBinding{ChannelID: 101, UpstreamGroup: "Pro"})
	require.NoError(t, err)
	require.Equal(t, "0.45", value.Multiplier)
	w = requestFeed(v, &off)
	require.Contains(t, w.Body.String(), `"success":true`, w.Body.String())
	currentRate = "0.99"
	require.NoError(t, syncRetailSiteRates(context.Background()))
	frozen, _, err := model.GetRetailPriceBook()
	require.NoError(t, err)
	require.Equal(t, "0.45", frozen.SiteFeeds[101].Snapshot.Groups[0].Multiplier)
	require.Equal(t, "another", account.Group)
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

// TestRetailSupplyGroupManagement verifies root/CSRF boundaries and write-only credentials.
// It uses real signed access identities; the management path never contacts a supplier.
func TestRetailSupplyGroupManagement(t *testing.T) {
	user, identity := setupSecurityEnrollmentTest(t)
	oldSecure := common.SessionCookieSecure
	common.SessionCookieSecure = true
	t.Cleanup(func() { common.SessionCookieSecure = oldSecure })
	require.NoError(t, model.DB.AutoMigrate(&model.Channel{}, &model.Ability{}, &model.GPTHostingBinding{}))
	endpoint := "https://aixingxing.pro"
	require.NoError(t, model.DB.Create(&model.Channel{Id: 101, Name: "Site", Key: "parent-secret", Type: 1, BaseURL: &endpoint, Group: "retail", Status: 1}).Error)
	path := filepath.Join(t.TempDir(), "retail.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"enabled":true,"group":"retail","supplies":[{"channel_id":101,"kind":"reseller"}]}`), 0600))
	t.Setenv("RETAIL_RELAY_CONFIG", path)
	access, _, err := service.IssueAccessToken(identity)
	require.NoError(t, err)
	r := gin.New()
	r.POST("/groups", middleware.RootAuth(), middleware.SessionCookieOriginGuard(), CreateRetailSupplyGroup)
	r.GET("/supplies", middleware.RootAuth(), RetailSupplies)
	request := func(method, route, auth, origin, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "https://example.com"+route, bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", origin)
		if auth != "" {
			req.Header.Set("Authorization", "Bearer "+auth)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	payload := `{"site_id":101,"name":"Normal","key":"child-secret","models":"gpt-example","base_url":"https://ignored.example","retail_site_id":999}`
	require.Equal(t, 401, request("POST", "/groups", "", "https://example.com", payload).Code)
	require.Equal(t, 403, request("POST", "/groups", access, "https://example.com", payload).Code)
	require.NoError(t, model.DB.Model(user).Update("role", common.RoleRootUser).Error)
	require.NoError(t, model.PublishUserAuthCache(user.Id))
	require.Equal(t, 403, request("POST", "/groups", access, "https://attacker.example", payload).Code)
	w := request("POST", "/groups", access, "https://example.com", payload)
	require.Equal(t, 200, w.Code)
	require.Contains(t, w.Body.String(), `"success":true`)
	require.NotContains(t, w.Body.String(), "child-secret")
	var child model.Channel
	require.NoError(t, model.DB.Where("retail_site_id = ?", 101).First(&child).Error)
	require.Equal(t, endpoint, child.GetBaseURL(), "address and enrollment are derived server-side")
	w = request("GET", "/supplies", access, "https://example.com", "")
	require.Contains(t, w.Body.String(), `"site_id":101`)
	require.NotContains(t, w.Body.String(), "child-secret")
	require.NotContains(t, w.Body.String(), "parent-secret")
	w = request("POST", "/groups", access, "https://example.com", strings.Repeat(" ", 33<<10)+payload)
	require.Contains(t, w.Body.String(), `"success":false`)
}

// TestRetailStudioDirectRequests exercises first-send identity initialization, real
// routing and settlement with synthetic upstreams; no setup modal or legacy ratio is required.
func TestRetailStudioDirectRequests(t *testing.T) {
	user, identity := setupSecurityEnrollmentTest(t)
	// Initialize dialect-specific quoted columns through normal startup, using
	// the disposable fixture database and keeping its original connection alive.
	fixtureDB, master, sqlitePath := model.DB, common.IsMasterNode, common.SQLitePath
	dialect := os.Getenv("TEST_SECURITY_DIALECT")
	if dialect == "" || dialect == "sqlite" {
		var databases []struct{ File string }
		require.NoError(t, fixtureDB.Raw("PRAGMA database_list").Scan(&databases).Error)
		common.SQLitePath = databases[0].File
		t.Setenv("SQL_DSN", "local")
	} else {
		t.Setenv("SQL_DSN", os.Getenv("TEST_"+strings.ToUpper(dialect)+"_DSN"))
	}
	common.IsMasterNode = false
	require.NoError(t, model.InitDB())
	startupConnection, err := model.DB.DB()
	require.NoError(t, err)
	require.NoError(t, startupConnection.Close())
	model.DB, common.IsMasterNode, common.SQLitePath = fixtureDB, master, sqlitePath

	t.Setenv("SITE_MODE", "retail")
	oldBatch, oldLogs := common.BatchUpdateEnabled, common.LogConsumeEnabled
	common.BatchUpdateEnabled, common.LogConsumeEnabled = false, false
	t.Cleanup(func() { common.BatchUpdateEnabled, common.LogConsumeEnabled = oldBatch, oldLogs })
	oldGroups := ratio_setting.GroupRatio2JSONString()
	t.Cleanup(func() { _ = ratio_setting.UpdateGroupRatioByJSONString(oldGroups) })
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1,"gpt-normal":1}`))
	require.NoError(t, model.DB.AutoMigrate(&model.Token{}, &model.Channel{}, &model.Ability{}, &model.GPTHostingBinding{}, &model.RetailCharge{}, &model.RetailAttempt{}, &model.RetailPriceState{}, &model.RetailPriceVersion{}, &model.RetailKeyPricePolicy{}, &model.RetailPriceBudget{}))
	require.NoError(t, model.DB.Model(user).Update("quota", 10000000).Error)
	t.Setenv("STUDIO_HISTORY_DIR", t.TempDir())
	referencePNG, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aD1sAAAAASUVORK5CYII=")
	require.NoError(t, err)
	original := append(referencePNG, bytes.Repeat([]byte{0x3a}, 9<<20)...)
	var hits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Header.Get("X-Retail-Product") != "" {
			t.Error("internal group header leaked upstream")
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/images/edits" {
			if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data;") {
				require.NoError(t, r.ParseMultipartForm(1<<20))
				defer r.MultipartForm.RemoveAll()
				require.Equal(t, "gpt-image-2", r.FormValue("model"))
				require.Equal(t, "high", r.FormValue("quality"))
				require.Equal(t, "1920x1088", r.FormValue("size"))
				file, _, err := r.FormFile("image[]")
				require.NoError(t, err)
				defer file.Close()
				data, err := io.ReadAll(file)
				require.NoError(t, err)
				require.True(t, bytes.Equal(original, data), "supplier must receive the original file")
			} else {
				var body struct {
					Size              string `json:"size"`
					Quality           string `json:"quality"`
					OutputFormat      string `json:"output_format"`
					OutputCompression int    `json:"output_compression"`
					Images            []struct {
						URL string `json:"image_url"`
					} `json:"images"`
				}
				require.NoError(t, common.DecodeJson(r.Body, &body))
				require.Len(t, body.Images, 1)
				require.Contains(t, body.Images[0].URL, "data:image/png;base64,")
				require.Equal(t, "1920x1088", body.Size)
				require.Equal(t, "auto", body.Quality)
				require.Equal(t, "webp", body.OutputFormat)
				require.Equal(t, 82, body.OutputCompression)
			}
			_, _ = w.Write([]byte(`{"data":[{"b64_json":"iVBORw0KGgo="}],"usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12,"input_tokens_details":{"text_tokens":4,"image_tokens":6,"cached_tokens":2,"cached_tokens_details":{"text_tokens":1,"image_tokens":1}}}}`))
			return
		}
		if r.URL.Path == "/v1/images/generations" {
			_, _ = w.Write([]byte(`{"data":[{"b64_json":"iVBORw0KGgo="}],"usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12,"input_tokens_details":{"text_tokens":10,"image_tokens":0}}}`))
			return
		}
		raw, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		if strings.Contains(string(raw), "Inspect upload") {
			require.Contains(t, string(raw), "data:image/png;base64,"+base64.StdEncoding.EncodeToString(referencePNG))
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: "+`{"id":"vision","model":"gpt-4o","choices":[{"index":0,"delta":{"content":"Saw image"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12,"prompt_tokens_details":{"image_tokens":8}}}`+"\n\ndata: [DONE]\n\n")
			return
		}
		_, _ = w.Write([]byte(`{"id":"studio-test","choices":[{"index":0,"message":{"role":"assistant","content":"synthetic OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`))
	}))
	t.Cleanup(upstream.Close)
	channel := model.Channel{Id: 101, Name: "local-test", Type: 1, Status: 1, Group: "retail", Models: "gpt-4o,gpt-image-2", Key: "test-only", BaseURL: &upstream.URL}
	require.NoError(t, model.DB.Create(&channel).Error)
	for _, name := range []string{"gpt-4o", "gpt-image-2"} {
		require.NoError(t, model.DB.Create(&model.Ability{ChannelId: 101, Model: name, Group: "retail", Enabled: true}).Error)
	}
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
	image := book.Models[0]
	image.ID, image.Kind = "gpt-image-2", "image"
	book.Models = append(book.Models, image)
	book.Products[0].Public = true
	book.Products[0].BindingGroup = "gpt-normal"
	book.Products[0].UserGroups = nil
	book.Products[0].Routes[0].Models[image.ID] = image.ID
	premium := book.Products[0]
	premium.ID, premium.Name = "premium", "Premium"
	book.Products = append(book.Products, premium)
	private := premium
	private.ID, private.Public, private.UserGroups = "private", false, []string{"vip"}
	book.Products = append(book.Products, private)
	_, err = model.PublishRetailPriceBook(0, book, nil, user.Id)
	require.NoError(t, err)
	access, _, err := service.IssueAccessToken(identity)
	require.NoError(t, err)
	engine := gin.New()
	// The integration fixture bypasses startup, which normally sets the SSE read timeout.
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })
	engine.Use(middleware.BodyStorageCleanup())
	studio := engine.Group("/api/retail/studio", middleware.UserAuth(), middleware.RetailStudioSession())
	for _, route := range []struct {
		path, relay string
		format      types.RelayFormat
	}{
		{"/chat/completions", "/v1/chat/completions", types.RelayFormatOpenAI},
		{"/images/generations", "/v1/images/generations", types.RelayFormatOpenAIImage},
		{"/images/edits", "/v1/images/edits", types.RelayFormatOpenAIImage},
	} {
		studio.POST(route.path, middleware.RetailStudioRelay(route.relay), middleware.Distribute(), func(c *gin.Context) { c.Set(common.RequestIdKey, common.NewRequestId()); Relay(c, route.format) })
	}
	engine.POST("/api/retail/studio/runs/:id", middleware.UserAuth(), middleware.SessionCookieOriginGuard(), StartStudioRun(engine))
	engine.GET("/api/retail/studio/runs/:id", middleware.UserAuth(), GetStudioRun)
	engine.GET("/api/retail/studio/media/:id/:role", middleware.UserAuth(), ReadStudioMedia)
	engine.DELETE("/api/retail/studio/history/:id", middleware.UserAuth(), DeleteStudioHistory)
	// Sessions and Origin are real; only procurement uses the synthetic local server.
	send := func(path, product, origin, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest("POST", "https://example.com/api/retail/studio"+path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+access)
		r.Header.Set("Origin", origin)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Retail-Product", product)
		r.RequestURI = r.URL.RequestURI()
		r.URL.Scheme, r.URL.Host = "", ""
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, r)
		return w
	}
	chat := `{"model":"gpt-4o","messages":[{"role":"user","content":"hello"}],"max_tokens":20}`
	w := send("/chat/completions", "standard", "https://attacker.example", chat)
	require.Equal(t, 403, w.Code)
	_, err = model.RetailStudioToken(user.Id, false)
	require.Error(t, err, "untrusted requests cannot create wallet identities")
	w = send("/chat/completions", "standard", "https://example.com", chat)
	require.Equal(t, 200, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), "synthetic OK")
	token, err := model.RetailStudioToken(user.Id, false)
	require.NoError(t, err)
	policy, revision, err := model.GetRetailKeyPolicy(user.Id, token.Id)
	require.NoError(t, err)
	require.True(t, policy.StudioCurrent)
	require.Equal(t, "5", policy.MaxRequestCNY)
	require.Equal(t, "50", policy.DailyBudgetCNY)
	// Simulate a returning user whose only saved model predates the image route.
	// The next send must succeed without opening settings or resetting spent budget.
	legacy := policy
	legacy.StudioCurrent = false
	legacy.Caps = map[string]retailprice.Prices{"gpt-4o": {Input: "1", Output: "1", CacheRead: "0", CacheWrite: "0", CacheWrite1h: "0"}}
	require.NoError(t, model.PutRetailKeyPolicy(user.Id, token.Id, revision, legacy))
	w = send("/images/generations", "premium", "https://example.com", `{"model":"gpt-image-2","prompt":"a tree","n":1,"output_format":"png"}`)
	require.Equal(t, 200, w.Code, w.Body.String())
	require.EqualValues(t, 2, hits.Load())
	// Edits must reach the mapped supplier with the uploaded bytes and settle mixed image/text usage.
	edit := `{"model":"gpt-image-2","prompt":"paint the tree","size":"1920x1088","quality":"auto","output_format":"webp","output_compression":82,"images":[{"image_url":"data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aD1sAAAAASUVORK5CYII="}]}`
	w = send("/images/edits", "premium", "https://example.com", edit)
	require.Equal(t, 200, w.Code, w.Body.String())
	w = send("/images/edits", "premium", "https://example.com", strings.Replace(edit, "data:image/png;base64,", "https://example.com/", 1))
	require.Equal(t, 403, w.Code, w.Body.String())
	after, afterRevision, err := model.GetRetailKeyPolicy(user.Id, token.Id)
	require.NoError(t, err)
	require.Equal(t, policy, after)
	require.Equal(t, revision+2, afterRevision, "saving the legacy policy and upgrading it each increment the revision once")
	revision = afterRevision
	var charges []model.RetailCharge
	require.NoError(t, model.DB.Find(&charges, "token_id = ?", token.Id).Error)
	require.Len(t, charges, 3)
	for _, charge := range charges {
		require.Equal(t, "settled", charge.Status)
		require.Positive(t, charge.Actual)
	}
	w = send("/chat/completions", "private", "https://example.com", chat)
	require.Equal(t, 403, w.Code)
	require.EqualValues(t, 3, hits.Load())
	// Existing customized limits survive subsequent sends and cannot be bypassed by group selection.
	policy.DailyBudgetCNY = "0.000001"
	require.NoError(t, model.PutRetailKeyPolicy(user.Id, token.Id, revision, policy))
	w = send("/chat/completions", "premium", "https://example.com", chat)
	require.Equal(t, 403, w.Code, w.Body.String())
	require.EqualValues(t, 3, hits.Load())
	after, _, err = model.GetRetailKeyPolicy(user.Id, token.Id)
	require.NoError(t, err)
	require.Equal(t, policy, after)
	// Reopen the budget for a real detached upload routed to the mock supplier.
	policy.DailyBudgetCNY = "50"
	_, revision, err = model.GetRetailKeyPolicy(user.Id, token.Id)
	require.NoError(t, err)
	require.NoError(t, model.PutRetailKeyPolicy(user.Id, token.Id, revision, policy))
	id := uuid.NewString()
	var upload bytes.Buffer
	form := multipart.NewWriter(&upload)
	require.NoError(t, form.WriteField("intent", fmt.Sprintf(`{"record":{"id":"%s","mode":"image","product":"premium","entries":[{"id":1,"prompt":"Edit original","model":"gpt-image-2"}]},"size":"1920x1088","quality":"high"}`, id)))
	part, err := form.CreateFormFile("image", "reference.png")
	require.NoError(t, err)
	_, err = part.Write(original)
	require.NoError(t, err)
	require.NoError(t, form.Close())
	runPath := "/api/retail/studio/runs/" + id
	call := func(method, path, contentType string, body io.Reader) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, body)
		req.Header.Set("Authorization", "Bearer "+access)
		req.Header.Set("Origin", "http://example.com")
		req.Header.Set("Content-Type", contentType)
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, req)
		return rec
	}
	owner := fmt.Sprintf("?owner=%d", user.Id)
	w = call("POST", runPath+owner, form.FormDataContentType(), bytes.NewReader(upload.Bytes()))
	require.Equal(t, 202, w.Code, w.Body.String())
	var result struct {
		Record studioHistoryRecord
	}
	require.Eventually(t, func() bool {
		rec := call("GET", runPath, "", nil)
		return common.Unmarshal(rec.Body.Bytes(), &result) == nil && len(result.Record.Entries) > 0
	}, 3*time.Second, 10*time.Millisecond)
	entry := result.Record.Entries[0]
	require.Empty(t, entry.Error)
	require.Equal(t, studioMediaPrefix+id+"/reference", entry.Reference)
	require.Equal(t, studioMediaPrefix+id+"/result", entry.Image)
	require.EqualValues(t, 4, hits.Load())
	w = call("POST", runPath+owner, form.FormDataContentType(), bytes.NewReader(upload.Bytes()))
	require.Equal(t, 200, w.Code, w.Body.String())
	require.EqualValues(t, 4, hits.Load(), "a repeated upload never reissues the paid request")
	w = call("GET", entry.Reference, "", nil)
	require.Equal(t, 200, w.Code)
	require.True(t, bytes.Equal(original, w.Body.Bytes()))
	w = call("GET", entry.Image, "", nil)
	require.Equal(t, 200, w.Code)
	require.Equal(t, []byte("\x89PNG\r\n\x1a\n"), w.Body.Bytes())
	w = call("DELETE", "/api/retail/studio/history/"+id+owner, "", nil)
	require.Equal(t, 200, w.Code)
	require.Equal(t, 404, call("GET", entry.Reference, "", nil).Code)
	_, err = os.Stat(filepath.Join(os.Getenv("STUDIO_HISTORY_DIR"), fmt.Sprint(user.Id), id+".result"))
	require.ErrorIs(t, err, os.ErrNotExist)
	// Drive the browser's multipart entry point, then continue without uploading again.
	// Both paid calls must carry the same image bytes and settle against the mock supplier.
	first := uuid.NewString()
	upload.Reset()
	form = multipart.NewWriter(&upload)
	require.NoError(t, form.WriteField("intent", fmt.Sprintf(`{"record":{"id":"%s","mode":"text","product":"standard","entries":[{"id":1,"prompt":"Inspect upload","model":"gpt-4o"}]},"maxTokens":20}`, first)))
	part, err = form.CreateFormFile("image", "clipboard.png")
	require.NoError(t, err)
	_, err = part.Write(referencePNG)
	require.NoError(t, err)
	require.NoError(t, form.Close())
	w = call("POST", "/api/retail/studio/runs/"+first+owner, form.FormDataContentType(), bytes.NewReader(upload.Bytes()))
	require.Equal(t, 202, w.Code, w.Body.String())
	readVision := func(id string) studioHistoryRecord {
		t.Helper()
		var out struct{ Record studioHistoryRecord }
		require.Eventually(t, func() bool {
			rec := call("GET", "/api/retail/studio/runs/"+id, "", nil)
			return common.Unmarshal(rec.Body.Bytes(), &out) == nil && len(out.Record.Entries) > 0
		}, 3*time.Second, 10*time.Millisecond)
		tail := out.Record.Entries[len(out.Record.Entries)-1]
		require.Empty(t, tail.Error)
		require.Contains(t, tail.Content, "Saw image")
		return out.Record
	}
	conversation := readVision(first)
	require.Equal(t, referencePNG, call("GET", conversation.Entries[0].Reference, "", nil).Body.Bytes())
	next := uuid.NewString()
	conversation.ID = next
	conversation.Entries = append(conversation.Entries, studioHistoryEntry{ID: 2, Prompt: "What color?", Model: "gpt-4o"})
	follow, err := common.Marshal(studioIntent{Record: conversation, Parent: first, MaxTokens: 20})
	require.NoError(t, err)
	w = call("POST", "/api/retail/studio/runs/"+next+owner, "application/json", bytes.NewReader(follow))
	require.Equal(t, 202, w.Code, w.Body.String())
	continued := readVision(next)
	require.Equal(t, referencePNG, call("GET", continued.Entries[0].Reference, "", nil).Body.Bytes())
	require.Equal(t, 404, call("GET", conversation.Entries[0].Reference, "", nil).Code)
	w = call("POST", "/api/retail/studio/runs/"+next+owner, "application/json", bytes.NewReader(follow))
	require.Equal(t, 200, w.Code)
	require.EqualValues(t, 6, hits.Load())
	require.NoError(t, model.DB.Find(&charges, "token_id = ?", token.Id).Error)
	require.Len(t, charges, 6)
	for _, charge := range charges {
		require.Equal(t, "settled", charge.Status)
	}
	require.Equal(t, 200, call("DELETE", "/api/retail/studio/history/"+next+owner, "", nil).Code)
	require.Equal(t, 404, call("GET", continued.Entries[0].Reference, "", nil).Code)
	ordinary := model.Token{UserId: user.Id, ServiceMode: model.TokenServiceRetail, Key: "test-ordinary", Status: 1, ExpiredTime: -1}
	require.NoError(t, model.DB.Create(&ordinary).Error)
	require.Error(t, model.PutRetailKeyPolicy(user.Id, ordinary.Id, 0, policy), "API keys cannot opt into studio-only pricing")
}

// TestRetailSiteRateFeedIdentity 校验不同站点不能冒充同名分组，金额保留原始小数精度。
func TestRetailSiteRateFeedIdentity(t *testing.T) {
	now := time.Now().UTC()
	body := []byte(fmt.Sprintf(`{"success":true,"server":"https://second.example","updated_at":%q,"groups":[{"group":"shared","name":"Shared","rate":0.12345678}]}`, now.Format(time.RFC3339)))
	rates, _, ok := parseRetailSiteRates(body, now, "https://second.example")
	require.True(t, ok)
	require.Equal(t, "0.12345678", rates[0].Multiplier)
	_, _, ok = parseRetailSiteRates(body, now, "https://first.example")
	require.False(t, ok)
}
