import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { ErrorState } from '@/components/error-state'
import { SectionPageLayout } from '@/components/layout'
import { LoadingState } from '@/components/loading-state'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Empty, EmptyHeader, EmptyTitle } from '@/components/ui/empty'
import {
  Pagination,
  PaginationContent,
  PaginationItem,
} from '@/components/ui/pagination'
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { getUserQuotaDataByUsers } from '@/features/dashboard/api'
import type { QuotaDataItem } from '@/features/dashboard/types'
import { WindowReading } from '@/features/gpt-quota/quota-reading'
import { quotaSchema } from '@/features/gpt-quota/schema'
import { getUsers } from '@/features/users/api'
import type { User } from '@/features/users/types'
import { toIntlLocale } from '@/i18n/languages'
import { api } from '@/lib/api'
import { formatQuotaWithCurrency } from '@/lib/currency'
import { formatNumber } from '@/lib/format'
import { ROLE } from '@/lib/roles'
import { requireServerSuccess } from '@/lib/server-error-message'
import { useAuthStore } from '@/stores/auth-store'
import { useSystemConfigStore } from '@/stores/system-config-store'

type UsageTotal = { count: number; tokens: number; quota: number }
type Period = { days: number; end: number }

/** Sum saved time buckets using the username identity returned by the existing API. */
function aggregateUsage(rows: QuotaDataItem[]): Map<string, UsageTotal> {
  const totals = new Map<string, UsageTotal>()
  for (const row of rows) {
    if (!row.username) continue
    const total = totals.get(row.username) ?? { count: 0, tokens: 0, quota: 0 }
    total.count += row.count ?? 0
    total.tokens += row.token_used ?? 0
    total.quota += row.quota ?? 0
    totals.set(row.username, total)
  }
  return totals
}

/** Drop the entire admin view on account/role changes, including in-flight query state. */
export function UserUsageOverview(props: { embedded?: boolean } = {}) {
  const user = useAuthStore((state) => state.auth.user)
  const sid = useAuthStore((state) => state.auth.session?.sid)
  const { t } = useTranslation()
  if (!user || user.role < ROLE.ADMIN) {
    return <ErrorState title={t('Access denied')} />
  }
  return (
    <AdminUsagePage
      key={`${user.id}:${sid ?? ''}`}
      embedded={props.embedded}
      viewerId={user.id}
      sid={sid}
    />
  )
}

