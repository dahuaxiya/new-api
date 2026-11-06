package controller

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/hostingpay"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type alipayFixture struct {
	client            *hostingpay.Client
	appKey, alipayKey *rsa.PrivateKey
	configPath        string
	config            hostingpay.Config
}

// newAlipayFixture 使用临时 RSA 密钥和受限文件验证实际 SDK，不连接支付宝或使用真实收款凭据。
func newAlipayFixture(t *testing.T) alipayFixture {
	t.Helper()
	dir := t.TempDir()
	appKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	aliKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	private, err := x509.MarshalPKCS8PrivateKey(appKey)
	require.NoError(t, err)
	public, err := x509.MarshalPKIXPublicKey(&aliKey.PublicKey)
	require.NoError(t, err)
	cfg := hostingpay.Config{Enabled: true, Environment: "production", AppID: "2026000000000001", SellerID: "2088000000000001", PrivateKeyFile: filepath.Join(dir, "private.key"), PublicKeyFile: filepath.Join(dir, "alipay.pub")}
	require.NoError(t, os.WriteFile(cfg.PrivateKeyFile, []byte(base64.StdEncoding.EncodeToString(private)), 0600))
	require.NoError(t, os.WriteFile(cfg.PublicKeyFile, []byte(base64.StdEncoding.EncodeToString(public)), 0600))
	path := filepath.Join(dir, "config.json")
	raw, err := common.Marshal(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, raw, 0600))
	client, err := hostingpay.Load(path)
	require.NoError(t, err)
	old := hostingAlipayClient
	hostingAlipayClient = sync.OnceValues(func() (*hostingpay.Client, error) { return hostingpay.Load(path) })
	t.Cleanup(func() { hostingAlipayClient = old })
	t.Setenv("ALIPAY_HOSTING_CONFIG", path)
	return alipayFixture{client: client, appKey: appKey, alipayKey: aliKey, configPath: path, config: cfg}
}

// alipayTestSign 直接以标准库生成 RSA2 签名，使回归不靠被测 SDK 自己证明自己的验签结果。
func alipayTestSign(t *testing.T, key *rsa.PrivateKey, raw []byte) string {
	t.Helper()
	digest := sha256.Sum256(raw)
	sign, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	require.NoError(t, err)
	return base64.StdEncoding.EncodeToString(sign)
}

