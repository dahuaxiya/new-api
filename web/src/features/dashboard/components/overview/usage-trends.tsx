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
import { useId, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Area, AreaChart, CartesianGrid, XAxis, YAxis } from 'recharts'

import { MultiSelect } from '@/components/multi-select'
import {
  ChartContainer,
  ChartTooltip,
  ChartTooltipContent,
} from '@/components/ui/chart'
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { toIntlLocale } from '@/i18n/languages'
import { formatQuotaWithCurrency } from '@/lib/currency'
import dayjs from '@/lib/dayjs'
import { formatCompactNumber, formatNumber } from '@/lib/format'

import {
  buildUsageBreakdown,
  type UsageDimension,
} from '../../lib/usage-trends'
import type { QuotaDataItem } from '../../types'

const SERIES_COLORS = [
  { light: '#697b58', dark: '#afc58b' },
  { light: '#b56144', dark: '#e6a183' },
  { light: '#536f96', dark: '#93b5e3' },
  { light: '#8a6793', dark: '#c8a6d4' },
  { light: '#a28236', dark: '#d8be77' },
  { light: '#747b7d', dark: '#b4babc' },
]

/**
 * 个人 Token 与费用按同一维度拆分，两图共用系列顺序和过滤条件。
 * @param props 已成功加载的 self 小时明细；父组件保留维度，账号切换时必须重建。
 * @return 支持维度切换、搜索筛选和键盘提示的个人趋势图。
 */
export function UsageTrends(props: {
  data: QuotaDataItem[]
  range: { start_timestamp: number; end_timestamp: number }
  granularity: 'hour' | 'day'
  dimension: UsageDimension
  onDimensionChange: (value: UsageDimension) => void
}) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const filterId = useId()
  const [selected, setSelected] = useState<string[]>([])
  const result = useMemo(
    () =>
      buildUsageBreakdown(
        props.data,
        props.range,
        props.granularity,
        props.dimension,
        selected,
        t
      ),
    [props.data, props.range, props.granularity, props.dimension, selected, t]
  )
  const dimensions = [
    { value: 'total', label: t('All usage') },
    { value: 'model', label: t('By model') },
    { value: 'key', label: t('By API key') },
    { value: 'group', label: t('By group') },
  ]
  const metrics = [
    { key: 'tokens' as const, label: t('Token usage') },
    { key: 'quota' as const, label: t('Site consumption') },
  ]
  return (
    <div className='min-w-0 px-5 pb-4 sm:px-6'>
      <div className='mb-5 flex flex-wrap items-center gap-2'>
        <Select
          value={props.dimension}
          items={dimensions}
          onValueChange={(value) => {
            if (!value) return
            // 不同维度的 ID 可能相同，不能把模型筛选值沿用为密钥或分组筛选。
            setSelected([])
            props.onDimensionChange(value as UsageDimension)
          }}
        >
          <SelectTrigger className='w-32' aria-label={t('Usage dimension')}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectGroup>
              {dimensions.map((item) => (
                <SelectItem key={item.value} value={item.value}>
                  {item.label}
                </SelectItem>
              ))}
            </SelectGroup>
          </SelectContent>
        </Select>
        {props.dimension !== 'total' && (
          <div className='min-w-0 flex-1 basis-44'>
            <label htmlFor={filterId} className='sr-only'>
              {t('Filter usage items')}
            </label>
            <MultiSelect
              id={filterId}
              options={result.options}
              selected={selected}
              onChange={setSelected}
              placeholder={t('All items · search to filter')}
              maxVisibleChips={1}
            />
          </div>
        )}
        <p className='text-muted-foreground w-full text-xs tabular-nums'>
          {t('Requests in range')} · {formatNumber(result.requests, locale)}
        </p>
      </div>
      <div className='grid min-w-0 divide-y sm:grid-cols-2 sm:divide-x sm:divide-y-0'>
        {metrics.map((metric) => {
          /**
           * 先聚合原始 quota，再按站点币种显示，小额费用保留六位精度。
           * @param value 原始 Token 或 quota 数量，不能传已换算金额。
           * @return 当前语言及币种的显示值。
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
                  {formatValue(result[metric.key])}
                </p>
              </figcaption>
              <ChartContainer
                className='aspect-auto h-36 w-full'
                config={Object.fromEntries(
                  result.series.map((series, index) => [
                    `${series.key}_${metric.key}`,
                    { label: series.label, theme: SERIES_COLORS[index] },
                  ])
                )}
              >
                <AreaChart
                  data={result.points}
                  accessibilityLayer
                  margin={{ top: 6, right: 6, left: 0, bottom: 0 }}
                >
                  <CartesianGrid
                    vertical={false}
                    stroke='var(--border)'
                    strokeDasharray='3 4'
                  />
                  {/* 显式绑定主题，避免 Recharts 默认灰色在深色背景失去对比度。 */}
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
                        className='max-w-72'
                        labelFormatter={(_, payload) =>
                          dayjs(
                            Number(payload[0]?.payload.timestamp) * 1000
                          ).format(
                            props.granularity === 'hour'
                              ? 'YYYY-MM-DD HH:mm'
                              : 'YYYY-MM-DD'
                          )
                        }
                        formatter={(value, name, item) => (
                          <div className='flex min-w-0 items-center gap-2'>
                            <span
                              className='size-2 shrink-0 rounded-full'
                              style={{ backgroundColor: item.color }}
                            />
                            <span className='min-w-0 break-all'>{name}</span>
                            <span className='ml-auto shrink-0 tabular-nums'>
                              {formatValue(Number(value))}
                            </span>
                          </div>
                        )}
                      />
                    }
                  />
                  {result.series.map((series) => (
                    <Area
                      key={series.key}
                      name={series.label}
                      type='linear'
                      dataKey={`${series.key}_${metric.key}`}
                      stackId={metric.key}
                      stroke={`var(--color-${series.key}_${metric.key})`}
                      fill={`var(--color-${series.key}_${metric.key})`}
                      fillOpacity={0.1}
                      strokeWidth={2}
                      isAnimationActive={false}
                    />
                  ))}
                </AreaChart>
              </ChartContainer>
            </figure>
          )
        })}
      </div>
      <ul
        aria-label={t('Usage legend')}
        className='mt-4 flex flex-wrap gap-x-4 gap-y-2 text-xs'
      >
        {result.series.map((series, index) => (
          <li
            key={series.key}
            className='flex min-w-0 items-center gap-1.5'
            title={series.label}
          >
            <span
              aria-hidden='true'
              className='size-2 shrink-0 rounded-full dark:hidden'
              style={{ backgroundColor: SERIES_COLORS[index].light }}
            />
            <span
              aria-hidden='true'
              className='hidden size-2 shrink-0 rounded-full dark:block'
              style={{ backgroundColor: SERIES_COLORS[index].dark }}
            />
            <span className='max-w-56 truncate'>{series.label}</span>
          </li>
        ))}
      </ul>
      {result.groupedCount > 0 && (
        <p className='text-muted-foreground mt-2 text-xs'>
          {t('Top 5 items are shown separately. Filter to inspect the others.')}
        </p>
      )}
    </div>
  )
}
