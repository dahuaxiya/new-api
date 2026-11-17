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

import { Button } from '@/components/ui/button'
import {
  getRetailCatalog,
  getRetailKeyPolicy,
} from '@/features/channels/retail-pricing-api'
import { useAuthStore } from '@/stores/auth-store'

import type { ApiKey } from '../types'
import { useApiKeys } from './api-keys-provider'

/** Show the saved retail route rather than the unrelated legacy account group; open its existing policy editor. */
export function RetailKeyGroupCell(props: { apiKey: ApiKey }) {
  const { t } = useTranslation()
  const identity = useAuthStore(
    (s) => `${s.auth.user?.id}:${s.auth.session?.sid}`
  )
  const { setCurrentRow, setOpen } = useApiKeys()
  const policy = useQuery({
    queryKey: ['retail-key-pricing', identity, props.apiKey.id],
    queryFn: async ({ signal }) => {
      const [catalog, saved] = await Promise.all([
        getRetailCatalog(signal),
        getRetailKeyPolicy(props.apiKey.id, signal),
      ])
      return { catalog, saved }
    },
    retry: false,
    staleTime: 30_000,
    meta: { errorToast: false },
  })
  let label = t('Set call group')
  if (policy.isPending) label = t('Loading...')
  else if (policy.isError) label = t('Unable to load retail pricing')
  else if (policy.data.saved.policy.product_id) {
    const id = policy.data.saved.policy.product_id
    label =
      policy.data.catalog.products.find((product) => product.id === id)?.name ??
      t('Call group unavailable')
  }
  return (
    <Button
      type='button'
      variant='ghost'
      size='sm'
      className='max-w-full truncate'
      onClick={() => {
        setCurrentRow(props.apiKey)
        setOpen('price-limits')
      }}
    >
      {label}
    </Button>
  )
}
