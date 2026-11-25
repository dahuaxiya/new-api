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
import {
  getServerErrorMessage,
  requireServerSuccess,
} from '@/lib/server-error-message'

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
export type RetailPublicRates = {
  source: string
  updated_at: number
  fetched_at: number
  groups: Array<{ group: string; name: string; multiplier: string }>
}

/** Read public multipliers without binding a channel or changing the published price book. */
export async function getRetailPublicRates(
  signal: AbortSignal
): Promise<RetailPublicRates> {
  return requireServerSuccess(
    (await api.get('/api/retail/admin/pricing/public-rates', { signal })).data
  ).data
}
export type RetailPriceBook = {
  site_group_rates?: Record<number, Record<string, string>>
  group_bindings?: RetailGroupBinding[]
  enabled: boolean
  wallet_cny_per_unit: string
  receipt_ratio: string
  margin_bps: number
  fee_bps: number
  buffer_bps: number
  models: Array<{
    id: string
    kind?: 'text' | 'image' | ''
    price_mode?: 'official' | 'manual'
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
    public?: boolean
    binding_group?: string
    rate_source_channel_id?: number
    rate_source_key_id?: number
    margin_bps?: number
    user_groups: string[]
    routes: Array<{
      library_id?: number
      channel_id: number
      rate_id: string
      models: Record<string, string>
    }>
  }>
  rates: RetailRate[]
}
export type RetailPricingState = {
  sale_multipliers?: Record<string, string> | null
  version: number
  server_time: number
  book: RetailPriceBook
  account_group?: string
}

export type RetailOfficialDraft = {
  version: number
  book: RetailPriceBook
  excluded: Array<{ channel_id: number; model: string; reason: string }>
}

/** Generate an unpublished proposal from saved bindings; stale versions and unapproved supplies are rejected. */
export async function generateRetailOfficialDraft(
  version: number
): Promise<RetailOfficialDraft> {
  return requireServerSuccess(
    (
      await api.post('/api/retail/admin/pricing/official-draft', {
        expected_version: version,
      })
    ).data
  ).data
}

export type RetailGroupBinding = {
  local_group: string
  channel_id: number
  upstream_group: string
  follow_models?: boolean
}
export type RetailSalePolicy = Pick<
  RetailPriceBook,
  'receipt_ratio' | 'margin_bps' | 'fee_bps' | 'buffer_bps'
>
export type RetailBindingChannel = {
  library_id?: number
  allow_new_credential?: boolean
  model_sync_supported?: boolean
  id: number
  name: string
  site_id: number
  type: number
  base_url: string
  models: string
  has_key: boolean
  status: number
}

/** 只有专门校验完整清单的协议才可以定时同步；普通渠道的手动探测支持更多协议。
 * @param channel 已登记的供货线路；旧接口没有返回同步资格时按协议判断。
 * @return 该线路是否可开启同步。后端明确拒绝时不能只按协议放行。
 */
export function supportsRetailModelSync(
  channel?: RetailBindingChannel
): boolean {
  return (
    channel?.model_sync_supported ??
    (channel?.type === 1 || channel?.type === 14)
  )
}

export type RetailSupplyEdit = {
  enabled?: boolean
  confirm_key_group?: string
  id: number
  site_id: number
  name: string
  key: string
  models: string
}
export type RetailSiteFeed = {
  url: string
  follow: boolean
  last_error?: string
  snapshot: RetailPublicRates
}

/** 服务端重新读取同源接口，浏览器只提交站点、地址和跟随选项。 */
export async function saveRetailSiteFeed(
  site: number,
  version: number,
  url: string,
  follow?: boolean
) {
  requireServerSuccess(
    (
      await api.put('/api/retail/admin/pricing/site-rates', {
        site_id: site,
        expected_version: version,
        url,
        follow,
      })
    ).data
  )
}

export type RetailBindingState = {
  inventory_keys?: Array<{
    id: number
    owner_site_id: number
    name: string
    upstream_group: string
  }>
  rate_channels?: RetailBindingChannel[]
  rate_site_by_channel?: Record<number, number>
  rate_source_channels?: Record<string, number>
  site_feeds?: Record<number, RetailSiteFeed>
  group_margins?: Record<string, number>
  credential_groups?: Record<number, string>
  sale_policy: RetailSalePolicy
  version: number
  server_time: number
  model_sync?: Record<
    string,
    { pending?: string[]; paused?: string[]; failed?: boolean }
  >
  bindings: RetailGroupBinding[] | null
  local_groups: string[]
  channels: RetailBindingChannel[]
  routing_channels?: RetailBindingChannel[]
  sync: { enabled: boolean; last_error?: string; snapshot?: RetailPublicRates }
  pricing_ready: boolean
  pricing_enabled: boolean
}

