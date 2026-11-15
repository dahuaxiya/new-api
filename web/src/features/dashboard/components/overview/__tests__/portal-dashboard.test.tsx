import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  createMemoryHistory,
  createRootRoute,
  createRouter,
  RouterProvider,
} from '@tanstack/react-router'
import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

import { OverviewDashboard } from '../overview-dashboard'

// 图表绘制依赖 Canvas；这里验证真实请求范围及会话隔离，绘制在浏览器中检查。
vi.mock('../../models/model-charts', () => ({
  ModelCharts: () => <div data-testid='model-chart' />,
}))

let client: QueryClient
let failUsage = false
let emptyUsage = false
let emptyRecent = false
beforeEach(() => {
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  useAuthStore.setState(useAuthStore.getInitialState(), true)
  useAuthStore.getState().auth.setUser({ id: 1, username: 'admin', role: 100 })
  failUsage = false
  emptyUsage = false
  emptyRecent = false
  vi.spyOn(api, 'get').mockImplementation(async (url) => {
    if (url === '/api/status') {
      return { data: { success: true, data: { site_mode: 'hosting' } } }
    }
    if (url === '/api/user/self') {
      return {
        data: {
          success: true,
          data: { quota: 1000000, used_quota: 10000, request_count: 9 },
        },
      }
    }
    if (url.startsWith('/api/token/')) {
      return { data: { success: true, data: { items: [], total: 2 } } }
    }
    if (url === '/api/data/self') {
      return {
        data: {
          success: !failUsage,
          message: 'Unavailable',
          data: [
            {
              created_at: Math.floor(Date.now() / 1000) - 3600,
              model_name: 'test-model',
              token_used: emptyUsage ? 0 : 312,
              count: emptyUsage ? 0 : 8,
              quota: emptyUsage ? 0 : 10,
            },
          ],
        },
      }
    }
    if (url.startsWith('/api/log/self?')) {
      return {
        data: {
          success: true,
          data: {
            items: emptyRecent
              ? []
              : [
                  {
                    id: 1,
                    user_id: 1,
                    created_at: Math.floor(Date.now() / 1000) - 3600,
                    type: 2,
                    content: '',
                    model_name: 'test-model',
                  },
                ],
          },
        },
      }
    }
    if (url === '/api/gpt-quota/self') {
      return { data: { success: true, data: { bound: false } } }
    }
    throw new Error(`Unexpected request: ${url}`)
  })
})
afterEach(() => {
  cleanup()
  client.clear()
  vi.restoreAllMocks()
  useAuthStore.setState(useAuthStore.getInitialState(), true)
})

/** 使用真实路由、会话和查询缓存，仅替代 HTTP 边界。 */
async function mount() {
  const router = createRouter({
    routeTree: createRootRoute({ component: OverviewDashboard }),
    history: createMemoryHistory({ initialEntries: ['/'] }),
  })
  await router.load()
  return render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
}

it('keeps an administrator personal dashboard on self endpoints without fetching cleartext keys', async () => {
  await mount()
  expect(await screen.findByText('test-model')).toBeInTheDocument()
  const urls = vi.mocked(api.get).mock.calls.map(([url]) => url)
  expect(urls).toContain('/api/data/self')
  expect(urls.some((url) => url.startsWith('/api/log/self?'))).toBe(true)
  expect(urls).not.toContain('/api/data')
  expect(urls.some((url) => url.endsWith('key'))).toBe(false)
  expect(screen.queryByText('Setup guide')).not.toBeInTheDocument()
})

it('shows a query failure instead of zero usage and refreshes it without an official probe', async () => {
  failUsage = true
  const post = vi.spyOn(api, 'post')
  await mount()
  expect(await screen.findByRole('alert')).toHaveTextContent(
    'Usage data could not be loaded'
  )
  failUsage = false
  await waitFor(() =>
    expect(screen.getByRole('button', { name: 'Refresh usage' })).toBeEnabled()
  )
  fireEvent.click(screen.getByRole('button', { name: 'Refresh usage' }))
  await waitFor(() =>
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  )
  expect(post).not.toHaveBeenCalled()
})

it('never keeps the former account recent usage while a new account loads', async () => {
  await mount()
  await screen.findByText('test-model')
  vi.mocked(api.get).mockImplementation(() => new Promise(() => {}))
  act(() =>
    useAuthStore.getState().auth.setUser({ id: 2, username: 'bob', role: 1 })
  )
  expect(screen.queryByText('test-model')).not.toBeInTheDocument()
})

