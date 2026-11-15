import { expect, it } from 'vitest'

import dayjs from '@/lib/dayjs'

import { buildUsageTrend } from '../usage-trends'

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
