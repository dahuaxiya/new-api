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
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

import { Onboarding } from '..'
import { isOfficialAuthorizationURL, onboardingSchema } from '../lib/schema'
import type { OnboardingOptions, OnboardingRequest } from '../types'

const options: OnboardingOptions = {
  instances: [
    {
      id: 'cc',
      name: 'CloudCone',
      available: true,
      slots: [
        { id: 'slot-1', name: 'Available slot', available: true },
        {
          id: 'slot-2',
          name: 'Assigned slot',
          available: false,
          reason: 'Already assigned',
        },
      ],
    },
  ],
  models: ['gpt-test'],
  api_base_url: 'https://gateway.example/v1',
}
const officialAuthorizationURL = `https://chatgpt.com/codex/desktop-auth?authorize_url=${encodeURIComponent('https://auth.openai.com/oauth/authorize?response_type=code&state=synthetic-state-for-official-login&code_challenge_method=S256&code_challenge=synthetic-challenge-with-at-least-43-characters&redirect_uri=http://localhost:1455/auth/callback')}`

let client: QueryClient
beforeEach(() => {
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  useAuthStore.getState().auth.setUser({ id: 1, username: 'root', role: 100 })
  localStorage.clear()
  sessionStorage.clear()
})
afterEach(() => {
  cleanup()
  client.clear()
  useAuthStore.getState().auth.reset()
  vi.restoreAllMocks()
})

/**
 * 用真实 Router、表单和缓存验证开户交互，只模拟 HTTP 边界。
 * @param data 实例列表测试数据。
 * @param requestId 可选恢复操作编号。
 */
function mountPage(data = options, requestId?: string): void {
  vi.spyOn(api, 'get').mockImplementation(async (url) => {
    if (!url.endsWith('/options')) {
      throw {
        response: {
          status: 404,
          data: { success: false, message: 'Request not found' },
        },
      }
    }
    return { data: { success: true, data } }
  })
  const router = createRouter({
    routeTree: createRootRoute({
      component: () => <Onboarding requestId={requestId} />,
    }),
    history: createMemoryHistory(),
  })
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
}

/**
 * 填写实际可见控件并打开确认对话框；返回用户事件实例供下一步确认。
 * @return 已完成填表的用户交互对象。
 */
async function reviewAccount(): Promise<ReturnType<typeof userEvent.setup>> {
  const user = userEvent.setup()
  await user.selectOptions(await screen.findByLabelText('Instance'), 'cc')
  await user.selectOptions(screen.getByLabelText('Account slot'), 'slot-1')
  await user.type(screen.getByLabelText('Username'), 'alice')
  await user.type(
    screen.getByLabelText('Initial password'),
    'password-example-123'
  )
  await user.click(screen.getByRole('button', { name: 'Review account' }))
  await screen.findByRole('alertdialog')
  return user
}

it('requires confirmation and shows only the safe result after opening an account', async () => {
  const post = vi.spyOn(api, 'post').mockImplementation(async (_url, body) => ({
    data: {
      success: true,
      data: {
        ...(body as OnboardingRequest),
        password: undefined,
        status: 'completed',
        user_id: 2,
      },
    },
  }))
  mountPage()
  const user = await reviewAccount()
  expect(post).not.toHaveBeenCalled()
  const dialog = screen.getByRole('alertdialog')
  expect(
    within(dialog).queryByText('password-example-123')
  ).not.toBeInTheDocument()
  await user.click(within(dialog).getByRole('button', { name: 'Open account' }))
  expect(await screen.findByText('Account opened')).toBeInTheDocument()
  expect(post).toHaveBeenCalledWith(
    '/api/vm2api/onboarding',
    expect.objectContaining({
      username: 'alice',
      instance_id: 'cc',
      slot_id: 'slot-1',
      quota: 0,
    })
  )
  expect(screen.queryByLabelText('Initial password')).not.toBeInTheDocument()
  expect(JSON.stringify(localStorage)).not.toContain('password-example-123')
  expect(JSON.stringify(sessionStorage)).not.toContain('password-example-123')
})

it('does not allow choosing an occupied slot and explains an empty instance', async () => {
  const user = userEvent.setup()
  mountPage({
    ...options,
    instances: [
      { ...options.instances[0], slots: [options.instances[0].slots[1]] },
    ],
  })
  await user.selectOptions(await screen.findByLabelText('Instance'), 'cc')
  expect(screen.getByRole('option', { name: /Assigned slot/ })).toBeDisabled()
  expect(
    screen.getByText('This instance has no available account slots.')
  ).toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Review account' })).toBeDisabled()
})

