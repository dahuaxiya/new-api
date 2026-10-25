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

/**
 * 展示真实额度读数及断档；数据点和时间轴提示共用设备时区与当前界面语言。
 * @param props 当前用户已过滤的历史读数；单个读数只画点，不补造曲线。
 * @return 复用应用主题的额度历史图。
 */
export default function QuotaHistoryChart(props: {
  points: QuotaHistoryPoint[]
}) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const { resolvedTheme, themeReady } = useChartTheme()
  const spec = useMemo(() => {
    const values = quotaChartPoints(props.points)
    const segmentSizes = new Map<string, number>()
    for (const row of values) {
      segmentSizes.set(row.series, (segmentSizes.get(row.series) ?? 0) + 1)
    }
    const segments = [...segmentSizes.keys()]
    const firstTime = props.points[0]?.observed_at ?? 0
    const lastTime = props.points.at(-1)?.observed_at ?? firstTime
    const tooltipPattern = {
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
    }
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
      line: {
        visible: true,
        style: { lineWidth: 2.5, lineDash: [], curveType: 'linear' as const },
      },
      // 密集采样点会遮住折线；仅保留无法组成线段的孤立读数，不能为了连线跨越断档。
      point: {
        style: {
          visible: (row: QuotaChartPoint) => segmentSizes.get(row.series) === 1,
          size: 5,
        },
        state: { dimension_hover: { visible: true, size: 6 } },
      },
      // 常态隐藏采样点时仍为悬停位置绘制标记，保持时间轴提示的定位能力。
      activePoint: true,
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
        // 折线默认会触发时间轴维度提示；只配置 mark 会显示毫秒时间和 7d:0。
        // series 仍用于断档分段，两种提示均以 window 生成可读名称。
        mark: tooltipPattern,
        dimension: tooltipPattern,
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