/** 从仪表盘进入日志必须保留所选时间范围，日志 URL 使用毫秒而汇总接口使用秒。 */
it('preserves the selected period when opening all usage logs', async () => {
  await mount()
  await screen.findByText('test-model')
  const href = screen
    .getByRole('button', { name: 'View all' })
    .getAttribute('href')
  const params = vi
    .mocked(api.get)
    .mock.calls.find(([url]) => url === '/api/data/self')?.[1]?.params
  if (!href) throw new Error('Usage link is missing its destination')
  const url = new URL(href, 'http://localhost')
  expect(url.searchParams.get('username')).toBe('admin')
  expect(url.searchParams.get('startTime')).toBe(
    String(params.start_timestamp * 1000)
  )
  expect(url.searchParams.get('endTime')).toBe(
    String(params.end_timestamp * 1000)
  )
})

/** 中转站首页不能请求托管额度，也不能把钱包余额说明成官方订阅额度。 */
it('shows wallet usage without fetching hosted quota on retail', async () => {
  client.setQueryData(
    ['status'],
    { site_mode: 'retail' },
    { updatedAt: Date.now() + 60000 }
  )
  await mount()
  await screen.findByText('test-model')
  expect(
    screen.getByText('Balance available for API usage')
  ).toBeInTheDocument()
  expect(
    vi
      .mocked(api.get)
      .mock.calls.some(([url]) => url.startsWith('/api/gpt-quota/'))
  ).toBe(false)
  expect(screen.getByRole('heading', { name: 'API overview' })).toBeVisible()
  const actions = within(
    screen.getByRole('navigation', { name: 'Quick access' })
  )
  expect(actions.getByRole('button', { name: 'API Keys' })).toHaveAttribute(
    'href',
    '/keys'
  )
  expect(
    actions.getByRole('button', { name: 'Billing and redemption' })
  ).toHaveAttribute('href', '/wallet')
  expect(actions.getByRole('button', { name: 'Model Square' })).toHaveAttribute(
    'href',
    '/pricing'
  )
  expect(actions.queryByRole('button', { name: 'Hosted accounts' })).toBeNull()
})

/** 托管站共用首页模板，但主入口必须指向账号和套餐，不能出现零售钱包入口。 */
it('offers hosted account and plan actions on the shared hosting dashboard', async () => {
  await mount()
  await screen.findByRole('heading', { name: 'Hosting overview' })
  const actions = within(
    screen.getByRole('navigation', { name: 'Quick access' })
  )
  expect(
    actions.getByRole('button', { name: 'Hosted accounts' })
  ).toHaveAttribute('href', '/account/gpt')
  expect(
    actions.getByRole('button', { name: 'Hosting plans' })
  ).toHaveAttribute('href', '/wallet')
  expect(
    actions.queryByRole('button', { name: 'Billing and redemption' })
  ).toBeNull()
  expect(actions.queryByRole('button', { name: 'Model Square' })).toBeNull()
})

/** 菜单关闭后，首页快捷入口必须同步消失，避免同一功能出现两套可见性规则。 */
it('respects administrator and user menu settings in dashboard quick access', async () => {
  client.setQueryData(
    ['status'],
    {
      site_mode: 'retail',
      SidebarModulesAdmin: JSON.stringify({
        personal: { enabled: true, topup: false },
      }),
    },
    { updatedAt: Date.now() + 60000 }
  )
  useAuthStore.getState().auth.setUser({
    id: 1,
    username: 'customer',
    role: 1,
    sidebar_modules: JSON.stringify({
      console: { enabled: true, token: false },
    }),
  })
  await mount()
  await screen.findByText('test-model')
  const actions = within(
    screen.getByRole('navigation', { name: 'Quick access' })
  )
  expect(actions.queryByRole('button', { name: 'API Keys' })).toBeNull()
  expect(
    actions.queryByRole('button', { name: 'Billing and redemption' })
  ).toBeNull()
  expect(actions.getByRole('button', { name: 'Model Square' })).toHaveAttribute(
    'href',
    '/pricing'
  )
})

/** 成功返回的全零统计显示下一步说明，不加载空图表或图表切换控件。 */
it('replaces empty charts with guidance only after a successful zero-usage response', async () => {
  emptyUsage = true
  emptyRecent = true
  await mount()
  expect(
    await screen.findByText(
      'Open Connection details to copy the endpoint, then use an API key to send a request.'
    )
  ).toBeVisible()
  expect(screen.queryByRole('tablist', { name: 'Usage charts' })).toBeNull()
  expect(screen.queryByRole('figure', { name: 'Token usage' })).toBeNull()
  expect(screen.queryByTestId('model-chart')).toBeNull()
  expect(screen.queryByText('Recent usage')).toBeNull()
  expect(screen.queryByRole('button', { name: 'Copy API endpoint' })).toBeNull()
  expect(
    screen.getByRole('button', { name: 'Connection details' })
  ).toBeEnabled()
})

