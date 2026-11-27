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
import {
  QueryClient,
  QueryClientProvider,
  useQuery,
} from '@tanstack/react-query'
import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import type { ReactNode } from 'react'
import { toast } from 'sonner'
import { afterEach, expect, it, vi } from 'vitest'

import {
  getRetailGroupBindings,
  type RetailBindingState,
  type RetailPriceBook,
} from '@/features/channels/retail-pricing-api'
import { api } from '@/lib/api'

import { PublishedGroupDirectory } from '../published-group-directory'

const policy = {
  margin_bps: 2000,
  receipt_ratio: '1',
  fee_bps: 0,
  buffer_bps: 0,
}
const book: RetailPriceBook = {
  ...policy,
  enabled: true,
  wallet_cny_per_unit: '1',
  models: [],
  rates: [],
  products: [
    {
      id: 'one',
      name: 'claude-normal',
      binding_group: 'claude-normal',
      public: true,
      user_groups: [],
      routes: [
        { channel_id: 2, library_id: 9, rate_id: 'regular', models: {} },
        { channel_id: 5, rate_id: 'backup', models: {} },
      ],
    },
    {
      id: 'empty',
      name: 'private-preview',
      public: false,
      user_groups: [],
      routes: [],
    },
  ],
  group_bindings: [
    {
      channel_id: 5,
      local_group: 'claude-normal',
      upstream_group: 'legacy-cursor',
    },
  ],
}
const site = {
  site_id: 1,
  base_url: 'https://example.invalid',
  name: 'site',
  type: 14,
  models: '',
  has_key: true,
}
const supply: RetailBindingState = {
  sale_policy: policy,
  version: 1,
  server_time: 1,
  bindings: null,
  local_groups: [],
  sync: { enabled: false },
  pricing_ready: true,
  pricing_enabled: true,
  channels: [],
  inventory_keys: [
    { id: 9, name: 'Kiro', owner_site_id: 1, upstream_group: 'kiro-standard' },
  ],
  rate_channels: [
    { ...site, id: 2, status: 1 },
    { ...site, id: 5, status: 2 },
  ],
}
const props = {
  identity: 'test-session',
  book,
  supply,
  priceError: false,
  supplyError: false,
  retryPrices: vi.fn(),
  retrySupply: vi.fn(),
  onEdit: vi.fn(),
  publishButton: null,
}

/** 每个目录独立使用库存查询缓存，只有打开某把 Key 才会读取编辑资料。 */
function renderDirectory(view: ReactNode) {
  return render(
    <QueryClientProvider client={new QueryClient()}>{view}</QueryClientProvider>
  )
}