/** Keep administrator reads session-scoped; refreshing this page only reads saved data. */
function AdminUsagePage(props: {
  embedded?: boolean
  viewerId: number
  sid: string | undefined
}) {
  const { t } = useTranslation()
  const client = useQueryClient()
  const [page, setPage] = useState(1)
  const [period, setPeriod] = useState<Period>(() => ({
    days: 7,
    end: Math.floor(Date.now() / 1000),
  }))
  const prefix = ['admin-user-usage', props.viewerId, props.sid]
  const start = period.end - period.days * 86400
  const users = useQuery({
    queryKey: [...prefix, 'users', page],
    queryFn: async () => {
      const response = requireServerSuccess(
        await getUsers({
          p: page,
          page_size: 10,
          sort_by: 'id',
          sort_order: 'asc',
        })
      )
      if (!response.data) throw new Error('Missing user list')
      return response.data
    },
    staleTime: 15_000,
    gcTime: 0,
    retry: false,
    meta: { errorToast: false },
  })
  const usage = useQuery({
    queryKey: [...prefix, 'usage', start, period.end],
    queryFn: async () => {
      const response = requireServerSuccess(
        await getUserQuotaDataByUsers({
          start_timestamp: start,
          end_timestamp: period.end,
        })
      )
      return aggregateUsage(response.data ?? [])
    },
    staleTime: 15_000,
    gcTime: 0,
    retry: false,
    meta: { errorToast: false },
  })
  let usageState: 'ready' | 'loading' | 'error' = 'ready'
  if (usage.isPending) usageState = 'loading'
  if (usage.isError) usageState = 'error'
  const ranges = [
    { value: '1', label: t('Last 24 hours') },
    { value: '7', label: t('Last 7 days') },
    { value: '30', label: t('Last 30 days') },
  ]
  const actions = (
    <>
      <Select
        value={String(period.days)}
        items={ranges}
        onValueChange={(value) => {
          if (value) {
            setPeriod({
              days: Number(value),
              end: Math.floor(Date.now() / 1000),
            })
          }
        }}
      >
        <SelectTrigger aria-label={t('Usage period')}>
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectGroup>
            {ranges.map((range) => (
              <SelectItem key={range.value} value={range.value}>
                {range.label}
              </SelectItem>
            ))}
          </SelectGroup>
        </SelectContent>
      </Select>
      <Button
        variant='outline'
        disabled={users.isFetching || usage.isFetching}
        onClick={() => {
          setPeriod((previous) => ({
            ...previous,
            end: Math.floor(Date.now() / 1000),
          }))
          void client.invalidateQueries({ queryKey: prefix })
        }}
      >
        {t('Refresh display')}
      </Button>
    </>
  )
  const content = (
    <>
      <p className='text-muted-foreground mb-4 text-sm leading-relaxed'>
        {t(
          'Site usage only includes requests through New API. Saved summaries may lag behind usage logs. Official GPT quota also includes usage outside this site.'
        )}{' '}
        {t(
          'Refresh display reads saved data; it does not probe every GPT account.'
        )}
      </p>
      {usage.isError && (
        <ErrorState
          className='min-h-0 py-3'
          title={t('Site usage summary unavailable')}
          description={t(
            'Failed reads are shown as unavailable, not zero usage.'
          )}
          onRetry={() => void usage.refetch()}
        />
      )}
      {users.isPending && <LoadingState inline message={t('Loading...')} />}
      {users.isError && (
        <ErrorState
          title={t('Failed to load users')}
          onRetry={() => void users.refetch()}
        />
      )}
      {!users.isError && users.data && (
        <>
          <div className='grid gap-4 xl:grid-cols-2'>
            {users.data.items.map((user) => (
              <UserUsageCard
                key={user.id}
                user={user}
                total={
                  usage.isError ? undefined : usage.data?.get(user.username)
                }
                usageState={usageState}
                prefix={prefix}
                start={start}
                end={period.end}
              />
            ))}
          </div>
          {users.data.items.length === 0 && (
            <Empty>
              <EmptyHeader>
                <EmptyTitle>{t('No users found')}</EmptyTitle>
              </EmptyHeader>
            </Empty>
          )}
          <Pagination className='mt-4' aria-label={t('User pages')}>
            <PaginationContent>
              <PaginationItem>
                <Button
                  variant='outline'
                  disabled={page === 1}
                  onClick={() => setPage(page - 1)}
                >
                  {t('Previous')}
                </Button>
              </PaginationItem>
              <PaginationItem>
                <span className='px-3 text-sm'>
                  {t('Page {{page}} of {{pages}}', {
                    page,
                    pages: Math.max(1, Math.ceil(users.data.total / 10)),
                  })}
                </span>
              </PaginationItem>
              <PaginationItem>
                <Button
                  variant='outline'
                  disabled={page * 10 >= users.data.total}
                  onClick={() => setPage(page + 1)}
                >
                  {t('Next')}
                </Button>
              </PaginationItem>
            </PaginationContent>
          </Pagination>
        </>
      )}
    </>
  )
  if (props.embedded) {
    return (
      <div className='space-y-4'>
        <div className='flex flex-wrap gap-2'>{actions}</div>
        {content}
      </div>
    )
  }
  return (
    <SectionPageLayout stackActionsOnMobile>
      <SectionPageLayout.Title>
        {t('User usage overview')}
      </SectionPageLayout.Title>
      <SectionPageLayout.Actions>{actions}</SectionPageLayout.Actions>
      <SectionPageLayout.Content>{content}</SectionPageLayout.Content>
    </SectionPageLayout>
  )
}

