import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

import { RetailGroupLaunch } from './retail-group-launch'

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
  useAuthStore.getState().auth.reset()
})

/** 新 Claude Key 自动读取模型。冲突后保留用户输入，重新预览才能提交。 */
it('previews a new Claude credential and retains the draft after a conflicting publish', async () => {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Infinity } },
  })
  client.setQueryData(['status'], { site_mode: 'retail' })
  useAuthStore.getState().auth.setUser({ id: 1, username: 'root', role: 100 })
  const state = {
    version: 12,
    local_groups: [],
    pricing_enabled: true,
    channels: [
      {
        id: 2,
        site_id: 2,
        name: 'Anthropic',
        type: 14,
        base_url: 'https://aixingxing.pro',
      },
    ],
    sale_policy: { margin_bps: 2000 },
    sync: {
      enabled: true,
      snapshot: {
        groups: [{ name: 'Claude', group: 'claude', multiplier: '0.5' }],
      },
    },
  }
  vi.spyOn(api, 'get').mockImplementation(async (url) => ({
    data: {
      success: true,
      data: url.endsWith('/pricing')
        ? {
            book: {
              products: [
                {
                  id: 'saved',
                  name: 'claude',
                  binding_group: 'claude',
                  routes: [{ channel_id: 5 }],
                },
              ],
              group_bindings: [
                {
                  local_group: 'claude',
                  channel_id: 5,
                  upstream_group: 'actual-upstream',
                },
              ],
            },
          }
        : state,
    },
  }))
  const post = vi.spyOn(api, 'post').mockImplementation(async (url, body) => {
    if (url.endsWith('fetch_models')) {
      return { data: { success: true, data: ['known', 'unknown'] } }
    }
    if ((body as { review: string }).review) {
      return {
        data: {
          success: false,
          message: 'Retail pricing changed. Reload before saving.',
        },
      }
    }
    return {
      data: {
        success: true,
        data: {
          version: 12,
          review: 'reviewed',
          rows: [
            { model: 'known', tiers: [] },
            { model: 'unknown', reason: 'official_price_missing_or_expired' },
          ],
        },
      },
    }
  })
  render(
    <QueryClientProvider client={client}>
      <RetailGroupLaunch />
    </QueryClientProvider>
  )
  // 即使账号定价表没有同名分组，已保存的调用分组和真实供货线路也必须可见。
  expect(await screen.findByRole('rowheader', { name: 'claude' })).toBeVisible()
  const legacyRow = screen.getByRole('row', { name: /claude.*actual-upstream/ })
  expect(within(legacyRow).getByText('Channel #5')).toBeVisible()
  fireEvent.click(screen.getByRole('button', { name: 'Publish a call group' }))
  fireEvent.change(await screen.findByLabelText('Selling multiplier source'), {
    target: { value: 'claude' },
  })
  fireEvent.change(screen.getByLabelText('Upstream API Key'), {
    target: { value: 'test-only' },
  })
  fireEvent.change(screen.getByLabelText('Call group name'), {
    target: { value: 'claude' },
  })
  expect(screen.getByRole('alert')).toHaveTextContent('#5 · actual-upstream')
  expect(
    screen.getByRole('button', { name: 'Preview CNY selling prices' })
  ).toBeDisabled()
  fireEvent.change(screen.getByLabelText('Call group name'), {
    target: { value: 'claude-normal' },
  })
  fireEvent.click(
    screen.getByRole('button', { name: 'Preview CNY selling prices' })
  )
  await screen.findByText(/unknown.*Official price is unverified or expired/)
  expect(post.mock.calls[1][1]).toMatchObject({
    models: ['known', 'unknown'],
    review: '',
    expected_version: 12,
  })
  fireEvent.click(
    screen.getByRole('button', { name: 'Save and publish group' })
  )
  await screen.findByRole('alert')
  expect(screen.getByLabelText('Call group name')).toHaveValue('claude-normal')
  expect(screen.getByLabelText('Upstream API Key')).toHaveValue('test-only')
  await waitFor(() =>
    expect(
      screen.getByRole('button', { name: 'Preview CNY selling prices' })
    ).toBeEnabled()
  )
  cleanup()
  client.clear()
})