/** 命中备用组时保留整组，每条线路的 Key 与配置状态保持配对。 */
it('keeps both routes and their individual switches when a search matches only the backup', () => {
  renderDirectory(<PublishedGroupDirectory {...props} />)
  fireEvent.change(
    screen.getByRole('textbox', { name: 'Find a group or Key' }),
    { target: { value: 'CuRsOr' } }
  )
  const primary = screen.getByRole('row', { name: /claude-normal.*Kiro/ })
  expect(within(primary).getByText('#9')).toBeVisible()
  expect(within(primary).getByText('Enabled')).toBeVisible()
  const backup = screen.getByRole('row', { name: /Channel #5/ })
  expect(within(backup).getByText('Disabled')).toBeVisible()
  expect(screen.queryByText('private-preview')).not.toBeInTheDocument()
  expect(
    screen.getAllByRole('button', { name: /Edit call group routing/ })
  ).toHaveLength(1)
})

/** 补充资料读不到时保留价表里的库存编号和旧绑定，不把未知的线路标为已停用或启用。 */
it('keeps the published bindings and offers a retry when supply metadata cannot be read', () => {
  const retrySupply = vi.fn()
  renderDirectory(
    <PublishedGroupDirectory
      {...props}
      supply={undefined}
      supplyError
      retrySupply={retrySupply}
    />
  )
  expect(screen.getByText('#9')).toBeVisible()
  expect(screen.getAllByText('Unknown')).toHaveLength(2)
  for (const control of screen.getAllByRole('switch')) {
    expect(control).toHaveAttribute('aria-disabled', 'true')
  }
  expect(screen.getByText('legacy-cursor')).toBeVisible()
  expect(screen.getByText('No routes configured')).toBeVisible()
  expect(
    screen.queryByRole('button', {
      name: /Edit call group routing.*private-preview/,
    })
  ).not.toBeInTheDocument()
  fireEvent.click(
    within(screen.getByRole('alert')).getByRole('button', { name: 'Retry' })
  )
  expect(retrySupply).toHaveBeenCalledOnce()
})

/** 搜索无结果可以清空后恢复列表，并继续用库存编号定位目标。 */
it('restores the directory after an empty search and finds a Key by its inventory number', () => {
  renderDirectory(<PublishedGroupDirectory {...props} />)
  const search = screen.getByRole('textbox', { name: 'Find a group or Key' })
  fireEvent.change(search, { target: { value: 'not-a-group' } })
  expect(screen.getByText('No results found')).toBeVisible()
  fireEvent.change(search, { target: { value: '#9' } })
  expect(screen.getByRole('rowheader', { name: 'claude-normal' })).toBeVisible()
  fireEvent.click(screen.getByRole('button', { name: 'Clear search' }))
  expect(
    screen.getByRole('rowheader', { name: 'private-preview' })
  ).toBeVisible()
})

/** 仅定价的来源、缺价和按线路计价不能被备用 Key 或账号倍率替代。 */
it('shows the published selling factor once for all routes and distinguishes missing and unanchored prices', () => {
  const anchored = {
    ...book.products[0],
    rate_source_channel_id: 10,
    rate_source_key_id: 8,
  }
  const result = renderDirectory(
    <PublishedGroupDirectory
      {...props}
      book={{ ...book, products: [anchored, book.products[1]] }}
      multipliers={{ one: '0.5625' }}
    />
  )
  expect(
    screen.getAllByRole('button', { name: /Selling multiplier.*0.5625×/ })
  ).toHaveLength(1)
  expect(screen.getByText('Pricing only · #8')).toBeVisible()
  expect(screen.getByText('Route-based rate')).toBeVisible()
  result.unmount()
  renderDirectory(
    <PublishedGroupDirectory
      {...props}
      book={{ ...book, products: [anchored] }}
      multipliers={{}}
    />
  )
  expect(screen.getByRole('button', { name: /Rate unavailable/ })).toBeVisible()
})

afterEach(() => {
  vi.restoreAllMocks()
})

/** 用与页面相同的查询订阅状态；每次回读走测试服务，区分组件自己翻转与服务端持久化。
 * @param view 可选的共享线路价表；默认使用两条独立线路。
 * @return 订阅本次会话线路状态的分组目录。
 */
function ConnectedDirectory(view: { book?: RetailPriceBook }) {
  const query = useQuery({
    queryKey: ['retail-group-bindings', props.identity],
    queryFn: ({ signal }) => getRetailGroupBindings(signal),
    initialData: supply,
    staleTime: Infinity,
  })
  return (
    <PublishedGroupDirectory
      {...props}
      book={view.book ?? book}
      supply={query.data}
    />
  )
}

/** 同一调用分组有主备两条线路：停用主线须确认，提交真实渠道号，不得误传库存编号或影响备用线。 */
it('confirms disabling the selected channel and reads its status back before enabling it again', async () => {
  let state = supply
  const get = vi
    .spyOn(api, 'get')
    .mockImplementation(async () => ({ data: { success: true, data: state } }))
  const post = vi.spyOn(api, 'post').mockImplementation(async (url, body) => {
    expect(url).toBe('/api/channel/2/status')
    state = {
      ...supply,
      rate_channels: supply.rate_channels?.map((ch) =>
        ch.id === 2
          ? { ...ch, status: (body as { status: number }).status }
          : ch
      ),
    }
    return { data: { success: true, data: true } }
  })
  renderDirectory(<ConnectedDirectory />)
  const first = screen.getByRole('switch', { name: /claude-normal · #2$/ })
  const backup = screen.getByRole('switch', { name: /claude-normal · #5$/ })
  fireEvent.click(first)
  fireEvent.click(
    within(screen.getByRole('alertdialog')).getByRole('button', {
      name: 'Cancel',
    })
  )
  expect(post).not.toHaveBeenCalled()
  fireEvent.click(first)
  fireEvent.click(
    within(screen.getByRole('alertdialog')).getByRole('button', {
      name: 'Disable',
    })
  )
  await waitFor(() => expect(first).not.toBeChecked())
  await waitFor(() =>
    expect(first).not.toHaveAttribute('aria-disabled', 'true')
  )
  expect(backup).not.toBeChecked()
  fireEvent.click(first)
  await waitFor(() => expect(first).toBeChecked())
  expect(post.mock.calls.map((call) => call[1])).toEqual([
    { status: 2 },
    { status: 1 },
  ])
  expect(get).toHaveBeenCalledTimes(2)
})

/** 即使另一个调用分组被筛选隐藏，共用同一渠道仍须确认影响范围。 */
it('names other affected groups even when the directory is filtered', () => {
  const extra = {
    ...book.products[0],
    id: 'two',
    name: 'shared',
    binding_group: 'shared',
    routes: [book.products[0].routes[1]],
  }
  const post = vi.spyOn(api, 'post')
  renderDirectory(
    <ConnectedDirectory
      book={{ ...book, products: [...book.products, extra] }}
    />
  )
  fireEvent.change(
    screen.getByRole('textbox', { name: 'Find a group or Key' }),
    { target: { value: 'claude-normal' } }
  )
  fireEvent.click(screen.getByRole('switch', { name: /claude-normal · #5$/ }))
  const dialog = screen.getByRole('alertdialog')
  expect(dialog).toHaveTextContent('claude-normal, shared')
  fireEvent.click(within(dialog).getByRole('button', { name: 'Cancel' }))
  expect(post).not.toHaveBeenCalled()
})

/** 拒绝写入或服务端状态未改变时保留旧状态；请求中的开关不可再次触发。 */
it.each([false, true])(
  'keeps the previous state when a write fails or its readback disagrees (%s)',
  async (accepted) => {
    let finish!: () => void
    const waiting = new Promise<void>((resolve) => {
      finish = resolve
    })
    const post = vi.spyOn(api, 'post').mockImplementation(async () => {
      await waiting
      return {
        data: {
          success: accepted,
          data: false,
          message: 'Failed to update channel',
        },
      }
    })
    vi.spyOn(api, 'get').mockResolvedValue({
      data: { success: true, data: supply },
    })
    const failure = vi.spyOn(toast, 'error')
    const success = vi.spyOn(toast, 'success')
    renderDirectory(<ConnectedDirectory />)
    const backup = screen.getByRole('switch', { name: /claude-normal · #5$/ })
    fireEvent.click(backup)
    await waitFor(() => expect(backup).toHaveAttribute('aria-disabled', 'true'))
    fireEvent.click(backup)
    finish()
    await waitFor(() => expect(failure).toHaveBeenCalled())
    expect(success).not.toHaveBeenCalled()
    expect(post).toHaveBeenCalledOnce()
    expect(backup).not.toBeChecked()
  }
)
