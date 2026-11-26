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

import { RetailGroupPrices } from './retail-group-prices'

/** Load the signed-in user’s catalog and display the published tariffs in their recorded currencies.
 * @return The retail pricing page with account-scoped loading and error states.
 */
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
  return (
    <PublicLayout>
      <div className='mx-auto w-full max-w-7xl space-y-6 px-4 py-8 sm:px-6 sm:py-10'>
        <div className='space-y-3'>
          <div className='flex flex-wrap items-start justify-between gap-3'>
            <h1 className='text-3xl font-semibold tracking-tight sm:text-4xl'>
              {t('Model catalog')}
            </h1>
            <Button
              size='sm'
              variant='outline'
              render={<Link to='/model-availability' />}
            >
              {t('Model availability')}
            </Button>
          </div>
          <p className='text-muted-foreground'>{t('Per million tokens')}</p>
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
                <RetailGroupPrices
                  key={`${user.id}:${sid}`}
                  products={query.data.products}
                />
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
