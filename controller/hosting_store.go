package controller

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Calcium-Ion/go-epay/epay"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
)

// hostingPaymentURL 只允许管理员配置的 HTTPS 地址，阻止付款表单降级或携带 URL 凭据。
func hostingPaymentURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, model.ErrHostingConflict
	}
	return u, nil
}

// hostingPaymentReady 只决定新订单是否可售；回调独立验签，关闭销售不能丢弃已经支付的订单。
func hostingPaymentReady() bool {
	_, gatewayErr := hostingPaymentURL(operation_setting.PayAddress)
	_, callbackErr := hostingPaymentURL(service.GetCallbackAddress())
	return operation_setting.IsPaymentComplianceConfirmed() && gatewayErr == nil && callbackErr == nil && GetEpayClient() != nil
}

// GetHostingStore 展示套餐和可用支付方式；不把商户 ID、密钥或服务器地址返回到目录接口。
func GetHostingStore(c *gin.Context) {
	if _, ok := hostingActor(c, false); !ok {
		return
	}
	plans, err := model.ListHostingPlans()
	if err != nil {
		hostingError(c, err)
		return
	}
	methods := []string{}
	for _, m := range []string{"wxpay", "alipay"} {
		if operation_setting.ContainsPayMethod(m) {
			methods = append(methods, m)
		}
	}
	c.JSON(200, gin.H{"success": true, "data": gin.H{"plans": plans, "payment_ready": hostingPaymentReady(), "methods": methods}})
}

// hostingOrderPage 限制分页范围，避免恶意大 offset；固定每页二十笔。
func hostingOrderPage(c *gin.Context) int {
	p, _ := strconv.Atoi(c.Query("page"))
	return min(max(p, 1), 10000)
}