/** Read safe root options; queries pass a cancellation signal, manual candidate refreshes may omit it. */
export async function getRetailGroupBindings(
  signal?: AbortSignal
): Promise<RetailBindingState> {
  return requireServerSuccess(
    (await api.get('/api/retail/admin/pricing/group-bindings', { signal })).data
  ).data
}

/** Save explicit mappings; the server obtains trusted multipliers itself. */
export async function saveRetailGroupBindings(
  version: number,
  bindings: RetailGroupBinding[],
  enabled: boolean,
  salePolicy: RetailSalePolicy,
  supplies?: RetailSupplyEdit[]
): Promise<void> {
  try {
    requireServerSuccess(
      (
        await api.put('/api/retail/admin/pricing/group-bindings', {
          expected_version: version,
          bindings,
          enabled,
          sale_policy: salePolicy,
          ...(supplies?.length ? { supplies } : {}),
        })
      ).data
    )
  } catch (error) {
    // Axios errors retain request bodies. Expose only the safe server message so
    // credential edits cannot enter mutation caches through error.config/cause.
    throw new Error(getServerErrorMessage(error))
  }
}

/** Refresh the committed bindings without publishing any unsaved form changes. */
export async function syncRetailGroupBindings(version: number): Promise<void> {
  requireServerSuccess(
    (
      await api.post('/api/retail/admin/pricing/group-bindings/sync', {
        expected_version: version,
      })
    ).data
  )
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
      kind?: 'text' | 'image' | ''
      official_model: string
      currency: 'CNY'
      tiers: RetailTier[]
      valid_until: number
      reference_image?: boolean
      max_output: number
    }>
  }>
}
export type RetailKeyPolicy = {
  group_current?: boolean
  studio_current?: boolean
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
export const retailKeyPolicySchema = z
  .object({
    studio_current: z.boolean().optional(),
    product_id: z.string().min(1),
    accept_floating: z.boolean().refine((value) => value),
    caps: z.record(z.string(), priceFields),
    max_request_cny: money.refine((value) => Number(value) > 0),
    daily_budget_cny: money.refine((value) => Number(value) > 0),
  })
  .refine((value) => value.studio_current || Object.keys(value.caps).length > 0)

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

/** Save only the selected call group; the server owns current-price billing and validates ownership and revision. */
export async function putRetailKeyGroup(
  id: number,
  revision: number,
  productId: string
): Promise<void> {
  requireServerSuccess(
    (
      await api.put(`/api/retail/keys/${id}/pricing`, {
        expected_revision: revision,
        product_id: productId,
      })
    ).data
  )
}

export type RetailLaunchRoute = {
  channel_id: number
  upstream_group: string
  rate_id: string
}

export type RetailLaunchInput = {
  library_id?: number
  additional_key_ids?: number[]
  apply?: boolean
  expected_version: number
  product_id?: string
  additional_routes?: RetailLaunchRoute[]
  pricing_only?: boolean
  site_id?: number
  channel_id?: number
  local_group: string
  upstream_group?: string
  margin_bps?: number
  key?: string
  models?: string[]
}
export type RetailLaunchReview = {
  applied?: boolean
  unavailable_channels?: number[]
  rows: Array<{
    model: string
    tiers?: RetailTier[]
    reason?: string
    channels?: number[]
  }>
  sale_multiplier?: string
  review: string
  version: number
}

/** 空 review 只预览，发布时使用同一版本和报价。丢弃 Axios 对请求体的引用，避免错误状态保留 Key。 */
export async function launchRetailGroup(
  input: RetailLaunchInput,
  review = ''
): Promise<RetailLaunchReview> {
  try {
    return requireServerSuccess(
      (
        await api.post('/api/retail/admin/pricing/group-launch', {
          ...input,
          review,
        })
      ).data
    ).data
  } catch (error) {
    throw new Error(getServerErrorMessage(error))
  }
}
