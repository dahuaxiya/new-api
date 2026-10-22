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
import { Link } from '@tanstack/react-router'
import { Activity, Coins, KeyRound, RefreshCw, Wallet } from 'lucide-react'
import { lazy, Suspense, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { CopyButton } from '@/components/copy-button'
import { SectionPageLayout } from '@/components/layout'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Skeleton } from '@/components/ui/skeleton'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { GptQuotaCard } from '@/features/gpt-quota/GptQuotaCard'
import { getApiKeys } from '@/features/keys/api'
import { getUserLogs } from '@/features/usage-logs/api'
import { usageLogSchema } from '@/features/usage-logs/data/schema'
import { toIntlLocale } from '@/i18n/languages'
import { getSelf } from '@/lib/api'
import { formatNumber, formatQuota } from '@/lib/format'
import { requireServerSuccess } from '@/lib/server-error-message'
import { useAuthStore } from '@/stores/auth-store'

import { getUserQuotaDates } from '../../api'
import { calculateDashboardStats } from '../../lib/stats'
import { StatCard } from '../ui/stat-card'

const ModelCharts = lazy(() =>
  import('../models/model-charts').then((module) => ({
    default: module.ModelCharts,
  }))
)
const ConsumptionChart = lazy(() =>
  import('../models/consumption-distribution-chart').then((module) => ({
    default: module.ConsumptionDistributionChart,
  }))
)

/** 登录账号切换时重建筛选及请求状态，旧账号的结果不得闪现在新账号页面。 */
export function OverviewDashboard() {
  const userId = useAuthStore((state) => state.auth.user?.id)
  const sessionId = useAuthStore((state) => state.auth.session?.sid)
  if (!userId) return null
  return (
    <PersonalDashboard
      key={`${userId}:${sessionId ?? ''}`}
      userId={userId}
      sessionId={sessionId}
    />
  )
}

