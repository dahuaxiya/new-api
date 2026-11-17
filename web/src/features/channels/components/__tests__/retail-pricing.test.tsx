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

const readyBook = {
  ...book,
  models: [{ id: 'gpt-6.1-sol' }],
  products: [
    { id: 'normal', name: 'Normal', user_groups: ['default'], routes: [] },
  ],
}
const priceRows = [{ product: 'Normal', model: 'gpt-6.1-sol', available: true }]

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

/** 空草稿可以预览，但不能再被描述为可用价表发布。 */
it('keeps empty draft publication blocked after a successful empty preview', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({
    data: { success: true, data: { version: 4, book } },
  })
  vi.spyOn(api, 'post').mockResolvedValue({ data: { success: true, data: [] } })
  openPricing()
  fireEvent.click(
    await screen.findByRole('button', { name: 'Preview CNY selling prices' })
  )
  await screen.findByText('No products configured')
  expect(
    screen.getByRole('button', { name: 'Publish price book' })
  ).toBeDisabled()
})

/** Publish calculates a fresh draft, displays it in the modal, and never writes on cancel. */
it('automatically previews on publish and confirms exactly the reviewed draft', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({
    data: { success: true, data: { version: 4, book: readyBook } },
  })
  const post = vi
    .spyOn(api, 'post')
    .mockResolvedValue({ data: { success: true, data: priceRows } })
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true } })
  openPricing()
  fireEvent.click(
    await screen.findByRole('button', { name: 'Publish price book' })
  )
  await screen.findByRole('alertdialog')
  expect(
    screen.getByText('Selling prices · CNY per million tokens')
  ).toBeVisible()
  expect(post).toHaveBeenCalledTimes(1)
  expect(put).not.toHaveBeenCalled()
  fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
  expect(put).not.toHaveBeenCalled()
  fireEvent.change(screen.getByLabelText('Upstream group multipliers (JSON)'), {
    target: { value: '[{"id":"test","multiplier":"0.4"}]' },
  })
  fireEvent.click(screen.getByRole('button', { name: 'Publish price book' }))
  fireEvent.click(await screen.findByRole('button', { name: 'Publish' }))
  await waitFor(() =>
    expect(put).toHaveBeenCalledWith('/api/retail/admin/pricing', {
      expected_version: 4,
      book: { ...readyBook, rates: [{ id: 'test', multiplier: '0.4' }] },
    })
  )
})

