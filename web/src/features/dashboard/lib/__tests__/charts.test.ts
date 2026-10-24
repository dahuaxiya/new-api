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
import { describe, expect, test } from 'vitest'

import type { QuotaDataItem } from '@/features/dashboard/types'
import type { TimeGranularity } from '@/lib/time'

import { processChartData, processUserChartData } from '../charts'

const cases: Array<{
  granularity: TimeGranularity
  start: string
  interval: number
  labels: string[]
}> = [
  {
    granularity: 'hour',
    start: '2025-12-31T20:00:00',
    interval: 3600,
    labels: [
      '12-31 20:00',
      '12-31 21:00',
      '12-31 22:00',
      '12-31 23:00',
      '01-01 00:00',
      '01-01 01:00',
      '01-01 02:00',
      '01-01 03:00',
    ],
  },
  {
    granularity: 'day',
    start: '2025-12-27T12:00:00',
    interval: 86400,
    labels: [
      '12-27',
      '12-28',
      '12-29',
      '12-30',
      '12-31',
      '01-01',
      '01-02',
      '01-03',
    ],
  },
  {
    granularity: 'week',
    start: '2025-11-29T12:00:00',
    interval: 604800,
    labels: [
      '11-29 - 12-05',
      '12-06 - 12-12',
      '12-13 - 12-19',
      '12-20 - 12-26',
      '12-27 - 01-02',
      '01-03 - 01-09',
      '01-10 - 01-16',
      '01-17 - 01-23',
    ],
  },
]

function unorderedUsage(start: string, interval: number): QuotaDataItem[] {
  const first = new Date(start).getTime() / 1000
  const rows = Array.from({ length: 8 }, (_, i) => ({
    created_at: first + i * interval,
    model_name: 'model-a',
    username: 'alice',
    quota: (i + 1) * 500000,
    count: i + 1,
  }))
  // Same hour, or a second hourly record within the same day/week label.
  const duplicate = {
    ...rows[0],
    created_at: first + (interval === 3600 ? 0 : 3600),
    quota: 5000000,
    count: 10,
  }
  return [
    rows[7],
    rows[2],
    duplicate,
    rows[5],
    rows[0],
    rows[6],
    rows[1],
    rows[4],
    rows[3],
  ]
}

describe.each(cases)('dashboard $granularity chart chronology', (scenario) => {
  test('orders model buckets across New Year while aggregating repeated labels', () => {
    const data = unorderedUsage(scenario.start, scenario.interval)
    const original = structuredClone(data)
    const result = processChartData(data, scenario.granularity)

    for (const key of ['spec_line', 'spec_area', 'spec_model_line'] as const) {
      const values: Array<{ Time: string; rawQuota: number; Count: number }> =
        result[key].data[0].values
      expect(values.map((row) => row.Time)).toEqual(scenario.labels)
      expect(
        values.map((row) =>
          key === 'spec_model_line' ? row.Count : row.rawQuota / 500000
        )
      ).toEqual([11, 2, 3, 4, 5, 6, 7, 8])
    }
    expect(data).toEqual(original)
  })

  test('orders user buckets across New Year while aggregating repeated labels', () => {
    const data = unorderedUsage(scenario.start, scenario.interval)
    const original = structuredClone(data)
    const result = processUserChartData(data, scenario.granularity)
    const values: Array<{ Time: string; rawQuota: number }> =
      result.spec_user_trend.data[0].values

    expect(values.map((row) => row.Time)).toEqual(scenario.labels)
    expect(values.map((row) => row.rawQuota / 500000)).toEqual([
      11, 2, 3, 4, 5, 6, 7, 8,
    ])
    expect(data).toEqual(original)
  })

  test('keeps the existing seven padded model buckets in chronological order', () => {
    const data = [unorderedUsage(scenario.start, scenario.interval)[0]]
    const result = processChartData(data, scenario.granularity)

    for (const key of ['spec_line', 'spec_area', 'spec_model_line'] as const) {
      const values: Array<{ Time: string; rawQuota: number; Count: number }> =
        result[key].data[0].values
      expect(values.map((row) => row.Time)).toEqual(scenario.labels.slice(1))
      expect(
        values.map((row) =>
          key === 'spec_model_line' ? row.Count : row.rawQuota / 500000
        )
      ).toEqual([0, 0, 0, 0, 0, 0, 8])
    }
  })
})

