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
import { Link } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'

import { ErrorState } from '@/components/error-state'
import { PublicLayout } from '@/components/layout'
import { LoadingState } from '@/components/loading-state'
import { Button } from '@/components/ui/button'
import { getRetailCatalog } from '@/features/channels/retail-pricing-api'
import { useAuthStore } from '@/stores/auth-store'

/** Show authorized CNY sales tariffs without exposing procurement groups or applying display FX. */
export function RetailProductPricing() {
  const { t } = useTranslation()
  const user = useAuthStore((s) => s.auth.user)
  const sid = useAuthStore((s) => s.auth.session?.sid)
  const query = useQuery({
    queryKey: ['retail-catalog', user?.id, sid],
    queryFn: ({ signal }) => getRetailCatalog(signal),
    enabled: Boolean(user),
    refetchInterval: 60_000,
    retry: false,
  })
  // These tariffs already use CNY; generic wallet display FX would change the contract.
  const fields = [
    { key: 'input', label: t('Input') },
    { key: 'output', label: t('Output') },
    { key: 'cache_read', label: t('Cache read') },
    { key: 'cache_write', label: t('Cache write') },
    { key: 'cache_write_1h', label: t('Cache write (1h)') },
  ] as const
  return (
    <PublicLayout>
      <div className='mx-auto max-w-5xl space-y-8 px-4 py-12 sm:px-6'>
        <div className='space-y-3'>
          <h1 className='text-3xl font-semibold tracking-tight'>
            {t('Model catalog')}
          </h1>
          <p className='text-muted-foreground'>
            {t('Selling prices · CNY per million tokens')}
          </p>
          <p className='text-muted-foreground text-sm'>
            {t(
              'Prices follow upstream changes within your CNY ceilings. Requests above a ceiling pause before reaching the upstream.'
            )}
          </p>
        </div>
        {!user ? (
          <Button render={<Link to='/sign-in' />}>
            {t('Sign in to view your model prices')}
          </Button>
        ) : (
          <>
            {query.isPending && (
              <LoadingState inline message={t('Loading...')} />
            )}
            {query.isError && (
              <ErrorState
                title={t('Unable to load retail pricing')}
                onRetry={() => void query.refetch()}
              />
            )}
            {query.data && !query.isError && (
              <>
                {!query.data.enabled && (
                  <p role='status'>
                    {t('New priced requests are currently paused')}
                  </p>
                )}
                {query.data.products.length === 0 && (
                  <p>{t('No products available for this account')}</p>
                )}
                {query.data.products.map((product) => (
                  <section key={product.id} className='space-y-4'>
                    <h2 className='text-xl font-medium'>{product.name}</h2>
                    {product.models.length === 0 && (
                      <p className='text-muted-foreground text-sm'>
                        {t('No models have a current verified price')}
                      </p>
                    )}
                    {product.models.map((model) => (
                      <div key={model.id} className='rounded-lg border p-5'>
                        <h3 className='mb-4 font-medium'>{model.id}</h3>
                        {model.tiers.map((tier) => (
                          <div key={tier.up_to} className='space-y-3 py-3'>
                            <p className='text-muted-foreground text-xs'>
                              {t('Context limit')}: {tier.up_to}
                            </p>
                            <dl className='grid grid-cols-2 gap-4 sm:grid-cols-5'>
                              {fields.map((field) => (
                                <div key={field.key}>
                                  <dt className='text-muted-foreground text-xs'>
                                    {field.label}
                                  </dt>
                                  <dd className='mt-1 font-mono text-sm tabular-nums'>
                                    ¥{tier.prices[field.key]}
                                  </dd>
                                </div>
                              ))}
                            </dl>
                          </div>
                        ))}
                      </div>
                    ))}
                  </section>
                ))}
                <Button variant='outline' render={<Link to='/keys' />}>
                  {t('API key price limits')}
                </Button>
              </>
            )}
          </>
        )}
      </div>
    </PublicLayout>
  )
}
