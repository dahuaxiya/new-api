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
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
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
  channels: [
    {
      id: 4,
      name: 'Xingxing key',
      site_id: 4,
      type: 1,
      base_url: 'https://aixingxing.pro',
      models: 'gpt-test',
      has_key: true,
      status: 1,
    },
  ],
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

/** 一次保存新分组凭据、模型、用户组、上游倍率和毛利，不重复填写地址。 */
it('creates supply and bindings in one save without repeating the site address', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      data: { ...state, sync: { enabled: false, snapshot: source } },
    },
  })
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true } })
  const post = vi.spyOn(api, 'post').mockResolvedValue({
    data: { success: true, data: ['gpt-test', 'extra'] },
  })
  mount()
  fireEvent.click(
    await screen.findByRole('button', { name: 'Add Key and bind group' })
  )
  expect(screen.getAllByText('https://aixingxing.pro')).toHaveLength(1)
  fireEvent.change(screen.getByLabelText('Customer group 1'), {
    target: { value: 'pro' },
  })
  fireEvent.change(screen.getByLabelText('Upstream group 1'), {
    target: { value: 'ChatGPT-pro稳定' },
  })
  expect(screen.getByLabelText('Credential label 1')).toHaveValue(
    'ChatGPT-pro稳定'
  )
  fireEvent.change(screen.getByLabelText('Upstream API Key 1'), {
    target: { value: 'test-only-private-key' },
  })
  fireEvent.click(screen.getByRole('button', { name: 'Fetch Models' }))
  fireEvent.click(await screen.findByRole('checkbox', { name: 'gpt-test' }))
  expect(post).toHaveBeenCalledWith(
    '/api/channel/fetch_models',
    {
      type: 1,
      base_url: 'https://aixingxing.pro',
      key: 'test-only-private-key',
    },
    expect.anything()
  )
  fireEvent.change(screen.getByLabelText('Target margin (%)'), {
    target: { value: '25' },
  })
  fireEvent.click(
    screen.getByRole('button', { name: 'Save bindings and margin' })
  )
  fireEvent.click(screen.getByRole('button', { name: 'Confirm save' }))
  await waitFor(() =>
    expect(put).toHaveBeenCalledWith(
      '/api/retail/admin/pricing/group-bindings',
      expect.objectContaining({
        bindings: [
          {
            local_group: 'pro',
            channel_id: -1,
            upstream_group: 'ChatGPT-pro稳定',
          },
        ],
        supplies: [
          {
            id: -1,
            site_id: 4,
            name: 'ChatGPT-pro稳定',
            key: 'test-only-private-key',
            models: 'gpt-test',
          },
        ],
        sale_policy: { ...state.sale_policy, margin_bps: 2500 },
      })
    )
  )
  expect(post).toHaveBeenCalledTimes(1)
  expect(JSON.stringify(clients[0].getMutationCache().getAll())).not.toContain(
    'test-only-private-key'
  )
})