/** 汇总暂未更新时仍保留已经返回的日志，不因零值汇总隐藏真实请求。 */
it('keeps recent requests when the usage aggregation has not caught up', async () => {
  emptyUsage = true
  await mount()
  expect(await screen.findByText('test-model')).toBeVisible()
  expect(screen.getByRole('button', { name: 'View all' })).toBeVisible()
})

/** 请求仍在加载时不能显示零用量提示，避免用户误以为没有产生消耗。 */
it('does not present a pending query as empty usage', async () => {
  const get = vi.mocked(api.get).getMockImplementation()
  if (!get) throw new Error('HTTP fixture is missing')
  vi.mocked(api.get).mockImplementation((url, config) =>
    url === '/api/data/self' ? new Promise(() => {}) : get(url, config)
  )
  await mount()
  await screen.findByText('test-model')
  expect(
    screen.getByRole('region', { name: 'Usage overview' })
  ).toHaveAttribute('aria-busy', 'true')
  expect(
    screen.queryByText(
      'Open Connection details to copy the endpoint, then use an API key to send a request.'
    )
  ).toBeNull()
  expect(screen.queryByRole('figure', { name: 'Token usage' })).toBeNull()
})

/** 两项高频指标同时可见，模型细分按需打开，不能引入第二套请求范围。 */
it('shows tokens and cost together and expands model details only on demand', async () => {
  await mount()
  expect(
    await screen.findByRole('figure', { name: 'Token usage' })
  ).toBeVisible()
  expect(screen.getByRole('figure', { name: 'Site consumption' })).toBeVisible()
  expect(screen.queryByTestId('model-chart')).toBeNull()
  const trigger = screen.getByRole('button', { name: 'Model Call Analytics' })
  expect(trigger).toHaveAttribute('aria-expanded', 'false')
  fireEvent.click(trigger)
  expect(await screen.findByTestId('model-chart')).toBeVisible()
  expect(trigger).toHaveAttribute('aria-expanded', 'true')
  expect(screen.getByRole('figure', { name: 'Token usage' })).toBeVisible()
  expect(
    vi.mocked(api.get).mock.calls.filter(([url]) => url === '/api/data/self')
  ).toHaveLength(1)
})

/** 低频地址默认收起，弹层支持键盘退出并把焦点交还入口。 */
it('reveals the endpoint on demand and restores focus after Escape', async () => {
  await mount()
  const trigger = screen.getByRole('button', { name: 'Connection details' })
  expect(trigger).toHaveAttribute('aria-expanded', 'false')
  expect(screen.queryByText(`${window.location.origin}/v1`)).toBeNull()
  fireEvent.click(trigger)
  const copy = await screen.findByRole('button', { name: 'Copy API endpoint' })
  expect(copy).toBeEnabled()
  expect(trigger).toHaveAttribute('aria-expanded', 'true')
  expect(screen.getByText(`${window.location.origin}/v1`)).toBeVisible()
  fireEvent.keyDown(copy, { key: 'Escape', code: 'Escape' })
  await waitFor(() => expect(trigger).toHaveAttribute('aria-expanded', 'false'))
  await waitFor(() => expect(trigger).toHaveFocus())
})

/** 筛选改动同时更新两图和日志，不能让新时间标题对应旧数据。 */
it('updates both trend charts and recent logs with the same selected range', async () => {
  await mount()
  await screen.findByRole('figure', { name: 'Token usage' })
  const user = userEvent.setup()
  await user.click(screen.getByRole('combobox', { name: 'Time range' }))
  await user.click(await screen.findByRole('option', { name: 'Last 24 hours' }))
  await waitFor(() => {
    const params = [...vi.mocked(api.get).mock.calls]
      .reverse()
      .find(([url]) => url === '/api/data/self')?.[1]?.params
    expect(params.end_timestamp - params.start_timestamp).toBe(86400)
  })
  expect(
    await screen.findByRole('figure', { name: 'Token usage' })
  ).toBeVisible()
  expect(screen.getByRole('figure', { name: 'Site consumption' })).toBeVisible()
  const params = [...vi.mocked(api.get).mock.calls]
    .reverse()
    .find(([url]) => url === '/api/data/self')?.[1]?.params
  const logs = [...vi.mocked(api.get).mock.calls]
    .reverse()
    .find(([url]) => url.startsWith('/api/log/self?'))?.[0]
  expect(logs).toContain(`start_timestamp=${params.start_timestamp}`)
  expect(logs).toContain(`end_timestamp=${params.end_timestamp}`)
})
