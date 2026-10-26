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
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

import { HostingAccount } from '../account'
import { postHosting, type HostingSelf } from '../api'

let client: QueryClient
const binding = '00000000-0000-4000-8000-000000000001'
const granted = {
  grant: {
    seat_id: '00000000-0000-4000-8000-000000000002',
    user_id: 2,
    starts_at: 1,
    ends_at: 4102444800,
    enabled: true,
    retention_days: 7,
    note: '',
  },
  valid: true,
  binding_id: binding,
  binding: { state: 'unbound', paused: false, account_label: '', plan: '' },
} satisfies HostingSelf
beforeEach(() => {
  client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  useAuthStore
    .getState()
    .auth.setUser({ id: 2, username: 'hosting-user', role: 1 })
  localStorage.clear()
  sessionStorage.clear()
})
afterEach(() => {
  cleanup()
  client.clear()
  useAuthStore.getState().auth.reset()
  vi.restoreAllMocks()
})

/** 使用真实页面、Router 和验证弹窗，HTTP 边界只提供独立合成数据。 */
function mountPage(data: HostingSelf): void {
  vi.spyOn(api, 'get').mockImplementation(async (url) => {
    if (url === '/api/gpt-hosting/store') {
      return {
        data: {
          success: true,
          data: { plans: [], methods: [], payment_ready: false },
        },
      }
    }
    if (url === '/api/gpt-hosting/orders') {
      return { data: { success: true, data: { items: [], total: 0 } } }
    }
    if (url === '/api/verify/methods') {
      return {
        data: {
          success: true,
          data: {
            scope: 'gpt.hosting.manage',
            methods: [{ method: 'password', available: true }],
            oauth_providers: [],
            password_encryption_enabled: false,
          },
        },
      }
    }
    if (url === '/api/gpt-quota/self') {
      return { data: { success: true, data: { bound: false } } }
    }
    return { data: { success: true, data } }
  })
  const router = createRouter({
    routeTree: createRootRoute({ component: HostingAccount }),
    history: createMemoryHistory({ initialEntries: ['/account/gpt'] }),
  })
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
}

/** 账号页只提供跳转入口，套餐价格和订单详情由账单页集中展示。 */
it('keeps purchase and selected-seat renewal links without showing billing details', async () => {
  mountPage(granted)
  const renew = await screen.findByRole('link', { name: 'Renew this seat' })
  const renewal = new URL(
    renew.getAttribute('href') || '',
    'https://portal.test'
  )
  expect(renewal.pathname).toBe('/wallet')
  expect(renewal.searchParams.get('tab')).toBe('hosting')
  expect(renewal.searchParams.get('seat_id')).toBe(granted.grant.seat_id)
  const purchase = screen.getByRole('link', { name: 'Buy a hosting seat' })
  const destination = new URL(
    purchase.getAttribute('href') || '',
    'https://portal.test'
  )
  expect(destination.pathname).toBe('/wallet')
  expect(destination.searchParams.get('tab')).toBe('hosting')
  expect(destination.searchParams.has('seat_id')).toBe(false)
  expect(screen.queryByText('Hosting plans')).not.toBeInTheDocument()
  expect(
    screen.queryByRole('region', { name: 'Hosting orders' })
  ).not.toBeInTheDocument()
  expect(
    vi
      .mocked(api.get)
      .mock.calls.some(([url]) => url === '/api/gpt-hosting/store')
  ).toBe(false)
})

/** 永久名额保留购买入口，但不能出现会误导用户的续费入口。 */
it('keeps only the purchase link for a permanent seat', async () => {
  mountPage({ ...granted, grant: { ...granted.grant, ends_at: -1 } })
  expect(await screen.findByText(/Permanent hosting/)).toBeInTheDocument()
  expect(
    screen.getByRole('link', { name: 'Buy a hosting seat' })
  ).toBeInTheDocument()
  expect(
    screen.queryByRole('link', { name: 'Renew this seat' })
  ).not.toBeInTheDocument()
})

it('explains missing entitlement and blocks new login for expired users', async () => {
  mountPage({ grant: null, binding: null })
  expect(
    await screen.findByText('No hosting entitlement yet')
  ).toBeInTheDocument()
  expect(
    screen.queryByRole('button', { name: 'Connect account' })
  ).not.toBeInTheDocument()
  cleanup()
  client.clear()
  vi.restoreAllMocks()
  mountPage({ ...granted, valid: false })
  expect(
    await screen.findByRole('button', { name: 'Connect account' })
  ).toBeDisabled()
})

