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
import { useQuery } from '@tanstack/react-query'
import { RefreshCw } from 'lucide-react'
import { useId, useState, type KeyboardEvent } from 'react'
import { useTranslation } from 'react-i18next'

import { ErrorState } from '@/components/error-state'
import { PublicLayout } from '@/components/layout'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import {
  Tooltip,
  TooltipTrigger,
  TooltipContent,
} from '@/components/ui/tooltip'
import { toIntlLocale } from '@/i18n/languages'
import { api } from '@/lib/api'
import { requireServerSuccess } from '@/lib/server-error-message'
import { cn } from '@/lib/utils'

import './atlas.css'

const windows = ['90m', '24h'] as const
type Window = (typeof windows)[number]
type SliceStatus = 'success' | 'mixed' | 'failure' | 'empty'
type WindowMetrics = {
  success_pct: number | null
  cache_read_pct: number | null
  mean_ttft_ms: number | null
  slice_success_pct: (number | null)[]
}
type GroupAvailability = {
  group: string
  observed: boolean
  metrics?: Record<Window, WindowMetrics>
} & Record<Window, SliceStatus[]>
type Snapshot = { updated_at: number; groups: GroupAvailability[] }
const secondsPerSlice: Record<Window, number> = { '90m': 300, '24h': 4800 }

const sliceColors = {
  success: 'bg-success/80',
  mixed: 'bg-warning/80',
  failure: 'bg-destructive/75',
  empty: 'bg-muted-foreground/17',
}

