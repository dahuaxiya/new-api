import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  createMemoryHistory,
  createRootRoute,
  createRouter,
  RouterProvider,
} from '@tanstack/react-router'
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, expect, it } from 'vitest'

import { useSystemConfigStore } from '@/stores/system-config-store'

import { PublicHeader } from '../public-header'

let client: QueryClient
afterEach(() => {
  cleanup()
  client?.clear()
  useSystemConfigStore.setState(useSystemConfigStore.getInitialState(), true)
})

/** 关闭菜单时内容必须不可聚焦，展开状态和正文滚动锁与按钮状态同步。 */
it('keeps a closed mobile menu inert and restores it when opened', async () => {
  client = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Infinity } },
  })
  client.setQueryData(['status'], {})
  useSystemConfigStore
    .getState()
    .setConfig({ systemName: 'luobocode · 中转站' })
  const router = createRouter({
    routeTree: createRootRoute({
      component: () => (
        <PublicHeader
          showNotifications={false}
          showLanguageSwitcher={false}
          showThemeSwitch={false}
        />
      ),
    }),
    history: createMemoryHistory(),
  })
  await router.load()
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
  const toggle = screen.getByRole('button', { name: 'Toggle navigation menu' })
  const menu = document.querySelector(
    `[id="${toggle.getAttribute('aria-controls')}"]`
  )
  expect(menu).toHaveAttribute('inert')
  expect(toggle).toHaveAttribute('aria-expanded', 'false')
  fireEvent.click(toggle)
  expect(menu).not.toHaveAttribute('inert')
  expect(toggle).toHaveAttribute('aria-expanded', 'true')
  expect(document.body.style.overflow).toBe('hidden')
  fireEvent.click(toggle)
  expect(menu).toHaveAttribute('inert')
  expect(document.body.style.overflow).toBe('')
})