/** 跨分组的 Key 必须排除。相同分组只有一个已绑定 Key 时自动选用，多个时等待明确选择。 */
it('matches credentials by upstream group and publishes only the reviewed route', async () => {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  client.setQueryData(['status'], { site_mode: 'retail' })
  useAuthStore.getState().auth.setUser({ id: 1, username: 'root', role: 100 })
  const channels = [2, 4, 5, 6].map((id) => ({
    id,
    site_id: 2,
    name: `supply-${id}`,
    has_key: true,
    status: 1,
  }))
  const state = {
    version: 12,
    local_groups: [],
    pricing_enabled: true,
    channels,
    sale_policy: { margin_bps: 2000 },
    credential_groups: { 4: 'claude', 5: 'gpt', 6: 'gpt' },
    sync: {
      enabled: true,
      snapshot: {
        groups: ['claude', 'gpt'].map((group) => ({
          group,
          multiplier: '0.5',
        })),
      },
    },
  }
  vi.spyOn(api, 'get').mockImplementation(async (url) => ({
    data: {
      success: true,
      data: url.endsWith('/pricing') ? { book: { products: [] } } : state,
    },
  }))
  const post = vi.spyOn(api, 'post').mockResolvedValue({
    data: {
      success: true,
      data: {
        version: 12,
        applied: true,
        review: 'confirmed-price',
        rows: [{ model: 'claude-model', tiers: [] }],
      },
    },
  })
  render(
    <QueryClientProvider client={client}>
      <RetailGroupLaunch />
    </QueryClientProvider>
  )
  fireEvent.click(screen.getByRole('button', { name: 'Publish a call group' }))
  const upstream = await screen.findByLabelText('Selling multiplier source')
  fireEvent.change(upstream, { target: { value: 'gpt' } })
  expect(screen.getByLabelText(/Supply credential/)).toHaveValue('')
  fireEvent.change(upstream, { target: { value: 'claude' } })
  expect(screen.getByLabelText(/Supply credential/)).toHaveValue('4')
  expect(
    within(screen.getByLabelText(/Supply credential/)).queryByRole('option', {
      name: /supply-5/,
    })
  ).toBeNull()
  fireEvent.change(screen.getByLabelText('Add upstream route'), {
    target: { value: '5' },
  })
  expect(screen.getByLabelText('Upstream group')).toHaveValue('gpt')
  fireEvent.change(screen.getByLabelText('Call group name'), {
    target: { value: 'new-claude' },
  })
  fireEvent.click(
    screen.getByRole('button', { name: 'Preview CNY selling prices' })
  )
  expect(await screen.findByText('supply-4 · #4')).toBeVisible()
  fireEvent.click(screen.getByRole('button', { name: 'Back' }))
  expect(screen.getByLabelText('Call group name')).toHaveValue('new-claude')
  fireEvent.click(
    await screen.findByRole('button', { name: 'Save and publish group' })
  )
  await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
  expect(post.mock.calls.at(-1)?.[1]).toMatchObject({
    site_id: 2,
    channel_id: 4,
    upstream_group: 'claude',
    local_group: 'new-claude',
    additional_routes: [{ channel_id: 5, upstream_group: 'gpt', rate_id: '' }],
    review: '',
    apply: true,
  })
  cleanup()
  client.clear()
})

