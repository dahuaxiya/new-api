import { useInfiniteQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { Check, Copy } from 'lucide-react'
import { useRef, useState } from 'react'
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
import { useCopyToClipboard } from '@/hooks/use-copy-to-clipboard'
import { useIsMobile } from '@/hooks/use-mobile'
import { toIntlLocale } from '@/i18n/languages'
import { api } from '@/lib/api'
import { getCurrencyFractionDigits } from '@/lib/currency'
import { usageCostPrecision } from '@/lib/format'
import { useAuthStore } from '@/stores/auth-store'

import { BalanceTrend } from './components/balance-trend'

import './wallet-ledger.css'

const names = {
  hold: 'Request hold',
  hold_total: 'Legacy hold total',
  extra_hold: 'Additional hold',
  pending: 'Awaiting settlement',
  settle: 'Spending',
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
  held?: number
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
  'Awaiting settlement': ['pending'],
  Credits: ['topup', 'other_topup', 'redeem', 'referral'],
  Adjustments: ['adjustment'],
}

/** 按账户和视图隔离分页游标；未结算总额对应所有日期。
 * @param days 账单展示天数。
 * @param view 按请求汇总或查询全部未结算单。
 * @param enabled 仅在用户查看时加载待结算明细。
 * @return 当前视图的分页状态及账单数据。
 */
function useWalletActivity(
  days: number,
  view: 'activity' | 'pending',
  enabled: boolean
) {
  const id = useAuthStore((s) => s.auth.user?.id)
  return useInfiniteQuery({
    queryKey: ['retail-wallet-history', id, days, view],
    enabled: !!id && enabled,
    initialPageParam: null as Entry | null,
    gcTime: 0,
    queryFn: async ({ pageParam, signal }) => {
      const res = await api.get<{ success: boolean; data: History }>(
        '/api/user/self/wallet-history',
        {
          params: {
            days,
            view,
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
}

/** 把金额拆成整数和小数两段，让列表里不同精度的实扣按小数点对齐，同时不补零、不截断精度。
 * @param props.value 已按账本精度格式化好的金额文本，例如 -¥0.0187；占位符原样显示。
 * @return 供等宽数字列使用的两段式金额。
 */
function AlignedAmount({ value }: { value: string }) {
  const dot = value.search(/[.,](?=\d+$)/)
  return (
    <span className='wl-num'>
      <span>{dot < 0 ? value : value.slice(0, dot)}</span>
      <span>{dot < 0 ? '' : value.slice(dot)}</span>
    </span>
  )
}

/** 每次请求仅占一行，保留真实余额快照和可核对的预留差额。
 * @return 当前登录账户的钱包页面。
 */
export function WalletLedger() {
  const { t, i18n } = useTranslation()
  const mobile = useIsMobile()
  const [days, setDays] = useState(1)
  const [filter, setFilter] = useState<keyof typeof filters>('All')
  const [detail, setDetail] = useState<Entry | null>(null)
  const pendingTab = useRef<HTMLButtonElement>(null)
  const { copiedText, copyToClipboard } = useCopyToClipboard({ notify: false })
  const showingPending = filter === 'Awaiting settlement'
  const activity = useWalletActivity(days, 'activity', true)
  const pending = useWalletActivity(1, 'pending', showingPending)
  const query = showingPending ? pending : activity
  const summary = activity.data?.pages[0] ?? pending.data?.pages[0]
  const all = activity.data?.pages.flatMap((p) => p.events) ?? []
  const rows = (
    showingPending ? (pending.data?.pages.flatMap((p) => p.events) ?? []) : all
  ).filter((e) => filters[filter].includes(e.kind))
  const date = new Intl.DateTimeFormat(toIntlLocale(i18n.language), {
    timeZone: 'Asia/Shanghai',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    hour12: false,
  })
  /** 格式化余额及入账，余额摘要保留分位。
   * @param quota 以账本额度单位记录的金额，未知时显示占位。
   * @param signed 是否标明收支方向。
   * @return 本地化人民币金额。
   */
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
  /** 单笔请求按使用日志精度显示，直接使用人民币账本单位，避免再次做汇率换算。
   * @param quota 带方向的整数额度，缺失时显示占位。
   * @return 去掉末尾无意义零的人民币金额。
   */
  const requestMoney = (quota: number | null | undefined) => {
    if (quota == null || !summary) return '—'
    const amount = quota / summary.quota_per_unit
    return new Intl.NumberFormat(toIntlLocale(i18n.language), {
      style: 'currency',
      currency: 'CNY',
      currencyDisplay: 'narrowSymbol',
      minimumFractionDigits: 0,
      maximumFractionDigits: getCurrencyFractionDigits(
        amount,
        usageCostPrecision
      ),
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
  const snapshotTime = new Intl.DateTimeFormat(toIntlLocale(i18n.language), {
    timeZone: 'Asia/Shanghai',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    hour12: false,
    second: '2-digit',
  })
  const balanceFormat = new Intl.NumberFormat(toIntlLocale(i18n.language), {
    style: 'currency',
    currency: 'CNY',
    currencyDisplay: 'narrowSymbol',
    minimumFractionDigits: 2,
    maximumFractionDigits: 2,
  })
  /** 余额快照只在展示时取到分，不改变趋势点和结算使用的原始额度。
   * @param quota 账本记录的余额额度；缺失时显示占位。
   * @return 固定两位小数的人民币余额，或占位符。
   */
  const balance = (quota: number | null | undefined) =>
    quota == null || !summary
      ? '—'
      : balanceFormat.format(quota / summary.quota_per_unit)
  const after = (e: Entry) =>
    e.after === null ? t('Not recorded') : balance(e.after)
  /** 预留只表示占用，结算显示净支出，其余收支显示带方向的余额变动。
   * @param entry 服务端按请求汇总的流水项。
   * @return 列表中展示的人民币金额。
   */
  const rowAmount = (entry: Entry) => {
    if (entry.kind === 'pending') return requestMoney(entry.held)
    if (entry.kind === 'settle') return requestMoney(entry.delta)
    return money(entry.delta, true)
  }
  // 预留和结算的关联记录就是请求 ID（retail_charges.id = RequestId），可以直接定位使用日志；
  // 充值、调整等余额变动没有对应的请求，只提供复制。
  const requestSources = new Set([1, 5])
  const reference = detail ? detail.reference || detail.key : ''
  // 只有预留额和实扣额都已知时才展示结算算式；这里直接取出数字，后面的算式不需要再做空值判断。
  const settled =
    detail?.actual != null && detail.held != null
      ? { held: detail.held, actual: detail.actual }
      : null
  return (
    <Main className='retail-wallet'>
      <div className='wl wallet-wrap'>
        <header className='wl-head'>
          <div>
            <h1 className='text-base font-bold tracking-tight sm:text-lg'>
              {t('Wallet')}
            </h1>
            <p>{t('Balance and recent activity')}</p>
          </div>
        </header>
        <section className='wl-overview'>
          <article className='wl-card wl-balance'>
            <h2>{t('Available balance')}</h2>
            <strong className='wl-amount'>{balance(summary?.available)}</strong>
            <dl className='wl-metrics'>
              <div>
                <dt>{t('Awaiting settlement')}</dt>
                <dd>
                  <button
                    className='wl-pending-link'
                    type='button'
                    aria-label={`${t('Awaiting settlement')} ${money(summary?.pending)}`}
                    onClick={() => {
                      setFilter('Awaiting settlement')
                      pendingTab.current?.focus()
                    }}
                  >
                    {money(summary?.pending)} <span aria-hidden='true'>›</span>
                  </button>
                  <small>{t('Already deducted from available balance')}</small>
                </dd>
              </div>
              <div>
                <dt>{t('Charged today')}</dt>
                <dd>{money(summary?.today)}</dd>
              </div>
            </dl>
          </article>
          <article className='wl-card wl-trend'>
            <header>
              <h2>{t('Available balance trend')}</h2>
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
            {points.length && summary ? (
              <>
                <BalanceTrend
                  key={days}
                  points={points}
                  formatTime={(at) => snapshotTime.format(at * 1000)}
                  formatBalance={balance}
                />
                {/* 曲线只由已加载的余额快照构成；还有更早的页时明确说明，避免把首尾差当成整段时间的消费。 */}
                <p className='wl-coverage'>
                  {t('{{count}} balance records', { count: points.length })}
                  {activity.hasNextPage
                    ? ` · ${t('Older records not loaded yet')}`
                    : ''}
                </p>
              </>
            ) : (
              <p className='wl-empty'>
                {activity.isLoading
                  ? t('Loading…')
                  : t('Not enough recorded balances to show a trend')}
              </p>
            )}
          </article>
        </section>
        <section className='wl-card wl-activity'>
          <h2>{t('Balance activity')}</h2>
          <div className='wl-choices'>
            {(Object.keys(filters) as (keyof typeof filters)[]).map((f) => (
              <button
                type='button'
                aria-pressed={filter === f}
                key={f}
                ref={f === 'Awaiting settlement' ? pendingTab : undefined}
                onClick={() => setFilter(f)}
              >
                {t(f)}
              </button>
            ))}
          </div>
          {showingPending && <p>{t('Outstanding holds across all dates')}</p>}
          <div className='wl-labels'>
            <span>{t('Transaction')}</span>
            <span>{t('Amount')}</span>
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
                <small>{date.format(e.at * 1000)}</small>
              </span>
              <strong
                data-pending={e.kind === 'pending'}
                data-credit={e.delta > 0}
              >
                <AlignedAmount value={rowAmount(e)} />
              </strong>
              <span className='wl-after'>
                <span className='wl-mobile-label'>{t('Balance after')} </span>
                {after(e)}
              </span>
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
          <div className='wl-links'>
            <Link to='/wallet/recharge' search={{ show_history: true }}>
              {t('Top-up history')}
            </Link>
            <Link to='/wallet/recharge' search={{ tab: 'referral' }}>
              {t('Referral rewards')}
            </Link>
          </div>
        </section>
        <Sheet
          open={!!detail}
          onOpenChange={(open) => {
            if (!open) setDetail(null)
          }}
        >
          <SheetContent className='wl-sheet' side={mobile ? 'bottom' : 'right'}>
            <SheetHeader>
              <SheetTitle>
                {detail ? t(names[detail.kind]) : t('Transaction')}
              </SheetTitle>
              <SheetDescription>
                {detail && date.format(detail.at * 1000)}
              </SheetDescription>
            </SheetHeader>
            {detail && (
              <div className='wl-detail'>
                {settled ? (
                  <>
                    <div className='wl-charge'>
                      <span>{t('Actual charge')}</span>
                      <strong>{requestMoney(settled.actual)}</strong>
                    </div>
                    {/* 按结算顺序列出：先预留，再退回多余部分（或补扣不足部分），最后一行就是实扣，用户不用自己心算。 */}
                    <dl className='wl-equation'>
                      <div>
                        <dt>{t('Request hold')}</dt>
                        <dd>{requestMoney(settled.held)}</dd>
                      </div>
                      {settled.held !== settled.actual && (
                        <div>
                          <dt>
                            {t(
                              settled.held > settled.actual
                                ? 'Hold released'
                                : 'Additional charge'
                            )}
                          </dt>
                          <dd>
                            {settled.held > settled.actual ? '−' : '+'}{' '}
                            {requestMoney(
                              Math.abs(settled.held - settled.actual)
                            )}
                          </dd>
                        </div>
                      )}
                      <div data-total='true'>
                        <dt>{t('Actual charge')}</dt>
                        <dd>= {requestMoney(settled.actual)}</dd>
                      </div>
                    </dl>
                  </>
                ) : (
                  <div className='wl-charge'>
                    <span>
                      {t(detail.held != null ? 'Request hold' : 'Balance change')}
                    </span>
                    <strong>
                      {detail.held != null
                        ? requestMoney(detail.held)
                        : money(detail.delta, true)}
                    </strong>
                  </div>
                )}
                <dl className='wl-facts'>
                  <div>
                    <dt>{t('Balance after')}</dt>
                    <dd>{after(detail)}</dd>
                  </div>
                  <div>
                    <dt>{t('Reference')}</dt>
                    <dd>
                      <code>{reference}</code>
                      <button
                        type='button'
                        className='wl-icon'
                        aria-label={t('Copy')}
                        onClick={() => copyToClipboard(reference)}
                      >
                        {copiedText === reference ? (
                          <Check aria-hidden='true' />
                        ) : (
                          <Copy aria-hidden='true' />
                        )}
                      </button>
                    </dd>
                  </div>
                </dl>
                {requestSources.has(detail.source) && reference && (
                  <Button
                    variant='outline'
                    className='wl-log-link'
                    render={
                      <Link
                        to='/usage-logs/$section'
                        params={{ section: 'common' }}
                        search={{ requestId: reference }}
                      />
                    }
                  >
                    {t('View usage log')}
                  </Button>
                )}
              </div>
            )}
          </SheetContent>
        </Sheet>
      </div>
    </Main>
  )
}
