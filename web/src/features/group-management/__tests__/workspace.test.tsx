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
  fireEvent,
  render,
  screen,
  within,
  waitFor,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState, type ComponentProps } from 'react'
import { afterEach, expect, it, vi } from 'vitest'

import { defaultBillingSettings } from '@/features/system-settings/billing/defaults'
import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

import { GroupWorkspace } from '..'

const settings = { ...defaultBillingSettings, GroupRatio: '{"default":1}' }

/** 模拟外层路由控制的页签状态，表单和查询走生产组件。
 * @return 当前测试登录会话的分组工作区。
 */
function Workspace() {
  const [tab, setTab] =
    useState<ComponentProps<typeof GroupWorkspace>['tab']>('routes')
  return (
    <GroupWorkspace
      identity='11:undefined'
      tab={tab}
      onTabChange={setTab}
      settings={settings}
    />
  )
}

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
      <Workspace />
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

/** 路由行必须打开同一库存编号，显式保存只修改该 Key，关闭后仍保留分组的搜索结果。 */
it('edits a saved Key from its group, keeps the filter, and finds the same Key under the inventory tab', async () => {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Infinity } },
  })
  client.setQueryData(['status'], { site_mode: 'retail' })
  useAuthStore.getState().auth.setUser({ id: 11, username: 'staff', role: 100 })
  const entry = {
    id: 9,
    name: 'Kiro',
    owner_site_id: 2,
    upstream_group: 'kiro-a',
    version: 1,
  }
  const channel = {
    id: 2,
    site_id: 2,
    status: 1,
    library_id: 9,
    name: 'Example',
    base_url: 'https://example.invalid',
    has_key: true,
  }
  const product = {
    id: 'normal',
    name: 'claude-normal',
    binding_group: 'claude-normal',
    public: true,
    rate_source_channel_id: 2,
    routes: [{ channel_id: 2, library_id: 9, models: {} }],
  }
  vi.spyOn(api, 'get').mockImplementation(async (url) => {
    let data: unknown = {
      version: 1,
      book: { products: [product] },
      sale_multipliers: { normal: '0.5625' },
    }
    if (url.endsWith('/key-library')) {
      data = {
        keys: [{ ...entry }],
        channels: [channel],
        uses: { 9: [2] },
        groups: { 2: entry.upstream_group },
        consumers: { 2: ['claude-normal'] },
        site_rates: { 2: [] },
        stamp: 'one',
      }
    } else if (url.endsWith('/group-bindings')) {
      data = { channels: [channel], inventory_keys: [{ ...entry }] }
    }
    return { data: { success: true, data } }
  })
  const put = vi.spyOn(api, 'put').mockImplementation(async (_url, body) => {
    entry.upstream_group = (body as { upstream_group: string }).upstream_group
    return { data: { success: true } }
  })
  const user = userEvent.setup()
  render(
    <QueryClientProvider client={client}>
      <Workspace />
    </QueryClientProvider>
  )
  const filter = await screen.findByRole('textbox', {
    name: 'Find a group or Key',
  })
  await user.type(filter, '#9')
  await user.click(screen.getByRole('button', { name: 'Edit Key · Kiro #9' }))
  const dialog = within(await screen.findByRole('dialog'))
  const upstream = dialog.getByRole('combobox', { name: 'Upstream group' })
  fireEvent.change(upstream, { target: { value: 'kiro-b' } })
  await user.keyboard('{Escape}')
  expect(dialog.getByRole('button', { name: 'Save' })).toBeDisabled()
  await user.click(dialog.getByRole('checkbox'))
  await user.click(dialog.getByRole('button', { name: 'Save' }))
  await waitFor(() =>
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  )
  expect(put).toHaveBeenCalledWith(
    '/api/retail/admin/key-library',
    expect.objectContaining({
      id: 9,
      upstream_group: 'kiro-b',
      confirm_groups: { 2: 'kiro-a' },
      key: '',
    }),
    expect.anything()
  )
  expect(await screen.findByText('kiro-b')).toBeVisible()
  expect(filter).toHaveValue('#9')
  await user.click(screen.getByRole('tab', { name: 'Upstream Keys' }))
  await user.type(
    await screen.findByRole('textbox', { name: 'Find a group or Key' }),
    'claude-normal'
  )
  expect(
    await screen.findByRole('button', { name: 'Edit Key · Kiro #9' })
  ).toBeVisible()
  await user.click(screen.getByRole('tab', { name: 'Call group routes' }))
  expect(
    screen.getByRole('textbox', { name: 'Find a group or Key' })
  ).toHaveValue('#9')
})
