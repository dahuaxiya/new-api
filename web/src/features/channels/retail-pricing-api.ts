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
import { z } from 'zod'

import { api } from '@/lib/api'
import { requireServerSuccess } from '@/lib/server-error-message'

export type RetailPrices = {
  input: string
  output: string
  cache_read: string
  cache_write: string
  cache_write_1h: string
}
export type RetailTier = { up_to: number; prices: RetailPrices }
export type RetailRate = {
  id: string
  multiplier: string
  verified_at: number
  valid_until: number
}
export type RetailPriceBook = {
  enabled: boolean
  wallet_cny_per_unit: string
  receipt_ratio: string
  margin_bps: number
  fee_bps: number
  buffer_bps: number
  models: Array<{
    id: string
    official_model: string
    currency: 'USD' | 'CNY'
    source: string
    verified_at: number
    valid_until: number
    max_output: number
    tiers: RetailTier[]
  }>
  products: Array<{
    id: string
    name: string
    user_groups: string[]
    routes: Array<{
      channel_id: number
      rate_id: string
      models: Record<string, string>
    }>
  }>
  rates: RetailRate[]
}
export type RetailPricingState = {
  version: number
  server_time: number
  book: RetailPriceBook
}
export type RetailPricePreview = {
  product: string
  model: string
  available: boolean
  tiers?: RetailTier[]
  valid_until?: number
}
export type RetailCatalog = {
  enabled: boolean
  version: number
  products: Array<{
    id: string
    name: string
    models: Array<{
      id: string
      official_model: string
      currency: 'CNY'
      tiers: RetailTier[]
      valid_until: number
      max_output: number
    }>
  }>
}
export type RetailKeyPolicy = {
  product_id: string
  accept_floating: boolean
  caps: Record<string, RetailPrices>
  max_request_cny: string
  daily_budget_cny: string
}

const money = z.string().regex(/^(0|[1-9][0-9]{0,8})(\.[0-9]{1,8})?$/)
const priceFields = z.object({
  input: money,
  output: money,
  cache_read: money,
  cache_write: money,
  cache_write_1h: money,
})
export const retailKeyPolicySchema = z.object({
  product_id: z.string().min(1),
  accept_floating: z.boolean().refine((value) => value),
  caps: z
    .record(z.string(), priceFields)
    .refine((value) => Object.keys(value).length > 0),
  max_request_cny: money.refine((value) => Number(value) > 0),
  daily_budget_cny: money.refine((value) => Number(value) > 0),
})

const jsonArray = z.string().refine((value) => {
  try {
    return Array.isArray(JSON.parse(value))
  } catch {
    return false
  }
})
export const retailPricingDraftSchema = z.object({
  book: z.custom<RetailPriceBook>(),
  models: jsonArray,
  products: jsonArray,
  rates: jsonArray,
})

/** Load the current version without placing procurement data in shared/public caches. */
export async function getRetailPricing(
  signal: AbortSignal
): Promise<RetailPricingState> {
  const response = await api.get('/api/retail/admin/pricing', { signal })
  return requireServerSuccess(response.data).data
}

/** Preview the same server-side arithmetic that will enforce the published book. */
export async function previewRetailPricing(
  book: RetailPriceBook
): Promise<RetailPricePreview[]> {
  const response = await api.post('/api/retail/admin/pricing/preview', { book })
  return requireServerSuccess(response.data).data
}

/** Publish with compare-and-swap; a stale editor cannot overwrite another operator's price update. */
export async function publishRetailPricing(
  version: number,
  book: RetailPriceBook
): Promise<void> {
  requireServerSuccess(
    (
      await api.put('/api/retail/admin/pricing', {
        expected_version: version,
        book,
      })
    ).data
  )
}

/** Import only group multipliers; this endpoint cannot rewrite official tariffs or wallet conversion. */
export async function importRetailRates(
  version: number,
  rates: RetailRate[]
): Promise<void> {
  requireServerSuccess(
    (
      await api.post('/api/retail/admin/pricing/rates', {
        expected_version: version,
        rates,
      })
    ).data
  )
}

/** Read only the signed-in user's authorized products and public selling prices. */
export async function getRetailCatalog(
  signal: AbortSignal
): Promise<RetailCatalog> {
  return requireServerSuccess(
    (await api.get('/api/retail/catalog', { signal })).data
  ).data
}

/** Ownership is revalidated server-side even when a key has no price policy yet. */
export async function getRetailKeyPolicy(
  id: number,
  signal: AbortSignal
): Promise<{ revision: number; policy: RetailKeyPolicy }> {
  return requireServerSuccess(
    (await api.get(`/api/retail/keys/${id}/pricing`, { signal })).data
  ).data
}

/** Save explicit floating-price consent and absolute ceilings; expected revision protects concurrent edits. */
export async function putRetailKeyPolicy(
  id: number,
  revision: number,
  policy: RetailKeyPolicy
): Promise<void> {
  requireServerSuccess(
    (
      await api.put(`/api/retail/keys/${id}/pricing`, {
        expected_revision: revision,
        policy,
      })
    ).data
  )
}
