import { z } from 'zod'

const historyPointSchema = z.object({
  observed_at: z.number().int().positive(),
  segment: z.string(),
  remaining_5h: z.number().min(0).max(100).nullable(),
  remaining_7d: z.number().min(0).max(100).nullable(),
  reset_5h: z.number().int().nullable(),
  reset_7d: z.number().int().nullable(),
})

export const quotaHistorySchema = z.object({
  points: z.array(historyPointSchema).max(8641),
  retention_days: z.number(),
  sample_seconds: z.number(),
})
export type QuotaHistoryPoint = z.infer<typeof historyPointSchema>
export type QuotaChartPoint = {
  time: number
  remaining: number
  window: '5h' | '7d'
  series: string
  reset: number | null
}

/** Split real observations at missing windows, account changes, resets and gaps.
 * Linear segments never interpolate across a reset or invent a new full quota.
 */
export function quotaChartPoints(rows: QuotaHistoryPoint[]): QuotaChartPoint[] {
  const result: QuotaChartPoint[] = []
  for (const window of ['5h', '7d'] as const) {
    let segment = 0
    let previous: QuotaHistoryPoint | undefined
    for (const row of rows) {
      const remaining = row[`remaining_${window}`]
      const reset = row[`reset_${window}`]
      if (remaining === null || (reset !== null && reset <= row.observed_at)) {
        previous = undefined
        segment++
        continue
      }
      const previousReset = previous?.[`reset_${window}`]
      if (
        previous &&
        (row.segment !== previous.segment ||
          row.observed_at - previous.observed_at > 600_000 ||
          (reset === null) !== (previousReset === null) ||
          (reset !== null &&
            previousReset != null &&
            Math.abs(reset - previousReset) > 60_000))
      ) {
        segment++
      }
      result.push({
        time: row.observed_at,
        remaining,
        window,
        series: `${window}:${segment}`,
        reset,
      })
      previous = row
    }
  }
  return result
}
