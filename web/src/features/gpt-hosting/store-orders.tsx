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
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { getCoreRowModel, useReactTable } from '@tanstack/react-table'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { StaticDataTable, DataTablePagination } from '@/components/data-table'
import { EmptyState } from '@/components/empty-state'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Button } from '@/components/ui/button'
import { submitPaymentForm } from '@/features/wallet/lib/payment'
import { toIntlLocale } from '@/i18n/languages'
import { handleServerError } from '@/lib/handle-server-error'

import {
  getHostingStore,
  postHosting,
  type HostingOrders,
  type HostingCheckout,
} from './api'

/** 用户查询只返回本人订单；管理视图由服务端再次检查 root，不依赖隐藏按钮保护归属。 */
export function HostingOrderList(props: {
  identity: string
  admin?: boolean
  paymentReady: boolean
}) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const cache = useQueryClient()
  const [pagination, setPagination] = useState({ pageIndex: 0, pageSize: 20 })
  const prefix = ['hosting-orders', props.identity]
  const query = useQuery({
    queryKey: [...prefix, props.admin === true, pagination.pageIndex],
    queryFn: ({ signal }) =>
      getHostingStore<HostingOrders>(
        props.admin ? 'admin/store' : 'orders',
        pagination.pageIndex + 1,
        signal
      ),
    refetchInterval: (q) =>
      q.state.data?.items.some((o) => o.status === 'pending') ? 5000 : false,
  })
  const pay = useMutation({
    mutationFn: async (orderId: string) => {
      const result = await postHosting<HostingCheckout>('payment', {
        order_id: orderId,
      })
      const destination = new URL(result.url)
      if (
        destination.protocol !== 'https:' ||
        destination.username ||
        destination.password
      ) {
        throw new Error(t('Invalid payment address'))
      }
      submitPaymentForm(result.url, result.params)
    },
    retry: false,
    gcTime: 0,
    onError: handleServerError,
    onSettled: () => cache.invalidateQueries({ queryKey: prefix }),
  })
  const table = useReactTable({
    data: query.data?.items || [],
    columns: [],
    getCoreRowModel: getCoreRowModel(),
    manualPagination: true,
    rowCount: query.data?.total || 0,
    state: { pagination },
    onPaginationChange: setPagination,
  })
  const status = {
    pending: t('Awaiting payment'),
    expired: t('Order expired'),
    paid: t('Paid and fulfilled'),
    paid_review: t('Paid, awaiting review'),
  }
  return (
    <section className='space-y-3' aria-label={t('Hosting orders')}>
      <div className='flex items-center justify-between gap-2'>
        <h3 className='font-medium'>{t('Hosting orders')}</h3>
        <Button
          size='sm'
          variant='ghost'
          disabled={query.isFetching}
          onClick={() => {
            void query.refetch()
            void cache.invalidateQueries({ queryKey: ['gpt-hosting'] })
          }}
        >
          {t('Refresh orders')}
        </Button>
      </div>
      {query.isPending && <LoadingState inline />}
      {query.isError && <ErrorState onRetry={() => query.refetch()} />}
      {query.data && (
        <>
          <StaticDataTable
            data={query.data.items}
            getRowKey={(row) => row.id}
            emptyContent={<EmptyState title={t('No hosting orders yet')} />}
            columns={[
              {
                id: 'order',
                header: t('Order'),
                cell: (row) => (
                  <div className='max-w-56 space-y-1 break-all'>
                    <p className='text-xs'>{row.id}</p>
                    {props.admin && (
                      <p>
                        {t('User ID')}: {row.user_id}
                      </p>
                    )}
                    <p>
                      {t(row.kind === 'renew' ? 'Renewal' : 'New seat')} ·{' '}
                      {row.platform === 'claude' ? 'Claude' : 'GPT'}
                    </p>
                    <p className='text-muted-foreground text-xs'>
                      {t('Hosting seat')}: {row.seat_id}
                    </p>
                    <p className='text-muted-foreground text-xs'>
                      {new Date(row.created_at * 1000).toLocaleString(locale)}
                    </p>
                  </div>
                ),
              },
              {
                id: 'amount',
                header: t('Amount'),
                cell: (row) => (
                  <div>
                    ¥{(row.price_cents / 100).toFixed(2)}
                    <p className='text-muted-foreground text-xs'>
                      {t('{{days}} days per seat', { days: row.days })} ·{' '}
                      {t(row.method === 'wxpay' ? 'WeChat Pay' : 'Alipay')}
                    </p>
                  </div>
                ),
              },
              {
                id: 'status',
                header: t('Status'),
                cell: (row) => (
                  <div className='max-w-60 whitespace-normal'>
                    <p>{status[row.status]}</p>
                    {row.status === 'paid_review' && (
                      <p className='text-muted-foreground mt-1 text-xs'>
                        {t(
                          'Payment was received, but the seat was not changed. Contact the administrator with this order number.'
                        )}
                      </p>
                    )}
                    {row.status === 'paid' && (
                      <p className='text-muted-foreground text-xs'>
                        {t('Expires')}:{' '}
                        {new Date(row.ends_at * 1000).toLocaleString(locale)}
                      </p>
                    )}
                    {row.status === 'pending' && (
                      <p className='text-muted-foreground text-xs'>
                        {t('Pay before')}:{' '}
                        {new Date(row.expires_at * 1000).toLocaleString(locale)}
                      </p>
                    )}
                  </div>
                ),
              },
              ...(!props.admin
                ? [
                    {
                      id: 'action',
                      header: t('Actions'),
                      cell: (row: HostingOrders['items'][number]) =>
                        row.status === 'pending' ? (
                          <Button
                            size='sm'
                            variant='outline'
                            disabled={!props.paymentReady || pay.isPending}
                            onClick={() => pay.mutate(row.id)}
                          >
                            {t('Continue to payment')}
                          </Button>
                        ) : null,
                    },
                  ]
                : []),
            ]}
          />
          {query.data.total > 20 && <DataTablePagination table={table} />}
        </>
      )}
    </section>
  )
}
