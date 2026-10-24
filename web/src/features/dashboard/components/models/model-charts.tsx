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
import { VChart } from '@visactor/react-vchart'
import { PieChart as PieChartIcon } from 'lucide-react'
import { useEffect, useMemo, useState, useRef } from 'react'
import { useTranslation } from 'react-i18next'

import { IconBadge } from '@/components/ui/icon-badge'
import { ToggleGroup, ToggleGroupItem } from '@/components/ui/toggle-group'
import { useThemeCustomization } from '@/context/theme-customization-provider'
import { useTheme } from '@/context/theme-provider'
import {
  DEFAULT_TIME_GRANULARITY,
  MODEL_ANALYTICS_CHART_OPTIONS,
} from '@/features/dashboard/constants'
import { processChartData } from '@/features/dashboard/lib'
import type {
  ModelAnalyticsChartTab,
  ModelAnalyticsMetric,
  QuotaDataItem,
} from '@/features/dashboard/types'
import { useThemeRadiusPx } from '@/lib/theme-radius'
import type { TimeGranularity } from '@/lib/time'
import { VCHART_OPTION } from '@/lib/vchart'

let themeManagerPromise: Promise<
  (typeof import('@visactor/vchart'))['ThemeManager']
> | null = null

type ChartSpecKey = 'spec_model_line' | 'spec_pie' | 'spec_rank_bar'

const CHART_SPEC_KEYS: Record<ModelAnalyticsChartTab, ChartSpecKey> = {
  trend: 'spec_model_line',
  proportion: 'spec_pie',
  top: 'spec_rank_bar',
}

interface ModelChartsProps {
  data: QuotaDataItem[]
  loading?: boolean
  timeGranularity?: TimeGranularity
  defaultChartTab?: ModelAnalyticsChartTab
}

/**
 * 展示调用方授权范围内的模型用量；切换指标只重算图表，不扩大查询范围。
 * @param props 已过滤的统计数据、加载状态、时间粒度及默认图表类型。
 * @return 支持 Token 与调用次数切换的模型分析卡片。
 */
export function ModelCharts(props: ModelChartsProps) {
  const { t } = useTranslation()
  const { resolvedTheme } = useTheme()
  const { customization } = useThemeCustomization()
  const chartRadius = useThemeRadiusPx(
    '--radius-md',
    `${customization.preset}:${customization.radius}`
  )
  const [activeTab, setActiveTab] = useState<ModelAnalyticsChartTab>(
    props.defaultChartTab ?? 'trend'
  )
  const [metric, setMetric] = useState<ModelAnalyticsMetric>('tokens')
  const [themeReady, setThemeReady] = useState(false)
  const themeManagerRef = useRef<
    (typeof import('@visactor/vchart'))['ThemeManager'] | null
  >(null)
  const timeGranularity = props.timeGranularity ?? DEFAULT_TIME_GRANULARITY

  useEffect(() => {
    if (props.defaultChartTab) setActiveTab(props.defaultChartTab)
  }, [props.defaultChartTab])

  useEffect(() => {
    const updateTheme = async () => {
      setThemeReady(false)

      if (!themeManagerPromise) {
        themeManagerPromise = import('@visactor/vchart').then(
          (m) => m.ThemeManager
        )
      }

      const ThemeManager = await themeManagerPromise
      themeManagerRef.current = ThemeManager
      ThemeManager.setCurrentTheme(resolvedTheme === 'dark' ? 'dark' : 'light')
      setThemeReady(true)
    }

    updateTheme()
  }, [resolvedTheme])

  const chartData = useMemo(
    () =>
      processChartData(
        props.loading ? [] : props.data,
        timeGranularity,
        t,
        chartRadius,
        metric
      ),
    [props.data, props.loading, timeGranularity, t, chartRadius, metric]
  )

  const spec = chartData[CHART_SPEC_KEYS[activeTab]]
  const specType = typeof spec?.type === 'string' ? spec.type : activeTab
  const chartKey = [
    activeTab,
    metric,
    specType,
    props.loading ? 'loading' : 'ready',
    props.data.length,
    resolvedTheme,
    customization.preset,
  ].join('-')
  const chartOptions =
    metric === 'tokens'
      ? [
          { value: 'trend', labelKey: 'Token Usage Trend' },
          { value: 'proportion', labelKey: 'Token Usage Distribution' },
          { value: 'top', labelKey: 'Token Usage Ranking' },
        ]
      : MODEL_ANALYTICS_CHART_OPTIONS

  return (
    <section
      aria-label={t('Model Call Analytics')}
      className='min-w-0 overflow-hidden rounded-lg border'
    >
      <div className='flex w-full flex-col gap-3 border-b px-3 py-2 sm:px-5 sm:py-3'>
        <div className='flex flex-wrap items-center gap-2'>
          <IconBadge tone='chart-4' size='sm'>
            <PieChartIcon />
          </IconBadge>
          <div className='text-sm font-semibold'>
            {t('Model Call Analytics')}
          </div>
          <span
            className='text-muted-foreground text-xs tabular-nums'
            aria-live='polite'
          >
            {t('Total:')}{' '}
            {metric === 'tokens'
              ? `${chartData.totalTokensDisplay} Tokens`
              : chartData.totalCountDisplay}
          </span>
        </div>
        <div className='flex min-w-0 flex-wrap items-center gap-2'>
          <ToggleGroup
            aria-label={t('Model analytics metric')}
            value={[metric]}
            onValueChange={(values) => {
              // 点击已选项不能清空指标，否则汇总和曲线会失去明确单位。
              if (values[0] === 'requests' || values[0] === 'tokens') {
                setMetric(values[0])
              }
            }}
            variant='outline'
            size='sm'
          >
            <ToggleGroupItem value='tokens'>{t('Token Usage')}</ToggleGroupItem>
            <ToggleGroupItem value='requests'>
              {t('Call Count')}
            </ToggleGroupItem>
          </ToggleGroup>
          <ToggleGroup
            aria-label={t('Model analytics chart')}
            value={[activeTab]}
            onValueChange={(values) => {
              if (
                values[0] === 'trend' ||
                values[0] === 'proportion' ||
                values[0] === 'top'
              ) {
                setActiveTab(values[0])
              }
            }}
            size='sm'
            className='bg-muted/60 max-w-full overflow-x-auto rounded-lg p-0.5'
          >
            {chartOptions.map((tab) => (
              <ToggleGroupItem
                key={tab.value}
                value={tab.value}
                className='aria-pressed:bg-background text-xs aria-pressed:shadow-sm'
              >
                {t(tab.labelKey)}
              </ToggleGroupItem>
            ))}
          </ToggleGroup>
        </div>
        {metric === 'tokens' && (
          <p className='text-muted-foreground text-xs'>
            {t('Input + output tokens recorded through this site.')}
          </p>
        )}
      </div>

      <div className='h-[300px] p-1.5 sm:h-96 sm:p-2'>
        {themeReady && spec && (
          <VChart
            key={chartKey}
            spec={{
              ...spec,
              theme: resolvedTheme === 'dark' ? 'dark' : 'light',
              background: 'transparent',
            }}
            option={VCHART_OPTION}
          />
        )}
      </div>
    </section>
  )
}