/** 自动跟价时保留草稿并使用新版本；同一分组的人工改价需要重新加载才能继续。 */
it('re-previews a draft at the current price version and protects concurrent group changes', async () => {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Infinity } },
  })
  client.setQueryData(['status'], { site_mode: 'retail' })
  useAuthStore.getState().auth.setUser({ id: 1, username: 'root', role: 100 })
  let version = 1
  let margin = 2000
  const bindings = [
    { channel_id: 2, local_group: 'normal', upstream_group: 'fast' },
  ]
  vi.spyOn(api, 'get').mockImplementation(async (url) => ({
    data: {
      success: true,
      data: url.endsWith('/pricing')
        ? {
            version,
            book: {
              products: [
                {
                  id: 'existing',
                  name: 'normal',
                  public: true,
                  binding_group: 'normal',
                  margin_bps: margin,
                  rate_source_channel_id: 2,
                  routes: [
                    { channel_id: 2, rate_id: '', models: { text: 'text' } },
                  ],
                },
              ],
              group_bindings: bindings,
            },
          }
        : {
            version,
            channels: [
              { id: 2, site_id: 2, name: 'supply', has_key: true, status: 1 },
            ],
            local_groups: [],
            pricing_enabled: true,
            credential_groups: { 2: 'fast' },
            sync: {
              enabled: true,
              snapshot: { groups: [{ group: 'fast', multiplier: '0.5' }] },
            },
            sale_policy: { margin_bps: 2000 },
          },
    },
  }))
  const post = vi.spyOn(api, 'post').mockResolvedValue({
    data: {
      success: true,
      data: { version: 2, review: 'new-preview', rows: [] },
    },
  })
  render(
    <QueryClientProvider client={client}>
      <RetailGroupLaunch />
    </QueryClientProvider>
  )
  fireEvent.click(
    await screen.findByRole('button', { name: /Edit call group routing/ })
  )
  const input = await screen.findByLabelText('Target margin (%)')
  fireEvent.change(input, { target: { value: '30' } })
  version = 2
  fireEvent.click(
    screen.getByRole('button', { name: 'Preview CNY selling prices' })
  )
  await screen.findByRole('button', { name: 'Back' })
  expect(post.mock.calls[0][1]).toMatchObject({
    expected_version: 2,
    margin_bps: 3000,
  })
  fireEvent.click(screen.getByRole('button', { name: 'Back' }))
  version = 3
  margin = 4000
  fireEvent.click(
    screen.getByRole('button', { name: 'Preview CNY selling prices' })
  )
  await screen.findByRole('alert')
  expect(post).toHaveBeenCalledTimes(1)
  expect(screen.getByLabelText('Target margin (%)')).toHaveValue(30)
  fireEvent.click(screen.getByRole('button', { name: 'Reload saved bindings' }))
  expect(await screen.findByLabelText('Target margin (%)')).toHaveValue(40)
  cleanup()
  client.clear()
})

