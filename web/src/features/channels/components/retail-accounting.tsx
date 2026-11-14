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
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { StaticDataTable } from '@/components/data-table'
import {
  sideDrawerContentClassName,
  sideDrawerHeaderClassName,
} from '@/components/drawer-layout'
import { EmptyState } from '@/components/empty-state'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { useStatus } from '@/hooks/use-status'
import { toIntlLocale } from '@/i18n/languages'
import { formatLogQuota, formatNumber } from '@/lib/format'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

import {
  getRetailAccounting,
  type AccountingCursor,
  type RetailCharge,
} from '../retail-accounting-api'
import { RetailRefund } from './retail-refund'

type Ledger = 'charges' | 'attempts' | 'costs'

/** 站点与角色就绪后才挂载查询；切换用户或会话时卸载旧抽屉和审核操作。服务端仍逐请求检查 root。 */
export function RetailAccounting() {
  const { status } = useStatus()
  const user = useAuthStore((state) => state.auth.user)
  const sid = useAuthStore((state) => state.auth.session?.sid)
  if (status?.site_mode !== 'retail' || user?.role !== ROLE.SUPER_ADMIN) {
    return null
  }
  const identity = `${user.id}:${sid}`
  return <AccountingSheet key={identity} identity={identity} />
}

/** 三本账独立翻页；切换账本或请求筛选时回到首页，不把某本账的游标用于其他记录。 */
function AccountingSheet(props: { identity: string }) {
  const { t, i18n } = useTranslation()
  const queryClient = useQueryClient()
  const [open, setOpen] = useState(false)
  const [ledger, setLedger] = useState<Ledger>('charges')
  const [draft, setDraft] = useState('')
  const [requestID, setRequestID] = useState('')
  const [pages, setPages] = useState<AccountingCursor[]>([{}])
  const [refund, setRefund] = useState<RetailCharge | null>(null)
  const cursor = pages.at(-1)
  const query = useQuery({
    queryKey: ['retail-accounting', props.identity, ledger, requestID, cursor],
    queryFn: ({ signal }) =>
      getRetailAccounting(
        { ...cursor, request_id: requestID || undefined },
        signal
      ),
    enabled: open,
    retry: false,
  })
  const data = !query.isError ? query.data : undefined
  const next = data?.next
  const hasNext = {
    charges: !requestID && Boolean(next?.charge_before && next?.charge_id),
    attempts: Boolean(next?.attempt_before),
    costs: Boolean(next?.cost_before),
  }[ledger]
  const number = (value: number) =>
    formatNumber(value, toIntlLocale(i18n.language))
  const empty = <EmptyState title={t('No accounting records')} />
  const labels: Record<string, string> = {
    reserved: t('Reserved'),
    settled: t('Settled'),
    refunded: t('Refunded'),
  }

  /** 只传当前账本的游标；接口同时返回另两类记录，不代表它们也该推进。 */
  function nextPage() {
    if (!next || !hasNext) return
    const selected = {
      charges: { charge_before: next.charge_before, charge_id: next.charge_id },
      attempts: { attempt_before: next.attempt_before },
      costs: { cost_before: next.cost_before },
    }[ledger]
    setPages([...pages, selected])
  }

  return (
    <>
      <Button variant='outline' onClick={() => setOpen(true)}>
        {t('Retail accounting')}
      </Button>
      <Sheet open={open} onOpenChange={setOpen}>
        <SheetContent className={sideDrawerContentClassName('sm:max-w-4xl')}>
          <SheetHeader className={sideDrawerHeaderClassName()}>
            <SheetTitle>{t('Retail accounting')}</SheetTitle>
            <SheetDescription>
              {t(
                'Reservations are not revenue. Unknown procurement cost is not zero.'
              )}
            </SheetDescription>
          </SheetHeader>
          <div className='flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto p-6'>
            <form
              className='flex flex-wrap items-end gap-2'
              onSubmit={(event) => {
                event.preventDefault()
                setRequestID(draft.trim())
                setPages([{}])
              }}
            >
              <div className='min-w-0 flex-1 space-y-2'>
                <Label htmlFor='retail-request-id'>
                  {t('Exact request ID')}
                </Label>
                <Input
                  id='retail-request-id'
                  value={draft}
                  maxLength={80}
                  onChange={(event) => setDraft(event.target.value)}
                />
              </div>
              <Button type='submit'>{t('Search')}</Button>
              <Button
                variant='outline'
                type='button'
                disabled={query.isFetching}
                onClick={() => void query.refetch()}
              >
                {t('Refresh')}
              </Button>
            </form>
            <Tabs
              value={ledger}
              onValueChange={(value) => {
                if (
                  value === 'charges' ||
                  value === 'attempts' ||
                  value === 'costs'
                ) {
                  setLedger(value)
                  setPages([{}])
                }
              }}
            >
              <TabsList>
                <TabsTrigger value='charges'>
                  {t('Customer charges')}
                </TabsTrigger>
                <TabsTrigger value='attempts'>
                  {t('Upstream attempts')}
                </TabsTrigger>
                <TabsTrigger value='costs'>
                  {t('Procurement costs')}
                </TabsTrigger>
              </TabsList>
            </Tabs>
            {query.isPending && (
              <LoadingState inline message={t('Loading...')} />
            )}
            {query.isError && (
              <ErrorState
                title={t('Unable to load resources')}
                onRetry={() => void query.refetch()}
              />
            )}
            {data && ledger === 'charges' && (
              <StaticDataTable
                data={data.charges}
                getRowKey={(row) => row.id}
                emptyContent={empty}
                tableProps={{ 'aria-label': t('Customer charges') }}
                columns={[
                  {
                    id: 'request',
                    header: t('Request ID'),
                    cell: (row) => row.id,
                  },
                  {
                    id: 'user',
                    header: t('User ID'),
                    cell: (row) => number(row.user_id),
                  },
                  {
                    id: 'status',
                    header: t('Status'),
                    cell: (row) => (
                      <Badge
                        variant={
                          row.status === 'reserved' ? 'warning' : 'secondary'
                        }
                      >
                        {labels[row.status] ?? row.status}
                      </Badge>
                    ),
                  },
                  {
                    id: 'reserved',
                    header: t('Reserved'),
                    cell: (row) => formatLogQuota(row.reserved),
                  },
                  {
                    id: 'actual',
                    header: t('Settled amount'),
                    cell: (row) =>
                      row.status === 'reserved'
                        ? t('Pending review')
                        : formatLogQuota(row.actual),
                  },
                  {
                    id: 'actions',
                    header: t('Actions'),
                    cell: (row) =>
                      row.status === 'reserved' && (
                        <Button
                          variant='outline'
                          size='sm'
                          disabled={Date.now() / 1000 - row.updated_at < 86400}
                          onClick={() => setRefund(row)}
                        >
                          {t('Release reservation')}
                        </Button>
                      ),
                  },
                ]}
              />
            )}
            {data && ledger === 'attempts' && (
              <StaticDataTable
                data={data.attempts}
                getRowKey={(row) => row.id}
                emptyContent={empty}
                tableProps={{ 'aria-label': t('Upstream attempts') }}
                columns={[
                  {
                    id: 'id',
                    header: t('Attempt ID'),
                    cell: (row) => number(row.id),
                  },
                  {
                    id: 'request',
                    header: t('Request ID'),
                    cell: (row) => row.request_id,
                  },
                  {
                    id: 'channel',
                    header: t('Channel ID'),
                    cell: (row) => number(row.channel_id),
                  },
                  {
                    id: 'state',
                    header: t('Status'),
                    cell: (row) => row.state,
                  },
                  {
                    id: 'http',
                    header: 'HTTP',
                    cell: (row) => row.status_code || '—',
                  },
                  {
                    id: 'cost',
                    header: t('Procurement costs'),
                    cell: (row) =>
                      row.cost_state === 'confirmed'
                        ? t('Confirmed')
                        : t('Unknown cost'),
                  },
                ]}
              />
            )}
            {data && ledger === 'costs' && (
              <StaticDataTable
                data={data.costs}
                getRowKey={(row) => row.id}
                emptyContent={empty}
                tableProps={{ 'aria-label': t('Procurement costs') }}
                columns={[
                  {
                    id: 'ref',
                    header: t('Reference'),
                    cell: (row) => row.reference,
                  },
                  {
                    id: 'channel',
                    header: t('Channel ID'),
                    cell: (row) => number(row.channel_id),
                  },
                  {
                    id: 'currency',
                    header: t('Currency'),
                    cell: (row) => row.currency,
                  },
                  {
                    id: 'amount',
                    header: t('Amount (minor units)'),
                    cell: (row) => number(row.amount_minor),
                  },
                  {
                    id: 'period',
                    header: t('Period'),
                    cell: (row) => row.period || '—',
                  },
                ]}
              />
            )}
            <p className='text-muted-foreground text-sm'>
              {t(
                'Only reservations unchanged for at least 24 hours can be released after manual verification.'
              )}
            </p>
            <div className='flex gap-2'>
              <Button
                variant='outline'
                disabled={pages.length === 1 || query.isFetching}
                onClick={() => setPages(pages.slice(0, -1))}
              >
                {t('Previous page')}
              </Button>
              <Button
                variant='outline'
                disabled={!hasNext || query.isFetching}
                onClick={nextPage}
              >
                {t('Next page')}
              </Button>
            </div>
          </div>
        </SheetContent>
      </Sheet>
      {refund && (
        <RetailRefund
          key={refund.id}
          charge={refund}
          onClose={() => setRefund(null)}
          onReleased={() =>
            void queryClient.invalidateQueries({
              queryKey: ['retail-accounting', props.identity],
            })
          }
        />
      )}
    </>
  )
}
