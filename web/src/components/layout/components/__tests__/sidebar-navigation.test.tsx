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
  options: {
    mobile?: boolean
    collapsed?: boolean
    role?: number
    path?: string
    modules?: object
    userModules?: string
    canConfigureSidebar?: boolean
  } = {}
) {
  vi.stubGlobal('innerWidth', options.mobile ? 390 : 1440)
  client = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Infinity } },
  })
  client.setQueryData(['status'], {
    SidebarModulesAdmin: JSON.stringify(options.modules ?? {}),
  })
  useAuthStore.getState().auth.setUser({
    id: 1,
    username: 'test',
    role: options.role ?? 100,
    sidebar_modules: options.userModules,
    permissions: { sidebar_settings: options.canConfigureSidebar ?? true },
  })
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
    ['/users', '/channels', '/dashboard/overview', '/profile', '/security'].map(
      (path) => createRoute({ getParentRoute: () => root, path })
    )
  )
  const router = createRouter({
    routeTree,
    history: createMemoryHistory({
      initialEntries: [options.path ?? '/users'],
    }),
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
    await screen.findByRole('link', { name: 'Hosted accounts' })
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
  expect(screen.getByRole('link', { name: 'Hosted accounts' })).toBeVisible()
  expect(screen.queryByRole('link', { name: 'Users' })).toBeNull()
  expect(screen.queryByRole('link', { name: 'Administration' })).toBeNull()
})

/** 固定入口在普通用户工作区也可使用，子项导航沿用真实路由。 */
it('普通用户点击底部个人设置后可进入安全页，菜单收起并标记当前子栏目', async () => {
  const { user, router } = await mountSidebar({
    role: 1,
    path: '/dashboard/overview',
  })
  const trigger = screen.getByRole('button', { name: 'Personal settings' })
  expect(screen.queryByRole('link', { name: 'Personal settings' })).toBeNull()
  expect(trigger.closest('[data-slot=sidebar-footer]')).toBeInTheDocument()
  await user.click(trigger)
  const menu = await screen.findByRole('menu', { name: 'Personal settings' })
  await user.click(
    within(menu).getByRole('menuitem', { name: 'Account security' })
  )
  await waitFor(() => expect(router.state.location.pathname).toBe('/security'))
  await waitFor(() => expect(trigger).toHaveAttribute('aria-expanded', 'false'))
  await user.click(trigger)
  expect(
    await screen.findByRole('menuitem', { name: 'Account security' })
  ).toHaveAttribute('aria-current', 'page')
})

/** 只保留图标时仍可通过键盘打开菜单，Esc 恢复到同一个入口。 */
it('折叠侧栏的个人设置支持键盘打开、Esc关闭和资料页导航', async () => {
  const { user, router } = await mountSidebar({ collapsed: true })
  const trigger = screen.getByRole('button', { name: 'Personal settings' })
  trigger.focus()
  await user.keyboard('{Enter}')
  const menu = await screen.findByRole('menu', { name: 'Personal settings' })
  expect(trigger).toHaveAttribute('aria-expanded', 'true')
  await user.keyboard('{Escape}')
  await waitFor(() => expect(menu).not.toBeInTheDocument())
  expect(trigger).toHaveFocus()
  await user.keyboard('{Enter}')
  await user.click(
    await screen.findByRole('menuitem', { name: 'Basic profile' })
  )
  await waitFor(() => expect(router.state.location.pathname).toBe('/profile'))
})

/** 手机子菜单先于主抽屉处理 Esc，选择目标后两个导航层都必须关闭。 */
it('手机个人设置先关闭子菜单，再选择资料页会关闭主抽屉', async () => {
  const { user, router } = await mountSidebar({ mobile: true })
  await user.click(screen.getByRole('button', { name: 'Toggle navigation' }))
  const dialog = await screen.findByRole('dialog', { name: 'Sidebar' })
  const trigger = within(dialog).getByRole('button', {
    name: 'Personal settings',
  })
  await user.click(trigger)
  await screen.findByRole('menu', { name: 'Personal settings' })
  await user.keyboard('{Escape}')
  await waitFor(() =>
    expect(screen.queryByRole('menu', { name: 'Personal settings' })).toBeNull()
  )
  expect(dialog).toBeVisible()
  await user.click(trigger)
  await user.click(
    await screen.findByRole('menuitem', { name: 'Basic profile' })
  )
  await waitFor(() => expect(router.state.location.pathname).toBe('/profile'))
  await waitFor(() =>
    expect(screen.queryByRole('dialog', { name: 'Sidebar' })).toBeNull()
  )
})

