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

import { RetailGroupBindings } from '../retail-group-bindings'

const clients: QueryClient[] = []
const state = {
  version: 2,
  server_time: 1800000000,
  bindings: [],
  local_groups: ['default', 'pro'],
  channels: [{ id: 4, name: 'Xingxing key' }],
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
const source = {
  source: 'https://aixingxing.pro/api/public/rates',
  updated_at: 1800000000,
  fetched_at: 1800000000,
  groups: [{ group: 'ChatGPT-pro稳定', name: 'Pro', multiplier: '0.24' }],
}

/** Mount real selection controls with only the HTTP boundary replaced. */
function mount() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  clients.push(client)
  render(
    <QueryClientProvider client={client}>
      <RetailGroupBindings identity='test-session' onSaved={() => undefined} />
    </QueryClientProvider>
  )
}

afterEach(() => {
  cleanup()
  clients.splice(0).forEach((client) => client.clear())
  vi.restoreAllMocks()
})

it('refreshes new supply groups without losing unsaved bindings or margin', async () => {
  let created = false
  vi.spyOn(api, 'get').mockImplementation(async (path) => ({
    data: {
      success: true,
      data: path.endsWith('/supplies')
        ? {
            state: 'ready',
            group: 'retail',
            items: [
              {
                channel_id: 4,
                name: 'Site',
                base_url: 'https://aixingxing.pro',
                kind: 'reseller',
                status: 'eligible',
              },
            ],
          }
        : {
            ...state,
            channels: created
              ? [...state.channels, { id: 5, name: 'New group' }]
              : state.channels,
          },
    },
  }))
  vi.spyOn(api, 'post').mockImplementation(async () => {
    created = true
    return { data: { success: true, data: { channel_id: 5 } } }
  })
  mount()
  fireEvent.click(
    await screen.findByRole('button', { name: 'Add group binding' })
  )
  fireEvent.change(screen.getByLabelText('Local group 1'), {
    target: { value: 'pro' },
  })
  fireEvent.change(screen.getByLabelText('Target margin (%)'), {
    target: { value: '25' },
  })
  fireEvent.click(screen.getByRole('button', { name: 'Supply overview' }))
  fireEvent.click(
    await screen.findByRole('button', { name: 'Add supply group' })
  )
  fireEvent.change(screen.getByLabelText('Supply group name'), {
    target: { value: 'New group' },
  })
  fireEvent.click(screen.getByRole('button', { name: 'Create supply group' }))
  await waitFor(() =>
    expect(screen.queryByLabelText('Supply group name')).not.toBeInTheDocument()
  )
  fireEvent.click(screen.getByRole('button', { name: 'Close' }))
  expect(screen.getByLabelText('Local group 1')).toHaveValue('pro')
  expect(screen.getByLabelText('Target margin (%)')).toHaveValue(25)
  expect(screen.getByRole('option', { name: /New group/ })).toBeInTheDocument()
})

it('saves explicit group and credential choices without accepting browser-supplied prices', async () => {
  vi.spyOn(api, 'get').mockImplementation(async (path) => ({
    data: {
      success: true,
      data: path.endsWith('public-rates') ? source : state,
    },
  }))
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true } })
  mount()
  await screen.findByRole('button', { name: 'Add group binding' })
  fireEvent.click(screen.getByRole('button', { name: 'Load upstream groups' }))
  await screen.findByText('0.24×')
  fireEvent.click(screen.getByRole('button', { name: 'Add group binding' }))
  fireEvent.change(screen.getByLabelText('Local group 1'), {
    target: { value: 'default' },
  })
  fireEvent.change(screen.getByLabelText('Supply channel 1'), {
    target: { value: '4' },
  })
  fireEvent.change(screen.getByLabelText('Upstream group 1'), {
    target: { value: 'ChatGPT-pro稳定' },
  })
  fireEvent.click(
    screen.getByRole('button', { name: 'Save bindings and margin' })
  )
  fireEvent.click(screen.getByRole('button', { name: 'Confirm save' }))
  await waitFor(() =>
    expect(put).toHaveBeenCalledWith(
      '/api/retail/admin/pricing/group-bindings',
      {
        expected_version: 2,
        bindings: [
          {
            local_group: 'default',
            channel_id: 4,
            upstream_group: 'ChatGPT-pro稳定',
          },
        ],
        enabled: false,
        sale_policy: state.sale_policy,
      }
    )
  )
})