it('shows configuration guidance when no instances are configured', async () => {
  mountPage({ instances: [], models: [] })
  expect(
    await screen.findByText('No vm2api instances configured')
  ).toBeInTheDocument()
  expect(
    screen.queryByRole('button', { name: 'Review account' })
  ).not.toBeInTheDocument()
})

it('retries a network failure with the same request ID and freezes the original form', async () => {
  const post = vi
    .spyOn(api, 'post')
    .mockRejectedValueOnce(new Error('Network unavailable'))
    .mockImplementation(async (_url, body) => ({
      data: {
        success: true,
        data: {
          ...(body as OnboardingRequest),
          password: undefined,
          status: 'completed',
        },
      },
    }))
  mountPage()
  const user = await reviewAccount()
  await user.click(
    within(screen.getByRole('alertdialog')).getByRole('button', {
      name: 'Open account',
    })
  )
  await screen.findByText('Account opening failed')
  expect(screen.getByLabelText('Username')).toBeDisabled()
  await user.click(screen.getByRole('button', { name: 'Retry this request' }))
  await screen.findByText('Account opened')
  expect((post.mock.calls[0][1] as OnboardingRequest).request_id).toBe(
    (post.mock.calls[1][1] as OnboardingRequest).request_id
  )
})

it('recovers a failed request after refresh and retries without resending a password', async () => {
  const requestId = '8dd570c9-679d-4ce6-9ba3-6c4f619583d6'
  const record = {
    request_id: requestId,
    status: 'failed',
    username: 'alice',
    instance_id: 'cc',
    slot_id: 'slot-1',
  }
  const user = userEvent.setup()
  mountPage(options, requestId)
  vi.mocked(api.get).mockImplementation(async (url) => ({
    data: { success: true, data: url.endsWith('/options') ? options : record },
  }))
  const post = vi.spyOn(api, 'post').mockResolvedValue({
    data: { success: true, data: { ...record, status: 'completed' } },
  })
  await user.click(
    await screen.findByRole('button', { name: 'Retry this request' })
  )
  await screen.findByText('Account opened')
  expect(post).toHaveBeenCalledWith(`/api/vm2api/onboarding/${requestId}/retry`)
})

it.each([1, 10])(
  'role %s cannot view the opening form or fetch instance options',
  async (role) => {
    useAuthStore.getState().auth.setUser({ id: 2, username: 'user', role })
    mountPage()
    expect(await screen.findByText('Access Denied')).toBeInTheDocument()
    await waitFor(() => expect(api.get).not.toHaveBeenCalled())
    expect(screen.queryByLabelText('Initial password')).not.toBeInTheDocument()
  }
)

it('creates a slot only after confirmation and requires official login before account review', async () => {
  const user = userEvent.setup()
  mountPage({
    ...options,
    instances: [
      {
        ...options.instances[0],
        slots: [],
        exits: [{ id: 'exit-1', name: 'IPv6 exit', available: true }],
      },
    ],
  })
  const post = vi.spyOn(api, 'post').mockResolvedValue({
    data: {
      success: true,
      data: {
        slot: {
          id: 'new-slot',
          name: 'New slot',
          available: false,
          logged_in: false,
          assigned: false,
        },
      },
    },
  })
  await user.selectOptions(await screen.findByLabelText('Instance'), 'cc')
  await user.click(screen.getByRole('button', { name: 'Create new slot' }))
  await user.type(screen.getByLabelText('Slot name'), 'New slot')
  await user.selectOptions(
    screen.getByLabelText('Outbound connection'),
    'exit-1'
  )
  await user.click(screen.getByRole('button', { name: 'Create slot' }))
  expect(post).not.toHaveBeenCalled()
  await user.click(
    within(await screen.findByRole('alertdialog')).getByRole('button', {
      name: 'Create slot',
    })
  )
  expect(
    await screen.findByRole('button', { name: 'Start GPT authorization' })
  ).toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Review account' })).toBeDisabled()
  expect(screen.getByLabelText('Account slot')).toHaveValue('new-slot')
  expect(post).toHaveBeenCalledWith(
    '/api/vm2api/onboarding/slots',
    expect.objectContaining({
      instance_id: 'cc',
      name: 'New slot',
      exit_id: 'exit-1',
    })
  )
})

it('allows canceling slot creation without freezing the slot draft', async () => {
  const user = userEvent.setup()
  mountPage({
    ...options,
    instances: [
      {
        ...options.instances[0],
        exits: [{ id: 'exit-1', name: 'IPv6 exit', available: true }],
      },
    ],
  })
  await user.selectOptions(await screen.findByLabelText('Instance'), 'cc')
  await user.click(screen.getByRole('button', { name: 'Create new slot' }))
  await user.type(screen.getByLabelText('Slot name'), 'New slot')
  await user.selectOptions(
    screen.getByLabelText('Outbound connection'),
    'exit-1'
  )
  await user.click(screen.getByRole('button', { name: 'Create slot' }))
  await user.click(
    within(await screen.findByRole('alertdialog')).getByRole('button', {
      name: 'Cancel',
    })
  )
  expect(screen.getByLabelText('Slot name')).toBeEnabled()
})