/** Show site accounting and subscription windows separately; never infer missing quota. */
function UserUsageCard(props: {
  user: User
  total?: UsageTotal
  usageState: 'loading' | 'error' | 'ready'
  prefix: (string | number | undefined)[]
  start: number
  end: number
}) {
  const { t, i18n } = useTranslation()
  useSystemConfigStore((state) => state.config.currency)
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const { user, total } = props
  const deleted = Boolean(user.DeletedAt)
  const quota = useQuery({
    queryKey: [...props.prefix, 'quota', user.id, user.username, user.group],
    queryFn: async ({ signal }) => {
      const response = await api.get(`/api/gpt-quota/users/${user.id}`, {
        signal,
      })
      return quotaSchema.parse(requireServerSuccess(response.data).data)
    },
    enabled: !deleted,
    staleTime: 15_000,
    refetchInterval: 30_000,
    gcTime: 0,
    retry: false,
    meta: { errorToast: false },
  })
  const metrics = [
    {
      label: t('Period requests'),
      value: total ? formatNumber(total.count, locale) : '—',
    },
    {
      label: t('Period tokens'),
      value: total ? formatNumber(total.tokens, locale) : '—',
    },
    {
      label: t('Period site usage'),
      value: total ? formatQuotaWithCurrency(total.quota) : '—',
    },
    {
      label: t('Lifetime requests'),
      value: formatNumber(user.request_count, locale),
    },
    {
      label: t('Lifetime site usage'),
      value: formatQuotaWithCurrency(user.used_quota),
    },
    { label: t('Site balance'), value: formatQuotaWithCurrency(user.quota) },
  ]
  const data = quota.data
  let status = user.status === 1 ? t('Enabled') : t('Disabled')
  if (deleted) status = t('Deleted')
  return (
    <Card
      aria-label={t('Usage for {{name}}', {
        name: user.display_name || user.username,
      })}
    >
      <CardHeader>
        <div className='flex flex-wrap items-center justify-between gap-2'>
          <CardTitle>{user.display_name || user.username}</CardTitle>
          {/* The log route stores milliseconds; summary APIs use Unix seconds. */}
          <Button
            variant='outline'
            size='sm'
            nativeButton={false}
            role='link'
            render={
              <Link
                to='/usage-logs/$section'
                params={{ section: 'common' }}
                search={{
                  username: user.username,
                  startTime: props.start * 1000,
                  endTime: props.end * 1000,
                }}
              />
            }
          >
            {t('View user logs')}
          </Button>
        </div>
        <p className='text-muted-foreground text-xs'>
          {user.username} · {t('Group')}: {user.group} · {status}
        </p>
      </CardHeader>
      <CardContent className='space-y-4'>
        <dl className='grid grid-cols-2 gap-3 sm:grid-cols-3'>
          {metrics.map((metric) => (
            <div key={metric.label} className='min-w-0'>
              <dt className='text-muted-foreground text-xs'>{metric.label}</dt>
              <dd className='mt-1 text-base font-semibold break-words tabular-nums'>
                {metric.value}
              </dd>
            </div>
          ))}
        </dl>
        {props.usageState === 'loading' && (
          <p className='text-muted-foreground text-xs'>
            {t('Loading site usage...')}
          </p>
        )}
        {props.usageState === 'ready' && !total && (
          <p className='text-muted-foreground text-xs'>
            {t(
              'No saved usage summary for this period; check usage logs for recent requests.'
            )}
          </p>
        )}
        {props.usageState === 'error' && (
          <p className='text-destructive text-xs'>
            {t('Site usage summary unavailable')}
          </p>
        )}
        <div className='space-y-3 border-t pt-3'>
          <h3 className='text-sm font-medium'>{t('GPT subscription quota')}</h3>
          {deleted ? (
            <p className='text-muted-foreground text-sm'>{t('Deleted')}</p>
          ) : (
            <>
              {quota.isPending && (
                <LoadingState inline message={t('Loading GPT quota...')} />
              )}
              {quota.isError && (
                <ErrorState
                  className='min-h-0 py-2'
                  title={t('GPT quota could not be loaded')}
                  onRetry={() => void quota.refetch()}
                />
              )}
              {!quota.isError && data && !data.bound && (
                <p className='text-muted-foreground text-sm'>
                  {t('No linked quota account')}
                </p>
              )}
              {!quota.isError && data?.bound && (
                <>
                  {(data.stale || data.sync_status !== 'ok') && (
                    <p
                      role='status'
                      className='text-xs text-amber-700 dark:text-amber-400'
                    >
                      {t(
                        'GPT reading may be outdated; awaiting the next successful sync'
                      )}
                    </p>
                  )}
                  <div className='grid gap-3 sm:grid-cols-2'>
                    {data.windows.map((window) => (
                      <WindowReading
                        key={window.id}
                        window={window}
                        now={Date.now()}
                      />
                    ))}
                  </div>
                  <p className='text-muted-foreground text-xs'>
                    {t('GPT official reading: {{time}}', {
                      time: data.observed_at
                        ? new Date(data.observed_at).toLocaleString(locale, {
                            timeZoneName: 'short',
                          })
                        : t('Not available'),
                    })}
                  </p>
                </>
              )}
            </>
          )}
        </div>
      </CardContent>
    </Card>
  )
}
