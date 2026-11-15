import i18next from 'i18next'
import { expect, it } from 'vitest'

import dayjs from '@/lib/dayjs'

import { buildUsageTrend, buildUsageBreakdown } from '../usage-trends'

/** 跨年按实际时间排序，空白桶补零，免费模型 Token 仍计入，金额先合计再格式化。 */
it('aligns multiple models and free tokens to the selected range without rounding quota', () => {
  const start = Date.UTC(2025, 11, 31, 22) / 1000
  const range = { start_timestamp: start, end_timestamp: start + 10800 }
  const result = buildUsageTrend(
    [
      { created_at: start + 7200, token_used: 7, quota: 0 },
      { created_at: start, token_used: 10, quota: 1 },
      { created_at: start, token_used: 20, quota: 2 },
      { created_at: start + 10800 },
      { created_at: start - 1, token_used: 999, quota: 999 },
      { created_at: start + 10801, token_used: 999, quota: 999 },
    ],
    range,
    'hour'
  )
  expect(result).toEqual([
    { timestamp: start, tokens: 30, quota: 3 },
    { timestamp: start + 3600, tokens: 0, quota: 0 },
    { timestamp: start + 7200, tokens: 7, quota: 0 },
    { timestamp: start + 10800, tokens: 0, quota: 0 },
  ])
})

/** 按日合并同日小时记录，末尾无用量的日期仍显示，不从最后一条记录反推范围。 */
it('groups by local calendar day and includes trailing empty dates', () => {
  const start = dayjs('2026-11-01T00:00:00').unix()
  const next = dayjs('2026-11-02T00:00:00').unix()
  const end = dayjs('2026-11-03T00:00:00').unix()
  expect(
    buildUsageTrend(
      [
        { created_at: start + 3600, quota: 1, token_used: 20 },
        { created_at: start + 7200, quota: 2, token_used: 30 },
      ],
      { start_timestamp: start, end_timestamp: end },
      'day'
    )
  ).toEqual([
    { timestamp: start, quota: 3, tokens: 50 },
    { timestamp: next, quota: 0, tokens: 0 },
    { timestamp: end, quota: 0, tokens: 0 },
  ])
})

/** 小额费用不丢精度，同名密钥不合并，缺失历史维度也计入总量。 */
it('keeps distinct key IDs and unknown history while preserving totals', () => {
  const t = i18next.getFixedT('en')
  const range = { start_timestamp: 3600, end_timestamp: 7200 }
  const rows = [
    {
      created_at: 3600,
      token_id: 1,
      token_name: 'same',
      token_used: 10,
      quota: 1,
      count: 1,
    },
    {
      created_at: 3600,
      token_id: 2,
      token_name: 'same',
      token_used: 20,
      quota: 2,
      count: 1,
    },
    { created_at: 7200, token_id: 3, token_used: 30, quota: 0, count: 1 },
    { created_at: 7200, token_used: 40, quota: 3, count: 1 },
  ]
  const result = buildUsageBreakdown(rows, range, 'hour', 'key', [], t)
  expect(result.options.map((x) => x.value).sort()).toEqual([
    '0',
    '1',
    '2',
    '3',
  ])
  expect(result.options.find((x) => x.value === '1')?.label).toBe('same · #1')
  expect(result.options.find((x) => x.value === '2')?.label).toBe('same · #2')
  expect(result.tokens).toBe(100)
  expect(result.quota).toBe(6)
  expect(buildUsageBreakdown(rows, range, 'hour', 'key', ['3'], t).tokens).toBe(
    30
  )
})

/** 其他项只影响展示数量；筛选后仍能单独检查该项，且外部标签不能进入 CSS 配置键。 */
it('groups overflow without losing free usage and allows inspecting it independently', () => {
  const t = i18next.getFixedT('en')
  const rows = Array.from({ length: 7 }, (_, i) => ({
    created_at: 3600,
    model_name: i === 6 ? '</style><script>' : `model-${i}`,
    token_used: 10,
    quota: 6 - i,
    count: 1,
  }))
  const range = { start_timestamp: 3600, end_timestamp: 7200 }
  const result = buildUsageBreakdown(rows, range, 'hour', 'model', [], t)
  expect(result.series).toHaveLength(6)
  expect(result.groupedCount).toBe(2)
  expect(result.series.reduce((sum, x) => sum + x.tokens, 0)).toBe(70)
  expect(result.series.reduce((sum, x) => sum + x.quota, 0)).toBe(21)
  expect(result.series.map((x) => x.key)).toEqual([
    'series_0',
    'series_1',
    'series_2',
    'series_3',
    'series_4',
    'series_5',
  ])
  const filtered = buildUsageBreakdown(
    rows,
    range,
    'hour',
    'model',
    ['model:</style><script>'],
    t
  )
  expect(filtered.series).toHaveLength(1)
  expect(filtered.tokens).toBe(10)
  expect(filtered.quota).toBe(0)
})
