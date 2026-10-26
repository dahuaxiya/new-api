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

import { Wallet } from '@/features/wallet'
import { api } from '@/lib/api'
import { Route as WalletRoute } from '@/routes/_authenticated/wallet'
import { useAuthStore } from '@/stores/auth-store'

import type { HostingSelf } from '../api'
import { HostingBilling } from '../billing'

let client: QueryClient
const gptSeat = 'legacy-2'
const claudeSeat = '00000000-0000-4000-8000-000000000002'
const seats: HostingSelf[] = [
  {
    grant: {
      seat_id: gptSeat,
      user_id: 2,
      starts_at: 1,
      ends_at: -1,
      enabled: true,
      retention_days: 1,
      note: '',
    },
    binding: {
      state: 'active',
      platform: 'codex',
      paused: false,
      account_label: 'GPT account',
      plan: 'PRO',
    },
  },
  {
    grant: {
      seat_id: claudeSeat,
      user_id: 2,
      starts_at: 1,
      ends_at: 4102444800,
      enabled: true,
      retention_days: 1,
      note: '',
    },
    binding: {
      state: 'active',
      platform: 'claude',
      paused: false,
      account_label: 'Claude account',
      plan: 'MAX',
    },
  },
]

beforeEach(() => {
  client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  useAuthStore.getState().auth.setUser({ id: 2, username: 'buyer', role: 1 })
  localStorage.clear()
  vi.spyOn(api, 'get').mockImplementation(async (url, config) => {
    let data: unknown = {}
    if (url === '/api/gpt-hosting/self') {
      const selected = config?.params?.seat_id || gptSeat
      const seat = seats.find((item) => item.grant?.seat_id === selected)
      if (!seat) throw new Error('The selected seat is unavailable')
      data = { ...seat, seats }
    } else if (url === '/api/gpt-hosting/store') {
      data = {
        plans: [{ id: 'monthly', days: 30, price_cents: 4000, enabled: true }],
        payment_ready: true,
        methods: ['wxpay', 'alipay'],
      }
    } else if (url === '/api/gpt-hosting/orders') {
      data = { items: [], total: 0 }
    } else if (url === '/api/user/self') {
      data = { id: 2, quota: 0, used_quota: 0, request_count: 0 }
    } else if (url === '/api/user/aff') {
      data = 'fixture'
    } else if (url === '/api/user/topup/info') {
      data = {
        payment_compliance_confirmed: false,
        payment_methods: [],
        min_topup: 1,
      }
    }
    return { data: { success: true, data } }
  })
  vi.spyOn(api, 'post').mockResolvedValue({
    data: { success: false, message: 'Payment is unavailable' },
  })
})
afterEach(() => {
  cleanup()
  client.clear()
  useAuthStore.getState().auth.reset()
  localStorage.clear()
  vi.restoreAllMocks()
})

/** 旧名额使用 legacy 编号；账单路由必须原样保留它与新名额 UUID，避免续费链接失效。 */
it('preserves legacy and new seat identifiers in wallet search validation', () => {
  const schema = WalletRoute.options.validateSearch
  if (!schema || !('parse' in schema)) {
    throw new Error('Wallet search parser is missing')
  }
  for (const seatId of [gptSeat, claudeSeat]) {
    expect(schema.parse({ tab: 'hosting', seat_id: seatId })).toEqual({
      tab: 'hosting',
      seat_id: seatId,
    })
  }
  // 非法目标应明确失败，不可丢弃参数后静默续费默认名额。
  for (const seatId of ['', 'x'.repeat(37), 2]) {
    expect(() => schema.parse({ tab: 'hosting', seat_id: seatId })).toThrow()
  }
})

/** 从账号页进入账单后必须选中同一个名额，并用它的账号类型发起续费。 */
it('renews the requested seat in billing and keeps permanent seats non-renewable', async () => {
  const user = userEvent.setup()
  render(
    <QueryClientProvider client={client}>
      <HostingBilling seatId={claudeSeat} />
    </QueryClientProvider>
  )
  expect(await screen.findByLabelText('Hosting seat')).toHaveValue(claudeSeat)
  await screen.findByText('¥40')
  await user.click(screen.getByRole('button', { name: 'Renew this seat' }))
  const dialog = await screen.findByRole('dialog')
  expect(within(dialog).getByLabelText('Account platform')).toHaveValue(
    'claude'
  )
  expect(within(dialog).getByLabelText('Account platform')).toBeDisabled()
  await user.click(
    within(dialog).getByRole('button', { name: /Continue to payment/ })
  )
  await waitFor(() =>
    expect(api.post).toHaveBeenCalledWith(
      '/api/gpt-hosting/payment',
      expect.objectContaining({ seat_id: claudeSeat, platform: 'claude' }),
      expect.anything()
    )
  )
  await user.keyboard('{Escape}')
  await waitFor(() =>
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  )
  await user.selectOptions(screen.getByLabelText('Hosting seat'), gptSeat)
  await screen.findByText(/Permanent hosting/)
  expect(
    screen.queryByRole('button', { name: 'Renew this seat' })
  ).not.toBeInTheDocument()
  expect(
    screen.getByRole('button', { name: 'Buy a hosting seat' })
  ).toBeInTheDocument()
})

/** 无法读取指定名额时必须停止展示购买表单，不可把首个名额作为续费对象。 */
it('does not fall back to another seat when the requested seat is unavailable', async () => {
  render(
    <QueryClientProvider client={client}>
      <HostingBilling seatId='00000000-0000-4000-8000-000000000099' />
    </QueryClientProvider>
  )
  await screen.findByRole('button', { name: 'Retry' })
  expect(screen.queryByLabelText('Hosting seat')).not.toBeInTheDocument()
  expect(
    screen.queryByRole('button', { name: 'Renew this seat' })
  ).not.toBeInTheDocument()
  expect(api.post).not.toHaveBeenCalled()
})

/** 使用完整账单页验证托管分区可由链接打开，原充值兑换分区仍能正常切回。 */
it('shows hosting plans and orders in the wallet tab while preserving redemption', async () => {
  const user = userEvent.setup()
  render(
    <QueryClientProvider client={client}>
      <Wallet initialTab='hosting' hostingSeatId={claudeSeat} />
    </QueryClientProvider>
  )
  expect(screen.getByRole('tab', { name: 'Hosting plans' })).toHaveAttribute(
    'aria-selected',
    'true'
  )
  expect(await screen.findByLabelText('Hosting seat')).toHaveValue(claudeSeat)
  expect(await screen.findByText('¥40')).toBeVisible()
  expect(
    await screen.findByRole('region', { name: 'Hosting orders' })
  ).toBeVisible()
  await user.click(screen.getByRole('tab', { name: 'Balance and redemption' }))
  expect(
    screen.getByRole('tab', { name: 'Balance and redemption' })
  ).toHaveAttribute('aria-selected', 'true')
  expect(
    screen.queryByRole('region', { name: 'Hosting orders' })
  ).not.toBeInTheDocument()
  expect(screen.getByRole('button', { name: /Redeem/ })).toBeVisible()
})