// GetHostingOrders 订单查询始终使用当前会话的用户 ID，忽略浏览器伪造的归属字段。
func GetHostingOrders(c *gin.Context) {
	id, ok := hostingActor(c, false)
	if !ok {
		return
	}
	rows, total, err := model.ListHostingOrders(id, hostingOrderPage(c))
	if err != nil {
		hostingError(c, err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": gin.H{"items": rows, "total": total}})
}

// GetHostingStoreAdmin 将套餐和订单放在原用户管理内；只有 root 会话能访问付款归属记录。
func GetHostingStoreAdmin(c *gin.Context) {
	if _, ok := hostingActor(c, true); !ok {
		return
	}
	plans, err := model.ListHostingPlans()
	if err != nil {
		hostingError(c, err)
		return
	}
	rows, total, err := model.ListHostingOrders(0, hostingOrderPage(c))
	if err != nil {
		hostingError(c, err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": gin.H{"plans": plans, "payment_ready": hostingPaymentReady(), "items": rows, "total": total}})
}

// PutHostingStorePlan 改价和上架需要当前 root 的一次性安全证明；历史订单快照保持不变。
func PutHostingStorePlan(c *gin.Context) {
	actor, ok := hostingActor(c, true)
	if !ok {
		return
	}
	var input model.HostingPlan
	if !onboardingBody(c, &input) {
		return
	}
	if !hostingProof(c, "store-plan", input.ID) {
		return
	}
	if err := model.SaveHostingPlan(input); err != nil {
		hostingError(c, err)
		return
	}
	recordHostingUserAudit(c, actor, "hosting.plan", map[string]any{"plan_id": input.ID, "days": input.Days, "price_cents": input.PriceCents, "enabled": input.Enabled})
	c.JSON(200, gin.H{"success": true})
}

// hostingPurchaseCandidates 在下单前确认空槽及出口真实可用，远端失败不能解释成有库存。
func hostingPurchaseCandidates(ctx context.Context, platform string) []string {
	rows, err := model.ListHostingInventory()
	if err != nil {
		return nil
	}
	registry, err := onboardingRegistry()
	if err != nil {
		return nil
	}
	result := []string{}
	for _, instance := range registry.Instances() {
		client, err := registry.Client(instance.ID)
		if err != nil || len(client.HostingModels(platform)) == 0 {
			continue
		}
		local, cancel := context.WithTimeout(ctx, 10*time.Second)
		slots, err := client.HostedSlots(local)
		cancel()
		if err != nil {
			continue
		}
		for _, inv := range rows {
			if !inv.Enabled || inv.InstanceID != instance.ID || (inv.Platform != platform && !(inv.Platform == "" && platform == "codex")) {
				continue
			}
			for _, slot := range slots {
				if slot.ID == inv.SlotID && slot.Empty && slot.ExitReady && slot.Hosting != nil && slot.Hosting.State == "inventory" {
					result = append(result, inv.ID)
				}
			}
		}
	}
	return result
}

// RequestHostingPayment 只接收套餐与目标名额，价格和期限由数据库确定；支付参数只为本人待支付订单签发。
func RequestHostingPayment(c *gin.Context) {
	id, ok := hostingActor(c, false)
	if !ok {
		return
	}
	if !hostingPaymentReady() {
		common.ApiErrorT(c, "Hosting payments are not available yet.")
		return
	}
	var input struct {
		RequestID string `json:"request_id"`
		PlanID    string `json:"plan_id"`
		SeatID    string `json:"seat_id"`
		Platform  string `json:"platform"`
		Method    string `json:"method"`
		OrderID   string `json:"order_id"`
	}
	if !onboardingBody(c, &input) {
		return
	}
	var order *model.HostingOrder
	var err error
	if input.OrderID != "" {
		order, err = model.GetHostingOrder(id, input.OrderID)
	} else {
		if !operation_setting.ContainsPayMethod(input.Method) || (input.Method != "wxpay" && input.Method != "alipay") {
			hostingError(c, model.ErrHostingConflict)
			return
		}
		candidates := []string{}
		needsSlot := input.SeatID == ""
		if input.SeatID != "" {
			b, e := model.GetHostingBinding(id, input.SeatID)
			if e != nil {
				hostingError(c, model.ErrHostingConflict)
				return
			}
			needsSlot = b.InventoryID == nil
		}
		if needsSlot {
			candidates = hostingPurchaseCandidates(c.Request.Context(), input.Platform)
		}
		order, err = model.CreateHostingOrder(id, input.RequestID, input.PlanID, input.SeatID, input.Platform, input.Method, operation_setting.EpayId, candidates)
	}
	if err != nil {
		if err == model.ErrHostingSoldOut {
			common.ApiErrorT(c, "No hosting slots are available for this account type.")
		} else {
			hostingError(c, err)
		}
		return
	}
	if order.Status != "pending" || order.ExpiresAt <= time.Now().Unix() || order.Merchant != operation_setting.EpayId || !operation_setting.ContainsPayMethod(order.Method) {
		common.ApiErrorT(c, "This order can no longer be paid. Refresh the order list.")
		return
	}
	base, _ := hostingPaymentURL(service.GetCallbackAddress())
	notify := *base
	notify.Path = strings.TrimRight(notify.Path, "/") + "/api/gpt-hosting/payment/notify"
	back := *base
	// 浏览器返回只打开账单中的托管订单；名额发放仍只接受已验签的服务端通知。
	back.Path = strings.TrimRight(back.Path, "/") + "/wallet"
	back.RawQuery = "tab=hosting"
	uri, params, err := GetEpayClient().Purchase(&epay.PurchaseArgs{Type: order.Method, ServiceTradeNo: order.ID, Name: fmt.Sprintf("Hosting %d days", order.Days), Money: fmt.Sprintf("%d.%02d", order.PriceCents/100, order.PriceCents%100), Device: epay.PC, NotifyUrl: &notify, ReturnUrl: &back})
	if err != nil {
		hostingError(c, err)
		return
	}
	if _, err = hostingPaymentURL(uri); err != nil {
		hostingError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"url": uri, "params": params, "order": order}})
}

// HostingPaymentNotify 只根据有签名的服务端到账通知发放权益；浏览器返回页面永远不触发发放。
// 拒绝重复参数和非十进制金额，校验商户/币种口径/付款类型，避免签名解释差异或小额订单顶替。
func HostingPaymentNotify(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16<<10)
	if err := c.Request.ParseForm(); err != nil {
		c.String(400, "fail")
		return
	}
	values := c.Request.URL.Query()
	if c.Request.Method == "POST" {
		values = c.Request.PostForm
		if len(c.Request.URL.Query()) != 0 {
			c.String(400, "fail")
			return
		}
	}
	params := map[string]string{}
	for key, values := range values {
		if len(values) != 1 || len(key) > 80 || len(values[0]) > 2048 {
			c.String(400, "fail")
			return
		}
		params[key] = values[0]
	}
	client := GetEpayClient()
	if client == nil || params["sign_type"] != "MD5" || params["pid"] != operation_setting.EpayId {
		c.String(400, "fail")
		return
	}
	verified, err := client.Verify(params)
	if err != nil || !verified.VerifyStatus || verified.TradeStatus != epay.StatusTradeSuccess {
		c.String(400, "fail")
		return
	}
	amount := strings.Split(verified.Money, ".")
	if len(amount) > 2 || amount[0] == "" || len(amount[0]) > 7 {
		c.String(400, "fail")
		return
	}
	fraction := "00"
	if len(amount) == 2 {
		if len(amount[1]) < 1 || len(amount[1]) > 2 {
			c.String(400, "fail")
			return
		}
		fraction = (amount[1] + "0")[:2]
	}
	for _, v := range amount[0] + fraction {
		if v < '0' || v > '9' {
			c.String(400, "fail")
			return
		}
	}
	cents, err := strconv.ParseInt(amount[0]+fraction, 10, 64)
	if err != nil {
		c.String(400, "fail")
		return
	}
	_, err = model.CompleteHostingOrder(verified.ServiceTradeNo, params["pid"], verified.Type, verified.TradeNo, cents)
	if err != nil {
		c.String(400, "fail")
		return
	}
	// 远端策略沿用现有 30 秒维护任务重试；回调不被跨主机请求拖慢，也不重复续期。
	c.String(200, "success")
}
