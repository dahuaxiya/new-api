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
const granted: HostingSelf = {
  grant: {
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
}
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

it('explains missing entitlement and blocks new login for expired users', async () => {
  mountPage({ grant: null, binding: null })
  expect(
    await screen.findByText('No hosting entitlement yet')
  ).toBeInTheDocument()
  expect(
    screen.queryByRole('button', { name: 'Connect GPT account' })
  ).not.toBeInTheDocument()
  cleanup()
  client.clear()
  vi.restoreAllMocks()
  mountPage({ ...granted, valid: false })
  expect(
    await screen.findByRole('button', { name: 'Connect GPT account' })
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
    await screen.findByRole('button', { name: 'Connect GPT account' })
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
    {},
    expect.objectContaining({
      headers: { 'X-Security-Proof': 'fixture-proof' },
    })
  )
  const callback =
    'http://localhost:1455/auth/callback?code=private-fixture-code&state=fixture'
  await user.type(screen.getByLabelText('Authorization callback URL'), callback)
  await user.click(screen.getByRole('button', { name: 'Complete GPT sign-in' }))
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
    within(dialog).getByText(/service expiry does not change/)
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
    'Sign in to the same GPT account'
  )
  try {
    await postHosting('finish', {})
  } catch (error) {
    expect(JSON.stringify(error)).not.toContain('private-fixture-code')
  }
})
