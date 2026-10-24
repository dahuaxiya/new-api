import { VChart } from '@visactor/react-vchart'
import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'

import { toIntlLocale } from '@/i18n/languages'
import { formatNumber } from '@/lib/format'
import { useChartTheme } from '@/lib/use-chart-theme'
import { VCHART_OPTION } from '@/lib/vchart'

import {
  quotaChartPoints,
  type QuotaChartPoint,
  type QuotaHistoryPoint,
} from './history'

/** Reuse the app's VChart theme and real time axis; one observation stays a dot. */
export default function QuotaHistoryChart(props: {
  points: QuotaHistoryPoint[]
}) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const { resolvedTheme, themeReady } = useChartTheme()
  const spec = useMemo(() => {
    const values = quotaChartPoints(props.points)
    const segments = [...new Set(values.map((row) => row.series))]
    const firstTime = props.points[0]?.observed_at ?? 0
    const lastTime = props.points.at(-1)?.observed_at ?? firstTime
    return {
      type: 'line' as const,
      data: [{ id: 'quota-history', values }],
      xField: 'time',
      yField: 'remaining',
      seriesField: 'series',
      // Series are split for gaps; colors still consistently identify the two windows.
      color: segments.map((segment) =>
        segment.startsWith('5h:') ? '#2563eb' : '#0d9488'
      ),
      animation: false,
      line: { style: { lineWidth: 2, curveType: 'linear' as const } },
      point: { visible: true, style: { size: values.length > 500 ? 2 : 5 } },
      legends: { visible: false },
      axes: [
        {
          orient: 'bottom' as const,
          type: 'linear' as const,
          zero: false,
          // A single fresh reading needs a small real-time domain, not a zero-based epoch axis.
          min: firstTime === lastTime ? firstTime - 300_000 : firstTime,
          max: firstTime === lastTime ? lastTime + 300_000 : lastTime,
          tickCount: 5,
          label: {
            formatMethod: (value: number | string) =>
              new Date(Number(value)).toLocaleString(locale, {
                month: 'numeric',
                day: 'numeric',
                hour: '2-digit',
                minute: '2-digit',
              }),
          },
        },
        {
          orient: 'left' as const,
          min: 0,
          max: 100,
          label: { formatMethod: (value: number | string) => `${value}%` },
          grid: { visible: true },
        },
      ],
      tooltip: {
        mark: {
          title: {
            value: (row: QuotaChartPoint) =>
              new Date(row.time).toLocaleString(locale, {
                timeZoneName: 'short',
              }),
          },
          content: [
            {
              key: (row: QuotaChartPoint) =>
                row.window === '5h'
                  ? t('GPT 5-hour window')
                  : t('GPT 7-day window'),
              value: (row: QuotaChartPoint) =>
                t('GPT remaining {{percent}}%', {
                  percent: formatNumber(row.remaining, locale),
                }),
            },
          ],
        },
      },
    }
  }, [props.points, locale, t])

  return (
    <div
      className='h-64 w-full min-w-0'
      role='img'
      aria-label={t('GPT remaining quota history')}
    >
      {themeReady && (
        <VChart
          key={resolvedTheme}
          spec={{
            ...spec,
            theme: resolvedTheme === 'dark' ? 'dark' : 'light',
            background: 'transparent',
          }}
          option={VCHART_OPTION}
        />
      )}
    </div>
  )
}
