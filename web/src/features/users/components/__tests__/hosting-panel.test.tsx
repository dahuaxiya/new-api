import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
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
import { afterEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

import type { User } from '../../types'
import { HostingUserPanel } from '../hosting-user-panel'
import { UsersProvider } from '../users-provider'

const user: User = {
  id: 2,
  username: 'old-user',
  display_name: 'Old user',
  role: 1,
  status: 1,
  group: 'old-group',
  quota: 99,
  used_quota: 0,
  request_count: 0,
}
const clients: QueryClient[] = []
/** 仅模拟 HTTP 边界，保留真实表单、确认框和二次验证交互。 */
async function renderPanel(
  status = 'undated',
  role = 100,
  endsAt?: number,
  multi = false
) {
  useAuthStore.getState().auth.setUser({ id: 1, username: 'operator', role })
  const now = Math.floor(Date.now() / 1000)
  const summary = {
    user_id: 2,
    status,
    source: status === 'undated' ? 'legacy' : 'hosting',
    slot_id: 'old-slot',
    instance_name: 'CloudCone',
    grant:
      status === 'undated'
        ? null
        : {
            user_id: 2,
            starts_at: now - 100,
            ends_at: endsAt ?? now + 86400,
            retention_days: 7,
            enabled: true,
            note: '',
          },
    binding: null,
    quota: null,
  }
  const seats = multi
    ? [
        {
          grant: { ...summary.grant, seat_id: 'first' },
          binding: {
            state: 'active',
            account_label: 'GPT owner',
            plan: 'pro',
            paused: false,
          },
          binding_id: 'b1',
          valid: true,
        },
        {
          grant: { ...summary.grant, seat_id: 'second', ends_at: -1 },
          binding: {
            state: 'active',
            account_label: 'Claude owner',
            platform: 'claude',
            plan: 'max',
            paused: false,
          },
          binding_id: 'b2',
          valid: true,
        },
      ]
    : undefined
  const responseSummary = {
    ...summary,
    seats,
    seat_count: seats?.length,
    bound_seats: seats?.length,
  }
  vi.spyOn(api, 'get').mockImplementation(async (url) => {
    if (url === '/api/verify/methods') {
      return {
        data: {
          success: true,
          data: {
            scope: 'admin.user.update',
            methods: [{ method: '2fa', available: true }],
            oauth_providers: [],
            password_encryption_enabled: false,
          },
        },
      }
    }
    return { data: { success: true, data: [responseSummary] } }
  })
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  clients.push(client)
  render(
    <QueryClientProvider client={client}>
      <UsersProvider>
        <HostingUserPanel user={user} />
      </UsersProvider>
    </QueryClientProvider>
  )
  await screen.findByText(
    status === 'undated' ? 'Hosting term not set' : 'Hosting active'
  )
  return { client, summary }
}
afterEach(() => {
  cleanup()
  clients.splice(0).forEach((c) => c.clear())
  vi.restoreAllMocks()
  useAuthStore.getState().auth.reset()
})

it('legacy users show no preset end date and cancelling the preview never takes over the account', async () => {
  const post = vi.spyOn(api, 'post').mockResolvedValue({
    data: {
      success: true,
      data: {
        account_label: 'a***@example.com',
        instance_id: 'cc',
        slot_id: 'old-slot',
      },
    },
  })
  await renderPanel()
  expect(screen.getByLabelText('Ends at')).toHaveValue('')
  expect(
    screen.getByText(
      'Administrators can manage this term without GPT sign-in. Official identity is verified when the user signs in again.'
    )
  ).toBeInTheDocument()
  await userEvent.click(
    screen.getByRole('button', { name: 'Extend by 30 days' })
  )
  await userEvent.click(
    screen.getByRole('button', { name: 'Review and save hosting term' })
  )
  const confirm = await screen.findByRole('alertdialog')
  expect(
    within(confirm).getByText(
      'GPT sign-in is not required. Existing direct keys and credentials are retained; all retained keys share this hosting term. New API keeps using its existing upstream key.'
    )
  ).toBeInTheDocument()
  expect(post).toHaveBeenCalledWith(
    '/api/gpt-hosting/admin/takeover/check',
    { user_id: 2 },
    expect.anything()
  )
  await userEvent.click(within(confirm).getByRole('button', { name: 'Cancel' }))
  expect(
    post.mock.calls.every(
      (call) => call[0] === '/api/gpt-hosting/admin/takeover/check'
    )
  ).toBe(true)
})

it('failed ownership preflight leaves the form intact and never starts takeover', async () => {
  const post = vi.spyOn(api, 'post').mockResolvedValue({
    data: { success: false, message: 'Cannot verify account' },
  })
  await renderPanel()
  await userEvent.click(
    screen.getByRole('button', { name: 'Extend by 30 days' })
  )
  await userEvent.click(
    screen.getByRole('button', { name: 'Review and save hosting term' })
  )
  await waitFor(() =>
    expect(
      screen.getByRole('button', { name: 'Review and save hosting term' })
    ).toBeEnabled()
  )
  expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
  expect(post).toHaveBeenCalledTimes(1)
  expect(screen.getByLabelText('Ends at')).not.toHaveValue('')
})

it('background refresh cannot overwrite an edited hosting end date', async () => {
  const { client, summary } = await renderPanel('active')
  fireEvent.change(screen.getByLabelText('Ends at'), {
    target: { value: '2030-12-01T10:30' },
  })
  await act(async () => {
    client.setQueryData(['hosting-user', 1, undefined, 2], {
      ...summary,
      grant: { ...summary.grant, note: 'background' },
    })
  })
  expect(screen.getByLabelText('Ends at')).toHaveValue('2030-12-01T10:30')
})

it('ordinary administrators can read hosting details but cannot edit terms', async () => {
  await renderPanel('undated', 10)
  expect(screen.getByText('CloudCone / old-slot')).toBeInTheDocument()
  expect(
    screen.queryByRole('button', { name: 'Review and save hosting term' })
  ).not.toBeInTheDocument()
  expect(screen.queryByLabelText('Ends at')).not.toBeInTheDocument()
})

it('permanent hosting allows an empty end date and saves -1 only after administrator verification', async () => {
  const post = vi.spyOn(api, 'post').mockImplementation(async (url) => ({
    data: {
      success: true,
      data:
        url === '/api/verify'
          ? {
              proof_token: 'fixture-proof',
              method: '2fa',
              scope: 'admin.user.update',
              expires_at: 4102444800,
            }
          : {},
    },
  }))
  await renderPanel('active')
  fireEvent.change(screen.getByLabelText('Ends at'), { target: { value: '' } })
  await userEvent.click(
    screen.getByRole('switch', { name: 'Permanent hosting' })
  )
  expect(screen.getByLabelText('Ends at')).toBeDisabled()
  expect(
    screen.getByRole('button', { name: 'Extend by 30 days' })
  ).toBeDisabled()
  expect(
    screen.getByLabelText('Credential retention days after expiry')
  ).toBeDisabled()
  await userEvent.click(
    screen.getByRole('button', { name: 'Review and save hosting term' })
  )
  const confirmation = await screen.findByRole('alertdialog')
  expect(
    within(confirmation).getByText(/Permanent hosting/)
  ).toBeInTheDocument()
  await userEvent.click(
    within(confirmation).getByRole('button', { name: 'Continue' })
  )
  expect(post).not.toHaveBeenCalled()
  await userEvent.type(
    await screen.findByLabelText('Authenticator code or backup code'),
    '123456'
  )
  await userEvent.click(screen.getByRole('button', { name: 'Verify' }))
  await waitFor(() =>
    expect(post).toHaveBeenCalledWith(
      '/api/gpt-hosting/admin/grants',
      expect.objectContaining({ user_id: 2, ends_at: -1, retention_days: 7 }),
      expect.objectContaining({
        headers: { 'X-Security-Proof': 'fixture-proof' },
      })
    )
  )
})

it('saved permanent hosting reloads without an epoch date and requires a date when changed back', async () => {
  const post = vi.spyOn(api, 'post')
  const { client, summary } = await renderPanel('active', 100, -1)
  await waitFor(() =>
    expect(
      screen.getByRole('switch', { name: 'Permanent hosting' })
    ).toBeChecked()
  )
  expect(screen.getByLabelText('Ends at')).toHaveValue('')
  await userEvent.click(
    screen.getByRole('switch', { name: 'Permanent hosting' })
  )
  await userEvent.click(
    screen.getByRole('button', { name: 'Review and save hosting term' })
  )
  expect(
    await screen.findByText('The end date must be later than the start date.')
  ).toBeInTheDocument()
  expect(post).not.toHaveBeenCalled()
  await userEvent.click(
    screen.getByRole('button', { name: 'Extend by 30 days' })
  )
  const end = (screen.getByLabelText('Ends at') as HTMLInputElement).value
  expect(Date.parse(end)).toBeGreaterThan(Date.now())
  await act(async () => {
    client.setQueryData(['hosting-user', 1, undefined, 2], {
      ...summary,
      grant: { ...summary.grant, note: 'background' },
    })
  })
  expect(
    screen.getByRole('switch', { name: 'Permanent hosting' })
  ).not.toBeChecked()
  expect(screen.getByLabelText('Ends at')).toHaveValue(end)
  await userEvent.click(
    screen.getByRole('button', { name: 'Review and save hosting term' })
  )
  expect(await screen.findByRole('alertdialog')).toHaveTextContent(
    end.replace('T', ' ')
  )
})

it('shows independent seat terms and opens a blank form when adding another seat', async () => {
  await renderPanel('active', 100, undefined, true)
  const picker = screen.getByLabelText(/Hosting seats/)
  await userEvent.selectOptions(picker, 'second')
  await waitFor(() =>
    expect(
      screen.getByRole('switch', { name: 'Permanent hosting' })
    ).toBeChecked()
  )
  expect(
    screen.getByText(/Claude owner/, { selector: 'p' })
  ).toBeInTheDocument()
  await userEvent.click(
    screen.getByRole('button', { name: 'Add hosting seat' })
  )
  expect(
    screen.getByRole('switch', { name: 'Permanent hosting' })
  ).not.toBeChecked()
  expect(screen.getByLabelText('Ends at')).toHaveValue('')
  expect(picker).toHaveValue('__new')
})
