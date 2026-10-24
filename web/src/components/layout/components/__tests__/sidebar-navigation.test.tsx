import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  RouterProvider,
} from '@tanstack/react-router'
import {
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, expect, it, vi } from 'vitest'

import { SidebarProvider, SidebarTrigger } from '@/components/ui/sidebar'
import { LayoutProvider } from '@/context/layout-provider'
import { useAuthStore } from '@/stores/auth-store'

import { AppSidebar } from '../app-sidebar'

let client: QueryClient

/**
 * 使用真实路由、导航配置和抽屉，仅为接口状态提供独立缓存。
 * @param options 视口、角色与初始折叠状态，不改动持久化的用户布局偏好。
 * @return 用户交互驱动和当前路由，供断言实际导航结果。
 */
async function mountSidebar(
  options: { mobile?: boolean; collapsed?: boolean; role?: number } = {}
) {
  vi.stubGlobal('innerWidth', options.mobile ? 390 : 1440)
  client = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Infinity } },
  })
  client.setQueryData(['status'], { SidebarModulesAdmin: '{}' })
  useAuthStore
    .getState()
    .auth.setUser({ id: 1, username: 'test', role: options.role ?? 100 })
  const root = createRootRoute({
    component: () => (
      <LayoutProvider>
        <SidebarProvider defaultOpen={!options.collapsed}>
          <SidebarTrigger aria-label='Toggle navigation' />
          <AppSidebar />
        </SidebarProvider>
      </LayoutProvider>
    ),
  })
  const routeTree = root.addChildren(
    ['/users', '/channels', '/dashboard/overview'].map((path) =>
      createRoute({ getParentRoute: () => root, path })
    )
  )
  const router = createRouter({
    routeTree,
    history: createMemoryHistory({ initialEntries: ['/users'] }),
  })
  await router.load()
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
  return { user: userEvent.setup(), router }
}

afterEach(() => {
  cleanup()
  client?.clear()
  useAuthStore.setState(useAuthStore.getInitialState(), true)
  document.cookie = 'sidebar_state=; path=/; max-age=0'
  vi.unstubAllGlobals()
})

it('桌面点击其他菜单后选中态跟随路由，返回入口仍回到用户中心', async () => {
  const { user, router } = await mountSidebar()
  expect(screen.getByRole('link', { name: 'Users' })).toHaveAttribute(
    'data-active'
  )
  await user.click(screen.getByRole('link', { name: 'Channels' }))
  await waitFor(() => expect(router.state.location.pathname).toBe('/channels'))
  expect(screen.getByRole('link', { name: 'Channels' })).toHaveAttribute(
    'data-active'
  )
  expect(screen.getByRole('link', { name: 'Users' })).not.toHaveAttribute(
    'data-active'
  )
  await user.click(screen.getByRole('link', { name: 'Back to user center' }))
  await waitFor(() =>
    expect(router.state.location.pathname).toBe('/dashboard/overview')
  )
  expect(
    await screen.findByRole('link', { name: 'My GPT account' })
  ).toBeVisible()
})

it('手机在桌面已折叠时仍能展开完整导航，选择菜单后关闭抽屉', async () => {
  const { user, router } = await mountSidebar({ mobile: true, collapsed: true })
  await user.click(screen.getByRole('button', { name: 'Toggle navigation' }))
  const dialog = await screen.findByRole('dialog', { name: 'Sidebar' })
  expect(within(dialog).getByRole('link', { name: 'Users' })).toHaveAttribute(
    'data-active'
  )
  // Portal 内仍带主题容器，避免手机菜单回退为旧配色；内容必须可独立滚动。
  expect(dialog.querySelector('.workspace-sidebar')).toBeInTheDocument()
  expect(dialog.querySelector('[data-slot=sidebar-content]')).toHaveClass(
    'min-h-0',
    'overflow-auto'
  )
  await user.click(within(dialog).getByRole('link', { name: 'Channels' }))
  await waitFor(() => expect(router.state.location.pathname).toBe('/channels'))
  await waitFor(() =>
    expect(screen.queryByRole('dialog', { name: 'Sidebar' })).toBeNull()
  )
})

it('折叠后仍可键盘访问当前菜单并恢复展开状态', async () => {
  const { user } = await mountSidebar()
  await user.click(screen.getByRole('button', { name: 'Toggle navigation' }))
  const current = screen.getByRole('link', { name: 'Users' })
  expect(current.closest('[data-slot=sidebar]')).toHaveAttribute(
    'data-collapsible',
    'icon'
  )
  current.focus()
  expect(current).toHaveFocus()
  await user.keyboard('{Control>}b{/Control}')
  // 展开会移除折叠态 Tooltip 包装，按可访问名称重新获取链接。
  const expanded = screen.getByRole('link', { name: 'Users' })
  expect(expanded.closest('[data-slot=sidebar]')).toHaveAttribute(
    'data-state',
    'expanded'
  )
  expect(expanded).toHaveAttribute('data-active')
})

it('普通用户在管理地址也只看到用户菜单，样式调整不扩大管理权限', async () => {
  await mountSidebar({ role: 1 })
  expect(screen.getByRole('link', { name: 'My GPT account' })).toBeVisible()
  expect(screen.queryByRole('link', { name: 'Users' })).toBeNull()
  expect(screen.queryByRole('link', { name: 'Administration' })).toBeNull()
})
