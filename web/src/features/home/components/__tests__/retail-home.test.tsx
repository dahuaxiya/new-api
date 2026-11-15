import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  createMemoryHistory,
  createRootRoute,
  createRouter,
  RouterProvider,
} from '@tanstack/react-router'
import {
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

import { RetailHero } from '../retail-hero'

let client: QueryClient
let failed = false
let models: Record<string, unknown>[]
beforeEach(() => {
  failed = false
  models = [
    {
      id: 1,
      model_name: 'catalog-model',
      quota_type: 0,
      model_ratio: 1,
      completion_ratio: 3,
      enable_groups: ['default'],
    },
  ]
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  vi.spyOn(api, 'get').mockImplementation(async (url) => {
    if (url === '/api/status') {
      return {
        data: {
          success: true,
          data: {
            site_mode: 'retail',
            site_sso: {
              consumer: true,
              issuer_url: 'https://host.example.test',
            },
          },
        },
      }
    }
    if (url === '/api/pricing') {
      return {
        data: {
          success: !failed,
          message: 'Catalog unavailable',
          data: models,
          vendors: [],
          group_ratio: { default: 1 },
          usable_group: {},
          supported_endpoint: {},
          auto_groups: [],
        },
      }
    }
    throw new Error(`Unexpected request: ${url}`)
  })
})
afterEach(() => {
  cleanup()
  client.clear()
  vi.restoreAllMocks()
})

/**
 * 挂载真实路由和查询缓存；仅替换 HTTP 边界，不替换首页或定价逻辑。
 * @param authenticated 控制主按钮目的地，不建立真实登录会话。
 * @return 已挂载的测试页面。
 */
async function mount(authenticated = false) {
  const router = createRouter({
    routeTree: createRootRoute({
      component: () => <RetailHero isAuthenticated={authenticated} />,
    }),
    history: createMemoryHistory({ initialEntries: ['/'] }),
  })
  await router.load()
  return render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
}

/** 访客入口保留注册和托管跳转，价格取自后台倍率而不是宣传占位。 */
it('shows catalog prices with register and hosting destinations for a visitor', async () => {
  await mount()
  await screen.findByText('catalog-model')
  expect(screen.getByRole('button', { name: 'Get started' })).toHaveAttribute(
    'href',
    '/sign-up'
  )
  expect(
    screen.getByRole('button', { name: 'Account hosting' })
  ).toHaveAttribute('href', 'https://host.example.test/dashboard')
  expect(screen.getByText('$2')).toBeVisible()
  expect(screen.getByText('$6')).toBeVisible()
})

/** 目录失败不能伪装成零模型或零价，重试成功后恢复目录。 */
it('shows a retryable catalog failure and recovers to the current price list', async () => {
  failed = true
  await mount()
  expect(await screen.findByRole('alert')).toHaveTextContent(
    'Prices could not be loaded.'
  )
  expect(screen.queryByText('catalog-model')).toBeNull()
  failed = false
  fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
  expect(await screen.findByText('catalog-model')).toBeVisible()
  expect(screen.queryByRole('alert')).toBeNull()
})

/** 分段计费保留模型可见性与原有价格口径，不被首页过滤掉。 */
it('keeps dynamic models visible without inventing a fixed price', async () => {
  models = [
    {
      ...models[0],
      billing_mode: 'tiered_expr',
      billing_expr: 'p * 2 / 1000000 + c * 6 / 1000000',
    },
  ]
  await mount(true)
  expect(await screen.findByText('catalog-model')).toBeVisible()
  expect(screen.getByText('Dynamic Pricing')).toBeVisible()
  expect(screen.getByRole('link', { name: 'Full price list' })).toHaveAttribute(
    'href',
    '/pricing'
  )
  expect(
    screen.getByRole('button', { name: 'Enter user center' })
  ).toHaveAttribute('href', '/dashboard')
})

/** 切换示例必须同步复制内容，示例保留环境变量而不假设可用模型。 */
it('switches code examples by keyboard and copies the visible example', async () => {
  const user = userEvent.setup()
  const copy = vi.spyOn(navigator.clipboard, 'writeText').mockResolvedValue()
  await mount()
  const python = screen.getByRole('tab', { name: 'Python' })
  python.focus()
  await user.keyboard('{ArrowRight}')
  await user.keyboard('{Enter}')
  await waitFor(() =>
    expect(screen.getByRole('tab', { name: 'cURL' })).toHaveAttribute(
      'aria-selected',
      'true'
    )
  )
  const panel = screen.getByRole('tabpanel', { name: 'cURL' })
  expect(panel).toHaveTextContent('LUOBO_MODEL')
  expect(panel).toHaveTextContent('Content-Type: application/json')
  await user.click(within(panel).getByRole('button', { name: 'Copy code' }))
  expect(copy).toHaveBeenCalledWith(
    expect.stringContaining('curl "http://localhost:3000/v1/chat/completions"')
  )
  expect(copy).toHaveBeenCalledWith(
    expect.stringContaining('jq -n --arg model')
  )
})