/** 免费模型也按真实 token_used 汇总；次数与 Token 排序不同，不能借用次数数据。 */
test('aggregates token totals, trends and rankings independently of requests and price', () => {
  const time = new Date('2026-10-25T12:00:00').getTime() / 1000
  const data = [
    {
      created_at: time,
      model_name: 'many-calls',
      count: 20,
      token_used: 7,
      quota: 0,
    },
    {
      created_at: time,
      model_name: 'large-context',
      count: 1,
      token_used: 100000001,
      quota: 0,
    },
    {
      created_at: time + 3600,
      model_name: 'large-context',
      count: 2,
      token_used: 12,
      quota: 0,
    },
  ]
  const original = structuredClone(data)
  const tokens = processChartData(data, 'day', undefined, undefined, 'tokens')
  const requests = processChartData(data)
  expect(tokens.totalTokensDisplay).toBe('100,000,020')
  expect(tokens.totalCountDisplay).toBe('23')
  expect(tokens.totalQuotaDisplay).toBe(requests.totalQuotaDisplay)
  expect(tokens.spec_rank_bar.data[0].values).toEqual([
    { Model: 'large-context', Count: 100000013 },
    { Model: 'many-calls', Count: 7 },
  ])
  expect(requests.spec_rank_bar.data[0].values[0]).toEqual({
    Model: 'many-calls',
    Count: 20,
  })
  expect(tokens.spec_pie.data[0].values[0]).toEqual({
    type: 'large-context',
    value: 100000013,
  })
  expect(
    tokens.spec_model_line.data[0].values.filter(
      (row: { Count: number }) => row.Count > 0
    )
  ).toEqual([
    { Time: '10-25', Model: 'large-context', Count: 100000013 },
    { Time: '10-25', Model: 'many-calls', Count: 7 },
  ])
  expect(
    tokens.spec_rank_bar.tooltip.mark.content[0].value({ Count: 100000013 })
  ).toBe('100,000,013 Tokens')
  expect(data).toEqual(original)
})

/** 21 个模型触发 Top 20 边界，剩余 Token 必须进入 Other，且不按请求量选模型。 */
test('keeps all tokens when the model trend and ranking overflow into Other', () => {
  const data = Array.from({ length: 21 }, (_, index) => ({
    created_at: 1792886400,
    model_name: `model-${index + 1}`,
    count: 21 - index,
    token_used: index + 1,
  }))
  const result = processChartData(data, 'day', undefined, undefined, 'tokens')
  const ranking = result.spec_rank_bar.data[0].values as Array<{
    Model: string
    Count: number
  }>
  expect(ranking[0]).toEqual({ Model: 'model-21', Count: 21 })
  expect(ranking.at(-1)).toEqual({ Model: 'Other', Count: 1 })
  expect(ranking.reduce((sum, row) => sum + row.Count, 0)).toBe(231)
  expect(
    result.spec_model_line.data[0].values.filter(
      (row: { Model: string; Count: number }) =>
        row.Model === 'Other' && row.Count > 0
    )
  ).toHaveLength(1)
})

/** 空范围和没有 Token 的记录不能拿请求次数或账单金额填充 Token 数。 */
test('returns zero tokens for empty data and missing token counters', () => {
  expect(
    processChartData([], 'day', undefined, undefined, 'tokens')
      .totalTokensDisplay
  ).toBe('0')
  const result = processChartData(
    [{ created_at: 1792886400, count: 5, quota: 100 }],
    'day',
    undefined,
    undefined,
    'tokens'
  )
  expect(result.totalTokensDisplay).toBe('0')
  expect(result.spec_rank_bar.data[0].values).toEqual([
    { Model: 'Unknown', Count: 0 },
  ])
})
