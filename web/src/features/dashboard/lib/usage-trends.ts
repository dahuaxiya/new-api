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
import type { TFunction } from 'i18next'

import dayjs from '@/lib/dayjs'

import type { QuotaDataItem } from '../types'

export interface UsageTrendPoint {
  timestamp: number
  tokens: number
  quota: number
}

/**
 * 汇总个人用量到完整的所选时间轴，缺失时间桶补零，费用保持原始 quota 精度。
 * @param data self 接口返回的小时统计；可包含同一时间的多个模型。
 * @param range 与请求一致的 Unix 秒范围，首页只传入最近 1、7 或 30 天。
 * @param granularity 24 小时用小时桶，较长区间按浏览器本地日期汇总。
 * @return 按时间升序的双指标数据；无效或反向范围返回空数组。
 */
export function buildUsageTrend(
  data: QuotaDataItem[],
  range: { start_timestamp: number; end_timestamp: number },
  granularity: 'hour' | 'day'
): UsageTrendPoint[] {
  const { start_timestamp: start, end_timestamp: end } = range
  if (!Number.isFinite(start) || !Number.isFinite(end) || start > end) return []
  const buckets = new Map<number, UsageTrendPoint>()
  // 后端按 Unix 整小时落库；按日展示才转换本地日期，避免半小时时区错配小时桶。
  let cursor =
    granularity === 'hour'
      ? Math.floor(start / 3600) * 3600
      : dayjs(start * 1000)
          .startOf('day')
          .unix()
  while (cursor <= end) {
    buckets.set(cursor, { timestamp: cursor, tokens: 0, quota: 0 })
    // 本地日历推进，不能固定加 86400 秒，否则夏令时切换会错过午夜。
    cursor =
      granularity === 'hour'
        ? cursor + 3600
        : dayjs(cursor * 1000)
            .add(1, 'day')
            .startOf('day')
            .unix()
  }
  for (const item of data) {
    if (item.created_at < start || item.created_at > end) continue
    const timestamp =
      granularity === 'hour'
        ? Math.floor(item.created_at / 3600) * 3600
        : dayjs(item.created_at * 1000)
            .startOf('day')
            .unix()
    const bucket = buckets.get(timestamp)
    if (!bucket) continue
    // 不先换算或四舍五入金额；多个模型的小额消耗必须在汇总后统一格式化。
    bucket.tokens += Number(item.token_used) || 0
    bucket.quota += Number(item.quota) || 0
  }
  return [...buckets.values()]
}

export type UsageDimension = 'total' | 'model' | 'key' | 'group'

/**
 * 按展示维度聚合并选择绘图序列；所有原始值先相加，超出五项归入“其他”且不丢总量。
 * @param data 同一 self 查询返回的小时明细，不能混入管理员统计。
 * @param range 查询使用的 Unix 秒范围。
 * @param granularity 小时或本地日历日。
 * @param dimension 展示维度；密钥以 ID 区分，同名、改名和已删除密钥不能合并。
 * @param selected 选中的维度 ID；空数组表示该维度全部项。
 * @param t 当前界面语言，未知历史归属只标注未知，不用现存密钥猜测。
 * @return 完整选项、同序同色的系列、时间点及选中项合计。
 */
export function buildUsageBreakdown(
  data: QuotaDataItem[],
  range: { start_timestamp: number; end_timestamp: number },
  granularity: 'hour' | 'day',
  dimension: UsageDimension,
  selected: string[],
  t: TFunction
) {
  const groups = new Map<
    string,
    {
      id: string
      label: string
      data: QuotaDataItem[]
      quota: number
      tokens: number
    }
  >()
  for (const item of data) {
    if (
      item.created_at < range.start_timestamp ||
      item.created_at > range.end_timestamp
    ) {
      continue
    }
    let id = 'total'
    let label = t('Total')
    if (dimension === 'key') {
      id = String(item.token_id ?? 0)
      label = item.token_id
        ? `${item.token_name || t('Unnamed or deleted key')} · #${item.token_id}`
        : t('Key not recorded')
    } else if (dimension === 'model') {
      id = `model:${item.model_name ?? ''}`
      label = item.model_name || t('Model not recorded')
    } else if (dimension === 'group') {
      id = `group:${item.use_group ?? ''}`
      label = item.use_group || t('Group not recorded')
    }
    const group = groups.get(id) ?? { id, label, data: [], quota: 0, tokens: 0 }
    group.data.push(item)
    group.quota += Number(item.quota) || 0
    group.tokens += Number(item.token_used) || 0
    groups.set(id, group)
  }
  // 两图使用同一个排序，免费模型按 Token 继续排序，避免费用为零就从图中消失。
  const ordered = [...groups.values()].sort(
    (a, b) =>
      b.quota - a.quota || b.tokens - a.tokens || a.id.localeCompare(b.id)
  )
  const filtered = ordered.filter(
    (group) => selected.length === 0 || selected.includes(group.id)
  )
  const visible = filtered.slice(0, 5)
  const overflow = filtered.slice(5)
  if (overflow.length) {
    visible.push({
      id: '__other__',
      label: t('Other ({{count}})', { count: overflow.length }),
      data: overflow.flatMap((group) => group.data),
      quota: overflow.reduce((sum, group) => sum + group.quota, 0),
      tokens: overflow.reduce((sum, group) => sum + group.tokens, 0),
    })
  }
  const selectedData = filtered.flatMap((group) => group.data)
  const totals = buildUsageTrend(selectedData, range, granularity)
  const points: Record<string, number>[] = totals.map((point) => ({ ...point }))
  const series = visible.map((group, index) => {
    // ChartContainer 会把配置键写入 CSS。只用受控序号做键，模型名和密钥名仅作为转义文本。
    const key = `series_${index}`
    const values = buildUsageTrend(group.data, range, granularity)
    for (let i = 0; i < points.length; i++) {
      points[i][`${key}_tokens`] = values[i].tokens
      points[i][`${key}_quota`] = values[i].quota
    }
    return { key, label: group.label, tokens: group.tokens, quota: group.quota }
  })
  return {
    points,
    series,
    options: ordered.map((group) => ({ value: group.id, label: group.label })),
    tokens: totals.reduce((sum, point) => sum + point.tokens, 0),
    quota: totals.reduce((sum, point) => sum + point.quota, 0),
    requests: selectedData.reduce(
      (sum, item) => sum + (Number(item.count) || 0),
      0
    ),
    groupedCount: overflow.length,
  }
}
