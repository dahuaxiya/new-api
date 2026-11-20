import { defaultBillingSettings } from '@/features/system-settings/billing/defaults'
import { GroupPricingSection } from '@/features/system-settings/billing/group-pricing-section'
import { SettingsPage } from '@/features/system-settings/components/settings-page'
import type { BillingSettings } from '@/features/system-settings/types'

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
import { RetailGroupLaunch } from './retail-group-launch'

const GROUP_MANAGEMENT_SECTION = 'group-management'

/** 管理后台分组页复用系统设置中的真实表单，但不再挂载在系统设置嵌套侧栏下。 */
export function GroupManagement() {
  return (
    <SettingsPage<BillingSettings, typeof GROUP_MANAGEMENT_SECTION>
      routePath='/_authenticated/groups/'
      defaultSettings={defaultBillingSettings}
      defaultSection={GROUP_MANAGEMENT_SECTION}
      getSectionContent={(_section, settings) => (
        <>
          <RetailGroupLaunch />
          <GroupPricingSection
            settings={settings}
            titleKey='Group Management'
          />
        </>
      )}
      getSectionMeta={() => ({ titleKey: 'Group Management' })}
    />
  )
}