/** 展示本站分组的滚动统计。更新失败时隐藏过期数据，普通刷新成功前保留当前时间条。 */
export function ModelAvailability() {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const sliceLabels = {
    success: t('All requests succeeded'),
    mixed: t('Some requests failed'),
    failure: t('All requests failed'),
    empty: t('No samples'),
  }
  const [window, setWindow] = useState<Window>('90m')
  const [issuesOnly, setIssuesOnly] = useState(false)
  const query = useQuery({
    queryKey: ['retail-model-availability'],
    queryFn: async ({ signal }) => {
      const { data } = await api.get<{ success: boolean; data: Snapshot }>(
        '/api/retail/availability',
        { signal, skipErrorHandler: true }
      )
      return requireServerSuccess(data).data
    },
    refetchInterval: 60_000,
    retry: false,
  })
  const dateTime = new Intl.DateTimeFormat(locale, {
    dateStyle: 'short',
    timeStyle: 'short',
  })
  const snapshot = query.isError ? undefined : query.data

  // 筛选只看所选窗口的最近一格。未监测和无样本不计为故障。
  const groups = snapshot?.groups ?? []
  const issues = groups.filter((row) =>
    ['mixed', 'failure'].includes(row[window].at(-1) ?? 'empty')
  )
  const clock = new Intl.DateTimeFormat(locale, {
    hour: '2-digit',
    minute: '2-digit',
    hour12: false,
    timeZone: 'Asia/Shanghai',
  })
  const day = new Intl.DateTimeFormat(locale, {
    month: 'numeric',
    day: 'numeric',
    timeZone: 'Asia/Shanghai',
  })
  /** 所有行共用源站快照时间。跨日时带上日期，防止读成当天的同一时刻。 */
  function tick(offset: number) {
    const end = (snapshot?.updated_at ?? 0) * 1000,
      value = end - offset * secondsPerSlice[window] * 1000
    return (
      (day.format(value) !== day.format(end) ? day.format(value) + ' ' : '') +
      clock.format(value)
    )
  }
  return (
    <PublicLayout showMainContainer={false}>
      <main className='availability-atlas mx-auto w-full max-w-[850px] px-4 pt-20 pb-12 sm:px-5 sm:pt-24'>
        <header className='atlas-intro'>
          <div>
            <small className='atlas-kicker'>STATUS ATLAS / 01</small>
            <h1>
              {t('Model availability')}
              <em aria-hidden='true'>.</em>
            </h1>
            <p>
              {t(
                'Historical status for each call group, based on sampled requests. This is not a live test.'
              )}
            </p>
            {snapshot && (
              <div className='atlas-count'>
                {t('All groups')} <b>{groups.length}</b>　·　
                {t('Latest issues')} <b>{issues.length}</b>
              </div>
            )}
          </div>
          <img
            src='/brand/gateway-sculpture.webp'
            alt=''
            width='270'
            height='168'
          />
        </header>
        {query.isError && (
          <ErrorState
            title={t('Availability data is temporarily unavailable')}
            onRetry={() => void query.refetch()}
          />
        )}
        {!query.isError && (
          <section
            className='bg-card overflow-hidden rounded-xl border'
            aria-busy={query.isPending}
          >
            <div className='flex flex-wrap items-center justify-between gap-3 border-b px-4 py-3 sm:px-5'>
              <div>
                <small className='atlas-kicker'>GROUP HISTORY</small>
                <h2 className='text-lg font-semibold'>{t('Group timeline')}</h2>
              </div>
              <div
                className='bg-muted flex items-center gap-1 rounded-full p-1'
                role='group'
                aria-label={t('Time range')}
              >
                {windows.map((value) => (
                  <Button
                    key={value}
                    size='sm'
                    className='rounded-full motion-reduce:transition-none'
                    variant={window === value ? 'default' : 'ghost'}
                    aria-pressed={value === window}
                    onClick={() => setWindow(value)}
                  >
                    {value === '90m'
                      ? t('Last 90 minutes')
                      : t('Last 24 hours')}
                  </Button>
                ))}
              </div>
            </div>
            <ul className='text-muted-foreground flex flex-wrap gap-x-4 gap-y-2 border-b px-4 py-3 text-xs sm:px-5'>
              <li className='atlas-filters'>
                {[false, true].map((only) => (
                  <button
                    key={String(only)}
                    type='button'
                    aria-pressed={issuesOnly === only}
                    onClick={() => setIssuesOnly(only)}
                  >
                    {only
                      ? t('Latest issues') + ' · ' + issues.length
                      : t('All groups')}
                  </button>
                ))}
              </li>
              {(['success', 'mixed', 'failure', 'empty'] as const).map(
                (status) => (
                  <li key={status} className='flex items-center gap-1.5'>
                    <span
                      aria-hidden='true'
                      className={cn('size-2.5 rounded-xs', sliceColors[status])}
                    />
                    {sliceLabels[status]}
                  </li>
                )
              )}
              <li className='basis-full sm:ml-auto sm:basis-auto'>
                {t('{{minutes}} min per segment', {
                  minutes: secondsPerSlice[window] / 60,
                })}
              </li>
            </ul>
            {snapshot && (
              <div className='atlas-line atlas-axis' aria-hidden='true'>
                <span>{t('Call group')}</span>
                <div>
                  {[18, 9, 0].map((n) => (
                    <span key={n}>{tick(n)}</span>
                  ))}
                </div>
                <span>{t('Latest interval')}</span>
              </div>
            )}
            {query.isPending && (
              <div role='status' aria-label={t('Loading...')}>
                {Array.from({ length: 5 }, (_, index) => (
                  <div
                    key={index}
                    aria-hidden='true'
                    className='space-y-3 border-b px-4 py-4 last:border-b-0 sm:px-5'
                  >
                    <Skeleton className='h-4 w-32 motion-reduce:animate-none' />
                    <Skeleton className='h-6 w-full motion-reduce:animate-none' />
                    <Skeleton className='h-3 w-36 motion-reduce:animate-none' />
                  </div>
                ))}
              </div>
            )}
            {snapshot?.groups.length === 0 && (
              <p className='text-muted-foreground px-4 py-8 text-center text-sm'>
                {t('No published call groups')}
              </p>
            )}
            {snapshot && issuesOnly && !issues.length && groups.length > 0 && (
              <p className='p-4 text-sm'>
                {t('No groups with failures in the latest interval')}
              </p>
            )}
            {snapshot &&
              (issuesOnly ? issues : groups).map((row) => (
                <GroupTimeline
                  key={`${row.group}:${window}`}
                  row={row}
                  window={window}
                  end={snapshot.updated_at}
                  locale={locale}
                  labels={sliceLabels}
                />
              ))}
          </section>
        )}
        <div className='text-muted-foreground flex items-center gap-2 text-xs tabular-nums'>
          {snapshot && (
            <span>
              {t('Data updated {{time}}', {
                time: dateTime.format(snapshot.updated_at * 1000),
              })}
            </span>
          )}
          <Button
            size='icon'
            variant='ghost'
            aria-label={t('Refresh')}
            disabled={query.isFetching}
            onClick={() => void query.refetch()}
          >
            <RefreshCw
              aria-hidden='true'
              className={
                query.isFetching ? 'motion-safe:animate-spin' : undefined
              }
            />
          </Button>
        </div>
      </main>
    </PublicLayout>
  )
}

