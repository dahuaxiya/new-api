import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  createMemoryHistory,
  createRootRoute,
  createRouter,
  RouterProvider,
} from '@tanstack/react-router'
import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, expect, it } from 'vitest'

import { isRetiredPortalPage } from '@/lib/portal-navigation'
import { useAuthStore } from '@/stores/auth-store'

import { useSidebarView } from '../use-sidebar-view'

let client: QueryClient
/** 输出真实解析后的导航，避免测试依赖菜单动画和图标实现。 */
function Navigation() {
  const { navGroups, view } = useSidebarView()
  return (
    <nav>
      {view && <span>{view.parent.label}</span>}
      {navGroups
        .flatMap((g) => g.items)
        .map((i) => (
          <span key={i.title}>{i.title}</span>
        ))}
    </nav>
  )
}
/** 设置后台配置与角色，在指定 URL 解析实际工作区。 */
async function mount(path: string, role: number, modules?: object) {
  client = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Infinity } },
  })
  client.setQueryData(['status'], {
    SidebarModulesAdmin: JSON.stringify(modules ?? {}),
  })
  useAuthStore.getState().auth.setUser({ id: 1, username: 'test', role })
  const router = createRouter({
    routeTree: createRootRoute({ component: Navigation }),
    history: createMemoryHistory({ initialEntries: [path] }),
  })
  await router.load()
  return render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
}
afterEach(() => {
  cleanup()
  client.clear()
  useAuthStore.setState(useAuthStore.getInitialState(), true)
})

it('only lists customer functions for ordinary users even on a management URL', async () => {
  await mount('/user-usage', 1)
  expect(screen.getByText('API Keys')).toBeInTheDocument()
  for (const title of [
    'Administration',
    'Users',
    'Channels',
    'Chat',
    'Playground',
    'Task Logs',
  ]) {
    expect(screen.queryByText(title)).toBeNull()
  }
})
it('separates the admin workspace and filters super-admin-only functions', async () => {
  await mount('/user-usage', 10)
  expect(screen.getByText('Back to user center')).toBeInTheDocument()
  expect(screen.getByText('User usage overview')).toBeInTheDocument()
  expect(screen.queryByText('API Keys')).toBeNull()
  expect(screen.queryByText('System Settings')).toBeNull()
  expect(screen.queryByText('Open account')).toBeNull()
})
it('still applies backend navigation switches inside administration', async () => {
  await mount('/user-usage', 100, { admin: { enabled: true, channel: false } })
  expect(screen.queryByText('Channels')).toBeNull()
  expect(screen.getByText('Users')).toBeInTheDocument()
})
it('offers one admin entry from the user center instead of all management links', async () => {
  await mount('/dashboard/overview', 100)
  expect(screen.getByText('Administration')).toBeInTheDocument()
  expect(screen.queryByText('Channels')).toBeNull()
})
it('keeps old bookmarks from reopening retired user pages', async () => {
  await mount('/dashboard/overview', 1)
  for (const path of [
    '/playground',
    '/chat/abc',
    '/chat2link',
    '/usage-logs/task',
    '/usage-logs/drawing',
    '/dashboard/flow',
  ]) {
    expect(isRetiredPortalPage(path, 1)).toBe(true)
  }
  expect(isRetiredPortalPage('/keys', 1)).toBe(false)
  expect(isRetiredPortalPage('/dashboard/models', 100)).toBe(false)
})

it('shows account opening only in the super-admin workspace', async () => {
  await mount('/users/onboarding', 100)
  expect(screen.getByText('Open account')).toBeInTheDocument()
  expect(screen.getByText('Back to user center')).toBeInTheDocument()
})
