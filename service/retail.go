package service

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/retail_setting"
	"github.com/gin-gonic/gin"
)

// ApplyRetailRouting 在每次渠道选择时重载商业授权，HTTP 重试与 WebSocket 消息共用。
// 分组只能由服务器配置决定，过滤器同样约束手动 pin 和亲和渠道。
func ApplyRetailRouting(c *gin.Context, retry *RetryParam) error {
	group, err := model.RetailTokenGroup(c.GetInt("id"), c.GetInt("token_id"))
	if err != nil {
		return err
	}
	constraints := GetChannelConstraints(c)
	// 长连接每条消息重读白名单；删除上条消息的副本，避免旧白名单永久限制或放行。
	filters := constraints.Filters[:0]
	for _, filter := range constraints.Filters {
		if filter.Kind != dto.FilterRetailSupply {
			filters = append(filters, filter)
		}
	}
	constraints.Filters = filters
	if group == "" {
		ids, loadErr := model.RetailReservedSupplyIDs()
		if loadErr != nil {
			// 配置损坏时无法判断哪些渠道属于商业供货，不能放行旧 Key 绕过账本。
			// 因此显式配置错误会阻断旧业务选路；运维必须原子替换文件，未设置配置且没有供货分组时不影响原业务。
			return loadErr
		}
		// 原有 Key 即使被改到销售组或命中亲和渠道，也不能绕过商业账本使用供货。
		// 停售仍保留这一隔离；站点子分组即使失去有效父站点，也不能进入旧钱包选路。
		if len(ids) > 0 {
			constraints.AddFilter(dto.ChannelFilter{Kind: dto.FilterRetailSupply, ExcludedChannelIDs: ids})
		}
		return nil
	}
	ids, err := model.RetailSupplyIDs(group)
	if err != nil || len(ids) == 0 {
		return retail_setting.ErrUnavailable
	}
	// Product authorization and current prices narrow the pre-existing public supply allowlist.
	quote, err := model.RetailQuote(c.GetInt("id"), c.GetInt("token_id"), retry.ModelName, 1, ids, c.GetString("retail_studio_product"))
	if err != nil {
		return err
	}
	if quote != nil {
		ids = quote.Channels
	}
	constraints.AddFilter(dto.ChannelFilter{Kind: dto.FilterRetailSupply, AllowedChannelIDs: ids})
	retry.TokenGroup = group
	common.SetContextKey(c, constant.ContextKeyUsingGroup, group)
	common.SetContextKey(c, constant.ContextKeyUserGroup, group)
	common.SetContextKey(c, constant.ContextKeyTokenGroup, group)
	common.SetContextKey(c, constant.ContextKeyTokenCrossGroupRetry, false)
	c.Set("retail_request", true)
	return nil
}
