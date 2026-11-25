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
import { fireEvent, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, expect, it, vi } from 'vitest'

import { defaultBillingSettings } from '@/features/system-settings/billing/defaults'
import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

import { GroupWorkspace } from '..'

afterEach(() => {
  vi.restoreAllMocks()
  useAuthStore.getState().auth.reset()
})

/** 离开倍率表后，路由页只保留上架操作；回到倍率表时草稿仍在。 */
it('keeps unsaved account ratios while switching tabs and exposes Save only in the accounts panel', async () => {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Infinity } },
  })
  client.setQueryData(['status'], { site_mode: 'retail' })
  useAuthStore.getState().auth.setUser({ id: 11, username: 'staff', role: 100 })
  vi.spyOn(api, 'get').mockImplementation(async (url) => {
    let data: unknown = { version: 1, book: { products: [] } }
    if (url.endsWith('/group-bindings')) {
      data = { inventory_keys: [], channels: [] }
    }
    if (url.endsWith('/model_pricing')) {
      data = {
        entries: [],
        options: {
          'billing_setting.billing_mode': '{}',
          'billing_setting.billing_expr': '{}',
          'billing_setting.plugin_billing_expr': '{}',
        },
      }
    }
    return { data: { success: true, data } }
  })
  const user = userEvent.setup()
  render(
    <QueryClientProvider client={client}>
      <GroupWorkspace
        settings={{ ...defaultBillingSettings, GroupRatio: '{"default":1}' }}
      />
    </QueryClientProvider>
  )
  const accounts = screen.getByRole('tab', { name: 'Account groups' })
  const routes = screen.getByRole('tab', { name: 'Call group routes' })
  expect(routes).toHaveAttribute('aria-selected', 'true')
  await user.click(accounts)
  const ratio = await screen.findByRole('spinbutton', {
    name: 'Ratio',
  })
  fireEvent.change(ratio, { target: { value: '0.8' } })
  await user.click(routes)
  expect(
    screen.queryByRole('button', { name: 'Save group settings' })
  ).not.toBeInTheDocument()
  expect(
    screen.getByRole('button', { name: 'Publish a call group' })
  ).toBeVisible()
  await user.click(accounts)
  expect(screen.getByRole('spinbutton', { name: 'Ratio' })).toHaveValue(0.8)
  expect(
    screen.getByRole('button', { name: 'Save group settings' })
  ).toBeVisible()
})
