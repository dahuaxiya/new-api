package billing_setting_test

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/stretchr/testify/require"
)

// TestRetailOfficialTextPrices 验证官方标准价格、缓存用量、长上下文边界及销售倍率，不覆盖管理员定价。
func TestRetailOfficialTextPrices(t *testing.T) {
	settings := config.GlobalConfig.Get("billing_setting").(*billing_setting.BillingSetting)
	saved := *settings
	savedRatios, savedPrices := ratio_setting.ModelRatio2JSONString(), ratio_setting.ModelPrice2JSONString()
	t.Cleanup(func() {
		*settings = saved
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(savedRatios))
		require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(savedPrices))
	})
	for _, tc := range []struct {
		model                    string
		standard, boundary, long int
	}{
		{"gpt-6.1-sol", 1500, 92000, 181502},
		{"gpt-6-sol", 1500, 102000, 201502},
		{"gpt-5.6-sol", 3000, 204000, 403004},
		{"gpt-5.6-terra", 1600, 103000, 203002},
	} {
		t.Run(tc.model, func(t *testing.T) {
			*settings = billing_setting.BillingSetting{BillingMode: map[string]string{}, BillingExpr: map[string]string{}}
			require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{}`))
			require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{}`))
			expression, ok := billing_setting.GetBillingExpr(tc.model)
			require.True(t, ok)
			for _, usageCase := range []struct{ input, output, cached, written, quota int }{
				{1000, 100, 0, 0, tc.standard},
				{272000, 1000, 200000, 20000, tc.boundary},
				{272001, 1000, 200000, 20000, tc.long},
			} {
				usage := &dto.Usage{PromptTokens: usageCase.input, CompletionTokens: usageCase.output,
					PromptTokensDetails: dto.InputTokenDetails{CachedTokens: usageCase.cached, CacheWriteTokens: usageCase.written}}
				for _, multiplier := range []int{1, 2} {
					result, err := billingexpr.ComputeTieredQuota(&billingexpr.BillingSnapshot{
						ExprString: expression, ExprHash: billingexpr.ExprHashString(expression), GroupRatio: float64(multiplier), QuotaPerUnit: 500000,
					}, service.BuildTieredTokenParams(usage, false, billingexpr.UsedVars(expression)))
					require.NoError(t, err)
					require.Equal(t, usageCase.quota*multiplier, result.ActualQuotaAfterGroup)
				}
			}
			// 已配置的价格是站长业务决策，增加官方默认价不能改变旧客户账单。
			raw, err := common.Marshal(map[string]float64{tc.model: 0})
			require.NoError(t, err)
			require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(string(raw)))
			require.Equal(t, billing_setting.BillingModeRatio, billing_setting.GetBillingMode(tc.model))
			settings.BillingMode[tc.model] = billing_setting.BillingModeTieredExpr
			settings.BillingExpr[tc.model] = `tier("custom", p * 7)`
			actual, ok := billing_setting.GetBillingExpr(tc.model)
			require.True(t, ok)
			require.Equal(t, `tier("custom", p * 7)`, actual)
		})
	}
}
