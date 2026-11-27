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

import type { LogOtherData } from '../../types'
import { logCacheHitRate, retailLogPriceEntries } from '../log-billing'

describe('cache hit rate from recorded usage', () => {
  it.each([
    {
      name: 'OpenAI inclusive input',
      input: 1200,
      other: { cache_tokens: 300 },
      rate: 25,
    },
    {
      name: 'Anthropic separate cache counts',
      input: 300,
      other: {
        usage_semantic: 'anthropic',
        cache_tokens: 600,
        cache_creation_tokens: 300,
      },
      rate: 50,
    },
    {
      name: 'legacy Claude cache-only input',
      input: 0,
      other: { claude: true, cache_tokens: 300 },
      rate: 100,
    },
    {
      name: 'explicit semantic overrides upstream format',
      input: 1200,
      other: { usage_semantic: 'openai', claude: true, cache_tokens: 300 },
      rate: 25,
    },
    {
      name: 'split writes supersede the aggregate',
      input: 300,
      other: {
        usage_semantic: 'anthropic',
        cache_tokens: 600,
        cache_creation_tokens: 300,
        cache_creation_tokens_5m: 100,
        cache_creation_tokens_1h: 200,
      },
      rate: 50,
    },
    { name: 'uncached input', input: 1200, other: null, rate: 0 },
    { name: 'empty input', input: 0, other: null, rate: null },
    {
      name: 'cache exceeds inclusive input',
      input: 100,
      other: { cache_tokens: 300 },
      rate: null,
    },
    {
      name: 'negative cached usage',
      input: 1200,
      other: { cache_tokens: -1 },
      rate: null,
    },
  ])('handles $name', ({ input, other, rate }) => {
    expect(logCacheHitRate({ prompt_tokens: input }, other)).toBe(rate)
  })
})

const snapshot: LogOtherData = {
  retail_currency: 'CNY',
  retail_billing_status: 'settled',
  retail_sale_prices: [
    {
      up_to: 1000,
      prices: {
        input: '2.60000001',
        output: '15.6',
        cache_read: '0',
        cache_write: '3.25',
        cache_write_1h: '5.2',
      },
    },
    {
      up_to: 2000,
      prices: {
        input: '5.2',
        output: '23.4',
        cache_read: '0.26',
        cache_write: '6.5',
        cache_write_1h: '10.4',
      },
    },
  ],
}

describe('recorded retail unit prices', () => {
  it('preserves decimal precision and explicit zero at the inclusive tier boundary', () => {
    expect(
      retailLogPriceEntries(
        { prompt_tokens: 1000 },
        { ...snapshot, cache_tokens: 300 }
      )
    ).toEqual([
      { label: 'Input', value: '¥2.60000001/M' },
      { label: 'Output', value: '¥15.6/M' },
      { label: 'Cache Read', value: '¥0/M' },
    ])
  })

  it('selects the tier using full Anthropic context, counting cache writes once', () => {
    expect(
      retailLogPriceEntries(
        { prompt_tokens: 300 },
        {
          ...snapshot,
          usage_semantic: 'anthropic',
          cache_tokens: 600,
          cache_creation_tokens: 300,
          cache_creation_tokens_5m: 100,
          cache_creation_tokens_1h: 200,
        }
      )
    ).toEqual([
      { label: 'Input', value: '¥5.2/M' },
      { label: 'Output', value: '¥23.4/M' },
      { label: 'Cache Read', value: '¥0.26/M' },
      { label: 'Cache Write (5m)', value: '¥6.5/M' },
      { label: 'Cache Write (1h)', value: '¥10.4/M' },
    ])
  })

  it.each([
    { name: 'missing snapshot', other: { retail_currency: 'CNY' }, input: 100 },
    {
      name: 'different currency',
      other: { ...snapshot, retail_currency: 'USD' },
      input: 100,
    },
    { name: 'context above the recorded tiers', other: snapshot, input: 2001 },
    {
      name: 'malformed base price',
      other: {
        ...snapshot,
        retail_sale_prices: [
          {
            up_to: 2000,
            prices: {
              input: '-1',
              output: '15.6',
              cache_read: '0.13',
              cache_write: '3.25',
              cache_write_1h: '5.2',
            },
          },
        ],
      },
      input: 100,
    },
  ])('does not invent prices for $name', ({ other, input }) => {
    expect(retailLogPriceEntries({ prompt_tokens: input }, other)).toEqual([])
  })
})