it('completes official OAuth manually and then enables opening the selected slot', async () => {
  const user = userEvent.setup()
  mountPage({
    ...options,
    instances: [
      {
        ...options.instances[0],
        slots: [
          {
            id: 'slot-1',
            name: 'Available slot',
            available: false,
            logged_in: false,
            assigned: false,
          },
        ],
      },
    ],
  })
  vi.spyOn(api, 'post').mockImplementation(async (url) => {
    const data = url.endsWith('/start')
      ? {
          auth_url: officialAuthorizationURL,
          session_id: 'oauth-session',
          expires_at: '2099-01-01',
        }
      : {
          slot: {
            id: 'slot-1',
            name: 'Available slot',
            available: true,
            logged_in: true,
            assigned: false,
          },
        }
    return { data: { success: true, data } }
  })
  await user.selectOptions(await screen.findByLabelText('Instance'), 'cc')
  await user.selectOptions(screen.getByLabelText('Account slot'), 'slot-1')
  await user.click(
    screen.getByRole('button', { name: 'Start GPT authorization' })
  )
  const link = await screen.findByRole('link', {
    name: 'Open official authorization',
  })
  expect(link).toHaveAttribute('href', officialAuthorizationURL)
  expect(link).toHaveAttribute('rel', 'noopener noreferrer')
  await user.type(
    screen.getByLabelText('Authorization callback URL'),
    'http://localhost:1455/auth/callback?code=test&state=test'
  )
  await user.click(screen.getByRole('button', { name: 'Complete GPT sign-in' }))
  expect(
    await screen.findByText(
      'GPT account is signed in. Continue with the user details below.'
    )
  ).toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Review account' })).toBeEnabled()
  expect(
    screen.queryByLabelText('Authorization callback URL')
  ).not.toBeInTheDocument()
  expect(JSON.stringify(sessionStorage)).not.toContain('code=test')
})

it('does not select a late-created slot after the administrator changes instances', async () => {
  const user = userEvent.setup()
  mountPage({
    ...options,
    instances: [
      {
        ...options.instances[0],
        exits: [{ id: 'exit-1', name: 'IPv6 exit', available: true }],
      },
      { id: 'bwh', name: 'BandwagonHost', available: true, slots: [] },
    ],
  })
  let completeCreate: (value: unknown) => void = () => undefined
  vi.spyOn(api, 'post').mockImplementation(
    () =>
      new Promise((resolve) => {
        completeCreate = resolve
      })
  )
  await user.selectOptions(await screen.findByLabelText('Instance'), 'cc')
  await user.click(screen.getByRole('button', { name: 'Create new slot' }))
  await user.type(screen.getByLabelText('Slot name'), 'New slot')
  await user.selectOptions(
    screen.getByLabelText('Outbound connection'),
    'exit-1'
  )
  await user.click(screen.getByRole('button', { name: 'Create slot' }))
  await user.click(
    within(await screen.findByRole('alertdialog')).getByRole('button', {
      name: 'Create slot',
    })
  )
  await user.selectOptions(screen.getByLabelText('Instance'), 'bwh')
  await act(async () =>
    completeCreate({
      data: {
        success: true,
        data: {
          slot: {
            id: 'late-slot',
            name: 'Late slot',
            available: false,
            logged_in: false,
            assigned: false,
          },
        },
      },
    })
  )
  expect(screen.getByLabelText('Instance')).toHaveValue('bwh')
  expect(screen.getByLabelText('Account slot')).toHaveValue('')
})

it.each([
  'javascript:alert(1)',
  'https://auth.openai.com.attacker.example/oauth',
  'https://user:pass@auth.openai.com/oauth',
  'https://chatgpt.com/codex/desktop-auth?authorize_url=https%3A%2F%2Fevil.example%2Foauth%2Fauthorize',
  'https://auth.openai.com/oauth/authorize#unexpected',
])('refuses to open an untrusted official authorization URL: %s', (url) => {
  expect(isOfficialAuthorizationURL(url)).toBe(false)
})

it('rejects a short initial password before sending an opening request', () => {
  const result = onboardingSchema.safeParse({
    username: 'alice',
    display_name: 'Alice',
    password: 'short-password',
    quota_dollars: 0,
    instance_id: 'cc',
    slot_id: 'slot-1',
  })
  expect(result.success).toBe(false)
  if (!result.success) expect(result.error.issues[0].path).toEqual(['password'])
})