it('requires verification and never caches callback credentials or upstream keys', async () => {
  const user = userEvent.setup()
  const auth = 'https://auth.openai.com/oauth/authorize?state=fixture'
  const post = vi.spyOn(api, 'post').mockImplementation(async (url) => {
    if (url === '/api/verify') {
      return {
        data: {
          success: true,
          data: {
            proof_token: 'fixture-proof',
            method: 'password',
            scope: 'gpt.hosting.manage',
            expires_at: 4102444800,
          },
        },
      }
    }
    if (url === '/api/gpt-hosting/login') {
      return {
        data: {
          success: true,
          data: {
            auth_url: auth,
            session_id: 'fixture-session',
            expires_at: '2100-01-01T00:00:00Z',
          },
        },
      }
    }
    return { data: { success: true, data: granted } }
  })
  mountPage(granted)
  await user.click(
    await screen.findByRole('button', { name: 'Connect account' })
  )
  await user.type(
    await screen.findByLabelText('Password', { selector: 'input' }),
    'fixture-password'
  )
  await user.click(screen.getByRole('button', { name: 'Verify' }))
  expect(
    await screen.findByRole('link', { name: 'Open official authorization' })
  ).toHaveAttribute('href', auth)
  expect(post).toHaveBeenCalledWith(
    '/api/gpt-hosting/login',
    { platform: 'codex' },
    expect.objectContaining({
      headers: { 'X-Security-Proof': 'fixture-proof' },
    })
  )
  const callback =
    'http://localhost:1455/auth/callback?code=private-fixture-code&state=fixture'
  await user.type(screen.getByLabelText('Authorization callback URL'), callback)
  await user.click(
    screen.getByRole('button', { name: 'Complete account sign-in' })
  )
  await waitFor(() =>
    expect(
      screen.queryByLabelText('Authorization callback URL')
    ).not.toBeInTheDocument()
  )
  const cached = JSON.stringify(
    client
      .getMutationCache()
      .getAll()
      .map((m) => m.state)
  )
  expect(cached).not.toContain('private-fixture-code')
  expect(JSON.stringify(localStorage)).not.toContain('private-fixture-code')
  expect(JSON.stringify(sessionStorage)).not.toContain('private-fixture-code')
})

it('unbind requires a separate destructive confirmation and can be cancelled', async () => {
  const user = userEvent.setup()
  const post = vi.spyOn(api, 'post')
  mountPage({
    ...granted,
    binding: {
      state: 'authorizing',
      paused: false,
      account_label: '',
      plan: '',
    },
  })
  await user.click(
    await screen.findByRole('button', { name: 'Unbind account' })
  )
  const dialog = await screen.findByRole('alertdialog')
  expect(
    within(dialog).getByText(/Its term does not change/)
  ).toBeInTheDocument()
  await user.click(within(dialog).getByRole('button', { name: 'Cancel' }))
  expect(post).not.toHaveBeenCalled()
})

it('strips request config and uses fixed account mismatch messages', async () => {
  vi.spyOn(api, 'post').mockRejectedValue({
    isAxiosError: true,
    config: { data: 'private-fixture-code' },
    response: {
      data: { code: 'account_changed', message: 'private-fixture-code' },
    },
  })
  await expect(postHosting('finish', {})).rejects.toThrow(
    'Sign in to the same account'
  )
  try {
    await postHosting('finish', {})
  } catch (error) {
    expect(JSON.stringify(error)).not.toContain('private-fixture-code')
  }
})

it('permanent hosting displays no expiry while still allowing the user to unbind', async () => {
  mountPage({
    ...granted,
    grant: { ...granted.grant, ends_at: -1 },
    binding: {
      state: 'active',
      paused: false,
      account_label: 'fixture',
      plan: 'plus',
    },
  })
  expect(await screen.findByText(/Permanent hosting/)).toBeInTheDocument()
  expect(
    screen.queryByText(/After expiry, access stops/)
  ).not.toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Unbind account' })).toBeEnabled()
})

it('selects a seat and clears the authorization form before switching accounts', async () => {
  const user = userEvent.setup()
  const first = {
    ...granted,
    grant: { ...granted.grant, seat_id: 'seat-first' },
  }
  const second = {
    ...granted,
    grant: { ...granted.grant, seat_id: 'seat-second', ends_at: -1 },
    binding: {
      ...granted.binding,
      account_label: 'Claude owner',
      platform: 'claude' as const,
    },
  }
  mountPage({ ...first, seats: [first, second] })
  const picker = await screen.findByLabelText('Hosting seat')
  expect(
    screen.getByRole('option', { name: /Claude owner/ })
  ).toBeInTheDocument()
  await user.selectOptions(picker, 'seat-second')
  await waitFor(() =>
    expect(
      vi
        .mocked(api.get)
        .mock.calls.some(
          ([url, config]) =>
            url === '/api/gpt-hosting/self' &&
            config?.params?.seat_id === 'seat-second'
        )
    ).toBe(true)
  )
  expect(
    screen.queryByLabelText('Authorization callback URL')
  ).not.toBeInTheDocument()
})
