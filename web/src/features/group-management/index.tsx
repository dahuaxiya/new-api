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
import { Network, UsersRound } from 'lucide-react'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { defaultBillingSettings } from '@/features/system-settings/billing/defaults'
import { GroupPricingSection } from '@/features/system-settings/billing/group-pricing-section'
import { SettingsPage } from '@/features/system-settings/components/settings-page'
import { SettingsPageProvider } from '@/features/system-settings/components/settings-page-context'
import type { BillingSettings } from '@/features/system-settings/types'
import { useStatus } from '@/hooks/use-status'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

import { RetailGroupLaunch } from './retail-group-launch'

import './group-management.css'

/** 在原有管理页保留权限和设置加载流程，中转站才提供库存 Key 路由。
 * @return 当前站点的分组工作区。
 */
export function GroupManagement() {
  const { status } = useStatus()
  const { user, session } = useAuthStore((s) => s.auth)
  const hasRouting =
    status?.site_mode === 'retail' && user?.role === ROLE.SUPER_ADMIN
  return (
    <SettingsPage<BillingSettings, 'group-management'>
      routePath='/_authenticated/groups/'
      defaultSettings={defaultBillingSettings}
      defaultSection='group-management'
      getSectionContent={(_section, settings) =>
        hasRouting ? (
          <GroupWorkspace
            key={`${user.id}:${session?.sid}`}
            settings={settings}
          />
        ) : (
          <GroupPricingSection
            settings={settings}
            titleKey='Group Management'
          />
        )
      }
      getSectionMeta={() => ({ titleKey: 'Group Management' })}
    />
  )
}

/** 切换页签保留账号草稿；保存入口随表单隐藏，避免在路由页误保存倍率。
 * @param props 由系统设置加载的账号分组配置。
 * @return 当前管理员的分组路由和账号设置页签。
 */
export function GroupWorkspace(props: { settings: BillingSettings }) {
  const { t } = useTranslation()
  const [activeTab, setActiveTab] = useState('routes')
  const [visitedAccounts, setVisitedAccounts] = useState(false)
  const [accountActions, setAccountActions] = useState<HTMLDivElement | null>(
    null
  )
  // 倍率编辑器按传入的配置对象重置草稿。页签和保存按钮的切换不代表服务端配置变动。
  const accountForm = useMemo(
    () => <GroupPricingSection settings={props.settings} />,
    [props.settings]
  )
  return (
    <Tabs
      className='group-workspace'
      value={activeTab}
      onValueChange={(value) => {
        setActiveTab(value)
        // 按需打开倍率表，首次访问后保留输入到本会话离开管理页为止。
        if (value === 'accounts') setVisitedAccounts(true)
      }}
    >
      <div className='gm-nav'>
        <TabsList variant='line' aria-label={t('Group Management')}>
          <TabsTrigger value='routes'>
            <Network aria-hidden />
            {t('Call group routes')}
          </TabsTrigger>
          <TabsTrigger value='accounts'>
            <UsersRound aria-hidden />
            {t('Account groups')}
          </TabsTrigger>
        </TabsList>
        <div ref={setAccountActions} hidden={activeTab !== 'accounts'} />
      </div>
      <TabsContent
        value='routes'
        keepMounted
        className='min-w-0 data-hidden:hidden'
      >
        <RetailGroupLaunch />
      </TabsContent>
      <TabsContent
        value='accounts'
        keepMounted
        className='gm-accounts min-w-0 data-hidden:hidden'
      >
        {visitedAccounts && (
          <SettingsPageProvider actionsContainer={accountActions}>
            {accountForm}
          </SettingsPageProvider>
        )}
      </TabsContent>
    </Tabs>
  )
}
