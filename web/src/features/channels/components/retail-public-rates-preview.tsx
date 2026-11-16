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
import { useTranslation } from 'react-i18next'

import { StaticDataTable } from '@/components/data-table'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { toIntlLocale } from '@/i18n/languages'

import { getRetailPublicRates } from '../retail-pricing-api'

/** Manual read-only supplier preview, isolated from the pricing editor and scoped to the login session. */
export function RetailPublicRatesPreview(props: { identity: string }) {
  const { t, i18n } = useTranslation()
  const query = useQuery({
    queryKey: ['retail-public-rates', props.identity],
    queryFn: ({ signal }) => getRetailPublicRates(signal),
    enabled: false,
    retry: false,
    gcTime: 0,
    meta: { errorToast: false },
  })
  const dateFormat = new Intl.DateTimeFormat(
    toIntlLocale(i18n.resolvedLanguage || i18n.language),
    { dateStyle: 'medium', timeStyle: 'medium' }
  )
  return (
    <section
      aria-label={t('Xingxing public multipliers')}
      className='space-y-4 rounded-lg border p-4'
    >
      <div className='flex flex-wrap items-center justify-between gap-3'>
        <div className='space-y-2'>
          <h3 className='text-sm font-medium'>
            {t('Xingxing public multipliers')}
          </h3>
          <Badge variant='outline'>{t('Preview only')}</Badge>
        </div>
        <Button
          variant='outline'
          size='sm'
          disabled={query.isFetching}
          onClick={() => void query.refetch()}
        >
          {t('Read public multipliers')}
        </Button>
      </div>
      <p className='text-muted-foreground text-sm'>
        {t(
          'Reading does not bind Keys or channels, import a price book, or enable automatic repricing.'
        )}
      </p>
      <a
        href='https://aixingxing.pro/api/public/rates'
        target='_blank'
        rel='noreferrer'
        className='text-muted-foreground block text-xs break-all underline underline-offset-4'
      >
        https://aixingxing.pro/api/public/rates
      </a>
      <div aria-live='polite' aria-busy={query.isFetching}>
        {query.isFetching && <LoadingState inline message={t('Loading...')} />}
        {query.isError && !query.isFetching && (
          <ErrorState
            className='min-h-0 py-4'
            title={t('Unable to read upstream public multipliers')}
            description={t(
              'Prices are unchanged. Retry after checking the upstream service.'
            )}
          />
        )}
        {/* A failed refresh must not leave an old snapshot looking current. */}
        {query.data && !query.isError && !query.isFetching && (
          <div className='space-y-4'>
            <dl className='text-muted-foreground grid gap-2 text-xs sm:grid-cols-2'>
              <div>
                <dt>{t('Upstream update time')}</dt>
                <dd className='mt-1'>
                  {dateFormat.format(new Date(query.data.updated_at * 1000))}
                </dd>
              </div>
              <div>
                <dt>{t('Read by this site')}</dt>
                <dd className='mt-1'>
                  {dateFormat.format(new Date(query.data.fetched_at * 1000))}
                </dd>
              </div>
            </dl>
            <p className='text-muted-foreground text-xs'>
              {t(
                'Public multipliers do not confirm Key assignments, account discounts or model availability. Reading time does not verify price freshness.'
              )}
            </p>
            <StaticDataTable
              tableProps={{ 'aria-label': t('Xingxing public multipliers') }}
              data={query.data.groups}
              getRowKey={(row) => row.group}
              columns={[
                {
                  id: 'group',
                  header: t('Upstream group'),
                  cell: (row) => (
                    <div className='min-w-0 break-all whitespace-normal'>
                      <p>{row.group}</p>
                      {row.name !== row.group && (
                        <p className='text-muted-foreground mt-1 text-xs'>
                          {row.name}
                        </p>
                      )}
                    </div>
                  ),
                },
                {
                  id: 'multiplier',
                  header: t('Public multiplier'),
                  className: 'w-28 text-right',
                  cellClassName: 'text-right',
                  cell: (row) => (
                    <span className='font-mono tabular-nums'>
                      {row.multiplier}×
                    </span>
                  ),
                },
              ]}
            />
          </div>
        )}
      </div>
    </section>
  )
}
