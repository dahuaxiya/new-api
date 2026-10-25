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
import '@/styles/users-workspace.css'
import { getRouteApi } from '@tanstack/react-router'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { SectionPageLayout } from '@/components/layout'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { HostingStoreAdmin } from '@/features/gpt-hosting/store-admin'
import { UserUsageOverview } from '@/features/user-usage'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

import { HostingResources } from './components/hosting-resources'
import { UsersDeleteDialog } from './components/users-delete-dialog'
import { UsersMutateDrawer } from './components/users-mutate-drawer'
import { UsersPrimaryButtons } from './components/users-primary-buttons'
import { UsersProvider, useUsers } from './components/users-provider'
import { UsersTable } from './components/users-table'

const route = getRouteApi('/_authenticated/users/')

/** 用户管理只有一个导航入口，查询参数兼容旧收藏并保留权限边界。 */
function UsersContent() {
  const { t } = useTranslation()
  const { open, setOpen, currentRow } = useUsers()
  const search = route.useSearch()
  const navigate = route.useNavigate()
  const isRoot = useAuthStore((s) => s.auth.user?.role === ROLE.SUPER_ADMIN)
  const [store, setStore] = useState(false)
  const [resources, setResources] = useState(
    search.resources === true && isRoot
  )
  const tab = search.tab || 'list'
  useEffect(() => {
    if (search.create) {
      setOpen('create')
      void navigate({
        search: (prev) => ({ ...prev, create: undefined }),
        replace: true,
      })
    }
  }, [search.create, setOpen, navigate])
  useEffect(() => {
    if (search.resources && isRoot) {
      setResources(true)
      void navigate({
        search: (prev) => ({ ...prev, resources: undefined }),
        replace: true,
      })
    }
  }, [search.resources, isRoot, navigate])

  return (
    <>
      <SectionPageLayout
        className='users-workspace'
        stackActionsOnMobile
        description={t(
          'View accounts, renew hosting and manage quotas in one place.'
        )}
      >
        <SectionPageLayout.Title>{t('Users')}</SectionPageLayout.Title>
        <SectionPageLayout.Actions>
          <UsersPrimaryButtons
            onResources={() => setResources(true)}
            onStore={() => setStore(true)}
          />
        </SectionPageLayout.Actions>
        <SectionPageLayout.Content>
          <div className='flex min-h-0 flex-1 flex-col gap-4'>
            <Tabs
              value={tab}
              onValueChange={(value) =>
                navigate({
                  search: (prev) => ({
                    ...prev,
                    tab: value === 'usage' ? 'usage' : 'list',
                  }),
                })
              }
            >
              <TabsList variant='line'>
                <TabsTrigger value='list'>{t('User list')}</TabsTrigger>
                <TabsTrigger value='usage'>{t('Usage statistics')}</TabsTrigger>
              </TabsList>
            </Tabs>
            {tab === 'usage' ? <UserUsageOverview embedded /> : <UsersTable />}
          </div>
        </SectionPageLayout.Content>
      </SectionPageLayout>

      <UsersMutateDrawer
        open={open === 'create' || open === 'update'}
        onOpenChange={(isOpen) => !isOpen && setOpen(null)}
        currentRow={open === 'update' ? currentRow || undefined : undefined}
      />
      <UsersDeleteDialog />
      {isRoot && (
        <>
          <HostingResources open={resources} onOpenChange={setResources} />
          <HostingStoreAdmin open={store} onOpenChange={setStore} />
        </>
      )}
    </>
  )
}

/** 承载真实用户管理状态，页面外观与托管抽屉共用局部主题。 */
export function Users() {
  return (
    <UsersProvider>
      <UsersContent />
    </UsersProvider>
  )
}