/** 同名上游组切换站点后显示对应倍率；手动读取只提交该站点，已冻结的报价仍可用于上架。 */
it('scopes identical upstream group names to their site and refreshes without changing follow', async () => {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  client.setQueryData(['status'], { site_mode: 'retail' })
  useAuthStore.getState().auth.setUser({ id: 1, username: 'root', role: 100 })
  const channels = [1, 2].map((id) => ({
    id,
    site_id: id,
    name: `site${id}`,
    type: 1,
    base_url: `https://s${id}.example`,
    has_key: true,
    status: 1,
  }))
  const state = {
    version: 7,
    pricing_enabled: true,
    channels: [],
    routing_channels: channels,
    rate_channels: channels,
    sync: { enabled: false },
    local_groups: [],
    sale_policy: { margin_bps: 0 },
    credential_groups: { 1: 'shared', 2: 'shared' },
    site_feeds: Object.fromEntries(
      channels.map((c) => [
        c.id,
        {
          url: `${c.base_url}/prices`,
          follow: false,
          snapshot: {
            groups: [
              {
                group: 'shared',
                name: 'Shared',
                multiplier: c.id === 1 ? '0.4' : '0.2',
              },
            ],
          },
        },
      ])
    ),
  }
  vi.spyOn(api, 'get').mockImplementation(async (url) => ({
    data: {
      success: true,
      data: url.endsWith('/pricing')
        ? { book: { products: [], rates: [] }, version: 7 }
        : state,
    },
  }))
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true, data: { version: 7 } } })
  render(
    <QueryClientProvider client={client}>
      <RetailGroupLaunch />
    </QueryClientProvider>
  )
  fireEvent.click(
    await screen.findByRole('button', { name: 'Publish a call group' })
  )
  const site = await screen.findByLabelText('Supply site')
  expect(
    screen.getByRole('option', { name: 'Shared · 0.4×' })
  ).toBeInTheDocument()
  fireEvent.change(site, { target: { value: '2' } })
  expect(
    screen.queryByRole('option', { name: 'Shared · 0.4×' })
  ).not.toBeInTheDocument()
  expect(
    screen.getByRole('option', { name: 'Shared · 0.2×' })
  ).toBeInTheDocument()
  fireEvent.change(screen.getByLabelText('Selling multiplier source'), {
    target: { value: 'shared' },
  })
  fireEvent.change(screen.getByLabelText('Call group name'), {
    target: { value: 'test' },
  })
  expect(
    screen.getByRole('button', { name: 'Preview CNY selling prices' })
  ).toBeEnabled()
  fireEvent.click(
    screen.getByText('Upstream rate feed', { selector: 'summary' })
  )
  fireEvent.click(
    await screen.findByRole('button', { name: 'Load upstream groups' })
  )
  await waitFor(() =>
    expect(put).toHaveBeenCalledWith('/api/retail/admin/pricing/site-rates', {
      site_id: 2,
      expected_version: 7,
      url: 'https://s2.example/prices',
      follow: undefined,
    })
  )
})

/** The local group sends only inventory references. The server resolves the supplier and its tariff. */
it('publishes a local group using its selected inventory Key ID', async () => {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Infinity } },
  })
  client.setQueryData(['status'], { site_mode: 'retail' })
  useAuthStore.getState().auth.setUser({ id: 1, username: 'root', role: 100 })
  const site = {
    id: 2,
    site_id: 2,
    name: 'Site',
    base_url: 'https://example.com',
    type: 14,
  }
  vi.spyOn(api, 'get').mockImplementation(async (url) => ({
    data: {
      success: true,
      data: url.endsWith('/pricing')
        ? { version: 23, book: { products: [] } }
        : {
            inventory_keys: [
              { id: 9, owner_site_id: 2, name: 'Kiro', upstream_group: 'kiro' },
            ],
            version: 23,
            local_groups: [],
            pricing_enabled: true,
            channels: [site],
            rate_channels: [site],
            sale_policy: { margin_bps: 2000 },
            sync: {
              enabled: true,
              snapshot: { groups: [{ group: 'kiro', multiplier: '0.45' }] },
            },
          },
    },
  }))
  const post = vi
    .spyOn(api, 'post')
    .mockResolvedValue({ data: { success: true, data: { applied: true } } })
  render(
    <QueryClientProvider client={client}>
      <RetailGroupLaunch />
    </QueryClientProvider>
  )
  fireEvent.click(
    await screen.findByRole('button', { name: 'Publish a call group' })
  )
  const field = await screen.findByLabelText('Call group name')
  fireEvent.change(field, { target: { value: 'claude-normal' } })
  fireEvent.change(screen.getByLabelText('Inventory Key'), {
    target: { value: '9' },
  })
  expect(screen.queryByLabelText('Upstream group')).toBeNull()
  fireEvent.click(
    screen.getByRole('button', { name: 'Save and publish group' })
  )
  await waitFor(() => expect(post).toHaveBeenCalled())
  const body = post.mock.calls[0][1]
  expect(body).toMatchObject({
    library_id: 9,
    additional_key_ids: [],
    local_group: 'claude-normal',
    expected_version: 23,
  })
  for (const field of [
    'key',
    'upstream_group',
    'site_id',
    'channel_id',
    'routes',
  ]) {
    expect(body).not.toHaveProperty(field)
  }
})