/** 凭据失败清空输入，保留其他草稿；错误缓存也不能留下 Axios 请求配置。 */
it('clears failed Key submissions while retaining mappings and excluding secrets from caches', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      data: { ...state, sync: { enabled: false, snapshot: source } },
    },
  })
  vi.spyOn(api, 'put').mockRejectedValue({
    message: 'Network failed',
    config: { data: 'test-only-private-key' },
  })
  mount()
  fireEvent.click(
    await screen.findByRole('button', { name: 'Add Key and bind group' })
  )
  fireEvent.change(screen.getByLabelText('Customer group 1'), {
    target: { value: 'pro' },
  })
  fireEvent.change(screen.getByLabelText('Upstream group 1'), {
    target: { value: 'ChatGPT-pro稳定' },
  })
  fireEvent.change(screen.getByLabelText('Upstream API Key 1'), {
    target: { value: 'test-only-private-key' },
  })
  fireEvent.click(
    screen.getByRole('button', { name: 'Save bindings and margin' })
  )
  fireEvent.click(screen.getByRole('button', { name: 'Confirm save' }))
  await screen.findByRole('alert')
  expect(screen.getByLabelText('Customer group 1')).toHaveValue('pro')
  expect(screen.getByLabelText('Upstream API Key 1')).toHaveValue('')
  expect(JSON.stringify(clients[0].getMutationCache().getAll())).not.toContain(
    'test-only-private-key'
  )
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
  await screen.findByRole('button', { name: 'Reuse an existing Key' })
  fireEvent.click(screen.getByRole('button', { name: 'Load upstream groups' }))
  await screen.findByText('0.24×')
  fireEvent.click(screen.getByRole('button', { name: 'Reuse an existing Key' }))
  fireEvent.change(screen.getByLabelText('Customer group 1'), {
    target: { value: 'default' },
  })
  fireEvent.change(screen.getByLabelText('Supply credential 1'), {
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

it('reloads saved assignments and keeps following an unchanged published rate after five minutes', async () => {
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
  expect(await screen.findByLabelText('Customer group 1')).toHaveValue(
    'default'
  )
  expect(screen.getByLabelText('Upstream group 1')).toHaveValue(
    'ChatGPT-pro稳定'
  )
  expect(
    screen.queryByText('Paused: multiplier unavailable')
  ).not.toBeInTheDocument()
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
    await screen.findByRole('button', { name: 'Reuse an existing Key' })
  )
  expect(
    screen.getByRole('button', { name: 'Save bindings and margin' })
  ).toBeDisabled()
  fireEvent.change(screen.getByLabelText('Customer group 1'), {
    target: { value: 'pro' },
  })
  fireEvent.change(screen.getByLabelText('Supply credential 1'), {
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
  expect(screen.getByLabelText('Customer group 1')).toHaveValue('pro')
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

/** Model following is opt-in per credential binding, independently of automatic price updates. */
it('saves model following on the selected binding without enabling multiplier sync', async () => {
  const binding = {
    local_group: 'default',
    channel_id: 4,
    upstream_group: 'ChatGPT-pro稳定',
  }
  vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      data: {
        ...state,
        bindings: [binding],
        model_sync: { '4:default': { pending: ['previous-observation'] } },
        sync: { enabled: false, snapshot: source },
      },
    },
  })
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true } })
  mount()
  const summary = (
    await screen.findByLabelText('Estimated selling multiplier 1')
  ).closest('summary')
  if (!summary)
    throw new Error('The multiplier must be inside the route summary')
  expect(summary.closest('details')).not.toHaveAttribute('open')
  fireEvent.click(summary)
  expect(screen.queryByText('previous-observation')).toBeNull()
  fireEvent.click(
    await screen.findByRole('switch', {
      name: 'Follow this supply group’s models',
    })
  )
  expect(screen.getByRole('checkbox', { name: 'gpt-test' })).toBeDisabled()
  expect(screen.queryByText('previous-observation')).toBeNull()
  fireEvent.click(
    screen.getByRole('button', { name: 'Save bindings and margin' })
  )
  fireEvent.click(screen.getByRole('button', { name: 'Confirm save' }))
  await waitFor(() =>
    expect(put).toHaveBeenCalledWith(
      '/api/retail/admin/pricing/group-bindings',
      expect.objectContaining({
        bindings: [{ ...binding, follow_models: true }],
        enabled: false,
      })
    )
  )
})

/** Dragging across skipped rows applies one direction, and a failed refresh preserves the draft. */
it('fetches a saved supply by ID and saves only the models left checked after dragging', async () => {
  const binding = {
    local_group: 'pro',
    channel_id: 4,
    upstream_group: 'ChatGPT-pro稳定',
  }
  const get = vi.spyOn(api, 'get').mockImplementation(async (path) => ({
    data: {
      success: true,
      data: path.endsWith('/fetch_models/4')
        ? ['a', 'b', 'c']
        : {
            ...state,
            bindings: [binding],
            sync: { enabled: false, snapshot: source },
          },
    },
  }))
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true } })
  mount()
  const fetch = await screen.findByRole('button', { name: 'Fetch Models' })
  fireEvent.click(fetch)
  const a = await screen.findByRole('checkbox', { name: 'a' })
  const b = screen.getByRole('checkbox', { name: 'b' })
  const c = screen.getByRole('checkbox', { name: 'c' })
  expect(
    screen.getByRole('button', { name: 'Save bindings and margin' })
  ).toBeDisabled()
  fireEvent.mouseDown(a, { button: 0, buttons: 1 })
  fireEvent.mouseEnter(c, { buttons: 1 })
  fireEvent.mouseEnter(b, { buttons: 1 })
  fireEvent.mouseUp(window)
  expect(a).toBeChecked()
  expect(b).toBeChecked()
  expect(c).toBeChecked()
  fireEvent.mouseDown(c, { button: 0, buttons: 1 })
  fireEvent.mouseEnter(b, { buttons: 1 })
  fireEvent.mouseUp(window)
  expect(b).not.toBeChecked()
  expect(c).not.toBeChecked()
  get.mockRejectedValueOnce(new Error('unavailable'))
  fireEvent.click(fetch)
  await screen.findByRole('alert')
  expect(screen.getByRole('checkbox', { name: 'a' })).toBeChecked()
  expect(screen.getByRole('checkbox', { name: 'gpt-test' })).toBeChecked()
  fireEvent.click(
    screen.getByRole('button', { name: 'Save bindings and margin' })
  )
  fireEvent.click(screen.getByRole('button', { name: 'Confirm save' }))
  await waitFor(() =>
    expect(put).toHaveBeenCalledWith(
      '/api/retail/admin/pricing/group-bindings',
      expect.objectContaining({
        supplies: [
          expect.objectContaining({ id: 4, models: 'gpt-test,a', key: '' }),
        ],
      })
    )
  )
})

