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
import type {
  RetailBindingChannel,
  RetailGroupBinding,
  RetailPriceBook,
  RetailPricingState,
} from '@/features/channels/retail-pricing-api'

export type Inventory = {
  version: number
  targets: Record<string, Record<number, string>>
  stamp: string
  channels: RetailBindingChannel[] | null
  groups: Record<number, string>
  products: RetailPriceBook['products'] | null
  bindings: RetailGroupBinding[] | null
}

/** 按服务端消费者名称归类正式商品，保留每个商品按其 ID 查得的独立售价。
 * @param state 同一已发布版本的价表与倍率。
 * @return 分组到候选商品的索引，同名商品需继续按该 Key 的线路匹配。
 */
export function indexConsumerPrices(state: RetailPricingState) {
  const grouped = new Map<
    string,
    { product: RetailPriceBook['products'][number]; multiplier?: string }[]
  >()
  for (const product of state.book?.products ?? []) {
    const name = product.binding_group || product.name
    const rows = grouped.get(name) ?? []
    rows.push({ product, multiplier: state.sale_multipliers?.[product.id] })
    grouped.set(name, rows)
  }
  return grouped
}

/** 从正式商品计算引用；尚未上架的绑定也单独列出，避免掩盖停用影响。 */
export function credentialUses(data: Inventory, id: number) {
  const bindings = (data.bindings ?? []).filter((b) => b.channel_id === id)
  const products = (data.products ?? []).filter(
    (p) =>
      p.rate_source_channel_id === id ||
      (p.routes ?? []).some((r) => r.channel_id === id)
  )
  return {
    products,
    bindings: bindings.filter(
      (b) => !products.some((p) => p.binding_group === b.local_group)
    ),
  }
}

/** 按本线路提供的模型计算其他已启用线路的覆盖；这是配置检查，上游健康和请求预算只在调用时确定。 */
export function credentialCoverage(
  p: RetailPriceBook['products'][number],
  id: number,
  channels: RetailBindingChannel[],
  targets: Inventory['targets']
) {
  const required = Object.keys(
    (p.routes ?? []).find((r) => r.channel_id === id)?.models ?? {}
  )
  const covered = new Set<string>()
  const candidates: string[] = []
  for (const route of p.routes ?? []) {
    const c = channels.find((c) => c.id === route.channel_id)
    if (!c || c.id === id || c.status !== 1 || !c.has_key) continue
    const matches = required.filter(
      (m) =>
        Boolean(route.models?.[m]) && targets[m]?.[c.id] === route.models[m]
    )
    if (matches.length) {
      candidates.push(
        c.name +
          ' #' +
          c.id +
          ' (' +
          matches.length +
          '/' +
          required.length +
          ')'
      )
      matches.forEach((m) => covered.add(m))
    }
  }
  return { candidates, missing: required.filter((m) => !covered.has(m)) }
}
