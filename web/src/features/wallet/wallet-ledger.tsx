import { useInfiniteQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Main } from '@/components/layout'
import { Button } from '@/components/ui/button'
import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
  SheetDescription,
} from '@/components/ui/sheet'
import { useIsMobile } from '@/hooks/use-mobile'
import { toIntlLocale } from '@/i18n/languages'
import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

import './wallet-ledger.css'

const names = {
  hold: 'Request hold',
  hold_total: 'Legacy hold total',
  extra_hold: 'Additional hold',
  settle: 'Request settlement',
  release: 'Hold released',
  topup: 'Balance top-up',
  other_topup: 'Balance top-up',
  redeem: 'Code redemption',
  referral: 'Referral transfer',
  adjustment: 'Balance adjustment',
} as const
interface Entry {
  reference: string
  kind: keyof typeof names
  at: number
  delta: number
  actual: number | null
  after: number | null
  source: number
  key: string
}
interface History {
  available: number
  pending: number
  today: number
  quota_per_unit: number
  events: Entry[]
  more: boolean
}
const filters = {
  All: Object.keys(names),
  Spending: ['settle'],
  Holds: ['hold', 'hold_total', 'extra_hold', 'release'],
  Credits: ['topup', 'other_topup', 'redeem', 'referral'],
  Adjustments: ['adjustment'],
}

