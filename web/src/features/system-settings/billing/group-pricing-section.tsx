/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { RatioSettingsCard } from '../models/ratio-settings-card'
import type { BillingSettings } from '../types'

/** 将分组表单单独暴露给管理后台页面，同时保持系统设置中的数据和保存逻辑不变。 */
export function GroupPricingSection(props: {
  settings: BillingSettings
  titleKey?: string
}) {
  const settings = props.settings
  return (
    <RatioSettingsCard
      titleKey={props.titleKey}
      modelDefaults={{
        ModelPrice: settings.ModelPrice,
        ModelRatio: settings.ModelRatio,
        CacheRatio: settings.CacheRatio,
        CreateCacheRatio: settings.CreateCacheRatio,
        CompletionRatio: settings.CompletionRatio,
        ImageRatio: settings.ImageRatio,
        AudioRatio: settings.AudioRatio,
        AudioCompletionRatio: settings.AudioCompletionRatio,
        ExposeRatioEnabled: settings.ExposeRatioEnabled,
        BillingMode: settings['billing_setting.billing_mode'],
        BillingExpr: settings['billing_setting.billing_expr'],
        PluginBillingExpr: settings['billing_setting.plugin_billing_expr'],
      }}
      groupDefaults={{
        TopupGroupRatio: settings.TopupGroupRatio,
        GroupRatio: settings.GroupRatio,
        UserUsableGroups: settings.UserUsableGroups,
        GroupGroupRatio: settings.GroupGroupRatio,
        AutoGroups: settings.AutoGroups,
        MaxTokenAutoGroups: settings.MaxTokenAutoGroups,
        DefaultUseAutoGroup: settings.DefaultUseAutoGroup,
        GroupSpecialUsableGroup:
          settings['group_ratio_setting.group_special_usable_group'],
      }}
      toolPricesDefault={settings['tool_price_setting.prices']}
      visibleTabs={['groups']}
    />
  )
}