/** Rotating a draft Key must invalidate in-flight suggestions without clearing previously selected models. */
it('ignores a late saved-key response after previewing a replacement key', async () => {
  type Response = { data: { success: boolean; data: string[] } }
  let resolveStale!: (result: Response) => void
  const stale = new Promise<Response>((resolve) => {
    resolveStale = resolve
  })
  vi.spyOn(api, 'get').mockImplementation(async (path) =>
    path.endsWith('/fetch_models/4')
      ? stale
      : {
          data: {
            success: true,
            data: {
              ...state,
              bindings: [
                {
                  local_group: 'pro',
                  channel_id: 4,
                  upstream_group: 'ChatGPT-pro稳定',
                },
              ],
            },
          },
        }
  )
  const preview = vi
    .spyOn(api, 'post')
    .mockResolvedValue({ data: { success: true, data: ['replacement-model'] } })
  mount()
  fireEvent.click(await screen.findByRole('button', { name: 'Fetch Models' }))
  fireEvent.change(screen.getByLabelText('Upstream API Key 1'), {
    target: { value: 'replacement-key' },
  })
  fireEvent.click(screen.getByRole('button', { name: 'Fetch Models' }))
  expect(
    await screen.findByRole('checkbox', { name: 'replacement-model' })
  ).not.toBeChecked()
  expect(preview).toHaveBeenCalledWith(
    '/api/channel/fetch_models',
    { type: 1, base_url: 'https://aixingxing.pro', key: 'replacement-key' },
    expect.anything()
  )
  await act(async () => {
    resolveStale({ data: { success: true, data: ['stale-model'] } })
    await stale
  })
  expect(screen.queryByRole('checkbox', { name: 'stale-model' })).toBeNull()
  expect(screen.getByRole('checkbox', { name: 'gpt-test' })).toBeChecked()
})

/** Switching credentials replaces both the upstream label and the quote, even without a saved route. */
it('reuses the registered group and clears the quote for an unregistered credential', async () => {
  const first = source.groups[0].group
  vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      data: {
        ...state,
        credential_groups: { 4: first, 5: 'regular' },
        channels: [
          ...state.channels,
          { ...state.channels[0], id: 5 },
          { ...state.channels[0], id: 6 },
        ],
        sync: {
          enabled: false,
          snapshot: {
            ...source,
            groups: [
              ...source.groups,
              { group: 'regular', multiplier: '0.12' },
            ],
          },
        },
      },
    },
  })
  mount()
  fireEvent.click(
    await screen.findByRole('button', { name: 'Reuse an existing Key' })
  )
  const select = screen.getByLabelText('Supply credential 1')
  const group = screen.getByLabelText('Upstream group 1')
  fireEvent.change(select, { target: { value: '4' } })
  expect(group).toHaveValue(first)
  expect(group).toBeDisabled()
  const initialQuote = screen.getByLabelText(
    'Estimated selling multiplier 1'
  ).textContent
  fireEvent.change(select, { target: { value: '5' } })
  expect(group).toHaveValue('regular')
  expect(
    screen.getByLabelText('Estimated selling multiplier 1').textContent
  ).not.toBe(initialQuote)
  fireEvent.change(select, { target: { value: '6' } })
  expect(group).toHaveValue('')
  expect(group).toBeEnabled()
  expect(screen.queryByLabelText('Estimated selling multiplier 1')).toBeNull()
  fireEvent.change(group, { target: { value: first } })
  fireEvent.click(screen.getByRole('button', { name: 'Reuse an existing Key' }))
  fireEvent.change(screen.getByLabelText('Supply credential 2'), {
    target: { value: '6' },
  })
  expect(screen.getByLabelText('Upstream group 2')).toHaveValue(first)
  fireEvent.change(group, { target: { value: 'regular' } })
  expect(screen.getByLabelText('Upstream group 2')).toHaveValue('regular')
})

