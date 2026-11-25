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
import { getRouteApi } from '@tanstack/react-router'
import { KeyRound, Network, SlidersHorizontal, UsersRound } from 'lucide-react'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { RetailPricing } from '@/features/channels/components/retail-pricing'
import { defaultBillingSettings } from '@/features/system-settings/billing/defaults'
import { GroupPricingSection } from '@/features/system-settings/billing/group-pricing-section'
import { SettingsPage } from '@/features/system-settings/components/settings-page'
import { SettingsPageProvider } from '@/features/system-settings/components/settings-page-context'
import type { BillingSettings } from '@/features/system-settings/types'
import { InventoryPanel } from '@/features/upstream-keys'
import { useStatus } from '@/hooks/use-status'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

import { RetailGroupLaunch } from './retail-group-launch'

import './group-management.css'

const route = getRouteApi('/_authenticated/groups/')
type GroupTab = NonNullable<ReturnType<typeof route.useSearch>['tab']>

/** 在原有管理页保留权限和设置加载流程，中转站才提供库存 Key 路由。
 * @return 当前站点的分组工作区。
 */
export function GroupManagement() {
  const { status } = useStatus()
  const { user, session } = useAuthStore((s) => s.auth)
  const { tab = 'routes' } = route.useSearch()
  const navigate = route.useNavigate()
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
            identity={`${user.id}:${session?.sid}`}
            settings={settings}
            tab={tab}
            onTabChange={(next) =>
              void navigate({ search: { tab: next }, resetScroll: false })
            }
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

/** 每个页签首次打开时才读取对应配置；切换或在浏览器中后退时保留当前会话的编辑草稿。
 * @param props 当前价表管理员身份、账号设置，以及由路由控制的页签。
 * @return 本站分组、库存 Key 与倍率的管理入口。
 */
export function GroupWorkspace(props: {
  identity: string
  settings: BillingSettings
  tab: GroupTab
  onTabChange: (tab: GroupTab) => void
}) {
  const { t } = useTranslation()
  const { tab } = props
  const [visited, setVisited] = useState<readonly GroupTab[]>([tab])
  // URL 也能通过旧书签或后退改变，不能只在按钮点击时登记已打开的页签。
  if (!visited.includes(tab)) setVisited([...visited, tab])
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
      value={tab}
      onValueChange={(value) => props.onTabChange(value as GroupTab)}
    >
      <div className='gm-nav'>
        <TabsList variant='line' aria-label={t('Group Management')}>
          <TabsTrigger value='routes'>
            <Network aria-hidden />
            {t('Call group routes')}
          </TabsTrigger>
          <TabsTrigger value='keys'>
            <KeyRound aria-hidden />
            {t('Upstream Keys')}
          </TabsTrigger>
          <TabsTrigger value='rates'>
            <SlidersHorizontal aria-hidden />
            {t('Rates and sync')}
          </TabsTrigger>
          <TabsTrigger value='accounts'>
            <UsersRound aria-hidden />
            {t('Account groups')}
          </TabsTrigger>
        </TabsList>
        <div ref={setAccountActions} hidden={tab !== 'accounts'} />
      </div>
      <TabsContent
        value='routes'
        keepMounted
        className='min-w-0 data-hidden:hidden'
      >
        {visited.includes('routes') && <RetailGroupLaunch />}
      </TabsContent>
      <TabsContent
        value='keys'
        keepMounted
        className='min-w-0 data-hidden:hidden'
      >
        {visited.includes('keys') && (
          <InventoryPanel identity={props.identity} />
        )}
      </TabsContent>
      <TabsContent
        value='rates'
        keepMounted
        className='min-w-0 data-hidden:hidden'
      >
        {visited.includes('rates') && <RetailPricing />}
      </TabsContent>
      <TabsContent
        value='accounts'
        keepMounted
        className='gm-accounts min-w-0 data-hidden:hidden'
      >
        {visited.includes('accounts') && (
          <SettingsPageProvider actionsContainer={accountActions}>
            {accountForm}
          </SettingsPageProvider>
        )}
      </TabsContent>
    </Tabs>
  )
}