/** 固定底栏必须遵守已有管理员配置，不能因迁移位置重新暴露被关闭的栏目。 */
it('管理员关闭安全栏目后浮层只显示资料，全部关闭时仍保留账户退出入口', async () => {
  const { user } = await mountSidebar({
    modules: { personal: { enabled: true, security: false } },
  })
  await user.click(screen.getByRole('button', { name: 'Personal settings' }))
  expect(
    await screen.findByRole('menuitem', { name: 'Basic profile' })
  ).toBeVisible()
  expect(
    screen.queryByRole('menuitem', { name: 'Account security' })
  ).toBeNull()
  cleanup()
  await mountSidebar({ modules: { personal: { enabled: false } } })
  expect(screen.queryByRole('button', { name: 'Personal settings' })).toBeNull()
  await user.click(screen.getByRole('button', { name: 'My account' }))
  expect(
    await screen.findByRole('menuitem', { name: 'Sign out' })
  ).toBeVisible()
  expect(screen.queryByRole('menuitem', { name: 'Basic profile' })).toBeNull()
  expect(
    screen.queryByRole('menuitem', { name: 'Account security' })
  ).toBeNull()
})

/** 普通用户的隐藏偏好继续生效；不能配置侧栏的 root 忽略历史偏好。 */
it.each([true, false])(
  '底栏按 sidebar_settings=%s 处理用户的安全栏目隐藏偏好',
  async (canConfigureSidebar) => {
    const { user } = await mountSidebar({
      canConfigureSidebar,
      userModules: JSON.stringify({
        personal: { enabled: true, security: false },
      }),
    })
    await user.click(screen.getByRole('button', { name: 'Personal settings' }))
    await screen.findByRole('menuitem', { name: 'Basic profile' })
    expect(
      screen.queryByRole('menuitem', { name: 'Account security' }) !== null
    ).toBe(!canConfigureSidebar)
  }
)

/** 合并后身份和设置共享一个可操作入口，避免出现第二个头像按钮。 */
it('底部将用户名、角色和个人设置合并成唯一账户按钮', async () => {
  await mountSidebar({ role: 1, path: '/dashboard/overview' })
  const trigger = screen.getByRole('button', { name: 'Personal settings' })
  const footer = trigger.closest('[data-slot=sidebar-footer]') as HTMLElement
  expect(within(footer).getAllByRole('button')).toHaveLength(1)
  expect(within(trigger).getByText('test')).toBeVisible()
  expect(within(trigger).getByText('Personal settings')).toBeVisible()
  expect(within(footer).queryByText('My account')).toBeNull()
})

/** 合并入口不能丢失原头像菜单的退出操作，也不能直接触发注销。 */
it('合并账户菜单的退出操作仍先显示确认窗口', async () => {
  const { user } = await mountSidebar({ role: 1 })
  await user.click(screen.getByRole('button', { name: 'Personal settings' }))
  await user.click(await screen.findByRole('menuitem', { name: 'Sign out' }))
  expect(
    await screen.findByRole('alertdialog', { name: 'Sign out' })
  ).toBeVisible()
  expect(useAuthStore.getState().auth.user?.username).toBe('test')
})

/** 手机抽屉关闭会卸载其内容，退出确认必须由抽屉外持有，避免随入口一起消失。 */
it('手机账户菜单退出时关闭抽屉，退出确认仍可取消', async () => {
  const { user } = await mountSidebar({ mobile: true, role: 1 })
  await user.click(screen.getByRole('button', { name: 'Toggle navigation' }))
  await user.click(
    await screen.findByRole('button', { name: 'Personal settings' })
  )
  await user.click(await screen.findByRole('menuitem', { name: 'Sign out' }))
  const confirmation = await screen.findByRole('alertdialog', {
    name: 'Sign out',
  })
  await waitFor(() =>
    expect(screen.queryByRole('dialog', { name: 'Sidebar' })).toBeNull()
  )
  expect(confirmation).toBeVisible()
  await user.click(within(confirmation).getByRole('button', { name: 'Cancel' }))
  await waitFor(() => expect(screen.queryByRole('alertdialog')).toBeNull())
  expect(useAuthStore.getState().auth.user?.username).toBe('test')
})
