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

import { RetailPricing } from '../retail-pricing'

const clients: QueryClient[] = []
const book = {
  enabled: false,
  wallet_cny_per_unit: '1',
  receipt_ratio: '1',
  margin_bps: 2000,
  fee_bps: 0,
  buffer_bps: 0,
  models: [],
  products: [],
  rates: [],
}

/** Mount the production drawer with only the HTTP boundary replaced. */
function openPricing() {
  useAuthStore.getState().auth.setUser({ id: 1, username: 'root', role: 100 })
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  clients.push(client)
  render(
    <QueryClientProvider client={client}>
      <RetailPricing />
    </QueryClientProvider>
  )
  fireEvent.click(screen.getByRole('button', { name: 'Retail pricing' }))
}

afterEach(() => {
  cleanup()
  clients.splice(0).forEach((client) => client.clear())
  vi.restoreAllMocks()
  useAuthStore.getState().auth.reset()
})

it('invalidates the preview after a multiplier edit and requires confirmation to publish', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({
    data: { success: true, data: { version: 4, book } },
  })
  const post = vi
    .spyOn(api, 'post')
    .mockResolvedValue({ data: { success: true, data: [] } })
  const put = vi.spyOn(api, 'put').mockResolvedValue({
    data: {
      success: false,
      message: 'Retail pricing changed. Reload before saving.',
    },
  })
  openPricing()
  await screen.findByLabelText('Upstream group multipliers (JSON)')
  expect(
    screen.getByRole('button', { name: 'Publish price book' })
  ).toBeDisabled()
  fireEvent.click(
    screen.getByRole('button', { name: 'Preview CNY selling prices' })
  )
  await waitFor(() =>
    expect(
      screen.getByRole('button', { name: 'Publish price book' })
    ).toBeEnabled()
  )
  fireEvent.change(screen.getByLabelText('Upstream group multipliers (JSON)'), {
    target: { value: '[{"id":"test","multiplier":"0.4"}]' },
  })
  expect(
    screen.getByRole('button', { name: 'Publish price book' })
  ).toBeDisabled()
  fireEvent.click(
    screen.getByRole('button', { name: 'Preview CNY selling prices' })
  )
  await waitFor(() =>
    expect(
      screen.getByRole('button', { name: 'Publish price book' })
    ).toBeEnabled()
  )
  fireEvent.click(screen.getByRole('button', { name: 'Publish price book' }))
  expect(put).not.toHaveBeenCalled()
  fireEvent.click(await screen.findByRole('button', { name: 'Publish' }))
  await waitFor(() =>
    expect(put).toHaveBeenCalledWith(
      '/api/retail/admin/pricing',
      expect.objectContaining({ expected_version: 4 })
    )
  )
  expect(screen.getByText('Publish new prices?')).toBeInTheDocument()
  expect(post).toHaveBeenCalledWith(
    '/api/retail/admin/pricing/preview',
    expect.objectContaining({ book: expect.anything() })
  )
  // A stale publication must be recoverable without closing the whole page.
  vi.mocked(api.get).mockResolvedValue({
    data: { success: true, data: { version: 5, book } },
  })
  fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
  fireEvent.click(screen.getByRole('button', { name: 'Refresh' }))
  await screen.findByText('Price version 5')
  expect(
    screen.getByLabelText('Upstream group multipliers (JSON)')
  ).toHaveValue('[]')
  expect(
    screen.getByRole('button', { name: 'Publish price book' })
  ).toBeDisabled()
})

it('rejects malformed JSON without sending a price preview', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({
    data: { success: true, data: { version: 4, book } },
  })
  const post = vi.spyOn(api, 'post')
  openPricing()
  fireEvent.change(
    await screen.findByLabelText('Upstream group multipliers (JSON)'),
    { target: { value: '{invalid' } }
  )
  fireEvent.click(
    screen.getByRole('button', { name: 'Preview CNY selling prices' })
  )
  expect(await screen.findByRole('alert')).toHaveTextContent(
    'Import valid JSON arrays before previewing'
  )
  expect(post).not.toHaveBeenCalled()
})

it('hides editing controls when the price book cannot be loaded', async () => {
  vi.spyOn(api, 'get').mockRejectedValue(new Error('Unavailable'))
  openPricing()
  expect(
    await screen.findByText('Unable to load retail pricing')
  ).toBeInTheDocument()
  expect(
    screen.queryByRole('button', { name: 'Publish price book' })
  ).not.toBeInTheDocument()
})
