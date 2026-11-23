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

import { RetailHero } from '../retail-hero'

const loginTransition = vi.hoisted(() => ({
  startSiteLogin: vi.fn(),
  startRetailLoginTransition: vi.fn(),
}))

vi.mock('@/features/auth/lib/site-switch', async (importOriginal) => {
  const actual =
    await importOriginal<typeof import('@/features/auth/lib/site-switch')>()
  return {
    ...actual,
    startSiteLogin: loginTransition.startSiteLogin,
  }
})

vi.mock('../../lib/retail-login-transition', async (importOriginal) => {
  const actual =
    await importOriginal<
      typeof import('@/features/home/lib/retail-login-transition')
    >()
  return {
    ...actual,
    startRetailLoginTransition: loginTransition.startRetailLoginTransition,
  }
})

let client: QueryClient
let failed = false
let hostingEnabled = true
let models: Record<string, unknown>[]
beforeEach(() => {
  failed = false
  hostingEnabled = true
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
  loginTransition.startSiteLogin.mockReset()
  loginTransition.startSiteLogin.mockResolvedValue(
    'https://host.example.test/sso?state=login-state'
  )
  loginTransition.startRetailLoginTransition.mockReset()
  loginTransition.startRetailLoginTransition.mockReturnValue({
    finished: Promise.resolve(),
    cleanup: vi.fn(),
  })
  vi.spyOn(api, 'get').mockImplementation(async (url) => {
    if (url === '/api/status') {
      return {
        data: {
          success: true,
          data: {
            site_mode: 'retail',
            site_sso: hostingEnabled
              ? {
                  consumer: true,
                  issuer_url: 'https://host.example.test',
                }
              : undefined,
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
  const view = render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
  return { router, view }
}

/** 访客入口保留登录和托管跳转，价格取自后台倍率而不是宣传占位。 */
it('shows catalog prices with login and hosting destinations for a visitor', async () => {
  await mount()
  await screen.findByText('catalog-model')
  expect(screen.getByRole('button', { name: 'Get started' })).toHaveAttribute(
    'href',
    '/sign-in'
  )
  expect(
    screen.getByRole('button', { name: 'Account hosting' })
  ).toHaveAttribute('href', 'https://host.example.test/dashboard')
  expect(screen.getByText('$2')).toBeVisible()
  expect(screen.getByText('$6')).toBeVisible()
})

/** 登录事务先失败时仍要等小猫动画收束，不能把转场截断成一次生硬跳页。 */
it('keeps the login transition visible when the handoff fails early', async () => {
  let finishTransition: () => void = () => undefined
  const finished = new Promise<void>((resolve) => {
    finishTransition = resolve
  })
  loginTransition.startSiteLogin.mockRejectedValueOnce(
    new Error('handoff unavailable')
  )
  loginTransition.startRetailLoginTransition.mockReturnValueOnce({
    finished,
    cleanup: vi.fn(),
  })
  const { router } = await mount()
  await screen.findByRole('button', { name: 'Account hosting' })

  await userEvent.click(screen.getByRole('button', { name: 'Get started' }))

  expect(loginTransition.startSiteLogin).toHaveBeenCalledOnce()
  expect(router.state.location.pathname).toBe('/')
  await act(async () => {
    finishTransition()
  })
  await waitFor(() => expect(router.state.location.pathname).toBe('/sign-in'))
})

/** 首页省略无描述和未采集的性能项，价格和可操作的详情入口仍然存在。 */
it('keeps the homepage card concise without dropping prices or its details action', async () => {
  await mount()
  await screen.findByText('catalog-model')
  expect(
    screen.queryByText('No description available.')
  ).not.toBeInTheDocument()
  expect(screen.queryByText('Groups')).not.toBeInTheDocument()
  expect(screen.queryByText('Latency')).not.toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Details' })).toBeEnabled()
  expect(screen.getByText('$2')).toBeVisible()
})

/** 未配置统一身份源时，不留下只有介绍却无法进入的托管区域。 */
it('hides the hosting callout when no hosting destination is configured', async () => {
  hostingEnabled = false
  await mount()
  await screen.findByText('catalog-model')
  expect(
    screen.queryByRole('heading', { name: 'Account hosting' })
  ).not.toBeInTheDocument()
  expect(
    screen.queryByRole('button', { name: 'Account hosting' })
  ).not.toBeInTheDocument()
})

/** 首页卸载已关闭的详情抽屉后，键盘焦点必须回到用户原来的详情按钮。 */
it('returns focus to the homepage details button after closing the drawer', async () => {
  const user = userEvent.setup()
  await mount()
  const trigger = await screen.findByRole('button', { name: 'Details' })
  await user.click(trigger)
  expect(
    await screen.findByRole('dialog', { name: 'catalog-model' })
  ).toBeVisible()
  await user.keyboard('{Escape}')
  await waitFor(() =>
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  )
  expect(trigger).toHaveFocus()
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