// alipayTestCanonical 生成协议规定的未转义排序串；通知忽略 sign_type，请求签名必须包含它。
func alipayTestCanonical(values url.Values, notification bool) []byte {
	keys := []string{}
	for k := range values {
		if k != "sign" && !(notification && k == "sign_type") && values.Get(k) != "" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	parts := []string{}
	for _, k := range keys {
		parts = append(parts, k+"="+values.Get(k))
	}
	return []byte(strings.Join(parts, "&"))
}

type alipayTestTransport func(*http.Request) (*http.Response, error)

// RoundTrip 将测试中的支付宝请求截获在内存边界，不能发往真实官方网关。
func (f alipayTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestHostingAlipayProtocol 验证固定网关、分金额、有效期、真实 RSA2 签名和不可信响应拒绝。
func TestHostingAlipayProtocol(t *testing.T) {
	f := newAlipayFixture(t)
	for _, invalid := range []string{"0", "-1", "+1", "4e1", "40.001", "40.", "40..00"} {
		_, err := hostingpay.AmountCents(invalid)
		require.Error(t, err)
	}
	for value, want := range map[string]int64{"40": 4000, "40.0": 4000, "40.00": 4000, "0.01": 1} {
		got, err := hostingpay.AmountCents(value)
		require.NoError(t, err)
		assert.Equal(t, want, got)
	}
	expires := time.Now().Add(15 * time.Minute).Unix()
	endpoint, params, err := f.client.PagePay("HPfixture", 4000, 30, expires, "https://portal.test/api/notify", "https://portal.test/wallet?tab=hosting")
	require.NoError(t, err)
	assert.Equal(t, "https://openapi.alipay.com/gateway.do", endpoint)
	assert.Equal(t, "alipay.trade.page.pay", params["method"])
	assert.Equal(t, "RSA2", params["sign_type"])
	values := url.Values{}
	for k, v := range params {
		values.Set(k, v)
	}
	sig, err := base64.StdEncoding.DecodeString(params["sign"])
	require.NoError(t, err)
	digest := sha256.Sum256(alipayTestCanonical(values, false))
	require.NoError(t, rsa.VerifyPKCS1v15(&f.appKey.PublicKey, crypto.SHA256, digest[:], sig))
	var biz map[string]any
	require.NoError(t, common.UnmarshalJsonStr(params["biz_content"], &biz))
	assert.Equal(t, "40.00", biz["total_amount"])
	assert.Equal(t, "FAST_INSTANT_TRADE_PAY", biz["product_code"])
	assert.Equal(t, f.config.SellerID, biz["seller_id"])
	assert.Equal(t, time.Unix(expires, 0).In(time.FixedZone("CST", 8*3600)).Format("2006-01-02 15:04:05"), biz["time_expire"])
	_, _, err = f.client.PagePay("HPfixture", 4000, 30, expires, "http://portal.test/notify", "https://portal.test/wallet")
	require.Error(t, err)
	// 宽权限私钥和未知环境都必须禁用渠道，不回落到默认生产收款配置。
	require.NoError(t, os.Chmod(f.config.PrivateKeyFile, 0644))
	_, err = hostingpay.Load(f.configPath)
	require.ErrorIs(t, err, hostingpay.ErrConfig)
	require.NoError(t, os.Chmod(f.config.PrivateKeyFile, 0600))
	f.config.Environment = "unexpected"
	raw, err := common.Marshal(f.config)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(f.configPath, raw, 0600))
	_, err = hostingpay.Load(f.configPath)
	require.ErrorIs(t, err, hostingpay.ErrConfig)
	oldTransport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = oldTransport })
	for _, tc := range []struct {
		name, status, order, amount   string
		tampered, expectedError, paid bool
	}{
		{"paid", "TRADE_SUCCESS", "HPfixture", "40.00", false, false, true},
		{"waiting", "WAIT_BUYER_PAY", "HPfixture", "40.00", false, false, false},
		{"closed", "TRADE_CLOSED", "HPfixture", "40.00", false, false, false},
		{"wrong-order", "TRADE_SUCCESS", "HPother", "40.00", false, true, false},
		{"wrong-signature", "TRADE_SUCCESS", "HPfixture", "40.00", true, true, false},
		{"invalid-amount", "TRADE_SUCCESS", "HPfixture", "4e1", false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			http.DefaultTransport = alipayTestTransport(func(r *http.Request) (*http.Response, error) {
				require.Equal(t, "openapi.alipay.com", r.URL.Host)
				require.NoError(t, r.ParseForm())
				require.Equal(t, "alipay.trade.query", r.PostForm.Get("method"))
				body, err := common.Marshal(map[string]string{"code": "10000", "trade_no": "2026110600000001", "out_trade_no": tc.order, "trade_status": tc.status, "total_amount": tc.amount})
				require.NoError(t, err)
				sign := alipayTestSign(t, f.alipayKey, body)
				if tc.tampered {
					sign = alipayTestSign(t, f.appKey, body)
				}
				content := `{"alipay_trade_query_response":` + string(body) + `,"sign":"` + sign + `"}`
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(content))}, nil
			})
			payment, err := f.client.Query(context.Background(), "HPfixture")
			if tc.expectedError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tc.paid, payment.Paid)
			}
		})
	}
}

