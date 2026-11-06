package controller

import (
	"context"
	"mime"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/hostingpay"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
)

// 配置与签名密钥在进程内固定；更换收款主体必须重启，不能让一次下单混用两套身份。
var hostingAlipayClient = sync.OnceValues(func() (*hostingpay.Client, error) {
	return hostingpay.Load(os.Getenv("ALIPAY_HOSTING_CONFIG"))
})

// hostingPaymentMerchant 选择新订单的收款方，官方配置有误时不降级到另一个商户。
func hostingPaymentMerchant(method string) (string, bool) {
	if method == "alipay" && os.Getenv("ALIPAY_HOSTING_CONFIG") != "" {
		client, err := hostingAlipayClient()
		if err != nil || !client.Enabled() || !client.Production() {
			return "", false
		}
		return client.Merchant(), true
	}
	if method != "alipay" && method != "wxpay" {
		return "", false
	}
	_, err := hostingPaymentURL(operation_setting.PayAddress)
	return operation_setting.EpayId, err == nil && operation_setting.ContainsPayMethod(method) && GetEpayClient() != nil
}

// hostingPaymentMethods 只展示当前可签发订单的渠道，未配置的微信支付不因支付宝开通而出现。
func hostingPaymentMethods() []string {
	methods := []string{}
	for _, method := range []string{"wxpay", "alipay"} {
		if _, ok := hostingPaymentMerchant(method); ok {
			methods = append(methods, method)
		}
	}
	return methods
}

// HostingAlipayNotify 接收官方 POST 表单；仅在完整验签和订单核对之后进入原有交付事务。
func HostingAlipayNotify(c *gin.Context) {
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
	client, err := hostingAlipayClient()
	if err != nil || client == nil || !client.Production() {
		c.String(400, "fail")
		return
	}
	payment, err := client.VerifyNotification(c.Request.Context(), c.Request.PostForm)
	if err != nil {
		c.String(400, "fail")
		return
	}
	order, err := model.GetHostingPaymentOrder(payment.OrderID)
	if err != nil || order.Merchant != client.Merchant() || order.Method != "alipay" || order.PriceCents != payment.AmountCents {
		c.String(400, "fail")
		return
	}
	if payment.NeedsReview {
		// 退款通知先于付款通知抵达时保留人工核对状态，后续付款重放或查询不能再自动发名额。
		err = model.ReviewHostingPayment(order.ID, client.Merchant(), client.TradeKey(payment.TradeID), payment.AmountCents)
		if err != nil {
			c.String(400, "fail")
			return
		}
	} else if payment.Paid {
		// 持久化商户/订单/流水共同提供幂等，重复通知和查询竞争均只延长一次权益。
		_, err = model.CompleteHostingOrder(payment.OrderID, client.Merchant(), "alipay", client.TradeKey(payment.TradeID), payment.AmountCents)
		if err != nil {
			c.String(400, "fail")
			return
		}
	}
	c.String(200, "success")
}

// reconcileHostingAlipayOrder 只查询当前商户的订单，网络或验签失败保留原状态等待重试。
func reconcileHostingAlipayOrder(ctx context.Context, client *hostingpay.Client, order *model.HostingOrder) error {
	if order.Merchant != client.Merchant() || order.Method != "alipay" {
		return model.ErrHostingConflict
	}
	if order.Status == "paid" || order.Status == "paid_review" {
		return nil
	}
	payment, err := client.Query(ctx, order.ID)
	if err != nil {
		return err
	}
	if !payment.Paid {
		return nil
	}
	_, err = model.CompleteHostingOrder(order.ID, client.Merchant(), "alipay", client.TradeKey(payment.TradeID), payment.AmountCents)
	return err
}

// QueryHostingAlipayPayment 允许已登录用户核实本人订单；即使知道他人订单号也不能查询或交付。
func QueryHostingAlipayPayment(c *gin.Context) {
	id, ok := hostingActor(c, false)
	if !ok {
		return
	}
	var input struct {
		OrderID string `json:"order_id"`
	}
	if !onboardingBody(c, &input) {
		return
	}
	order, err := model.GetHostingOrder(id, input.OrderID)
	if err != nil {
		hostingError(c, model.ErrHostingConflict)
		return
	}
	client, err := hostingAlipayClient()
	if err != nil || client == nil || !client.Production() {
		common.ApiErrorT(c, "Hosting payments are not available yet.")
		return
	}
	if err = reconcileHostingAlipayOrder(c.Request.Context(), client, order); err != nil {
		hostingError(c, err)
		return
	}
	order, err = model.GetHostingOrder(id, input.OrderID)
	if err != nil {
		hostingError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": order})
}

// StartHostingAlipayReconciliation 补偿最近七天未确认订单，销售关闭后继续处理已经付出的款项。
// 逐页游标避免未支付订单长期占据首批；单轮有超时，不与下一轮重叠，也不自动退款。
func StartHostingAlipayReconciliation() {
	if os.Getenv("GPT_HOSTING_ENABLED") != "true" {
		return
	}
	client, err := hostingAlipayClient()
	if err != nil {
		common.SysError("Alipay hosting configuration is unavailable; official Alipay payments remain disabled")
		return
	}
	if client == nil || !client.Production() {
		return
	}
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		cursor := ""
		for range ticker.C {
			orders, err := model.PendingHostingPayments(client.Merchant(), cursor, time.Now().Unix())
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
				_ = reconcileHostingAlipayOrder(ctx, client, &orders[i])
				cursor = orders[i].ID
			}
			cancel()
		}
	}()
}
