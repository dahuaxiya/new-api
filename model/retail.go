package model

import (
	"gorm.io/gorm"
	"os"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/QuantumNous/new-api/setting/retail_setting"
)

const TokenServiceRetail = "retail"
const TokenServiceStudio = "retail_studio"
const TokenServiceHosting = "hosting"

// RetailTokenGroup 从数据库核对 Key 的归属与业务类型；空类型保留发布版的自动托管行为。
// 每次请求及 WebSocket 消息都重新查询，停用销售和撤销 Key 不依赖令牌缓存过期。
func RetailTokenGroup(userID, tokenID int) (string, error) {
	if tokenID <= 0 {
		return "", nil
	}
	var token Token
	if err := DB.Select("id", "user_id", "service_mode", "hosting_seat_id", "status", "expired_time").First(&token, "id = ? AND user_id = ?", tokenID, userID).Error; err != nil {
		return "", retail_setting.ErrUnavailable
	}
	switch token.ServiceMode {
	case "":
		return "", nil
	case TokenServiceHosting:
		// 显式托管 Key 在托管功能关闭时必须拒绝，不能意外落入普通钱包计费。
		if os.Getenv("GPT_HOSTING_ENABLED") != "true" || token.HostingSeatID == "" {
			return "", retail_setting.ErrUnavailable
		}
		return "", nil
	case TokenServiceRetail, TokenServiceStudio:
	default:
		return "", retail_setting.ErrUnavailable
	}
	if token.HostingSeatID != "" || token.Status != common.TokenStatusEnabled || token.ExpiredTime != -1 && token.ExpiredTime < common.GetTimestamp() {
		return "", retail_setting.ErrUnavailable
	}
	cfg, err := retail_setting.Load()
	if err != nil || !cfg.Enabled || common.RedisEnabled || common.BatchUpdateEnabled || !retailRoutingGroupAvailable(cfg.Group) {
		return "", retail_setting.ErrUnavailable
	}
	return cfg.Group, nil
}

// RetailSupplyIDs 只允许显式登记、属于销售组且未被托管客户占用的渠道进入候选列表。
// 重新查库防止渠道缓存或后台误改分组将客户账号混入公共供货。
func RetailSupplyIDs(group string) ([]int, error) {
	return retailSupplyIDsIn(DB, group)
}

// retailSupplyIDsIn includes pending channel creation only inside its publishing transaction.
func retailSupplyIDsIn(db *gorm.DB, group string) ([]int, error) {
	cfg, err := RetailSupplyConfig(db)
	if err != nil || !cfg.Enabled || cfg.Group != group {
		return nil, retail_setting.ErrUnavailable
	}
	ids := make([]int, 0, len(cfg.Supplies))
	for _, supply := range cfg.Supplies {
		ids = append(ids, supply.ChannelID)
	}
	var channels []Channel
	if err = db.Select("id", "group", "status", "type", "base_url", "retail_key_target", "retail_key_id", "retail_site_id", "channel_info").Where("id IN ?", ids).Find(&channels).Error; err != nil {
		return nil, err
	}
	owners, err := retailKeyOwners(db, channels)
	if err != nil {
		return nil, err
	}
	var bound []int
	if err = db.Model(&GPTHostingBinding{}).Where("channel_id IN ?", ids).Pluck("channel_id", &bound).Error; err != nil {
		return nil, err
	}
	blocked := map[int]bool{}
	for _, id := range bound {
		blocked[id] = true
	}
	allowed := []int{}
	for _, channel := range channels {
		// A legacy foreign reference remains detachable, but cannot be offered for a new request.
		if channel.RetailKeyID != 0 && !channel.hasSupplyKey(owners) {
			continue
		}
		// 首版仅验收 OpenAI 兼容和 Claude 适配器；其他厂商的用量与退款语义需单独验证。
		if channel.Type != constant.ChannelTypeOpenAI && channel.Type != constant.ChannelTypeAnthropic {
			continue
		}
		matches, private := false, false
		for item := range strings.SplitSeq(channel.Group, ",") {
			matches = matches || item == group
			private = private || strings.HasPrefix(item, "acct_")
		}
		if matches && !private && !blocked[channel.Id] && channel.Status == common.ChannelStatusEnabled {
			allowed = append(allowed, channel.Id)
		}
	}
	return allowed, nil
}

// NormalizeTokenService 固定 Key 的计费语义；更换模式必须创建新 Key，避免旧客户端静默开始收费。
func NormalizeTokenService(userID int, mode, seatID string) (string, error) {
	switch mode {
	case TokenServiceRetail:
		cfg, err := retail_setting.Load()
		if err != nil || !cfg.Enabled || common.RedisEnabled || common.BatchUpdateEnabled || seatID != "" || !retailRoutingGroupAvailable(cfg.Group) {
			return "", retail_setting.ErrUnavailable
		}
		return "", nil
	case "", TokenServiceHosting:
		seat, err := SelectTokenHostingSeat(userID, seatID)
		if err != nil {
			return "", err
		}
		if mode == TokenServiceHosting && seat == "" {
			return "", ErrHostingUnavailable
		}
		return seat, nil
	default:
		return "", retail_setting.ErrUnavailable
	}
}

// retailRoutingGroupAvailable separates internal procurement routing from customer-facing
// legacy multipliers. Published pricebooks validate product access and CNY prices later;
// unpublished legacy billing still requires its original ratio entry and cannot bypass it.
func retailRoutingGroupAvailable(group string) bool {
	if ratio_setting.ContainsGroupRatio(group) {
		return true
	}
	book, version, err := GetRetailPriceBook()
	return err == nil && version > 0 && book.Enabled
}
