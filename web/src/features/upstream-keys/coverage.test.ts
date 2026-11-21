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
import { describe, expect, it } from 'vitest'

import { credentialUses, credentialCoverage, type Inventory } from './coverage'

const product: NonNullable<Inventory['products']>[number] = {
  id: 'fast',
  name: 'Fast',
  binding_group: 'fast',
  user_groups: [],
  rate_source_channel_id: 4,
  routes: [
    { channel_id: 1, rate_id: 'r1', models: { a: 'up-a', b: 'up-b', c: 'c' } },
    { channel_id: 2, rate_id: 'r2', models: { a: 'mapped-a', b: 'mapped-b' } },
    { channel_id: 3, rate_id: 'r3', models: { c: 'c' } },
  ],
}
const rows = [1, 2, 3, 4].map((id) => ({
  id,
  site_id: 1,
  type: 1,
  base_url: '',
  name: 'line',
  models: 'a,b,c',
  has_key: true,
  status: id === 3 ? 2 : 1,
}))
describe('upstream credential impact', () => {
  it('counts only enabled routes with a matching outbound model', () => {
    expect(
      credentialCoverage(product, 1, rows, {
        a: { 2: 'mapped-a' },
        b: { 2: 'changed' },
        c: { 3: 'c' },
      })
    ).toEqual({ candidates: ['line #2 (1/3)'], missing: ['b', 'c'] })
  })
  it('shows rate-only and unpublished references independently of supply routes', () => {
    const binding = {
      local_group: 'draft',
      upstream_group: 'up',
      channel_id: 4,
    }
    const data: Inventory = {
      products: [product],
      bindings: [binding],
      channels: rows,
      groups: {},
      targets: {},
      stamp: 'v',
      version: 3,
    }
    expect(credentialUses(data, 4)).toEqual({
      products: [product],
      bindings: [binding],
    })
    expect(credentialCoverage(product, 4, rows, {})).toEqual({
      candidates: [],
      missing: [],
    })
  })
})