it('reloads saved assignments and shows expired prices as paused without a zero fallback', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      data: {
        ...state,
        server_time: 1800000500,
        bindings: [
          {
            local_group: 'default',
            channel_id: 4,
            upstream_group: 'ChatGPT-pro稳定',
          },
        ],
        sync: { enabled: true, snapshot: source },
      },
    },
  })
  mount()
  expect(await screen.findByLabelText('Local group 1')).toHaveValue('default')
  expect(screen.getByLabelText('Upstream group 1')).toHaveValue(
    'ChatGPT-pro稳定'
  )
  expect(
    screen.getByText('Paused: multiplier unavailable or expired')
  ).toBeInTheDocument()
  expect(
    screen.getByRole('button', { name: 'Save bindings and margin' })
  ).toBeDisabled()
})

it('rejects incomplete selections and keeps the draft when a version conflict occurs', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      data: { ...state, sync: { enabled: false, snapshot: source } },
    },
  })
  vi.spyOn(api, 'put').mockResolvedValue({
    data: {
      success: false,
      message: 'Retail pricing changed. Reload before saving.',
    },
  })
  mount()
  fireEvent.click(
    await screen.findByRole('button', { name: 'Add group binding' })
  )
  expect(
    screen.getByRole('button', { name: 'Save bindings and margin' })
  ).toBeDisabled()
  fireEvent.change(screen.getByLabelText('Local group 1'), {
    target: { value: 'pro' },
  })
  fireEvent.change(screen.getByLabelText('Supply channel 1'), {
    target: { value: '4' },
  })
  fireEvent.change(screen.getByLabelText('Upstream group 1'), {
    target: { value: 'ChatGPT-pro稳定' },
  })
  fireEvent.click(
    screen.getByRole('button', { name: 'Save bindings and margin' })
  )
  fireEvent.click(screen.getByRole('button', { name: 'Confirm save' }))
  expect(await screen.findByRole('alert')).toHaveTextContent(
    'Retail pricing changed. Reload before saving.'
  )
  expect(screen.getByLabelText('Local group 1')).toHaveValue('pro')
})

it('keeps the newer saved multiplier when a cached public preview is older', async () => {
  vi.spyOn(api, 'get').mockImplementation(async (path) => ({
    data: {
      success: true,
      data: path.endsWith('public-rates')
        ? source
        : {
            ...state,
            sync: {
              enabled: false,
              snapshot: {
                ...source,
                fetched_at: source.fetched_at + 60,
                groups: [{ ...source.groups[0], multiplier: '0.4' }],
              },
            },
          },
    },
  }))
  mount()
  await screen.findByText('0.4×')
  fireEvent.click(screen.getByRole('button', { name: 'Load upstream groups' }))
  await waitFor(() =>
    expect(
      screen.getByRole('button', { name: 'Load upstream groups' })
    ).not.toBeDisabled()
  )
  expect(screen.getByText('0.4×')).toBeInTheDocument()
  expect(screen.queryByText('0.24×')).not.toBeInTheDocument()
})

/** A margin-only edit must persist without uploading a tariff or changing feed consent. */
it('saves margin with following settings without requiring a catalog or a file', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({
    data: { success: true, data: state },
  })
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true } })
  mount()
  const margin = await screen.findByLabelText('Target margin (%)')
  expect(margin).toHaveValue(20)
  fireEvent.change(margin, { target: { value: '25' } })
  fireEvent.click(
    screen.getByRole('button', { name: 'Save bindings and margin' })
  )
  fireEvent.click(screen.getByRole('button', { name: 'Confirm save' }))
  await waitFor(() =>
    expect(put).toHaveBeenCalledWith(
      '/api/retail/admin/pricing/group-bindings',
      expect.objectContaining({
        enabled: false,
        bindings: [],
        sale_policy: {
          receipt_ratio: '1',
          margin_bps: 2500,
          fee_bps: 0,
          buffer_bps: 0,
        },
      })
    )
  )
})

/** User sees the margin effect before committing and cannot publish invalid percentages. */
it('previews the following multiplier with margin and rejects an out of range margin', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      data: {
        ...state,
        bindings: [
          {
            local_group: 'default',
            channel_id: 4,
            upstream_group: 'ChatGPT-pro稳定',
          },
        ],
        sync: { enabled: true, snapshot: source },
      },
    },
  })
  mount()
  expect(
    await screen.findByLabelText('Estimated selling multiplier 1')
  ).toHaveTextContent('≈0.3×')
  fireEvent.change(screen.getByLabelText('Target margin (%)'), {
    target: { value: '25' },
  })
  expect(
    screen.getByLabelText('Estimated selling multiplier 1')
  ).toHaveTextContent('≈0.32×')
  expect(
    screen.getByRole('button', { name: 'Save bindings and margin' })
  ).toBeEnabled()
  fireEvent.change(screen.getByLabelText('Target margin (%)'), {
    target: { value: '100' },
  })
  expect(
    screen.getByRole('button', { name: 'Save bindings and margin' })
  ).toBeDisabled()
  expect(
    screen.queryByLabelText('Estimated selling multiplier 1')
  ).not.toBeInTheDocument()
})
