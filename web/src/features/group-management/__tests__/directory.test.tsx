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
import { fireEvent, render, screen, within } from '@testing-library/react'
import { expect, it, vi } from 'vitest'

import type {
  RetailBindingState,
  RetailPriceBook,
} from '@/features/channels/retail-pricing-api'

import { PublishedGroupDirectory } from '../published-group-directory'

const policy = {
  margin_bps: 2000,
  receipt_ratio: '1',
  fee_bps: 0,
  buffer_bps: 0,
}
const book: RetailPriceBook = {
  ...policy,
  enabled: true,
  wallet_cny_per_unit: '1',
  models: [],
  rates: [],
  products: [
    {
      id: 'one',
      name: 'claude-normal',
      binding_group: 'claude-normal',
      public: true,
      user_groups: [],
      routes: [
        { channel_id: 2, library_id: 9, rate_id: 'regular', models: {} },
        { channel_id: 5, rate_id: 'backup', models: {} },
      ],
    },
    {
      id: 'empty',
      name: 'private-preview',
      public: false,
      user_groups: [],
      routes: [],
    },
  ],
  group_bindings: [
    {
      channel_id: 5,
      local_group: 'claude-normal',
      upstream_group: 'legacy-cursor',
    },
  ],
}
const site = {
  site_id: 1,
  base_url: 'https://example.invalid',
  name: 'site',
  type: 14,
  models: '',
  has_key: true,
}
const supply: RetailBindingState = {
  sale_policy: policy,
  version: 1,
  server_time: 1,
  bindings: null,
  local_groups: [],
  sync: { enabled: false },
  pricing_ready: true,
  pricing_enabled: true,
  channels: [],
  inventory_keys: [
    { id: 9, name: 'Kiro', owner_site_id: 1, upstream_group: 'kiro-standard' },
  ],
  rate_channels: [
    { ...site, id: 2, status: 1 },
    { ...site, id: 5, status: 2 },
  ],
}
const props = {
  book,
  supply,
  priceError: false,
  supplyError: false,
  retryPrices: vi.fn(),
  retrySupply: vi.fn(),
  onEdit: vi.fn(),
  publishButton: null,
}

/** 命中备用组时保留整组，每条线路的 Key 与配置状态保持配对。 */
it('keeps both routes and their individual switches when a search matches only the backup', () => {
  render(<PublishedGroupDirectory {...props} />)
  fireEvent.change(
    screen.getByRole('textbox', { name: 'Find a group or Key' }),
    { target: { value: 'CuRsOr' } }
  )
  const primary = screen.getByRole('row', { name: /claude-normal.*Kiro/ })
  expect(within(primary).getByText('#9')).toBeVisible()
  expect(within(primary).getByText('Enabled')).toBeVisible()
  const backup = screen.getByRole('row', { name: /Channel #5/ })
  expect(within(backup).getByText('Disabled')).toBeVisible()
  expect(screen.queryByText('private-preview')).not.toBeInTheDocument()
  expect(
    screen.getAllByRole('button', { name: /Edit call group routing/ })
  ).toHaveLength(1)
})

/** 补充资料读不到时保留价表里的库存编号和旧绑定，不把未知的线路标为已停用或启用。 */
it('keeps the published bindings and offers a retry when supply metadata cannot be read', () => {
  const retrySupply = vi.fn()
  render(
    <PublishedGroupDirectory
      {...props}
      supply={undefined}
      supplyError
      retrySupply={retrySupply}
    />
  )
  expect(screen.getByText('#9')).toBeVisible()
  expect(screen.getAllByText('Unknown')).toHaveLength(2)
  expect(screen.getByText('legacy-cursor')).toBeVisible()
  expect(screen.getByText('No routes configured')).toBeVisible()
  expect(
    screen.queryByRole('button', {
      name: /Edit call group routing.*private-preview/,
    })
  ).not.toBeInTheDocument()
  fireEvent.click(
    within(screen.getByRole('alert')).getByRole('button', { name: 'Retry' })
  )
  expect(retrySupply).toHaveBeenCalledOnce()
})

/** 搜索无结果可以清空后恢复列表，并继续用库存编号定位目标。 */
it('restores the directory after an empty search and finds a Key by its inventory number', () => {
  render(<PublishedGroupDirectory {...props} />)
  const search = screen.getByRole('textbox', { name: 'Find a group or Key' })
  fireEvent.change(search, { target: { value: 'not-a-group' } })
  expect(screen.getByText('No results found')).toBeVisible()
  fireEvent.change(search, { target: { value: '#9' } })
  expect(screen.getByRole('rowheader', { name: 'claude-normal' })).toBeVisible()
  fireEvent.click(screen.getByRole('button', { name: 'Clear search' }))
  expect(
    screen.getByRole('rowheader', { name: 'private-preview' })
  ).toBeVisible()
})
