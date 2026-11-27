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
import type { RetailPrices } from '@/features/channels/retail-pricing-api'

import type { UsageLog } from '../data/schema'
import type { LogOtherData } from '../types'

/** Identify recorded retail billing independently of the current site mode.
 * @param other Historical billing metadata, including pre-snapshot records.
 * @return Whether legacy group multipliers and USD ratio prices are inapplicable.
 */
export function isRetailLog(other: LogOtherData | null): boolean {
  return (
    other != null &&
    (other.retail_billing_status != null ||
      other.retail_currency != null ||
      other.retail_sale_prices != null)
  )
}

/** Recover full context for cache rate and historical price-tier selection.
 * @param log Recorded input usage. Anthropic input excludes cache reads and writes.
 * @param other Semantic marker takes precedence over the legacy upstream-format flag.
 * @return Full input context, or null for invalid or missing usage.
 */
export function logInputContext(
  log: Pick<UsageLog, 'prompt_tokens'>,
  other: LogOtherData | null
): number | null {
  const input = log.prompt_tokens
  const read = other?.cache_tokens ?? 0
  const write5m = other?.cache_creation_tokens_5m ?? 0
  const write1h = other?.cache_creation_tokens_1h ?? 0
  const write =
    write5m + write1h > 0
      ? write5m + write1h
      : (other?.cache_creation_tokens ?? 0)
  if (
    ![input, read, write, write5m, write1h].every(
      (value) => Number.isFinite(value) && value >= 0
    )
  ) {
    return null
  }
  // The provider-format flag alone is insufficient once an explicit usage semantic is recorded.
  const anthropic =
    other?.usage_semantic === 'anthropic' ||
    (!other?.usage_semantic && other?.claude === true)
  const context = anthropic ? input + read + write : input
  return Number.isFinite(context) && context >= read ? context : null
}

/** Cache reads divided by full input context, with no invented rate for empty input.
 * @param log Recorded token counts.
 * @param other Provider cache usage and semantic markers.
 * @return Percentage in [0, 100], or null when it cannot be calculated reliably.
 */
export function logCacheHitRate(
  log: Pick<UsageLog, 'prompt_tokens'>,
  other: LogOtherData | null
): number | null {
  const context = logInputContext(log, other)
  return context != null && context > 0
    ? ((other?.cache_tokens ?? 0) / context) * 100
    : null
}

/** Select the same frozen context tier as retail settlement and preserve decimal prices.
 * @param log Recorded input usage.
 * @param other CNY sale-price snapshot. Current catalog prices must never replace it.
 * @return Applicable recorded unit prices, or an empty list when the snapshot is unavailable.
 */
export function retailLogPriceEntries(
  log: Pick<UsageLog, 'prompt_tokens'>,
  other: LogOtherData | null
): Array<{ label: string; value: string }> {
  const context = logInputContext(log, other)
  if (
    context == null ||
    other?.retail_currency !== 'CNY' ||
    !Array.isArray(other.retail_sale_prices)
  ) {
    return []
  }
  const tier = other.retail_sale_prices.find(
    (entry) => Number.isFinite(entry?.up_to) && context <= entry.up_to
  )
  const validPrice = (price: unknown): price is string =>
    typeof price === 'string' && /^\d+(?:\.\d+)?$/.test(price)
  // Both base tariffs must exist before presenting a usable historical price pair.
  if (!validPrice(tier?.prices?.input) || !validPrice(tier?.prices?.output)) {
    return []
  }
  const hasSplitWrites =
    (other.cache_creation_tokens_5m ?? 0) +
      (other.cache_creation_tokens_1h ?? 0) >
    0
  const write5m = hasSplitWrites
    ? (other.cache_creation_tokens_5m ?? 0)
    : (other.cache_creation_tokens ?? 0)
  const fields: Array<[keyof RetailPrices, string, boolean]> = [
    ['input', 'Input', true],
    ['output', 'Output', true],
    ['cache_read', 'Cache Read', (other.cache_tokens ?? 0) > 0],
    ['cache_write', 'Cache Write (5m)', write5m > 0],
    [
      'cache_write_1h',
      'Cache Write (1h)',
      (other.cache_creation_tokens_1h ?? 0) > 0,
    ],
  ]
  // Explicit zero is a valid price. Missing, negative and malformed prices remain unavailable.
  return fields
    .filter(([, , visible]) => visible)
    .map(([field, label]) => {
      const price = tier?.prices?.[field]
      return { label, value: validPrice(price) ? `¥${price}/M` : '' }
    })
    .filter((entry) => entry.value !== '')
}
