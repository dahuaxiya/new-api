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
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

import { ApiKeyPricePolicy } from '../api-key-price-policy'

const clients: QueryClient[] = []
const prices = {
  input: '0.00000001',
  output: '6',
  cache_read: '0',
  cache_write: '0',
  cache_write_1h: '0',
}

/** Use real form validation and dialog semantics; fake only HTTP responses. */
function openPolicy(empty = false) {
  useAuthStore.getState().auth.setUser({ id: 2, username: 'customer', role: 1 })
  vi.spyOn(api, 'get').mockImplementation(async (url) => ({
    data: {
      success: true,
      data: String(url).endsWith('/catalog')
        ? {
            enabled: true,
            version: 1,
            products: [
              {
                id: 'standard',
                name: 'Standard',
                models: empty
                  ? []
                  : [{ id: 'test-model', tiers: [{ up_to: 1000, prices }] }],
              },
            ],
          }
        : {
            revision: 0,
            policy: { caps: {}, accept_floating: false, product_id: '' },
          },
    },
  }))
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  clients.push(client)
  const onOpenChange = vi.fn()
  render(
    <QueryClientProvider client={client}>
      <ApiKeyPricePolicy open tokenId={7} onOpenChange={onOpenChange} />
    </QueryClientProvider>
  )
  return onOpenChange
}

afterEach(() => {
  cleanup()
  clients.splice(0).forEach((client) => client.clear())
  vi.restoreAllMocks()
  useAuthStore.getState().auth.reset()
})

/** Existing keys can select a group directly, including keys without a previous pricing policy. */
it('saves only the selected group and revision without customer budgets or price consent', async () => {
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true } })
  const close = openPolicy()
  fireEvent.change(
    await screen.findByRole('combobox', { name: 'Call group' }),
    {
      target: { value: 'standard' },
    }
  )
  expect(screen.queryByText('Use current prices as ceilings')).toBeNull()
  expect(screen.queryByLabelText('Maximum request charge (CNY)')).toBeNull()
  expect(screen.queryByLabelText('Daily budget (CNY, UTC)')).toBeNull()
  expect(screen.queryByRole('switch')).toBeNull()
  fireEvent.click(screen.getByRole('button', { name: 'Save' }))
  await waitFor(() =>
    expect(put).toHaveBeenCalledWith('/api/retail/keys/7/pricing', {
      expected_revision: 0,
      product_id: 'standard',
    })
  )
  await waitFor(() => expect(close).toHaveBeenCalledWith(false))
})

/** A group selection remains a draft on save failure; no success callback or hidden follow-up mutation occurs. */
it('keeps the selected group open when the server rejects the save', async () => {
  vi.spyOn(api, 'put').mockResolvedValue({
    data: {
      success: false,
      message: 'This product or API key is not available to this account',
    },
  })
  const close = openPolicy()
  fireEvent.change(
    await screen.findByRole('combobox', { name: 'Call group' }),
    {
      target: { value: 'standard' },
    }
  )
  fireEvent.click(screen.getByRole('button', { name: 'Save' }))
  await waitFor(() => expect(api.put).toHaveBeenCalled())
  await waitFor(() =>
    expect(screen.getByRole('button', { name: 'Save' })).toBeEnabled()
  )
  expect(close).not.toHaveBeenCalled()
  expect(screen.getByRole('combobox', { name: 'Call group' })).toHaveValue(
    'standard'
  )
})