/** 显示当前账号的真实流水。查询按账号隔离，切换时间范围时不保留上一范围的金额。 */
export function WalletLedger() {
  const { t, i18n } = useTranslation()
  const id = useAuthStore((s) => s.auth.user?.id)
  const mobile = useIsMobile()
  const [days, setDays] = useState(1)
  const [filter, setFilter] = useState<keyof typeof filters>('All')
  const [detail, setDetail] = useState<Entry | null>(null)
  const query = useInfiniteQuery({
    queryKey: ['retail-wallet-history', id, days],
    enabled: !!id,
    initialPageParam: null as Entry | null,
    gcTime: 0,
    queryFn: async ({ pageParam, signal }) => {
      const res = await api.get<{ success: boolean; data: History }>(
        '/api/user/self/wallet-history',
        {
          params: {
            days,
            ...(pageParam
              ? {
                  before: pageParam.at,
                  source: pageParam.source,
                  key: pageParam.key,
                }
              : {}),
          },
          signal,
        }
      )
      if (!res.data.success || !(res.data.data?.quota_per_unit > 0)) {
        throw new Error('wallet history unavailable')
      }
      return res.data.data
    },
    getNextPageParam: (p) => (p.more ? p.events.at(-1) : undefined),
  })
  const summary = query.data?.pages[0]
  const all = query.data?.pages.flatMap((p) => p.events) ?? []
  const rows = all.filter((e) => filters[filter].includes(e.kind))
  const date = new Intl.DateTimeFormat(toIntlLocale(i18n.language), {
    timeZone: 'Asia/Shanghai',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    hour12: false,
  })
  /** 延用零售充值页人民币口径；非零的小额调用增加精度，避免显示成免费。 */
  const money = (quota: number | null | undefined, signed = false) => {
    if (quota == null || !summary) return '—'
    const amount = quota / summary.quota_per_unit
    return new Intl.NumberFormat(toIntlLocale(i18n.language), {
      style: 'currency',
      currency: 'CNY',
      currencyDisplay: 'narrowSymbol',
      minimumFractionDigits: 2,
      maximumFractionDigits: amount !== 0 && Math.abs(amount) < 0.01 ? 8 : 2,
      signDisplay: signed ? 'exceptZero' : 'auto',
    }).format(amount)
  }
  // 同一秒内不同来源没有可靠先后顺序，不用假定的顺序连线。趋势只绘制可以确定的余额点。
  const counts = new Map<number, number>()
  for (const e of all) counts.set(e.at, (counts.get(e.at) ?? 0) + 1)
  const points = all
    .filter(
      (e): e is Entry & { after: number } =>
        e.after !== null && counts.get(e.at) === 1
    )
    .reverse()
  const low = Math.min(...points.map((p) => p.after)),
    high = Math.max(...points.map((p) => p.after))
  const first = points.at(0),
    last = points.at(-1)
  const after = (e: Entry) =>
    e.after === null ? t('Not recorded') : money(e.after)
  return (
    <Main className='retail-wallet'>
      <div className='wl wallet-wrap'>
        <header className='wl-head'>
          <div>
            <h1>{t('Wallet')}</h1>
            <p>{t('Balance and recent activity')}</p>
          </div>
          <Button render={<Link to='/wallet/recharge' />}>
            {t('Recharge')}
          </Button>
        </header>
        <section className='wl-overview'>
          <article className='wl-card wl-balance'>
            <h2>{t('Available balance')}</h2>
            <strong className='wl-amount'>{money(summary?.available)}</strong>
            <dl className='wl-metrics'>
              <div>
                <dt>{t('On hold')}</dt>
                <dd>{money(summary?.pending)}</dd>
              </div>
              <div>
                <dt>{t('Charged today')}</dt>
                <dd>{money(summary?.today)}</dd>
              </div>
            </dl>
          </article>
          <article className='wl-card wl-trend'>
            <header>
              <h2>{t('Balance trend')}</h2>
              <div className='wl-choices'>
                {([1, 7, 30] as const).map((n) => (
                  <button
                    type='button'
                    key={n}
                    aria-pressed={days === n}
                    onClick={() => setDays(n)}
                  >
                    {t(
                      ({ 1: 'Today', 7: '7 days', 30: '30 days' } as const)[n]
                    )}
                  </button>
                ))}
              </div>
            </header>
            {first && last && first.at !== last.at ? (
              <>
                <svg
                  role='img'
                  aria-label={t('Recorded balance snapshots')}
                  viewBox='0 0 500 92'
                  preserveAspectRatio='none'
                >
                  <polyline
                    fill='none'
                    stroke='currentColor'
                    strokeWidth='2'
                    vectorEffect='non-scaling-stroke'
                    points={points
                      .map(
                        (p) =>
                          `${4 + (492 * (p.at - first.at)) / (last.at - first.at)},${high === low ? 46 : 84 - (76 * (p.after - low)) / (high - low)}`
                      )
                      .join(' ')}
                  />
                </svg>
                <footer>
                  {date.format(first.at * 1000)}
                  <span>{date.format(last.at * 1000)}</span>
                </footer>
              </>
            ) : (
              <p className='wl-empty'>
                {query.isLoading
                  ? t('Loading…')
                  : t('Not enough recorded balances to show a trend')}
              </p>
            )}
          </article>
        </section>
        <section className='wl-card wl-activity'>
          <h2>{t('Balance activity')}</h2>
          <p>
            {t(
              'Holds reduce available funds. Settlement shows the final charge and the difference returned or collected.'
            )}
          </p>
          <div className='wl-choices'>
            {(Object.keys(filters) as (keyof typeof filters)[]).map((f) => (
              <button
                type='button'
                aria-pressed={filter === f}
                key={f}
                onClick={() => setFilter(f)}
              >
                {t(f)}
              </button>
            ))}
          </div>
          <div className='wl-labels'>
            <span>{t('Transaction')}</span>
            <span>{t('Balance change')}</span>
            <span>{t('Balance after')}</span>
          </div>
          {rows.map((e) => (
            <button
              type='button'
              className='wl-row'
              key={`${e.source}:${e.key}`}
              onClick={() => setDetail(e)}
            >
              <span>
                <b>{t(names[e.kind])}</b>
                <small>
                  {date.format(e.at * 1000)}
                  {e.actual !== null && e.kind === 'settle'
                    ? ` · ${t('Actual charge')} ${money(e.actual)}`
                    : ''}
                </small>
              </span>
              <strong data-credit={e.delta > 0}>{money(e.delta, true)}</strong>
              <span className='wl-after'>{after(e)}</span>
            </button>
          ))}
          {query.isError && (
            <p role='alert'>
              {t('Wallet records could not be loaded')}{' '}
              <button
                type='button'
                onClick={() =>
                  query.isFetchNextPageError
                    ? query.fetchNextPage()
                    : query.refetch()
                }
              >
                {t('Retry')}
              </button>
            </p>
          )}
          {!query.isError && !rows.length && (
            <p className='wl-empty' role='status'>
              {query.isLoading
                ? t('Loading…')
                : t('No matching records on these pages')}
            </p>
          )}
          {query.hasNextPage && (
            <Button
              variant='ghost'
              disabled={query.isFetchingNextPage}
              onClick={() => query.fetchNextPage()}
            >
              {t('Load older records')}
            </Button>
          )}
          <p className='wl-foot'>
            {t(
              'Historical balances are shown only when recorded. Older credits may only appear in top-up history.'
            )}
          </p>
          <Link to='/wallet/recharge' search={{ show_history: true }}>
            {t('Top-up history')}
          </Link>
          <Link to='/wallet/recharge' search={{ tab: 'referral' }}>
            {t('Referral rewards')}
          </Link>
        </section>
        <Sheet
          open={!!detail}
          onOpenChange={(open) => {
            if (!open) setDetail(null)
          }}
        >
          <SheetContent side={mobile ? 'bottom' : 'right'}>
            <SheetHeader>
              <SheetTitle>
                {detail ? t(names[detail.kind]) : t('Transaction')}
              </SheetTitle>
              <SheetDescription>
                {detail && date.format(detail.at * 1000)}
              </SheetDescription>
            </SheetHeader>
            {detail && (
              <dl className='wl-detail'>
                {[
                  [t('Balance change'), money(detail.delta, true)],
                  [t('Balance after'), after(detail)],
                  ...(detail.actual !== null
                    ? [[t('Actual charge'), money(detail.actual)]]
                    : []),
                  [t('Reference'), detail.reference || detail.key],
                ].map(([label, v]) => (
                  <div key={label}>
                    <dt>{label}</dt>
                    <dd>{v}</dd>
                  </div>
                ))}
              </dl>
            )}
          </SheetContent>
        </Sheet>
      </div>
    </Main>
  )
}
