// Package hostingpay adapts Alipay's official web payment protocol for hosting orders.
package hostingpay

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/smartwalle/alipay/v3"
)

var (
	ErrConfig  = errors.New("Alipay hosting configuration is unavailable")
	ErrPayment = errors.New("Alipay payment could not be verified")
)

// Config 仅由服务器文件提供；浏览器不能选择网关、商户或密钥路径。
// Enabled 只控制新付款，关闭销售后仍接受已付款订单的通知与查询。
type Config struct {
	Enabled        bool   `json:"enabled"`
	Environment    string `json:"environment"`
	AppID          string `json:"app_id"`
	SellerID       string `json:"seller_id"`
	PrivateKeyFile string `json:"app_private_key_file"`
	PublicKeyFile  string `json:"alipay_public_key_file"`
}

// Client 固定绑定一个环境、应用和收款人，初始化后不再修改配置。
type Client struct {
	config Config
	sdk    *alipay.Client
}

// Payment 只保留交付所需字段，不向业务层传播买家身份、签名或原始响应。
type Payment struct {
	OrderID     string
	TradeID     string
	AmountCents int64
	Paid        bool
	NeedsReview bool
}

// readConfigFile 限制文件大小并拒绝符号链接和宽权限私钥；错误不包含文件内容。
func readConfigFile(path string, private bool) ([]byte, error) {
	if !filepath.IsAbs(path) {
		return nil, ErrConfig
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<10 || (private && info.Mode().Perm()&0077 != 0) {
		return nil, ErrConfig
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, ErrConfig
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, ErrConfig
	}
	b, err := io.ReadAll(io.LimitReader(f, (64<<10)+1))
	if err != nil || len(b) > 64<<10 {
		return nil, ErrConfig
	}
	return b, nil
}

// Load 读取服务端挂载文件；未配置时禁用该渠道，错误配置不静默切换环境。
func Load(path string) (*Client, error) {
	if path == "" {
		return nil, nil
	}
	raw, err := readConfigFile(path, true)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if common.Unmarshal(raw, &cfg) != nil || (cfg.Environment != "production" && cfg.Environment != "sandbox") ||
		!decimalID(cfg.AppID) || !decimalID(cfg.SellerID) {
		return nil, ErrConfig
	}
	privateKey, err := readConfigFile(cfg.PrivateKeyFile, true)
	if err != nil {
		return nil, err
	}
	publicKey, err := readConfigFile(cfg.PublicKeyFile, false)
	if err != nil {
		return nil, err
	}
	// 使用 SDK 内置的固定官方网关和默认 TLS 验证；禁止重定向携带签名到其他地址。
	httpClient := &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	sdk, err := alipay.New(cfg.AppID, strings.TrimSpace(string(privateKey)), cfg.Environment == "production",
		alipay.WithHTTPClient(httpClient), alipay.WithTimeLocation(time.FixedZone("Asia/Shanghai", 8*3600)))
	if err != nil {
		return nil, ErrConfig
	}
	if sdk.LoadAliPayPublicKey(strings.TrimSpace(string(publicKey))) != nil {
		return nil, ErrConfig
	}
	return &Client{config: cfg, sdk: sdk}, nil
}

// decimalID 限制应用和收款人标识，避免命名空间分隔符进入订单的商户快照。
func decimalID(value string) bool {
	if len(value) < 16 || len(value) > 32 {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// Merchant 同时绑定协议、环境、应用和收款人，防止 Epay 或沙箱通知结算生产订单。
func (c *Client) Merchant() string {
	return "alipay:" + c.config.Environment + ":" + c.config.AppID + ":" + c.config.SellerID
}

// TradeKey 支付宝交易号在同一环境全局唯一；环境前缀隔离沙箱，并保持数据库字段长度。
func (c *Client) TradeKey(id string) string { return "alipay:" + c.config.Environment + ":" + id }

// Enabled 返回销售开关；调用方仍须检查站点支付声明和回调地址。
func (c *Client) Enabled() bool { return c != nil && c.config.Enabled }

// Production 区分真实收款与沙箱；生产商城不接受沙箱资金发放权益。
func (c *Client) Production() bool { return c != nil && c.config.Environment == "production" }

// AmountCents 严格解析十进制人民币金额，不接受浮点、符号、指数或额外小数位。
func AmountCents(value string) (int64, error) {
	whole, fraction, hasFraction := strings.Cut(value, ".")
	if whole == "" || len(whole) > 9 || (hasFraction && (len(fraction) < 1 || len(fraction) > 2)) {
		return 0, ErrPayment
	}
	if !hasFraction {
		fraction = "00"
	} else if len(fraction) == 1 {
		fraction += "0"
	}
	for _, r := range whole + fraction {
		if r < '0' || r > '9' {
			return 0, ErrPayment
		}
	}
	cents, err := strconv.ParseInt(whole+fraction, 10, 64)
	if err != nil || cents < 1 || cents > 10000000000 {
		return 0, ErrPayment
	}
	return cents, nil
}

// PagePay 根据数据库订单生成 POST 表单字段；订单有效期固定，重试不能延长库存占用。
func (c *Client) PagePay(id string, cents int64, days int, expires int64, notifyURL, returnURL string) (string, map[string]string, error) {
	if !c.Enabled() || id == "" || len(id) > 40 || cents < 1 || cents > 10000000 || days < 1 || expires <= time.Now().Unix() {
		return "", nil, ErrPayment
	}
	for _, raw := range []string{notifyURL, returnURL} {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
			return "", nil, ErrPayment
		}
	}
	u, err := c.sdk.TradePagePay(alipay.TradePagePay{Trade: alipay.Trade{
		Subject: fmt.Sprintf("luobocode 账号托管 %d 天", days), OutTradeNo: id,
		TotalAmount: fmt.Sprintf("%d.%02d", cents/100, cents%100), ProductCode: "FAST_INSTANT_TRADE_PAY",
		SellerId: c.config.SellerID, TimeExpire: time.Unix(expires, 0).In(time.FixedZone("Asia/Shanghai", 8*3600)).Format("2006-01-02 15:04:05"),
		NotifyURL: notifyURL, ReturnURL: returnURL,
	}})
	if err != nil {
		return "", nil, ErrPayment
	}
	params := make(map[string]string)
	for key, values := range u.Query() {
		params[key] = values[0]
	}
	u.RawQuery = ""
	return u.String(), params, nil
}

// VerifyNotification 验证完整表单的 RSA2 签名和当前收款主体，不修改或丢弃未知签名字段。
// 退款等通知可确认接收，但不得被当作首次支付成功重新交付权益。
func (c *Client) VerifyNotification(ctx context.Context, values url.Values) (Payment, error) {
	for key, v := range values {
		if len(key) > 80 || len(v) != 1 || len(v[0]) > 16384 {
			return Payment{}, ErrPayment
		}
	}
	if values.Get("sign_type") != "RSA2" || values.Get("alipay_cert_sn") != "" ||
		values.Get("app_id") != c.config.AppID || values.Get("seller_id") != c.config.SellerID ||
		values.Get("notify_type") != "trade_status_sync" || values.Get("notify_id") == "" ||
		values.Get("credit_pay_mode") != "" || values.Get("auth_trade_pay_mode") != "" ||
		!strings.EqualFold(values.Get("charset"), "utf-8") {
		return Payment{}, ErrPayment
	}
	// 仅支持即时付款，信用/预授权成功不能交付；固定公钥模式不触发 SDK 的证书自动下载。
	if c.sdk.VerifySign(ctx, values) != nil {
		return Payment{}, ErrPayment
	}
	cents, err := AmountCents(values.Get("total_amount"))
	if err != nil {
		return Payment{}, err
	}
	payment := Payment{OrderID: values.Get("out_trade_no"), TradeID: values.Get("trade_no"), AmountCents: cents}
	if payment.OrderID == "" || len(payment.OrderID) > 40 || payment.TradeID == "" || len(payment.TradeID) > 64 {
		return Payment{}, ErrPayment
	}
	status := values.Get("trade_status")
	payment.Paid = status == "TRADE_SUCCESS" || status == "TRADE_FINISHED"
	for _, key := range []string{"out_biz_no", "out_request_no", "gmt_refund", "refund_fee", "refund_amount", "refund_status", "send_back_fee", "dback_status"} {
		if values.Get(key) != "" {
			payment.Paid = false
			payment.NeedsReview = true
		}
	}
	return payment, nil
}

// Query 通过当前应用签名查询订单，并让 SDK 验证支付宝响应签名；请求成功不等于付款成功。
func (c *Client) Query(ctx context.Context, id string) (Payment, error) {
	if id == "" || len(id) > 40 {
		return Payment{}, ErrPayment
	}
	rsp, err := c.sdk.TradeQuery(ctx, alipay.TradeQuery{OutTradeNo: id})
	if err != nil || rsp == nil {
		return Payment{}, ErrPayment
	}
	// 尚未打开收银台时支付宝可能没有交易；保持待支付，不能误作故障或已经付款。
	if rsp.SubCode == "ACQ.TRADE_NOT_EXIST" {
		return Payment{OrderID: id}, nil
	}
	if !rsp.IsSuccess() || rsp.OutTradeNo != id {
		return Payment{}, ErrPayment
	}
	if rsp.TransCurrency != "" && rsp.TransCurrency != "CNY" {
		return Payment{}, ErrPayment
	}
	cents, err := AmountCents(rsp.TotalAmount)
	if err != nil {
		return Payment{}, err
	}
	// 官方查询响应不保证返回 seller_id/app_id；固定应用凭据、已验签响应和本地商户快照共同限定归属。
	paid := rsp.TradeStatus == alipay.TradeStatusSuccess || rsp.TradeStatus == alipay.TradeStatusFinished
	if rsp.CreditPayMode != "" || rsp.AuthTradePayMode != "" {
		return Payment{}, ErrPayment
	}
	if rsp.TradeNo == "" || len(rsp.TradeNo) > 64 {
		return Payment{}, ErrPayment
	}
	return Payment{OrderID: id, TradeID: rsp.TradeNo, AmountCents: cents, Paid: paid}, nil
}