/** A response for an earlier draft cannot authorize publication of subsequent edits. */
it('blocks confirmation when the draft changes during automatic preview', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({
    data: { success: true, data: { version: 4, book: readyBook } },
  })
  let resolve: ((value: unknown) => void) | undefined
  vi.spyOn(api, 'post').mockImplementation(
    () =>
      new Promise((done) => {
        resolve = done
      })
  )
  const put = vi.spyOn(api, 'put')
  openPricing()
  fireEvent.click(
    await screen.findByRole('button', { name: 'Publish price book' })
  )
  await waitFor(() => expect(resolve).toBeDefined())
  fireEvent.change(screen.getByLabelText('CNY per existing wallet unit'), {
    target: { value: '2' },
  })
  resolve?.({ data: { success: true, data: priceRows } })
  expect(await screen.findByRole('button', { name: 'Publish' })).toBeDisabled()
  expect(put).not.toHaveBeenCalled()
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

/** Independently saved margin invalidates old catalog previews without discarding local edits. */
it('blocks an existing catalog draft after saving following margin', async () => {
  vi.spyOn(api, 'get').mockImplementation(async (url) => ({
    data: {
      success: true,
      data: url.endsWith('group-bindings')
        ? {
            version: 4,
            server_time: 1800000000,
            bindings: [],
            local_groups: ['default'],
            channels: [],
            sync: { enabled: false },
            pricing_ready: false,
            pricing_enabled: false,
            sale_policy: {
              receipt_ratio: '1',
              margin_bps: 2000,
              fee_bps: 0,
              buffer_bps: 0,
            },
          }
        : { version: 4, book: readyBook },
    },
  }))
  vi.spyOn(api, 'post').mockResolvedValue({
    data: { success: true, data: priceRows },
  })
  vi.spyOn(api, 'put').mockResolvedValue({
    data: { success: true, data: { version: 5 } },
  })
  openPricing()
  fireEvent.change(
    await screen.findByLabelText('CNY per existing wallet unit'),
    { target: { value: '2' } }
  )
  fireEvent.click(
    screen.getByRole('button', { name: 'Preview CNY selling prices' })
  )
  await waitFor(() =>
    expect(
      screen.getByRole('button', { name: 'Publish price book' })
    ).toBeEnabled()
  )
  fireEvent.change(screen.getByLabelText('Target margin (%)'), {
    target: { value: '25' },
  })
  fireEvent.click(
    screen.getByRole('button', { name: 'Save bindings and margin' })
  )
  fireEvent.click(await screen.findByRole('button', { name: 'Confirm save' }))
  await waitFor(() =>
    expect(
      screen.getByRole('button', { name: 'Preview CNY selling prices' })
    ).toBeDisabled()
  )
  expect(
    screen.getByRole('button', { name: 'Publish price book' })
  ).toBeDisabled()
  expect(screen.getByLabelText('CNY per existing wallet unit')).toHaveValue('2')
  expect(screen.getByRole('alert')).toHaveTextContent(
    'Bindings saved. Refresh pricing before publishing the existing draft.'
  )
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

it('reads public multipliers only on demand and never imports or publishes them', async () => {
  const get = vi.spyOn(api, 'get').mockImplementation(async (url) => ({
    data: {
      success: true,
      data: url.endsWith('/public-rates')
        ? {
            source: 'https://aixingxing.pro/api/public/rates',
            updated_at: 1767225600,
            fetched_at: 1794830400,
            groups: [
              { group: '生图', name: 'Image group', multiplier: '0.05000001' },
            ],
          }
        : { version: 4, book },
    },
  }))
  const post = vi.spyOn(api, 'post')
  const put = vi.spyOn(api, 'put')
  openPricing()
  await screen.findByLabelText('Upstream group multipliers (JSON)')
  expect(get).not.toHaveBeenCalledWith(
    '/api/retail/admin/pricing/public-rates',
    expect.anything()
  )
  expect(
    screen.queryByRole('table', { name: 'Xingxing public multipliers' })
  ).not.toBeInTheDocument()
  fireEvent.click(screen.getByText('Public multiplier reference'))
  fireEvent.click(
    screen.getByRole('button', { name: 'Read public multipliers' })
  )
  await screen.findByRole('table', { name: 'Xingxing public multipliers' })
  expect(screen.getByText('0.05000001×')).toBeInTheDocument()
  expect(screen.getByText('Upstream update time')).toBeInTheDocument()
  expect(screen.getByText('Read by this site')).toBeInTheDocument()
  expect(
    screen.getByLabelText('Upstream group multipliers (JSON)')
  ).toHaveValue('[]')
  expect(screen.getByText('Price version 4')).toBeInTheDocument()
  expect(post).not.toHaveBeenCalled()
  expect(put).not.toHaveBeenCalled()

  // A failed subsequent read must hide the previous table instead of presenting stale numbers as current.
  get.mockRejectedValueOnce(new Error('Upstream unavailable'))
  fireEvent.click(
    screen.getByRole('button', { name: 'Read public multipliers' })
  )
  await screen.findByText(
    'Prices are unchanged. Retry after checking the upstream service.'
  )
  expect(
    screen.queryByRole('table', { name: 'Xingxing public multipliers' })
  ).not.toBeInTheDocument()
  expect(
    screen.getByLabelText('Upstream group multipliers (JSON)')
  ).toHaveValue('[]')
  fireEvent.click(
    screen.getByRole('button', { name: 'Read public multipliers' })
  )
  await screen.findByRole('table', { name: 'Xingxing public multipliers' })
  expect(post).not.toHaveBeenCalled()
  expect(put).not.toHaveBeenCalled()

  // Public-source data is still session-scoped; changing accounts requires a deliberate new read.
  useAuthStore
    .getState()
    .auth.setUser({ id: 2, username: 'other-root', role: 100 })
  await waitFor(() =>
    expect(
      screen.queryByRole('table', { name: 'Xingxing public multipliers' })
    ).not.toBeInTheDocument()
  )
})

/** Importing a file is optional and hidden from the following workflow by default. */
it('keeps manual multiplier imports collapsed and explains that following needs no file', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({
    data: { success: true, data: { version: 4, book } },
  })
  openPricing()
  await screen.findByText('Manual multipliers · other suppliers')
  expect(screen.getByLabelText('Import multiplier file')).not.toBeVisible()
  const manual = screen
    .getByText('Manual multipliers · other suppliers')
    .closest('details')
  expect(manual).not.toHaveAttribute('open')
  fireEvent.click(screen.getByText('Manual multipliers · other suppliers'))
  expect(manual).toHaveAttribute('open')
  expect(screen.getByLabelText('Import multiplier file')).toBeVisible()
  expect(
    screen.getByText(
      'Only for suppliers without automatic following. Bound Xingxing channels do not need this file or JSON.'
    )
  ).toBeVisible()
})

/** Generating a proposal must not publish or consent to spending. */
it('generates a local official catalog and explains account-group mismatches before publication', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      data: { version: 4, book, account_group: 'default' },
    },
  })
  const proposal = {
    ...readyBook,
    products: [
      { id: 'normal', name: 'Normal', user_groups: ['gpt-normal'], routes: [] },
    ],
  }
  const post = vi.spyOn(api, 'post').mockResolvedValue({
    data: {
      success: true,
      data: {
        version: 4,
        book: proposal,
        excluded: [
          {
            channel_id: 1,
            model: 'unknown-alias',
            reason: 'official_price_missing_or_expired',
          },
        ],
      },
    },
  })
  const put = vi.spyOn(api, 'put')
  openPricing()
  fireEvent.click(
    await screen.findByRole('button', {
      name: 'Generate official prices and routes',
    })
  )
  await screen.findByText('1 models · 1 plans')
  expect(screen.getByText(/Your account is in default/)).toBeInTheDocument()
  expect(screen.getByLabelText('Official model catalog (JSON)')).toHaveValue(
    JSON.stringify(proposal.models, null, 2)
  )
  expect(
    screen.getByRole('switch', { name: 'Accept new priced requests' })
  ).not.toBeChecked()
  expect(
    screen.getByRole('button', { name: 'Publish price book' })
  ).toBeEnabled()
  expect(
    screen.getByRole('button', { name: 'Generate official prices and routes' })
  ).toBeDisabled()
  fireEvent.click(screen.getByText('Skipped 1 model entries'))
  expect(screen.getByText(/unknown-alias/)).toBeVisible()
  expect(post).toHaveBeenCalledWith(
    '/api/retail/admin/pricing/official-draft',
    { expected_version: 4 }
  )
  expect(put).not.toHaveBeenCalled()
})
