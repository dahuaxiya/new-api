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

import type { HostingSelf } from '../api'
import { HostingStore } from '../store'
import { HostingStoreAdmin } from '../store-admin'

let client: QueryClient
const plans = [
  { id: 'monthly', days: 30, price_cents: 4000, enabled: true },
  { id: 'quarterly', days: 90, price_cents: 9900, enabled: true },
  { id: 'yearly', days: 365, price_cents: 35000, enabled: true },
]
const seat: HostingSelf = {
  grant: {
    seat_id: 'seat-one',
    user_id: 2,
    starts_at: 1,
    ends_at: 4102444800,
    retention_days: 7,
    enabled: true,
    note: '',
  },
  binding: {
    state: 'active',
    platform: 'codex',
    paused: false,
    account_label: 'my-account',
    plan: 'PRO',
  },
}
beforeEach(() => {
  client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  useAuthStore.getState().auth.setUser({ id: 2, username: 'buyer', role: 1 })
})
afterEach(() => {
  cleanup()
  client.clear()
  useAuthStore.getState().auth.reset()
  vi.restoreAllMocks()
})

/** 在 HTTP 边界提供目录和订单，不替代被测支付逻辑或公共交互组件。 */
function mountStore(ready: boolean, current = seat) {
  vi.spyOn(api, 'get').mockImplementation(async (url) => ({
    data: {
      success: true,
      data: url.endsWith('/store')
        ? { plans, payment_ready: ready, methods: ['wxpay', 'alipay'] }
        : { items: [], total: 0 },
    },
  }))
  render(
    <QueryClientProvider client={client}>
      <HostingStore identity='buyer-session' seat={current} platform='codex' />
    </QueryClientProvider>
  )
}

it('shows exact CNY plans and prevents payment before merchant setup', async () => {
  const user = userEvent.setup()
  mountStore(false)
  expect(await screen.findByText('¥40')).toBeInTheDocument()
  expect(screen.getByText('¥99')).toBeInTheDocument()
  expect(screen.getByText('¥350')).toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'Buy a hosting seat' }))
  const dialog = await screen.findByRole('dialog')
  expect(
    within(dialog).getByRole('button', { name: /Continue to payment/ })
  ).toBeDisabled()
  expect(
    within(dialog).getByText('Hosting payments are not available yet.')
  ).toBeInTheDocument()
  await user.keyboard('{Escape}')
  await waitFor(() =>
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  )
})

it('retries the same purchase id and sends no client-controlled price or duration', async () => {
  const user = userEvent.setup()
  mountStore(true)
  vi.spyOn(api, 'post').mockResolvedValue({
    data: {
      success: false,
      message: 'No hosting slots are available for this account type.',
    },
  })
  await screen.findByText('¥40')
  await user.click(screen.getByRole('button', { name: 'Buy a hosting seat' }))
  const dialog = await screen.findByRole('dialog')
  await user.selectOptions(
    within(dialog).getByLabelText('Account platform'),
    'claude'
  )
  const pay = within(dialog).getByRole('button', {
    name: /Continue to payment/,
  })
  await user.click(pay)
  await waitFor(() => expect(pay).toBeEnabled())
  await user.click(pay)
  await waitFor(() => expect(api.post).toHaveBeenCalledTimes(2))
  const bodies = vi
    .mocked(api.post)
    .mock.calls.map((c) => c[1] as Record<string, unknown>)
  expect(bodies[0]).toEqual({
    request_id: expect.any(String),
    plan_id: 'monthly',
    seat_id: '',
    platform: 'claude',
    method: 'wxpay',
  })
  expect(bodies[1]).toEqual(bodies[0])
})

it('renews only the selected seat and locks its existing account platform', async () => {
  const user = userEvent.setup()
  mountStore(true)
  const submit = vi
    .spyOn(HTMLFormElement.prototype, 'submit')
    .mockImplementation(() => {})
  vi.spyOn(api, 'post').mockResolvedValue({
    data: {
      success: true,
      data: {
        url: 'https://pay.example.test/submit.php',
        params: { pid: 'synthetic', out_trade_no: 'synthetic-order' },
        order: { id: 'synthetic-order' },
      },
    },
  })
  await screen.findByText('¥40')
  await user.click(screen.getByRole('button', { name: 'Renew this seat' }))
  const dialog = await screen.findByRole('dialog')
  expect(within(dialog).getByLabelText('Account platform')).toBeDisabled()
  await user.click(
    within(dialog).getByRole('button', { name: /Continue to payment/ })
  )
  await waitFor(() => expect(submit).toHaveBeenCalledOnce())
  expect(vi.mocked(api.post).mock.calls[0][1]).toMatchObject({
    seat_id: 'seat-one',
    platform: 'codex',
  })
  await waitFor(() =>
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  )
})

it('does not offer renewal of a permanent seat', async () => {
  mountStore(true, {
    ...seat,
    grant: {
      ...(seat.grant as NonNullable<HostingSelf['grant']>),
      ends_at: -1,
    },
  })
  await screen.findByText('¥40')
  expect(
    screen.queryByRole('button', { name: 'Renew this seat' })
  ).not.toBeInTheDocument()
})

it('shows payment review as an unfulfilled order and offers no second payment', async () => {
  mountStore(true)
  await screen.findByText('No hosting orders yet')
  vi.mocked(api.get).mockResolvedValue({
    data: {
      success: true,
      data: {
        items: [
          {
            id: 'review-order',
            seat_id: 'seat-one',
            user_id: 2,
            kind: 'new',
            platform: 'claude',
            method: 'alipay',
            days: 30,
            price_cents: 4000,
            status: 'paid_review',
            created_at: 100,
            expires_at: 1000,
            paid_at: 100,
            ends_at: 0,
          },
        ],
        total: 1,
      },
    },
  })
  await userEvent.click(screen.getByRole('button', { name: 'Refresh orders' }))
  expect(await screen.findByText('Paid, awaiting review')).toBeInTheDocument()
  expect(
    screen.getByText(
      'Payment was received, but the seat was not changed. Contact the administrator with this order number.'
    )
  ).toBeInTheDocument()
  expect(
    screen.queryByRole('button', { name: 'Continue to payment' })
  ).not.toBeInTheDocument()
})

it('requires security verification before an administrator can save plan changes', async () => {
  const user = userEvent.setup()
  useAuthStore.getState().auth.setUser({ id: 1, username: 'root', role: 100 })
  const post = vi.spyOn(api, 'post')
  vi.spyOn(api, 'get').mockImplementation(async (url) => ({
    data: {
      success: true,
      data:
        url === '/api/verify/methods'
          ? {
              scope: 'gpt.hosting.manage',
              methods: [{ method: 'password', available: true }],
              oauth_providers: [],
              password_encryption_enabled: false,
            }
          : { plans, payment_ready: false, items: [], total: 0 },
    },
  }))
  render(
    <QueryClientProvider client={client}>
      <HostingStoreAdmin open onOpenChange={() => {}} />
    </QueryClientProvider>
  )
  const inputs = await screen.findAllByLabelText('Price (CNY)')
  await user.clear(inputs[0])
  await user.type(inputs[0], '0')
  expect(screen.getAllByRole('button', { name: 'Save' })[0]).toBeDisabled()
  await user.clear(inputs[0])
  await user.type(inputs[0], '40')
  await user.click(screen.getAllByRole('button', { name: 'Save' })[0])
  expect(
    await screen.findByLabelText('Password', { selector: 'input' })
  ).toBeInTheDocument()
  expect(post).not.toHaveBeenCalled()
})
