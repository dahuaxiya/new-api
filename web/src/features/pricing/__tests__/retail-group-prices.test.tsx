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
import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'

import type {
  RetailCatalog,
  RetailTier,
} from '@/features/channels/retail-pricing-api'

import { RetailGroupPrices } from '../components/retail-group-prices'

/** Build an input-priced tier with the other dimensions zero for focused price assertions.
 * @param upTo Inclusive context ceiling in tokens.
 * @param input Exact unit price sent by the catalog API.
 * @return A complete per-million-token tier.
 */
function tier(upTo: number, input: string): RetailTier {
  return {
    up_to: upTo,
    prices: {
      input,
      output: '0',
      cache_read: '0',
      cache_write: '0',
      cache_write_1h: '0',
    },
  }
}

const offer: RetailCatalog['products'][number]['models'][number] = {
  id: 'gpt-5',
  official_model: 'gpt-5',
  currency: 'CNY',
  tiers: [tier(128000, '0.625'), tier(272000, '1.25')],
  base_price: {
    currency: 'USD',
    official: true,
    tiers: [tier(128000, '1.25'), tier(272000, '2.5')],
  },
  sale_factor: '0.5',
  valid_until: 1900000000,
  max_output: 32768,
}
const products = [
  { id: 'regular', name: 'Regular', models: [offer] },
  {
    id: 'fast',
    name: 'Fast',
    models: [{ ...offer, tiers: [tier(128000, '1')], sale_factor: '0.8' }],
  },
]

describe('retail group pricing', () => {
  it('switching groups replaces the billed price for the same model and keeps its USD reference', async () => {
    const user = userEvent.setup()
    render(<RetailGroupPrices products={products} />)
    const groupNav = screen.getByRole('navigation', { name: 'Call groups' })
    const fast = within(groupNav).getByRole('button', { name: /Fast/ })
    await user.click(fast)
    expect(fast).toHaveAttribute('aria-current', 'true')
    const table = screen.getByRole('table')
    expect(
      within(table).getByRole('columnheader', { name: /Official price USD/ })
    ).toBeVisible()
    expect(within(table).getByRole('cell', { name: '$1.25' })).toBeVisible()
    expect(within(table).getByRole('cell', { name: '¥1' })).toBeVisible()
    expect(
      within(table).queryByRole('cell', { name: '¥0.625' })
    ).not.toBeInTheDocument()
  })

  it('shows the selected group factor once and removes it from every card', async () => {
    const user = userEvent.setup()
    render(<RetailGroupPrices products={products} />)
    expect(
      screen.getByRole('heading', {
        name: /Regular.*Effective sale factor ×0.5/,
      })
    ).toBeVisible()
    expect(
      within(screen.getByRole('article')).queryByText(/Effective sale factor/)
    ).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: /Fast/ }))
    expect(
      screen.getByRole('heading', { name: /Fast.*Effective sale factor ×0.8/ })
    ).toBeVisible()
  })

  it('keeps the full group factor range when a search hides a model', async () => {
    const user = userEvent.setup()
    render(
      <RetailGroupPrices
        products={[
          {
            ...products[0],
            models: [
              offer,
              { ...offer, id: 'another-model', sale_factor: '0.8' },
            ],
          },
        ]}
      />
    )
    await user.type(
      screen.getByRole('textbox', { name: 'Search models' }),
      'gpt-5'
    )
    expect(
      screen.getByRole('heading', { name: /Effective sale factor ×0.5–×0.8/ })
    ).toBeVisible()
    expect(screen.getAllByRole('article')).toHaveLength(1)
  })

  it('choosing the larger context band updates both columns from that band', async () => {
    const user = userEvent.setup()
    render(<RetailGroupPrices products={[products[0]]} />)
    await user.click(screen.getByRole('combobox', { name: 'gpt-5 Context' }))
    await user.click(await screen.findByRole('option', { name: /272,000/ }))
    const table = screen.getByRole('table')
    expect(within(table).getByRole('cell', { name: '$2.5' })).toBeVisible()
    expect(within(table).getByRole('cell', { name: '¥1.25' })).toBeVisible()
  })

  it('keeps the CNY price visible when an older server returns no reference tariff', () => {
    render(
      <RetailGroupPrices
        products={[
          { ...products[0], models: [{ ...offer, base_price: undefined }] },
        ]}
      />
    )
    const table = screen.getByRole('table')
    expect(within(table).getByRole('cell', { name: '¥0.625' })).toBeVisible()
    expect(
      within(table).queryByRole('columnheader', { name: /Official price/ })
    ).not.toBeInTheDocument()
  })
})