/** Two new Keys under one site must never share a draft ID or modify each other's rate. */
it('saves two distinct Keys and upstream groups under one site', async () => {
  const groups = [
    { group: 'normal', multiplier: '0.12' },
    { group: 'fast', multiplier: '0.15' },
  ]
  vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      data: {
        ...state,
        sync: { enabled: false, snapshot: { ...source, groups } },
      },
    },
  })
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true } })
  mount()
  const add = await screen.findByRole('button', {
    name: 'Add Key and bind group',
  })
  const routes = [
    { local: 'default', upstream: 'normal', key: 'sample-normal' },
    { local: 'pro', upstream: 'fast', key: 'sample-fast' },
  ]
  for (const [index, route] of routes.entries()) {
    fireEvent.click(add)
    const n = index + 1
    expect(screen.queryByLabelText(`Supply credential ${n}`)).toBeNull()
    fireEvent.change(screen.getByLabelText(`Customer group ${n}`), {
      target: { value: route.local },
    })
    fireEvent.change(screen.getByLabelText(`Upstream group ${n}`), {
      target: { value: route.upstream },
    })
    fireEvent.change(screen.getByLabelText(`Upstream API Key ${n}`), {
      target: { value: route.key },
    })
  }
  fireEvent.click(
    screen.getByRole('button', { name: 'Save bindings and margin' })
  )
  fireEvent.click(screen.getByRole('button', { name: 'Confirm save' }))
  await waitFor(() => expect(put).toHaveBeenCalledOnce())
  const payload = put.mock.calls[0][1] as {
    bindings: {
      local_group: string
      upstream_group: string
      channel_id: number
    }[]
    supplies: { id: number; site_id: number; key: string }[]
  }
  expect(payload.bindings).toEqual(
    routes.map((r, i) => ({
      local_group: r.local,
      channel_id: -1 - i,
      upstream_group: r.upstream,
    }))
  )
  expect(payload.supplies).toHaveLength(2)
  for (const [i, r] of routes.entries()) {
    expect(payload.supplies).toContainEqual(
      expect.objectContaining({ id: -1 - i, site_id: 4, key: r.key })
    )
  }
})

/** 相同上游组名在两个站点有不同倍率。站点读取失败时不能套用公共报价，非 OpenAI 线路不提供跟随开关。 */
it('aligns each supply route with its own rate and eligible model-follow control', async () => {
  const local = {
    local_group: 'default',
    channel_id: 4,
    upstream_group: 'ChatGPT-pro稳定',
  }
  const other = { ...local, local_group: 'claude', channel_id: 8 }
  vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      data: {
        ...state,
        bindings: [local, other],
        inventory_keys: [
          {
            id: 1,
            name: 'Alpha',
            owner_site_id: 4,
            upstream_group: local.upstream_group,
          },
          {
            id: 2,
            name: 'Beta',
            owner_site_id: 8,
            upstream_group: other.upstream_group,
          },
        ],
        rate_channels: [
          { ...state.channels[0], library_id: 1 },
          { ...state.channels[0], id: 8, site_id: 8, library_id: 2, type: 14 },
        ],
        rate_site_by_channel: { 4: 4, 8: 8 },
        site_feeds: {
          4: { snapshot: source },
          8: { last_error: 'read failed', snapshot: source },
        },
        sync: { enabled: true, snapshot: source },
        pricing_ready: true,
      },
    },
  })
  mount()
  const table = within(
    await screen.findByRole('table', { name: 'Supply routes' })
  )
  const first = within(table.getByRole('row', { name: /default/ }))
  expect(first.getByText('0.24×')).toBeVisible()
  expect(first.getByRole('switch', { name: /default/ })).not.toBeChecked()
  fireEvent.click(first.getByRole('switch', { name: /default/ }))
  expect(
    screen.getByRole('button', { name: 'Save bindings and margin' })
  ).toBeEnabled()
  const second = within(table.getByRole('row', { name: /claude/ }))
  expect(second.getByText('Rate unavailable')).toBeVisible()
  expect(second.getByText('Not applicable')).toBeVisible()
  expect(second.queryByRole('switch')).toBeNull()
})
