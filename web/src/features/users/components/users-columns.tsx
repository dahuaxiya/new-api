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
import type { ColumnDef } from '@tanstack/react-table'
import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'

import { LongText } from '@/components/long-text'
import { StatusBadge } from '@/components/status-badge'
import { Checkbox } from '@/components/ui/checkbox'
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { getCurrencyDisplay } from '@/lib/currency'
import { useSystemConfigStore } from '@/stores/system-config-store'

import {
  USER_STATUS,
  USER_STATUSES,
  USER_ROLES,
  isUserDeleted,
} from '../constants'
import type { User } from '../types'
import { DataTableRowActions } from './data-table-row-actions'
import { HostingCell } from './hosting-cells'
import { UserQuotaCell } from './user-quota-cell'

/** 桌面表格和手机卡片复用同一列定义，避免管理操作或额度含义分叉。 */
export function useUsersColumns(): ColumnDef<User>[] {
  const { t } = useTranslation()
  const currencyConfig = useSystemConfigStore((state) => state.config.currency)
  const { meta: currency } = getCurrencyDisplay()
  const quotaUnit = currency.kind === 'tokens' ? t('Tokens') : currency.symbol
  return useMemo<ColumnDef<User>[]>(
    () => [
      {
        id: 'select',
        header: ({ table }) => (
          <Checkbox
            checked={table.getIsAllPageRowsSelected()}
            indeterminate={table.getIsSomePageRowsSelected()}
            onCheckedChange={(value) =>
              table.toggleAllPageRowsSelected(!!value)
            }
            aria-label={t('Select all')}
            className='translate-y-[2px]'
          />
        ),
        cell: ({ row }) => (
          <Checkbox
            checked={row.getIsSelected()}
            onCheckedChange={(value) => row.toggleSelected(!!value)}
            aria-label={t('Select row')}
            className='translate-y-[2px]'
          />
        ),
        enableSorting: false,
        enableHiding: false,
        size: 40,
      },
      {
        accessorKey: 'username',
        header: t('Username'),
        cell: ({ row }) => {
          const username = row.getValue('username') as string
          const displayName = row.original.display_name
          const remark = row.original.remark

          return (
            <div className='users-identity'>
              <span className='users-avatar' aria-hidden='true'>
                {username.slice(0, 1).toUpperCase()}
              </span>
              <div className='min-w-0 space-y-1'>
                <div className='flex items-center gap-2'>
                  <LongText className='max-w-[160px] text-sm font-semibold'>
                    {username}
                  </LongText>
                  {remark && (
                    <Tooltip>
                      <TooltipTrigger
                        render={
                          <StatusBadge
                            variant='success'
                            copyable={false}
                            className='font-normal'
                          />
                        }
                      >
                        <LongText className='max-w-[80px]'>{remark}</LongText>
                      </TooltipTrigger>
                      <TooltipContent>
                        <p className='text-xs'>{remark}</p>
                      </TooltipContent>
                    </Tooltip>
                  )}
                </div>
                <p className='text-muted-foreground text-xs'>
                  #{row.original.id} ·{' '}
                  {t(
                    USER_ROLES[row.original.role as keyof typeof USER_ROLES]
                      ?.labelKey || 'User'
                  )}
                </p>
                {displayName && displayName !== username && (
                  <div
                    data-table-text='secondary'
                    className='text-muted-foreground max-w-[180px] text-xs font-normal'
                  >
                    <LongText>{displayName}</LongText>
                  </div>
                )}
              </div>
            </div>
          )
        },
        enableHiding: false,
        size: 220,
        meta: { mobileTitle: true },
      },
      {
        accessorKey: 'status',
        header: t('Status'),
        cell: ({ row }) => {
          const user = row.original
          const requestCount = user.request_count

          const statusConfig = isUserDeleted(user)
            ? USER_STATUSES[USER_STATUS.DELETED]
            : USER_STATUSES[user.status as keyof typeof USER_STATUSES]

          if (!statusConfig) {
            return null
          }

          return (
            <Tooltip>
              <TooltipTrigger render={<div className='-ml-1.5 cursor-help' />}>
                <StatusBadge
                  label={t(statusConfig.labelKey)}
                  variant={
                    isUserDeleted(user) ? 'neutral' : statusConfig.variant
                  }
                  copyable={false}
                  className='font-normal'
                />
              </TooltipTrigger>
              <TooltipContent>
                <p className='text-xs'>
                  {t('Requests:')} {requestCount.toLocaleString()}
                </p>
              </TooltipContent>
            </Tooltip>
          )
        },
        filterFn: (row, id, value) => {
          return value.includes(String(row.getValue(id)))
        },
        enableSorting: false,
        size: 120,
        meta: { mobileBadge: true },
      },
      { accessorKey: 'role', header: t('Role'), enableSorting: false },
      { accessorKey: 'group', header: t('User Group'), enableSorting: false },
      {
        id: 'gpt_account',
        header: t('GPT account'),
        cell: ({ row }) => (
          <HostingCell summary={row.original.hosting} kind='account' />
        ),
        enableSorting: false,
        meta: { mobileOrder: 20 },
      },
      {
        id: 'hosting',
        header: t('Hosting entitlement'),
        cell: ({ row }) => (
          <HostingCell summary={row.original.hosting} kind='term' />
        ),
        enableSorting: false,
        meta: { mobileOrder: 25 },
      },
      {
        id: 'official_quota',
        header: t('Official GPT quota'),
        cell: ({ row }) => (
          <HostingCell summary={row.original.hosting} kind='quota' />
        ),
        enableSorting: false,
        meta: { mobileOrder: 30 },
      },
      {
        id: 'quota',
        accessorKey: 'quota',
        header: `${t('Site balance')} (${quotaUnit})`,
        cell: ({ row }) => {
          const user = row.original
          return <UserQuotaCell remaining={user.quota} used={user.used_quota} />
        },
        size: 180,
        minSize: 160,
        meta: { mobileOrder: 40 },
      },
      {
        id: 'actions',
        header: () => t('Actions'),
        cell: ({ row }) => <DataTableRowActions row={row} />,
        meta: { pinned: 'right' as const },
      },
    ],
    // formatQuota reads the currency configuration from the store.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [t, quotaUnit, currencyConfig]
  )
}
