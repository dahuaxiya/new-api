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
  fireEvent,
  render,
  screen,
  waitFor,
} from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'

import { SignOutDialog } from '@/components/sign-out-dialog'
import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

import { SiteSSOAuthorize } from '../components/site-sso-authorize'

let client: QueryClient
const returnTo = `/sso?state=${'s'.repeat(43)}&challenge=${'c'.repeat(
  43
)}&client_id=retail&verify=login`

/** 使用真实注销 API 和路由，只替换 HTTP，验证会话清除与授权回跳顺序。 */
async function mountSwitch(
  options: {
    authorize?: boolean
    verify?: 'login' | 'verify'
    redirectTo?: string
  } = {}
) {
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  client.setQueryData(['status'], {
    site_sso: { consumer: false, client_url: 'https://retail.example.test' },
  })
  useAuthStore
    .getState()
    .auth.setUser({ id: 1, username: 'old-account', role: 1 })
  const root = createRootRoute()
  const home = createRoute({
    getParentRoute: () => root,
    path: '/',
    component: () =>
      options.authorize ? (
        <SiteSSOAuthorize
          search={{
            state: 's'.repeat(43),
            challenge: 'c'.repeat(43),
            client_id: 'retail',
            verify: options.verify ?? 'login',
          }}
        />
      ) : (
        <SignOutDialog
          open
          onOpenChange={() => undefined}
          redirectTo={options.redirectTo ?? returnTo}
        />
      ),
  })
  const login = createRoute({
    getParentRoute: () => root,
    path: '/sign-in',
    validateSearch: (search: Record<string, unknown>) => search,
    component: () => <p>Account and password form</p>,
  })
  const router = createRouter({
    routeTree: root.addChildren([home, login]),
    history: createMemoryHistory({ initialEntries: ['/'] }),
  })
  await router.load()
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
  if (!options.authorize) await screen.findByRole('alertdialog')
  else {
    await screen.findByRole('button', {
      name: /Continue to API site|Verify and continue/,
    })
  }
  return router
}

afterEach(() => {
  cleanup()
  client?.clear()
  useAuthStore.setState(useAuthStore.getInitialState(), true)
  vi.restoreAllMocks()
})

/** 先注销身份源当前会话，再显示密码页并保留原 PKCE/state 授权路径。 */
it('returns to account input with the original authorization path after successful server logout', async () => {
  const post = vi
    .spyOn(api, 'post')
    .mockResolvedValue({ data: { success: true } })
  const router = await mountSwitch()
  expect(post).not.toHaveBeenCalled()
  fireEvent.click(screen.getByRole('button', { name: 'Sign out' }))
  await screen.findByText('Account and password form')
  expect(post).toHaveBeenCalledWith(
    '/api/user/auth/logout',
    undefined,
    expect.objectContaining({ skipAuthRefresh: true })
  )
  expect(useAuthStore.getState().auth.user).toBeNull()
  expect(router.state.location.search).toEqual({ redirect: returnTo })
})

/** 身份源拒绝注销时不能仅清浏览器缓存，避免再次进入时仍自动使用原账号。 */
it('keeps the current identity and authorization page when server logout fails', async () => {
  vi.spyOn(api, 'post').mockResolvedValue({
    data: { success: false, message: 'Unable to sign out' },
  })
  const router = await mountSwitch()
  fireEvent.click(screen.getByRole('button', { name: 'Sign out' }))
  await waitFor(() =>
    expect(screen.getByRole('button', { name: 'Sign out' })).toBeEnabled()
  )
  expect(router.state.location.pathname).toBe('/')
  expect(useAuthStore.getState().auth.user?.username).toBe('old-account')
})

/** 已登录授权页提供明确的换号入口，先确认再注销，最终回到密码输入。 */
it('exposes another-account entry on login authorization and preserves its return flow', async () => {
  const post = vi
    .spyOn(api, 'post')
    .mockResolvedValue({ data: { success: true } })
  const router = await mountSwitch({ authorize: true })
  expect(screen.getByText('old-account')).toBeVisible()
  fireEvent.click(
    screen.getByRole('button', { name: 'Sign in with another account' })
  )
  expect(post).not.toHaveBeenCalled()
  fireEvent.click(await screen.findByRole('button', { name: 'Sign out' }))
  await screen.findByText('Account and password form')
  expect(router.state.location.search).toEqual({ redirect: returnTo })
})

/** 敏感操作验证不能通过切换身份继续原授权；外部回跳也不能被注销入口接受。 */
it('does not offer account switching while verifying a sensitive operation', async () => {
  await mountSwitch({ authorize: true, verify: 'verify' })
  expect(
    screen.queryByRole('button', { name: 'Sign in with another account' })
  ).not.toBeInTheDocument()
})

it('drops an external return target after successful logout', async () => {
  vi.spyOn(api, 'post').mockResolvedValue({ data: { success: true } })
  const router = await mountSwitch({
    redirectTo: 'https://external.example.test/steal',
  })
  fireEvent.click(screen.getByRole('button', { name: 'Sign out' }))
  await screen.findByText('Account and password form')
  expect(router.state.location.search).toEqual({})
})