// TestHostingAlipayOrderBoundary 覆盖会话归属、CSRF、通知幂等、退款先到及沙箱不能开启线上销售。
func TestHostingAlipayOrderBoundary(t *testing.T) {
	f := newAlipayFixture(t)
	user, identity := setupSecurityEnrollmentTest(t)
	t.Setenv("GPT_HOSTING_ENABLED", "true")
	oldSecure, oldOrigins := common.SessionCookieSecure, common.SessionCookieTrustedURLs
	common.SessionCookieSecure, common.SessionCookieTrustedURLs = true, nil
	t.Cleanup(func() { common.SessionCookieSecure, common.SessionCookieTrustedURLs = oldSecure, oldOrigins })
	oldOptions, oldRatio := common.OptionMap, ratio_setting.GroupRatio2JSONString()
	common.OptionMap = map[string]string{}
	t.Cleanup(func() { common.OptionMap = oldOptions; _ = ratio_setting.UpdateGroupRatioByJSONString(oldRatio) })
	require.NoError(t, model.DB.AutoMigrate(&model.HostingPlan{}, &model.HostingOrder{}, &model.GPTHostingGrant{}, &model.GPTHostingBinding{}, &model.GPTHostingInventory{}, &model.VM2APIEnrollment{}, &model.Token{}))
	require.NoError(t, model.MigrateHostingStore(model.DB))
	require.NoError(t, model.SaveHostingPlan(model.HostingPlan{ID: "monthly", Days: 30, PriceCents: 4000, Enabled: true}))
	inv, err := model.PutHostingInventory("cc", "alipay-test", true)
	require.NoError(t, err)
	order, err := model.CreateHostingOrder(user.Id, uuid.NewString(), "monthly", "", "codex", "alipay", f.client.Merchant(), []string{inv.ID})
	require.NoError(t, err)
	r := gin.New()
	r.POST("/notify", HostingAlipayNotify)
	r.GET("/orders", middleware.UserAuth(), GetHostingOrders)
	r.POST("/query", middleware.UserAuth(), middleware.SessionCookieOriginGuard(), QueryHostingAlipayPayment)
	r.POST("/pay", middleware.UserAuth(), middleware.SessionCookieOriginGuard(), RequestHostingPayment)
	base := url.Values{"app_id": {f.config.AppID}, "seller_id": {f.config.SellerID}, "notify_type": {"trade_status_sync"}, "notify_id": {"notice-one"}, "charset": {"utf-8"}, "out_trade_no": {order.ID}, "trade_no": {"2026110600000001"}, "trade_status": {"TRADE_SUCCESS"}, "total_amount": {"40.00"}, "sign_type": {"RSA2"}}
	callNotify := func(values url.Values, modify func(url.Values)) *httptest.ResponseRecorder {
		values.Set("sign", alipayTestSign(t, f.alipayKey, alipayTestCanonical(values, true)))
		if modify != nil {
			modify(values)
		}
		req := httptest.NewRequest("POST", "https://portal.test/notify", strings.NewReader(values.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	for _, tc := range []struct{ key, value string }{{"total_amount", "0.01"}, {"seller_id", "2088000000000002"}, {"app_id", "2026000000000002"}, {"sign_type", "RSA"}, {"alipay_cert_sn", "unknown"}, {"out_trade_no", "HPunknown"}, {"credit_pay_mode", "creditAdvanceV2"}, {"auth_trade_pay_mode", "CREDIT_PREAUTH_PAY"}} {
		values := maps.Clone(base)
		values.Set(tc.key, tc.value)
		assert.Equal(t, 400, callNotify(values, nil).Code, tc.key)
	}
	assert.Equal(t, 400, callNotify(maps.Clone(base), func(v url.Values) { v.Add("total_amount", "0.01") }).Code)
	assert.Equal(t, 400, callNotify(maps.Clone(base), func(v url.Values) { v.Set("total_amount", "80.00") }).Code)
	_, err = model.GetHostingGrant(user.Id, order.SeatID)
	require.Error(t, err)
	for range 2 {
		w := callNotify(maps.Clone(base), nil)
		require.Equal(t, 200, w.Code)
		require.Equal(t, "success", w.Body.String())
	}
	grant, err := model.GetHostingGrant(user.Id, order.SeatID)
	require.NoError(t, err)
	assert.Equal(t, int64(30*86400), grant.EndsAt-grant.StartsAt)
	// 新旧支付渠道可以并存，但订单金额、期限与商户取服务端快照，不取前端隐藏字段。
	confirmPaymentComplianceForTest(t)
	oldServer, oldCallback := system_setting.ServerAddress, operation_setting.CustomCallbackAddress
	system_setting.ServerAddress = "https://portal.test"
	operation_setting.CustomCallbackAddress = ""
	t.Cleanup(func() {
		system_setting.ServerAddress = oldServer
		operation_setting.CustomCallbackAddress = oldCallback
	})
	token, _, err := service.IssueAccessToken(identity)
	require.NoError(t, err)
	request := func(path, origin, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "http://portal.test"+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	input, err := common.Marshal(map[string]any{"request_id": uuid.NewString(), "plan_id": "monthly", "seat_id": order.SeatID, "platform": "codex", "method": "alipay", "price_cents": 1})
	require.NoError(t, err)
	w := request("/pay", "http://portal.test", string(input))
	require.Equal(t, 200, w.Code, w.Body.String())
	var checkout struct {
		Success bool `json:"success"`
		Data    struct {
			Order  model.HostingOrder `json:"order"`
			Params map[string]string  `json:"params"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(w.Body.Bytes(), &checkout))
	require.True(t, checkout.Success, w.Body.String())
	assert.Equal(t, int64(4000), checkout.Data.Order.PriceCents)
	assert.Equal(t, "https://portal.test/api/gpt-hosting/payment/alipay/notify", checkout.Data.Params["notify_url"])
	assert.Equal(t, "https://portal.test/wallet?tab=hosting", checkout.Data.Params["return_url"])
	assert.NotContains(t, w.Body.String(), "app_private_key")
	foreign := model.HostingOrder{ID: "HPforeign", UserID: user.Id + 1, RequestUser: user.Id + 1, RequestID: uuid.NewString(), Merchant: f.client.Merchant(), Method: "alipay", Status: "pending"}
	require.NoError(t, model.DB.Create(&foreign).Error)
	assert.Equal(t, 409, request("/query", "http://portal.test", `{"order_id":"HPforeign"}`).Code)
	assert.Equal(t, 403, request("/query", "https://foreign.test", `{"order_id":"`+order.ID+`"}`).Code)
	query := httptest.NewRequest("GET", "http://portal.test/orders", nil)
	query.Header.Set("Authorization", "Bearer "+token)
	list := httptest.NewRecorder()
	r.ServeHTTP(list, query)
	assert.Contains(t, list.Body.String(), `"can_query_payment":true`)
	assert.NotContains(t, list.Body.String(), f.config.SellerID)
	// 退款先到时只标记人工核对，随后的支付成功重放不能延长已存在的名额。
	refund := maps.Clone(base)
	refund.Set("out_trade_no", checkout.Data.Order.ID)
	refund.Set("trade_no", "2026110600000002")
	refund.Set("refund_fee", "40.00")
	require.Equal(t, 200, callNotify(refund, nil).Code)
	delete(refund, "refund_fee")
	require.Equal(t, 200, callNotify(refund, nil).Code)
	unchanged, err := model.GetHostingGrant(user.Id, order.SeatID)
	require.NoError(t, err)
	assert.Equal(t, grant.EndsAt, unchanged.EndsAt)
	review, err := model.GetHostingOrder(user.Id, checkout.Data.Order.ID)
	require.NoError(t, err)
	assert.Equal(t, "paid_review", review.Status)
	f.config.Environment = "sandbox"
	raw, err := common.Marshal(f.config)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(f.configPath, raw, 0600))
	hostingAlipayClient = sync.OnceValues(func() (*hostingpay.Client, error) { return hostingpay.Load(f.configPath) })
	_, available := hostingPaymentMerchant("alipay")
	assert.False(t, available)
	assert.Equal(t, 400, callNotify(maps.Clone(base), nil).Code)
}

// TestHostingAlipayQueryCompensation 验证漏通知补偿走相同事务，签名正确但金额不符仍不得发放权益。
func TestHostingAlipayQueryCompensation(t *testing.T) {
	f := newAlipayFixture(t)
	user, _ := setupSecurityEnrollmentTest(t)
	oldOptions, oldRatio := common.OptionMap, ratio_setting.GroupRatio2JSONString()
	common.OptionMap = map[string]string{}
	t.Cleanup(func() { common.OptionMap = oldOptions; _ = ratio_setting.UpdateGroupRatioByJSONString(oldRatio) })
	require.NoError(t, model.DB.AutoMigrate(&model.HostingPlan{}, &model.HostingOrder{}, &model.GPTHostingGrant{}, &model.GPTHostingBinding{}, &model.GPTHostingInventory{}, &model.VM2APIEnrollment{}, &model.Token{}))
	require.NoError(t, model.MigrateHostingStore(model.DB))
	require.NoError(t, model.SaveHostingPlan(model.HostingPlan{ID: "monthly", Days: 30, PriceCents: 4000, Enabled: true}))
	inv, err := model.PutHostingInventory("cc", "query-compensation", true)
	require.NoError(t, err)
	order, err := model.CreateHostingOrder(user.Id, uuid.NewString(), "monthly", "", "codex", "alipay", f.client.Merchant(), []string{inv.ID})
	require.NoError(t, err)
	oldTransport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = oldTransport })
	amount := "0.01"
	http.DefaultTransport = alipayTestTransport(func(r *http.Request) (*http.Response, error) {
		require.Equal(t, "openapi.alipay.com", r.URL.Host)
		raw, err := common.Marshal(map[string]string{"code": "10000", "trade_no": "2026110600000099", "out_trade_no": order.ID, "trade_status": "TRADE_SUCCESS", "total_amount": amount})
		require.NoError(t, err)
		body := bytes.NewBufferString(`{"alipay_trade_query_response":` + string(raw) + `,"sign":"` + alipayTestSign(t, f.alipayKey, raw) + `"}`)
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(body)}, nil
	})
	require.Error(t, reconcileHostingAlipayOrder(context.Background(), f.client, order))
	_, err = model.GetHostingGrant(user.Id, order.SeatID)
	require.Error(t, err)
	amount = "40.00"
	for range 2 {
		require.NoError(t, reconcileHostingAlipayOrder(context.Background(), f.client, order))
	}
	grant, err := model.GetHostingGrant(user.Id, order.SeatID)
	require.NoError(t, err)
	assert.Equal(t, int64(30*86400), grant.EndsAt-grant.StartsAt)
}