it('restores a timed-out slot draft after refresh and retries the original request ID', async () => {
  const data = {
    ...options,
    instances: [
      {
        ...options.instances[0],
        exits: [{ id: 'exit-1', name: 'IPv6 exit', available: true }],
      },
    ],
  }
  const user = userEvent.setup()
  mountPage(data)
  const post = vi
    .spyOn(api, 'post')
    .mockRejectedValueOnce(new Error('Network unavailable'))
    .mockResolvedValue({
      data: {
        success: true,
        data: {
          slot: {
            id: 'new-slot',
            name: 'New slot',
            available: false,
            logged_in: false,
            assigned: false,
          },
        },
      },
    })
  await user.selectOptions(await screen.findByLabelText('Instance'), 'cc')
  await user.click(screen.getByRole('button', { name: 'Create new slot' }))
  await user.type(screen.getByLabelText('Slot name'), 'New slot')
  await user.selectOptions(
    screen.getByLabelText('Outbound connection'),
    'exit-1'
  )
  await user.click(screen.getByRole('button', { name: 'Create slot' }))
  await user.click(
    within(await screen.findByRole('alertdialog')).getByRole('button', {
      name: 'Create slot',
    })
  )
  await screen.findByText('Slot creation did not complete')
  const original = post.mock.calls[0][1] as { request_id: string }
  cleanup()
  mountPage({
    ...data,
    instances: [
      {
        ...data.instances[0],
        exits: [{ id: 'exit-1', name: 'IPv6 exit', available: false }],
      },
    ],
  })
  await user.selectOptions(await screen.findByLabelText('Instance'), 'cc')
  expect(await screen.findByDisplayValue('New slot')).toBeDisabled()
  await user.click(screen.getByRole('button', { name: 'Create slot' }))
  await user.click(
    within(await screen.findByRole('alertdialog')).getByRole('button', {
      name: 'Create slot',
    })
  )
  await screen.findByRole('button', { name: 'Start GPT authorization' })
  expect(post.mock.calls[1][1]).toEqual(
    expect.objectContaining({
      request_id: original.request_id,
      name: 'New slot',
      exit_id: 'exit-1',
    })
  )
  expect(sessionStorage.getItem('new-api:onboarding-slot:1:cc')).toBeNull()
})

it('blocks new slot creation when the saved draft is invalid instead of generating another request', async () => {
  sessionStorage.setItem('new-api:onboarding-slot:1:cc', '{invalid')
  mountPage()
  const post = vi.spyOn(api, 'post')
  const user = userEvent.setup()
  await user.selectOptions(await screen.findByLabelText('Instance'), 'cc')
  expect(
    await screen.findByText(
      'The slot draft cannot be saved or restored. Do not create another slot until this is resolved.'
    )
  ).toBeInTheDocument()
  expect(
    screen.queryByRole('button', { name: 'Create new slot' })
  ).not.toBeInTheDocument()
  expect(post).not.toHaveBeenCalled()
})

it('allows editing a preflight failure only after the server confirms no request was reserved', async () => {
  const user = userEvent.setup()
  const post = vi
    .spyOn(api, 'post')
    .mockRejectedValueOnce({
      response: {
        status: 409,
        data: {
          success: false,
          message: 'Onboarding request conflicts with an existing account',
        },
      },
    })
    .mockImplementation(async (_url, body) => ({
      data: {
        success: true,
        data: {
          ...(body as OnboardingRequest),
          password: undefined,
          status: 'completed',
        },
      },
    }))
  mountPage()
  await reviewAccount()
  await user.click(
    within(screen.getByRole('alertdialog')).getByRole('button', {
      name: 'Open account',
    })
  )
  const edit = await screen.findByRole('button', {
    name: 'Edit account details',
  })
  await waitFor(() => expect(edit).toBeEnabled())
  await user.click(edit)
  const name = screen.getByLabelText('Username')
  expect(name).toBeEnabled()
  await user.clear(name)
  await user.type(name, 'bob')
  await user.click(screen.getByRole('button', { name: 'Review account' }))
  await user.click(
    within(await screen.findByRole('alertdialog')).getByRole('button', {
      name: 'Open account',
    })
  )
  await screen.findByText('Account opened')
  expect((post.mock.calls[0][1] as OnboardingRequest).request_id).not.toBe(
    (post.mock.calls[1][1] as OnboardingRequest).request_id
  )
  expect(post.mock.calls[1][1]).toEqual(
    expect.objectContaining({ username: 'bob' })
  )
})
