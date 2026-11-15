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
