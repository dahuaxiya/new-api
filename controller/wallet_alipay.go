package controller

import (
	"context"
	"math"
	"mime"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/service/hostingpay"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/QuantumNous/new-api/setting/retail_setting"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
)

var walletAlipayClient = sync.OnceValues(func() (*hostingpay.Client, error) { return hostingpay.Load(os.Getenv("ALIPAY_WALLET_CONFIG")) })

// walletAlipayEnabled 只控制新订单；未配置商户时不显示可付款入口。
func walletAlipayEnabled() bool {
	client, err := walletAlipayClient()
	return err == nil && client != nil && client.Enabled() && client.Production() && operation_setting.IsPaymentComplianceConfirmed()
}

// requestWalletAlipay 在服务器计算并冻结充值金额，使用已有钱包表单返回格式。
func requestWalletAlipay(c *gin.Context, amount int64) {
	if !walletAlipayEnabled() || amount < getMinTopup() {
		paymentError(c, common.NewMessage("Wallet payments are unavailable"))
		return
	}
	quota, err := validateTopUpQuota(amount)
	if err != nil {
		paymentError(c, err)
		return
	}
	group, err := model.GetUserGroup(c.GetInt("id"), false)
	if err != nil {
		paymentError(c, model.ErrWalletPayment)
		return
	}
	money := getPayMoney(amount, group)
	if math.IsNaN(money) || math.IsInf(money, 0) || money < 0.01 || money > 100000 {
		paymentError(c, model.ErrWalletPayment)
		return
	}
	cents, err := hostingpay.AmountCents(strconv.FormatFloat(money, 'f', 2, 64))
	if err != nil {
		paymentError(c, model.ErrWalletPayment)
		return
	}
	client, _ := walletAlipayClient()
	storedAmount := amount
	if operation_setting.GetQuotaDisplayType() == operation_setting.QuotaDisplayTypeTokens {
		// 历史列表沿用整单位展示；实际入账使用上方冻结的 quota，不从展示值反算。
		storedAmount = decimal.NewFromInt(int64(quota)).Div(decimal.NewFromFloat(common.QuotaPerUnit)).IntPart()
	}
	order, err := model.CreateWalletPayment(c.GetInt("id"), client.Merchant(), cents, quota, storedAmount)
	if err != nil {
		paymentError(c, err)
		return
	}
	uri, params, err := client.WalletPagePay(order.OrderID, cents, order.ExpiresAt, service.GetCallbackAddress()+"/api/wallet/alipay/notify", paymentReturnPath("/wallet"))
	if err != nil {
		paymentError(c, model.ErrWalletPayment)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "success", "url": uri, "data": params})
}

// WalletAlipayNotify 只接受有限长度的唯一 POST 字段；所有字段验签后才进入订单交付事务。
func WalletAlipayNotify(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	contentType, _, err := mime.ParseMediaType(c.GetHeader("Content-Type"))
	if err != nil || contentType != "application/x-www-form-urlencoded" || c.Request.URL.RawQuery != "" {
		c.String(400, "fail")
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 64<<10)
	if c.Request.ParseForm() != nil {
		c.String(400, "fail")
		return
	}
	client, err := walletAlipayClient()
	if err != nil || client == nil || !client.Production() {
		c.String(400, "fail")
		return
	}
	payment, err := client.VerifyNotification(c.Request.Context(), c.Request.PostForm)
	if err != nil {
		c.String(400, "fail")
		return
	}
	if payment.Paid || payment.NeedsReview {
		if model.ApplyWalletPayment(payment.OrderID, client.Merchant(), client.TradeKey(payment.TradeID), payment.AmountCents, payment.NeedsReview) != nil {
			c.String(400, "fail")
			return
		}
	}
	c.String(200, "success")
}

// reconcileWalletPayment 使用官方查单补偿通知丢失；查询失败保留订单，不能用浏览器结果入账。
func reconcileWalletPayment(ctx context.Context, client *hostingpay.Client, order *model.WalletPayment) error {
	if order.Merchant != client.Merchant() {
		return model.ErrWalletPayment
	}
	if order.Status != "pending" {
		return nil
	}
	payment, err := client.Query(ctx, order.OrderID)
	if err != nil {
		return err
	}
	if !payment.Paid && !payment.NeedsReview {
		return nil
	}
	return model.ApplyWalletPayment(order.OrderID, client.Merchant(), client.TradeKey(payment.TradeID), payment.AmountCents, payment.NeedsReview)
}

// QueryWalletAlipay 仅允许门户用户补查本人订单，回包不包含商户或上游流水身份。
func QueryWalletAlipay(c *gin.Context) {
	var input struct {
		OrderID string `json:"order_id"`
	}
	if c.ShouldBindJSON(&input) != nil {
		common.ApiError(c, model.ErrWalletPayment)
		return
	}
	order, err := model.GetWalletPayment(c.GetInt("id"), input.OrderID)
	client, loadErr := walletAlipayClient()
	if err != nil || loadErr != nil || client == nil || !client.Production() {
		common.ApiError(c, model.ErrWalletPayment)
		return
	}
	if reconcileWalletPayment(c.Request.Context(), client, order) != nil {
		common.ApiError(c, model.ErrWalletPayment)
		return
	}
	order, err = model.GetWalletPayment(c.GetInt("id"), input.OrderID)
	if err != nil {
		common.ApiError(c, model.ErrWalletPayment)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": order})
}

// StartWalletAlipayReconciliation 定期补查未决订单；多实例允许并发查单，入账依靠数据库幂等。
func StartWalletAlipayReconciliation() {
	client, err := walletAlipayClient()
	if err != nil || client == nil || !client.Production() {
		return
	}
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		cursor := ""
		for range ticker.C {
			orders, err := model.PendingWalletPayments(client.Merchant(), cursor)
			if err != nil {
				continue
			}
			if len(orders) == 0 {
				cursor = ""
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			for i := range orders {
				if ctx.Err() != nil {
					break
				}
				_ = reconcileWalletPayment(ctx, client, &orders[i])
				cursor = orders[i].OrderID
			}
			cancel()
		}
	}()
}

// GetTokenServiceModes 只公开服务是否可选及可售模型，不暴露供货渠道、配置路径或凭据。
func GetTokenServiceModes(c *gin.Context) {
	cfg, err := retail_setting.Load()
	enabled := err == nil && cfg.Enabled && !common.RedisEnabled && !common.BatchUpdateEnabled && ratio_setting.ContainsGroupRatio(cfg.Group)
	models := []string{}
	if enabled {
		ids, e := model.RetailSupplyIDs(cfg.Group)
		if e != nil {
			enabled = false
		} else {
			for _, name := range model.GetGroupEnabledModels(cfg.Group) {
				for _, id := range ids {
					if model.IsChannelEnabledForGroupModel(cfg.Group, name, id) {
						models = append(models, name)
						break
					}
				}
			}
		}
	}
	enabled = enabled && len(models) > 0
	c.JSON(200, gin.H{"success": true, "data": gin.H{"retail": enabled, "models": models}})
}