/** 一组时间片共用一个键盘入口；触屏点选、指针悬停和键盘切换都在同一位置显示明细。 */
function GroupTimeline(props: {
  row: GroupAvailability
  window: Window
  end: number
  locale: string | undefined
  labels: Record<SliceStatus, string>
}) {
  const { t } = useTranslation()
  const { row, window, end, locale, labels } = props
  const heading = useId()
  const slices = row[window]
  // 旧接口或无计时样本时保持空态，不把空值转成 0%。
  const metrics = row.metrics?.[window]
  const pct = new Intl.NumberFormat(locale, {
    style: 'percent',
    maximumFractionDigits: 2,
  })
  const secondsFormat = new Intl.NumberFormat(locale, {
    style: 'unit',
    unit: 'second',
    unitDisplay: 'short',
    maximumFractionDigits: 2,
  })
  /** API 百分率以 0–100 表示；Intl 百分数格式需要 0–1 的比值。 */
  function rate(value: number | null | undefined) {
    return value == null ? '—' : pct.format(value / 100)
  }
  const seconds = secondsPerSlice[window]
  const [selected, setSelected] = useState(slices.length - 1)
  const [hovered, setHovered] = useState<number | null>(null)
  const current = hovered ?? selected
  const time = new Intl.DateTimeFormat(locale, {
    timeStyle: 'short',
    timeZone: 'Asia/Shanghai',
  })
  const date = new Intl.DateTimeFormat(locale, {
    month: '2-digit',
    day: '2-digit',
    timeZone: 'Asia/Shanghai',
  })
  const start = end - slices.length * seconds

  /** 时间边界与源数据快照一致，不能用访客加载页面的时间平移历史样本。 */
  function describeSlice(index: number) {
    const from = (start + index * seconds) * 1000
    const to = from + seconds * 1000
    const fromDay = date.format(from)
    const toDay = date.format(to)
    const toLabel =
      fromDay === toDay ? time.format(to) : `${toDay} ${time.format(to)}`
    const range = `${fromDay} ${time.format(from)}–${toLabel}`
    const status = slices[index] ?? 'empty'
    return status === 'empty'
      ? `${range} · ${labels.empty}`
      : `${range} · ${labels[status]} · ${t('Success rate')} ${rate(metrics?.slice_success_pct[index])}`
  }

  /** 用户按左右箭头或首尾键时，将焦点移到同一组的对应时间片。 */
  function moveSlice(event: KeyboardEvent<HTMLDivElement>) {
    const target = (
      {
        ArrowLeft: selected - 1,
        ArrowRight: selected + 1,
        Home: 0,
        End: slices.length - 1,
      } as Record<string, number>
    )[event.key]
    if (target === undefined) return
    event.preventDefault()
    const next = Math.max(0, Math.min(slices.length - 1, target))
    setHovered(null)
    event.currentTarget
      .querySelectorAll<HTMLButtonElement>('button')
      [next]?.focus()
  }

  return (
    <article
      aria-labelledby={heading}
      className='atlas-line atlas-row border-b last:border-b-0'
    >
      <div className='flex items-baseline justify-between gap-2'>
        <h2 id={heading} className='min-w-0 text-sm font-semibold break-words'>
          {row.group}
        </h2>
      </div>
      <span className='atlas-latest'>
        {row.observed ? labels[slices.at(-1) ?? 'empty'] : t('Not monitored')}
      </span>
      <div className='atlas-metrics text-muted-foreground flex flex-wrap items-center gap-x-4 gap-y-1 text-xs tabular-nums'>
        <span>
          {window === '90m' ? t('Last 90 minutes') : t('Last 24 hours')}
        </span>
        <span>
          {t('Success rate')} <b>{rate(metrics?.success_pct)}</b>
        </span>
        <span>
          {t('Cache read rate')} <b>{rate(metrics?.cache_read_pct)}</b>
        </span>
        <span>
          {t('Mean time to first token')}{' '}
          <b>
            {metrics?.mean_ttft_ms == null
              ? '—'
              : secondsFormat.format(metrics.mean_ttft_ms / 1000)}
          </b>
        </span>
      </div>
      <div
        className='atlas-track grid max-w-[392px] grid-cols-[repeat(18,minmax(0,1fr))] gap-0.5 sm:gap-1'
        role='group'
        aria-labelledby={heading}
        onKeyDown={moveSlice}
        onPointerLeave={() => setHovered(null)}
      >
        {slices.map((status, index) => {
          // 用相对时间保留刷新前的焦点，滚动窗口的绝对时间每次都会变化。
          const lookbackSeconds = (slices.length - index) * seconds
          return (
            <Tooltip key={lookbackSeconds}>
              <TooltipTrigger
                delay={0}
                render={
                  <button
                    type='button'
                    tabIndex={selected === index ? 0 : -1}
                    aria-label={describeSlice(index)}
                    aria-pressed={selected === index}
                    onFocus={() => setSelected(index)}
                    onClick={() => {
                      setHovered(null)
                      setSelected(index)
                    }}
                    onPointerEnter={(event) => {
                      if (event.pointerType === 'mouse') setHovered(index)
                    }}
                    className='focus-visible:ring-ring group relative h-8 min-w-0 rounded-sm outline-none focus-visible:ring-2 focus-visible:ring-offset-1'
                  >
                    <span
                      aria-hidden='true'
                      className={cn(
                        'absolute inset-x-0 top-1.5 bottom-1.5 rounded-sm transition-opacity motion-reduce:transition-none',
                        sliceColors[status],
                        current === index &&
                          'ring-foreground/55 ring-1 ring-inset'
                      )}
                    />
                  </button>
                }
              />
              <TooltipContent>
                {row.group} · {describeSlice(index)}
              </TooltipContent>
            </Tooltip>
          )
        })}
      </div>
      <p className='atlas-selection text-muted-foreground text-xs tabular-nums'>
        {describeSlice(current)}
      </p>
    </article>
  )
}
