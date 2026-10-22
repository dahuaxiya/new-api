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
} from '@testing-library/react'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

import { OverviewDashboard } from '../overview-dashboard'

// 图表绘制依赖 Canvas；这里验证真实请求范围及会话隔离，绘制在浏览器中检查。
vi.mock('../../models/model-charts', () => ({ ModelCharts: () => <div /> }))
vi.mock('../../models/consumption-distribution-chart', () => ({
  ConsumptionDistributionChart: () => <div />,
}))
let client: QueryClient
let failUsage = false
beforeEach(() => {
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  useAuthStore.setState(useAuthStore.getInitialState(), true)
  useAuthStore.getState().auth.setUser({ id: 1, username: 'admin', role: 100 })
  failUsage = false
  vi.spyOn(api, 'get').mockImplementation(async (url) => {
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
              created_at: 1770000000,
              model_name: 'test-model',
              token_used: 312,
              count: 8,
              quota: 10,
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
            items: [
              {
                id: 1,
                user_id: 1,
                created_at: 1770000000,
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
  expect(url.searchParams.get('startTime')).toBe(
    String(params.start_timestamp * 1000)
  )
  expect(url.searchParams.get('endTime')).toBe(
    String(params.end_timestamp * 1000)
  )
})
