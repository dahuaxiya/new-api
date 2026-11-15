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

it('requires consent and preserves tiny decimal prices when saving current ceilings', async () => {
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true } })
  const close = openPolicy()
  fireEvent.change(await screen.findByLabelText('Sales product'), {
    target: { value: 'standard' },
  })
  fireEvent.click(
    screen.getByRole('button', { name: 'Use current prices as ceilings' })
  )
  expect(screen.getByLabelText('Input')).toHaveValue('0.00000001')
  fireEvent.change(screen.getByLabelText('Maximum request charge (CNY)'), {
    target: { value: '1' },
  })
  fireEvent.change(screen.getByLabelText('Daily budget (CNY, UTC)'), {
    target: { value: '10' },
  })
  expect(
    screen.getByRole('button', { name: 'Save price limits' })
  ).toBeDisabled()
  fireEvent.click(
    screen.getByRole('switch', {
      name: 'Accept automatic price changes within these limits',
    })
  )
  fireEvent.click(screen.getByRole('button', { name: 'Save price limits' }))
  await waitFor(() =>
    expect(put).toHaveBeenCalledWith('/api/retail/keys/7/pricing', {
      expected_revision: 0,
      policy: {
        product_id: 'standard',
        accept_floating: true,
        caps: { 'test-model': prices },
        max_request_cny: '1',
        daily_budget_cny: '10',
      },
    })
  )
  await waitFor(() => expect(close).toHaveBeenCalledWith(false))
})

it('keeps saving disabled when no current model prices exist', async () => {
  openPolicy(true)
  fireEvent.change(await screen.findByLabelText('Sales product'), {
    target: { value: 'standard' },
  })
  expect(
    screen.getByText('No models have a current verified price')
  ).toBeInTheDocument()
  fireEvent.click(
    screen.getByRole('button', { name: 'Use current prices as ceilings' })
  )
  expect(
    screen.getByRole('button', { name: 'Save price limits' })
  ).toBeDisabled()
})