/** 用户首页始终请求 self 接口；即使当前是管理员，也不混入全站用量。 */
function PersonalDashboard(props: { userId: number; sessionId?: string }) {
  const { t, i18n } = useTranslation()
  const [days, setDays] = useState('7')
  const ranges = [
    { value: '1', label: t('Last 24 hours') },
    { value: '7', label: t('Last 7 days') },
    { value: '30', label: t('Last 30 days') },
  ]
  const [now, setNow] = useState(() => Date.now())
  const identity = [props.userId, props.sessionId]
  const range = useMemo(
    () => ({
      start_timestamp: Math.floor(now / 1000) - Number(days) * 86400,
      end_timestamp: Math.floor(now / 1000),
      default_time: days === '1' ? 'hour' : 'day',
    }),
    [days, now]
  )
  const account = useQuery({
    queryKey: ['portal-account', ...identity],
    queryFn: async () =>
      requireServerSuccess(await getSelf()).data as {
        quota: number
        used_quota: number
        request_count: number
      },
    staleTime: 30_000,
  })
  const keys = useQuery({
    queryKey: ['portal-key-count', ...identity],
    // 仅取一条已脱敏的元数据来获得总数，首页不请求任何明文密钥。
    queryFn: async () =>
      requireServerSuccess(await getApiKeys({ p: 1, size: 1 })).data?.total,
    staleTime: 30_000,
  })
  const usage = useQuery({
    queryKey: ['portal-usage', ...identity, range, now],
    queryFn: async () =>
      requireServerSuccess(await getUserQuotaDates(range, false)).data,
    staleTime: 30_000,
  })
  const recent = useQuery({
    queryKey: ['portal-recent', ...identity, range, now],
    queryFn: async () => {
      const result = requireServerSuccess(
        await getUserLogs({ ...range, p: 1, page_size: 5, type: 2 })
      )
      return usageLogSchema.array().parse(result.data?.items ?? [])
    },
    staleTime: 30_000,
  })
  const stats = calculateDashboardStats(usage.data ?? [])
  const busy =
    account.isFetching ||
    keys.isFetching ||
    usage.isFetching ||
    recent.isFetching
  const baseUrl = `${window.location.origin}/v1`
  const metrics = [
    {
      title: t('Site balance'),
      value: account.data ? formatQuota(account.data.quota) : '—',
      description: t('Separate from GPT subscription quota'),
      icon: Wallet,
      loading: account.isPending,
      error: account.isError,
    },
    {
      title: t('Total usage'),
      value: account.data ? formatQuota(account.data.used_quota) : '—',
      description: t('Cumulative site consumption'),
      icon: Coins,
      loading: account.isPending,
      error: account.isError,
    },
    {
      title: t('Total requests'),
      value: account.data ? formatNumber(account.data.request_count) : '—',
      description: t('Requests through this site'),
      icon: Activity,
      loading: account.isPending,
      error: account.isError,
    },
    {
      title: t('API Keys'),
      value: keys.data === undefined ? '—' : formatNumber(keys.data),
      description: t('Keys owned by this account'),
      icon: KeyRound,
      loading: keys.isPending,
      error: keys.isError,
    },
  ]

  let recentContent
  if (recent.isError) {
    recentContent = (
      <p role='alert' className='text-destructive text-sm'>
        {t('Usage data could not be loaded. Refresh to retry.')}
      </p>
    )
  } else if (recent.isPending) {
    recentContent = <Skeleton className='h-32' />
  } else if (recent.data?.length) {
    recentContent = (
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>{t('Time')}</TableHead>
            <TableHead>{t('Model')}</TableHead>
            <TableHead className='text-right'>Tokens</TableHead>
            <TableHead className='text-right'>
              {t('Site consumption')}
            </TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {recent.data.map((log) => (
            <TableRow key={log.id}>
              <TableCell className='text-muted-foreground whitespace-nowrap'>
                {new Date(log.created_at * 1000).toLocaleString(
                  toIntlLocale(i18n.resolvedLanguage || i18n.language)
                )}
              </TableCell>
              <TableCell>{log.model_name}</TableCell>
              <TableCell className='text-right tabular-nums'>
                {formatNumber(log.prompt_tokens + log.completion_tokens)}
              </TableCell>
              <TableCell className='text-right tabular-nums'>
                {formatQuota(log.quota)}
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    )
  } else {
    recentContent = (
      <p className='text-muted-foreground py-6 text-center text-sm'>
        {t('No requests in this period')}
      </p>
    )
  }

  /** 刷新站内读数只重查本站接口；官方额度由卡片自己的刷新动作负责。 */
  function refreshUsage() {
    // 查询范围按秒发送，缓存键还需保留毫秒刷新标记，保证同一秒内重试也真正重新取数。
    setNow((previous) => Math.max(Date.now(), previous + 1))
    void account.refetch()
    void keys.refetch()
  }

  return (
    <SectionPageLayout>
      <SectionPageLayout.Title>{t('Dashboard')}</SectionPageLayout.Title>
      <SectionPageLayout.Actions>
        <Button variant='outline' size='sm' render={<Link to='/docs' />}>
          {t('User guide')}
        </Button>
      </SectionPageLayout.Actions>
      <SectionPageLayout.Content>
        <div className='mx-auto w-full max-w-7xl space-y-5 pb-6'>
          <div className='grid grid-cols-2 gap-3 lg:grid-cols-4'>
            {metrics.map((metric) => (
              <Card key={metric.title} className='gap-0 py-0 shadow-none'>
                <CardContent className='p-4'>
                  <StatCard {...metric} compactMobile />
                </CardContent>
              </Card>
            ))}
          </div>
          <GptQuotaCard />
          <section className='space-y-4' aria-label={t('Usage overview')}>
            <div className='flex flex-wrap items-center justify-between gap-3'>
              <div>
                <h3 className='text-base font-semibold'>
                  {t('Usage overview')}
                </h3>
                <p className='text-muted-foreground mt-1 text-sm'>
                  {t(
                    'Only includes requests made through this account on this site.'
                  )}
                </p>
              </div>
              <div className='flex items-center gap-2'>
                <Select
                  value={days}
                  items={ranges}
                  onValueChange={(value) => {
                    if (value) {
                      setDays(value)
                      setNow(Date.now())
                    }
                  }}
                >
                  <SelectTrigger aria-label={t('Time range')} className='w-36'>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectGroup>
                      <SelectItem value='1'>{t('Last 24 hours')}</SelectItem>
                      <SelectItem value='7'>{t('Last 7 days')}</SelectItem>
                      <SelectItem value='30'>{t('Last 30 days')}</SelectItem>
                    </SelectGroup>
                  </SelectContent>
                </Select>
                <Button
                  size='sm'
                  variant='outline'
                  disabled={busy}
                  onClick={refreshUsage}
                >
                  <RefreshCw className={busy ? 'animate-spin' : ''} />
                  {t('Refresh usage')}
                </Button>
              </div>
            </div>
            {usage.isError ? (
              <p role='alert' className='text-destructive text-sm'>
                {t('Usage data could not be loaded. Refresh to retry.')}
              </p>
            ) : (
              <>
                <div className='bg-muted/30 grid grid-cols-1 divide-y rounded-xl border sm:grid-cols-3 sm:divide-x sm:divide-y-0'>
                  {[
                    [t('Requests in range'), formatNumber(stats.totalCount)],
                    [
                      t('Site consumption in range'),
                      formatQuota(stats.totalQuota),
                    ],
                    [t('Tokens in range'), formatNumber(stats.totalTokens)],
                  ].map(([label, value]) => (
                    <div key={label} className='p-4'>
                      <p className='text-muted-foreground text-xs'>{label}</p>
                      {usage.isPending ? (
                        <Skeleton className='mt-2 h-7 w-24' />
                      ) : (
                        <p className='mt-2 text-xl font-semibold tabular-nums'>
                          {value}
                        </p>
                      )}
                    </div>
                  ))}
                </div>
                <div className='grid min-w-0 gap-4 xl:grid-cols-2'>
                  <Suspense fallback={<Skeleton className='h-80' />}>
                    <ConsumptionChart
                      data={usage.data ?? []}
                      loading={usage.isPending}
                      timeGranularity={days === '1' ? 'hour' : 'day'}
                      defaultChartType='area'
                    />
                  </Suspense>
                  <Suspense fallback={<Skeleton className='h-80' />}>
                    <ModelCharts
                      data={usage.data ?? []}
                      loading={usage.isPending}
                      timeGranularity={days === '1' ? 'hour' : 'day'}
                      defaultChartTab='proportion'
                    />
                  </Suspense>
                </div>
              </>
            )}
          </section>
          <Card>
            <CardHeader className='flex-row items-center justify-between gap-3'>
              <CardTitle>{t('Recent usage')}</CardTitle>
              <Button
                variant='ghost'
                size='sm'
                // 汇总接口用秒，日志页的 URL 筛选用毫秒，跨页面必须显式转换。
                render={
                  <Link
                    to='/usage-logs/$section'
                    params={{ section: 'common' }}
                    search={{
                      startTime: range.start_timestamp * 1000,
                      endTime: range.end_timestamp * 1000,
                    }}
                  />
                }
              >
                {t('View all')}
              </Button>
            </CardHeader>
            <CardContent>{recentContent}</CardContent>
          </Card>
          <Card>
            <CardHeader>
              <CardTitle>{t('API endpoint')}</CardTitle>
            </CardHeader>
            <CardContent className='flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between'>
              <div className='flex min-w-0 items-center gap-2'>
                <code className='text-sm break-all'>{baseUrl}</code>
                <CopyButton
                  value={baseUrl}
                  aria-label={t('Copy API endpoint')}
                />
              </div>
              <Link
                to='/docs'
                className='text-primary text-sm underline-offset-4 hover:underline'
              >
                {t('Codex setup guide')}
              </Link>
            </CardContent>
          </Card>
        </div>
      </SectionPageLayout.Content>
    </SectionPageLayout>
  )
}
