import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'

import {
  quotaChartPoints,
  quotaHistorySchema,
  type QuotaHistoryPoint,
} from '../history'
import { QuotaHistory } from '../QuotaHistory'

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

const now = Date.UTC(2026, 9, 25)
const sample: QuotaHistoryPoint = {
  observed_at: now,
  segment: 'account-a',
  remaining_5h: 80,
  remaining_7d: 65,
  reset_5h: now + 3600_000,
  reset_7d: now + 86_400_000,
}

describe('quota history segments', () => {
  it('preserves a real zero and omits missing windows instead of inventing zeroes', () => {
    const points = quotaChartPoints([
      { ...sample, remaining_5h: null, remaining_7d: 0 },
    ])
    expect(points).toEqual([
      {
        time: now,
        remaining: 0,
        window: '7d',
        series: '7d:0',
        reset: sample.reset_7d,
      },
    ])
  })

  it('joins consecutive readings only within the same account and window', () => {
    const points = quotaChartPoints([
      sample,
      { ...sample, observed_at: now + 300_000, remaining_5h: 72 },
    ])
    expect(points.slice(0, 2).map((row) => row.series)).toEqual([
      '5h:0',
      '5h:0',
    ])
    expect(points.slice(0, 2).map((row) => row.remaining)).toEqual([80, 72])
  })

  it.each(['gap', 'reset', 'account', 'missing'] as const)(
    'breaks the line across %s instead of showing continuous consumption',
    (reason) => {
      const next = { ...sample, observed_at: now + 300_000, remaining_5h: 70 }
      if (reason === 'gap') next.observed_at = now + 900_000
      if (reason === 'reset') next.reset_5h = now + 7200_000
      if (reason === 'account') next.segment = 'account-b'
      const rows = [sample]
      if (reason === 'missing') {
        rows.push({ ...sample, observed_at: now + 100_000, remaining_5h: null })
      }
      rows.push(next)
      const points = quotaChartPoints(rows).filter((row) => row.window === '5h')
      expect(points).toHaveLength(2)
      expect(points[0].series).not.toBe(points[1].series)
    }
  )

  it('excludes already-expired windows at the observation time, not at viewing time', () => {
    const points = quotaChartPoints([{ ...sample, reset_5h: now }])
    expect(points.every((row) => row.window === '7d')).toBe(true)
  })

  it('rejects malformed percentages and does not turn an upstream error into empty history', () => {
    expect(
      quotaHistorySchema.safeParse({
        points: [{ ...sample, remaining_5h: 101 }],
        retention_days: 30,
        sample_seconds: 300,
      }).success
    ).toBe(false)
  })
})

/** Mount the real history panel with only the HTTP boundary controlled by the test. */
function mountHistory() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, retryDelay: 0 } },
  })
  const view = render(
    <QueryClientProvider client={client}>
      <QuotaHistory userId={2} sessionId='session-a' />
    </QueryClientProvider>
  )
  return { ...view, client }
}

it('loads empty history and changes range without sending a user or slot identifier', async () => {
  const get = vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      data: { points: [], retention_days: 30, sample_seconds: 300 },
    },
  })
  const { client } = mountHistory()
  await screen.findByText('No quota history in this range')
  fireEvent.change(
    screen.getByRole('combobox', { name: 'Quota history range' }),
    { target: { value: '720' } }
  )
  await waitFor(() =>
    expect(get).toHaveBeenCalledWith(
      '/api/gpt-quota/self/history',
      expect.objectContaining({ params: { hours: 720 } })
    )
  )
  expect(
    await screen.findByText(
      'History starts when recording is enabled. Earlier readings cannot be recovered.'
    )
  ).toBeTruthy()
  expect(screen.queryByRole('img')).toBeNull()
  client.clear()
})

it('shows a failed request as an error rather than an empty or zero-percent chart', async () => {
  vi.spyOn(api, 'get').mockRejectedValue(new Error('offline'))
  const { client } = mountHistory()
  await screen.findByText('Quota history could not be loaded')
  expect(screen.queryByText('No quota history in this range')).toBeNull()
  expect(screen.queryByRole('img')).toBeNull()
  client.clear()
})
