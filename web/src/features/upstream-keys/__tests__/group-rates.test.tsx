import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { cleanup, render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'

import { KeyLibrary } from '../library'

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

/** 两个站点的上游分组同名。该 Key 同时给固定售价商品和按线路计价的旧商品供货。 */
function openLibrary() {
  const data = {
    keys: [{ id: 9, name: 'Kiro', owner_site_id: 2, upstream_group: 'normal' }],
    channels: [
      {
        id: 2,
        site_id: 2,
        name: 'Alpha',
        base_url: 'https://example.test',
        library_id: 9,
      },
    ],
    groups: { 2: 'normal' },
    uses: { 9: [2] },
    consumers: { 2: ['claude-normal', 'legacy', 'unpriced'] },
    site_rates: {
      1: [{ group: 'normal', multiplier: '9' }],
      2: [
        { group: 'normal', multiplier: '0.45' },
        { group: 'fast', multiplier: '0.7' },
      ],
    },
    stamp: 'v1',
  }
  vi.spyOn(api, 'get').mockImplementation(async (path) => ({
    data: {
      success: true,
      data: path.endsWith('/key-library')
        ? data
        : {
            version: 1,
            sale_multipliers: { official: '0.5625', other: '7' },
            book: {
              products: [
                {
                  id: 'official',
                  binding_group: 'claude-normal',
                  rate_source_channel_id: 4,
                  routes: [{ channel_id: 2, library_id: 9 }],
                },
                {
                  id: 'other',
                  binding_group: 'claude-normal',
                  rate_source_channel_id: 7,
                  routes: [],
                },
                { id: 'old', name: 'legacy', routes: [{ channel_id: 2 }] },
              ],
            },
          },
    },
  }))
  render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <KeyLibrary identity='root' />
    </QueryClientProvider>
  )
}

/** 采购只认 Key 所属站点，售价只认引用当前线路的正式商品；旧价和缺价不猜数。 */
it('shows the owning site’s purchase rate and each referenced product’s published sale rate', async () => {
  openLibrary()
  await screen.findByRole('button', { name: 'Edit Key · Kiro #9' })
  const row = within(screen.getByRole('listitem'))
  expect(await row.findByText('0.5625×')).toBeVisible()
  expect(row.getByText('0.45×')).toBeVisible()
  expect(row.getByText('Route-based rate')).toBeVisible()
  expect(row.getByText('Rate unavailable')).toBeVisible()
  expect(row.queryByText(/^(7|9)×$/)).toBeNull()
})

/** 自由录入和选择都只改变组名；倍率在选择前后均可见，找不到报价时不能沿用上一项。 */
it('keeps the purchase rate visible while editing and lists alternatives with their own rates', async () => {
  openLibrary()
  const user = userEvent.setup()
  await user.click(
    await screen.findByRole('button', { name: 'Edit Key · Kiro #9' })
  )
  const dlg = within(await screen.findByRole('dialog'))
  const field = dlg.getByRole('combobox', { name: 'Upstream group' })
  expect(field).toHaveAccessibleDescription('Purchase multiplier 0.45×')
  await user.click(field)
  await user.click(await screen.findByRole('option', { name: /fast.*0\.7×/ }))
  expect(field).toHaveValue('fast')
  expect(field).toHaveAccessibleDescription('Purchase multiplier 0.7×')
  await user.clear(field)
  await user.type(field, 'custom-group')
  expect(field).toHaveValue('custom-group')
  expect(field).toHaveAccessibleDescription(
    'Purchase multiplier Rate unavailable'
  )
})
