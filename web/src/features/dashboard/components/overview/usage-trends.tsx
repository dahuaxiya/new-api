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
import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'
import { Area, AreaChart, CartesianGrid, XAxis, YAxis } from 'recharts'

import {
  ChartContainer,
  ChartTooltip,
  ChartTooltipContent,
} from '@/components/ui/chart'
import { toIntlLocale } from '@/i18n/languages'
import { formatQuotaWithCurrency } from '@/lib/currency'
import dayjs from '@/lib/dayjs'
import { formatCompactNumber, formatNumber } from '@/lib/format'

import { buildUsageTrend } from '../../lib/usage-trends'
import type { QuotaDataItem } from '../../types'

/**
 * 同屏展示 Token 与费用，两图共享时间轴、各用自己的单位，避免双轴误读。
 * @param props 已成功加载的个人汇总及同一请求范围；加载、错误和空状态由父组件处理。
 * @return 可用键盘浏览的两张趋势图及所选区间总量。
 */
export function UsageTrends(props: {
  data: QuotaDataItem[]
  range: { start_timestamp: number; end_timestamp: number }
  granularity: 'hour' | 'day'
}) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const points = useMemo(
    () => buildUsageTrend(props.data, props.range, props.granularity),
    [props.data, props.range, props.granularity]
  )
  const metrics = [
    {
      key: 'tokens' as const,
      label: t('Token usage'),
      color: { light: '#697b58', dark: '#afc58b' },
    },
    {
      key: 'quota' as const,
      label: t('Site consumption'),
      color: { light: '#b56144', dark: '#e6a183' },
    },
  ]
  return (
    <div className='grid min-w-0 divide-y px-5 pb-4 sm:grid-cols-2 sm:divide-x sm:divide-y-0 sm:px-6'>
      {metrics.map((metric) => {
        const total = points.reduce((sum, point) => sum + point[metric.key], 0)
        // 费用仍走站点币种配置；六位小数能显示最小 quota，不把小额真实消耗写成零。
        /**
         * 按当前语言及站点币种显示汇总，图上和提示框使用同一精度。
         * @param value 原始 Token 或 quota 数量，不接受已换算的金额。
         * @return 带适当单位的显示值。
         */
        const formatValue = (value: number) =>
          metric.key === 'quota'
            ? formatQuotaWithCurrency(value, { digitsSmall: 6, locale })
            : formatNumber(value, locale)
        return (
          <figure
            key={metric.key}
            aria-label={metric.label}
            className='min-w-0 py-3 first:pt-0 last:pb-0 sm:py-0 sm:first:pr-5 sm:last:pl-5'
          >
            <figcaption className='mb-4'>
              <p className='text-muted-foreground text-xs'>{metric.label}</p>
              <p className='mt-2 text-2xl font-medium tracking-tight tabular-nums'>
                {formatValue(total)}
              </p>
            </figcaption>
            {/* 现有模型图按模型拆分并按最后数据补点；概览需完整范围的总量，复用基础 Chart 组合。 */}
            <ChartContainer
              className='aspect-auto h-36 w-full'
              config={{
                [metric.key]: { label: metric.label, theme: metric.color },
              }}
            >
              <AreaChart
                data={points}
                accessibilityLayer
                margin={{ top: 6, right: 6, left: 0, bottom: 0 }}
              >
                <CartesianGrid
                  vertical={false}
                  stroke='var(--border)'
                  strokeDasharray='3 4'
                />
                {/* Recharts 4 的刻度类名与公共容器旧选择器不同，显式跟随主题以保证深色可读。 */}
                <XAxis
                  dataKey='timestamp'
                  tick={{ fill: 'var(--muted-foreground)' }}
                  tickLine={false}
                  axisLine={false}
                  minTickGap={28}
                  tickMargin={9}
                  tickFormatter={(value) =>
                    dayjs(Number(value) * 1000).format(
                      props.granularity === 'hour' ? 'HH:mm' : 'MM/DD'
                    )
                  }
                />
                <YAxis
                  width={metric.key === 'quota' ? 64 : 48}
                  tick={{ fill: 'var(--muted-foreground)' }}
                  tickLine={false}
                  axisLine={false}
                  tickCount={3}
                  domain={[0, 'auto']}
                  tickFormatter={(value) =>
                    metric.key === 'quota'
                      ? formatValue(Number(value))
                      : formatCompactNumber(Number(value), locale)
                  }
                />
                <ChartTooltip
                  isAnimationActive={false}
                  content={
                    <ChartTooltipContent
                      labelFormatter={(_, payload) =>
                        dayjs(
                          Number(payload[0]?.payload.timestamp) * 1000
                        ).format(
                          props.granularity === 'hour'
                            ? 'YYYY-MM-DD HH:mm'
                            : 'YYYY-MM-DD'
                        )
                      }
                      formatter={(value) => (
                        <span className='tabular-nums'>
                          {metric.label} · {formatValue(Number(value))}
                        </span>
                      )}
                    />
                  }
                />
                <Area
                  type='linear'
                  dataKey={metric.key}
                  stroke={`var(--color-${metric.key})`}
                  fill={`var(--color-${metric.key})`}
                  fillOpacity={0.08}
                  strokeWidth={2}
                  isAnimationActive={false}
                />
              </AreaChart>
            </ChartContainer>
          </figure>
        )
      })}
    </div>
  )
}
