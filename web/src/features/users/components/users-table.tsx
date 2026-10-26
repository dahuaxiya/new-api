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
import { useQuery } from '@tanstack/react-query'
import { getRouteApi } from '@tanstack/react-router'
import type { OnChangeFn, SortingState } from '@tanstack/react-table'
import { RefreshCw } from 'lucide-react'
import { Fragment, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import {
  DISABLED_ROW_DESKTOP,
  DISABLED_ROW_MOBILE,
  DataTablePage,
  DataTableRow,
  DataTableCardGrid,
  useDataTable,
} from '@/components/data-table'
import { Button } from '@/components/ui/button'
import { TableRow, TableCell } from '@/components/ui/table'
import { useMediaQuery } from '@/hooks'
import { useTableUrlState } from '@/hooks/use-table-url-state'
import {
  createServerError,
  requireServerSuccess,
} from '@/lib/server-error-message'
import { useAuthStore } from '@/stores/auth-store'

import { getGroups, getUsers, searchUsers } from '../api'
import {
  USER_STATUS,
  getUserStatusOptions,
  getUserRoleOptions,
  isUserDeleted,
} from '../constants'
import { getHostingSummaries, hostingStatusLabels } from '../hosting-api'
import { getUserTokenCounts } from '../token-api'
import type { User, UserSortBy } from '../types'
import { DataTableBulkActions } from './data-table-bulk-actions'
import { HostingTermNotice } from './hosting-term-notice'
import { UserTokensPanel } from './user-tokens-panel'
import { useUsersColumns } from './users-columns'
import { UsersMobileCard } from './users-mobile-card'
import { useUsers } from './users-provider'

const route = getRouteApi('/_authenticated/users/')

const USER_SORTABLE_COLUMNS = new Set<UserSortBy>([
  'id',
  'username',
  'quota',
  'group',
  'created_at',
  'last_login_at',
])

/** 已禁用和已删除用户保持可见，但继续使用共享禁用行样式。 */
function isDisabledUserRow(user: User) {
  return isUserDeleted(user) || user.status === USER_STATUS.DISABLED
}

/** 服务端分页和筛选保持唯一数据源，窄屏只切换展示方式。 */
export function UsersTable() {
  const { t } = useTranslation()
  const identity = useAuthStore(
    (s) => `${s.auth.user?.id}:${s.auth.session?.sid}`
  )
  const navigate = route.useNavigate()

  const { refreshTrigger, triggerRefresh } = useUsers()
  const isMobile = useMediaQuery('(max-width: 640px)')
  const [sorting, setSorting] = useState<SortingState>([])

  const {
    globalFilter,
    onGlobalFilterChange,
    columnFilters,
    onColumnFiltersChange,
    pagination,
    onPaginationChange,
    ensurePageInRange,
  } = useTableUrlState({
    search: route.useSearch(),
    navigate: route.useNavigate(),
    pagination: { defaultPage: 1, defaultPageSize: isMobile ? 10 : 20 },
    globalFilter: { enabled: true, key: 'filter' },
    columnFilters: [
      { columnId: 'hosting', searchKey: 'hosting', type: 'array' },
      { columnId: 'status', searchKey: 'status', type: 'array' },
      { columnId: 'role', searchKey: 'role', type: 'array' },
      { columnId: 'group', searchKey: 'group', type: 'array' },
    ],
  })
  const hostingFilter =
    (columnFilters.find((filter) => filter.id === 'hosting')?.value as
      | string[]
      | undefined) ?? []
  const statusFilter =
    (columnFilters.find((filter) => filter.id === 'status')?.value as
      | string[]
      | undefined) ?? []
  const roleFilter =
    (columnFilters.find((filter) => filter.id === 'role')?.value as
      | string[]
      | undefined) ?? []
  const groupFilter =
    (columnFilters.find((filter) => filter.id === 'group')?.value as
      | string[]
      | undefined) ?? []

  // 保存展开时的列表上下文，翻页、筛选或换登录身份后不再渲染旧用户的凭据。
  const contextKey = JSON.stringify([
    identity,
    pagination,
    globalFilter,
    columnFilters,
    sorting,
    refreshTrigger,
  ])
  const [expanded, setExpanded] = useState<{
    id: number
    context: string
  } | null>(null)
  const expandedId = expanded?.context === contextKey ? expanded.id : null
  const keyControls = useMemo(
    () => ({
      expandedId,
      toggle: (id: number) =>
        setExpanded((prev) =>
          prev?.id === id && prev.context === contextKey
            ? null
            : { id, context: contextKey }
        ),
    }),
    [expandedId, contextKey]
  )
  const columns = useUsersColumns(keyControls)

  const { data: groupsData } = useQuery({
    queryKey: ['groups'],
    queryFn: async () => requireServerSuccess(await getGroups()),
    staleTime: 5 * 60 * 1000,
  })

  const sortParams = useMemo(() => {
    const activeSort = sorting[0]
    if (
      !activeSort ||
      !USER_SORTABLE_COLUMNS.has(activeSort.id as UserSortBy)
    ) {
      return {}
    }

    return {
      sort_by: activeSort.id as UserSortBy,
      sort_order: activeSort.desc ? 'desc' : 'asc',
    } as const
  }, [sorting])

  const handleSortingChange: OnChangeFn<SortingState> = (updater) => {
    setSorting(updater)
    if (pagination.pageIndex > 0) {
      onPaginationChange({ ...pagination, pageIndex: 0 })
    }
  }

  // Fetch data with React Query
  const { data, isLoading, isFetching } = useQuery({
    queryKey: [
      'users',
      identity,
      pagination.pageIndex + 1,
      pagination.pageSize,
      globalFilter,
      hostingFilter,
      statusFilter,
      roleFilter,
      groupFilter,
      sortParams,
      refreshTrigger,
    ],
    queryFn: async () => {
      const hasFilter = globalFilter?.trim()
      const hasColumnFilter =
        hostingFilter.length > 0 ||
        statusFilter.length > 0 ||
        roleFilter.length > 0 ||
        groupFilter.length > 0
      const params = {
        p: pagination.pageIndex + 1,
        page_size: pagination.pageSize,
        ...sortParams,
      }

      const result =
        hasFilter || hasColumnFilter
          ? await searchUsers({
              ...params,
              keyword: globalFilter,
              status: statusFilter[0] ?? '',
              hosting: hostingFilter[0] ?? '',
              role: roleFilter[0] ?? '',
              group: groupFilter[0] ?? '',
            })
          : await getUsers(params)

      if (!result.success) {
        throw createServerError(
          result,
          t(hasFilter ? 'Failed to search users' : 'Failed to load users')
        )
      }

      const items = result.data?.items || []
      // 汇总接口故障时仍显示全部用户，不能把未知托管状态误显示成未开通。
      const [summaries, tokenCounts] = await Promise.all([
        getHostingSummaries(items.map((user) => user.id)).catch(() => []),
        getUserTokenCounts(items.map((user) => user.id)).catch(
          () => ({}) as Record<string, number>
        ),
      ])
      return {
        items: items.map((user) => ({
          ...user,
          token_count: tokenCounts[String(user.id)],
          hosting: summaries.find((summary) => summary.user_id === user.id),
        })),
        total: result.data?.total || 0,
      }
    },
    placeholderData: (previousData) => previousData,
  })

  const users = data?.items || []

  const { table } = useDataTable({
    data: users,
    columns,
    enableRowSelection: true,
    initialColumnVisibility: { role: false, group: false },
    columnFilters,
    globalFilter,
    pagination,
    sorting,
    globalFilterFn: (row, _columnId, filterValue) => {
      const searchValue = String(filterValue).toLowerCase()
      const fields = [
        row.getValue('username'),
        row.original.display_name,
        row.original.email,
      ]
      return fields.some((field) =>
        String(field || '')
          .toLowerCase()
          .includes(searchValue)
      )
    },
    onPaginationChange,
    onGlobalFilterChange,
    onColumnFiltersChange,
    onSortingChange: handleSortingChange,
    manualPagination: true,
    manualFiltering: true,
    manualSorting: true,
    totalCount: data?.total || 0,
    ensurePageInRange,
  })

  return (
    <>
      <HostingTermNotice
        onShowUndated={() => {
          // 提示是全局计数，清空旧筛选后再跳到第一页，避免只展示其中一部分。
          void navigate({
            search: (prev) => ({
              ...prev,
              page: 1,
              filter: undefined,
              status: undefined,
              role: undefined,
              group: undefined,
              hosting: ['undated'],
            }),
          })
        }}
      />
      <DataTablePage
        className='users-table'
        fixedHeight={false}
        paginationInFooter={false}
        tableClassName='users-table-surface'
        mobile={
          <DataTableCardGrid
            table={table}
            isLoading={isLoading}
            emptyTitle={t('No Users Found')}
            emptyDescription={t(
              'No users available. Try adjusting your search or filters.'
            )}
            gridClassName='grid grid-cols-1 gap-4'
            getRowKey={(row) => row.original.id}
            getRowClassName={(row) =>
              isDisabledUserRow(row.original) ? DISABLED_ROW_MOBILE : undefined
            }
            renderCard={(row) => (
              <>
                <UsersMobileCard row={row} />
                {expandedId === row.original.id && (
                  <UserTokensPanel
                    key={`${identity}:${row.original.id}`}
                    user={row.original}
                    onClose={() => setExpanded(null)}
                  />
                )}
              </>
            )}
          />
        }
        renderRow={(row, helpers) => (
          <Fragment key={row.id}>
            <DataTableRow
              row={row}
              getColumnClassName={(id) => helpers.getCellClassName(id)}
              cellRenderColumns={table.options.columns}
              className={
                isDisabledUserRow(row.original)
                  ? DISABLED_ROW_DESKTOP
                  : undefined
              }
            />
            {expandedId === row.original.id && (
              <TableRow className='hover:bg-transparent'>
                <TableCell
                  colSpan={row.getVisibleCells().length}
                  className='p-0'
                >
                  <UserTokensPanel
                    key={`${identity}:${row.original.id}`}
                    user={row.original}
                    onClose={() => setExpanded(null)}
                  />
                </TableCell>
              </TableRow>
            )}
          </Fragment>
        )}
        table={table}
        columns={columns}
        isLoading={isLoading}
        isFetching={isFetching}
        emptyTitle={t('No Users Found')}
        emptyDescription={t(
          'No users available. Try adjusting your search or filters.'
        )}
        skeletonKeyPrefix='users-skeleton'
        applyHeaderSize
        toolbarProps={{
          className: 'users-toolbar',
          collapsibleOnMobile: false,
          preActions: (
            <Button
              variant='outline'
              disabled={isFetching}
              onClick={triggerRefresh}
            >
              <RefreshCw
                aria-hidden='true'
                className={isFetching ? 'animate-spin' : ''}
              />
              {t('Refresh data')}
            </Button>
          ),
          searchPlaceholder: t('Filter by username, name or email...'),
          searchDebounceMs: 500,
          filters: [
            {
              columnId: 'hosting',
              title: t('Hosting entitlement'),
              options: [
                'unopened',
                'undated',
                'active',
                'expiring',
                'expired',
                'scheduled',
                'awaiting_login',
                'disabled',
                'takeover',
              ].map((value) => ({
                value,
                label: t(hostingStatusLabels[value]),
              })),
              singleSelect: true,
            },
            {
              columnId: 'status',
              title: t('Status'),
              options: getUserStatusOptions(t),
              singleSelect: true,
            },
            {
              columnId: 'role',
              title: t('Role'),
              options: getUserRoleOptions(t),
              singleSelect: true,
            },
            {
              columnId: 'group',
              title: t('User Group'),
              options: (groupsData?.data ?? []).map((group) => ({
                label: group,
                value: group,
              })),
              singleSelect: true,
            },
          ],
        }}
        getRowClassName={(row, { isMobile }) => {
          if (!isDisabledUserRow(row.original)) return undefined
          return isMobile ? DISABLED_ROW_MOBILE : DISABLED_ROW_DESKTOP
        }}
        bulkActions={<DataTableBulkActions table={table} />}
      />
    </>
  )
}
