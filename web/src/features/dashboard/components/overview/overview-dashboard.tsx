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
import { Activity, ArrowRight, RefreshCw } from 'lucide-react'
import { lazy, Suspense, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { CopyButton } from '@/components/copy-button'
import { EmptyState } from '@/components/empty-state'
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
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { GptQuotaCard } from '@/features/gpt-quota/GptQuotaCard'
import { getApiKeys } from '@/features/keys/api'
import { getUserLogs } from '@/features/usage-logs/api'
import { usageLogSchema } from '@/features/usage-logs/data/schema'
import { useSidebarConfig } from '@/hooks/use-sidebar-config'
import { useStatus } from '@/hooks/use-status'
import { toIntlLocale } from '@/i18n/languages'
import { getSelf } from '@/lib/api'
import { formatNumber, formatQuota } from '@/lib/format'
import { requireServerSuccess } from '@/lib/server-error-message'
import { getSiteProfile } from '@/lib/site-profile'
import { useAuthStore } from '@/stores/auth-store'

import { getUserQuotaDates } from '../../api'
import { calculateDashboardStats } from '../../lib/stats'

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

/**
 * 登录账号切换时重建筛选及请求状态，旧账号的结果不得闪现在新账号页面。
 * @return 当前登录账号的个人概览；无账号时不发起请求。
 */
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

/**
 * 两站复用个人数据和图表，仅按站点配置调整入口与托管额度的位置。
 * @param props 当前用户和会话标识；父组件必须在标识变化时重建组件，防止旧数据闪现。
 * @return 只查询 self 接口的用户首页，管理员也不会看到全站数据。
 */
function PersonalDashboard(props: { userId: number; sessionId?: string }) {
  const { t, i18n } = useTranslation()
  const { status } = useStatus()
  const profile = getSiteProfile(status?.site_mode)
  // 首页不是绕过菜单配置的第二入口；后台关闭或用户隐藏的功能在这里同样隐藏。
  const actionGroups = useSidebarConfig([
    {
      title: '',
      items: profile.navigation.map((item) => ({
        title: t(item.titleKey),
        url: item.url,
      })),
    },
  ])
  const actions = profile.navigation.filter((item) =>
    actionGroups.some((group) =>
      group.items.some((link) => link.url === item.url)
    )
  )
  const username = useAuthStore((state) => state.auth.user?.username)
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
  // 聚合接口可能返回零值时间桶；只有三个统计量均为零，才显示空状态。
  // 错误与加载在渲染分支中先处理，不把未取得的数据当作“没有用量”。
  const hasUsage =
    stats.totalCount > 0 || stats.totalTokens > 0 || stats.totalQuota > 0
  const walletAction = actions.find((item) => item.url === '/wallet')
  const connectionActions = actions.filter((item) => item.url !== '/wallet')
  // 汇总与日志均明确为空时只留一个空状态；汇总延迟或任一请求失败不能掩盖已有日志。
  const hideEmptyRecent =
    usage.isSuccess && !hasUsage && recent.isSuccess && recent.data.length === 0

  let keyCountContent
  if (keys.isPending) {
    keyCountContent = <Skeleton className='h-4 w-16' />
  } else if (keys.isError) {
    keyCountContent = t('Failed to load')
  } else {
    keyCountContent = t('API keys: {{value}}', {
      value: formatNumber(keys.data ?? 0),
    })
  }

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
    <SectionPageLayout
      className='luobo-workspace'
      description={status ? t(profile.descriptionKey) : undefined}
    >
      <SectionPageLayout.Title>
        {status ? t(profile.titleKey) : t('Dashboard')}
      </SectionPageLayout.Title>
      <SectionPageLayout.Content>
        <div className='luobo-overview mx-auto w-full max-w-7xl space-y-5 pb-6'>
          {/* 托管用户优先看订阅额度；状态尚未返回时不按默认模式发起托管请求。 */}
          {status && profile.showHostedQuota && <GptQuotaCard />}
          <nav
            aria-label={t('Quick access')}
            className='grid gap-4 lg:grid-cols-[0.85fr_1.15fr]'
          >
            <section
              aria-label={t('Site balance')}
              className='luobo-account-panel rounded-2xl border p-5 sm:p-6'
            >
              <div className='flex items-center justify-between gap-3'>
                <h3 className='text-sm font-medium'>{t('Site balance')}</h3>
                {status && walletAction && (
                  <Button
                    variant='ghost'
                    size='sm'
                    render={<Link to={walletAction.url} />}
                  >
                    {t(walletAction.titleKey)}
                    <ArrowRight aria-hidden='true' className='size-3.5' />
                  </Button>
                )}
              </div>
              {account.isPending ? (
                <Skeleton className='my-4 h-10 w-36' />
              ) : (
                <p className='my-3 text-4xl font-medium tracking-tight tabular-nums'>
                  {account.isError ? '—' : formatQuota(account.data.quota)}
                </p>
              )}
              <p
                role={account.isError ? 'alert' : undefined}
                className='text-muted-foreground text-xs leading-relaxed'
              >
                {account.isError
                  ? t('Failed to load')
                  : t(profile.balanceDescriptionKey)}
              </p>
              <dl className='mt-4 flex flex-wrap gap-x-5 gap-y-2 border-t pt-3'>
                {[
                  [
                    t('Total usage'),
                    account.data && !account.isError
                      ? formatQuota(account.data.used_quota)
                      : '—',
                  ],
                  [
                    t('Total requests'),
                    account.data && !account.isError
                      ? formatNumber(account.data.request_count)
                      : '—',
                  ],
                ].map(([label, value]) => (
                  <div key={label} className='flex items-baseline gap-2'>
                    <dt className='text-muted-foreground text-xs'>{label}</dt>
                    <dd className='text-sm font-medium tabular-nums'>
                      {account.isPending ? (
                        <Skeleton className='h-5 w-16' />
                      ) : (
                        value
                      )}
                    </dd>
                  </div>
                ))}
              </dl>
            </section>
            <section
              aria-label={t('API connection')}
              className='bg-card flex min-w-0 flex-col rounded-2xl border p-5 sm:p-6'
            >
              <div className='flex items-center justify-between gap-3'>
                <h3 className='text-sm font-medium'>{t('API connection')}</h3>
                <Link
                  to='/docs'
                  className='text-muted-foreground hover:text-foreground inline-flex min-h-9 items-center gap-1 text-xs underline-offset-4 hover:underline'
                >
                  {t(profile.guideTitleKey)}{' '}
                  <ArrowRight aria-hidden='true' className='size-3.5' />
                </Link>
              </div>
              <div className='mt-3'>
                <p className='text-muted-foreground mb-2 text-xs'>
                  {t('API endpoint')}
                </p>
                <div className='bg-muted/40 flex min-w-0 items-center justify-between gap-2 rounded-lg border px-3 py-2'>
                  <code className='min-w-0 text-sm break-all'>{baseUrl}</code>
                  <CopyButton
                    value={baseUrl}
                    aria-label={t('Copy API endpoint')}
                  />
                </div>
              </div>
              <div className='mt-auto flex flex-wrap items-center justify-between gap-3 pt-5'>
                <div className='flex flex-wrap gap-2'>
                  {status &&
                    connectionActions.map((item, index) => (
                      <Button
                        key={item.url}
                        size='sm'
                        variant={index === 0 ? 'default' : 'outline'}
                        render={<Link to={item.url} />}
                      >
                        <item.icon aria-hidden='true' />
                        {t(item.titleKey)}
                      </Button>
                    ))}
                </div>
                <p className='text-muted-foreground text-xs tabular-nums'>
                  {keyCountContent}
                </p>
              </div>
            </section>
          </nav>
          <section
            className='bg-card overflow-hidden rounded-2xl border'
            aria-label={t('Usage overview')}
            aria-busy={usage.isFetching}
          >
            <div className='flex flex-wrap items-center justify-between gap-3 p-5 sm:px-6'>
              <div>
                <h3 className='text-sm font-semibold'>{t('Usage overview')}</h3>
                <p className='text-muted-foreground mt-1 text-xs'>
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
                  <RefreshCw
                    aria-hidden='true'
                    className={busy ? 'motion-safe:animate-spin' : ''}
                  />
                  {t('Refresh usage')}
                </Button>
              </div>
            </div>
            {usage.isError ? (
              <p role='alert' className='text-destructive px-6 pb-6 text-sm'>
                {t('Usage data could not be loaded. Refresh to retry.')}
              </p>
            ) : (
              <>
                <dl className='grid grid-cols-3 gap-3 border-y px-5 py-4 sm:px-6'>
                  {[
                    [t('Requests in range'), formatNumber(stats.totalCount)],
                    [
                      t('Site consumption in range'),
                      formatQuota(stats.totalQuota),
                    ],
                    [t('Tokens in range'), formatNumber(stats.totalTokens)],
                  ].map(([label, value]) => (
                    <div key={label}>
                      <dt className='text-muted-foreground text-xs leading-relaxed'>
                        {label}
                      </dt>
                      <dd>
                        {usage.isPending ? (
                          <Skeleton className='mt-2 h-7 w-24' />
                        ) : (
                          <p className='mt-2 text-xl font-medium tabular-nums sm:text-2xl'>
                            {value}
                          </p>
                        )}
                      </dd>
                    </div>
                  ))}
                </dl>
                {usage.isPending && (
                  <div className='p-6'>
                    <Skeleton className='h-40 w-full' />
                  </div>
                )}
                {!usage.isPending && !hasUsage && (
                  <EmptyState
                    icon={Activity}
                    title={t('No requests in this period')}
                    description={t(
                      'Use the endpoint above and an API key to send a request.'
                    )}
                    className='min-h-44 py-8 [&_[data-slot=empty-media]]:mb-2'
                  />
                )}
                {!usage.isPending && hasUsage && (
                  <Tabs defaultValue='consumption' className='gap-0 p-3 sm:p-4'>
                    <TabsList
                      aria-label={t('Usage charts')}
                      variant='line'
                      className='mx-2 mb-2 h-10 gap-4'
                    >
                      <TabsTrigger value='consumption'>
                        {t('Quota Distribution')}
                      </TabsTrigger>
                      <TabsTrigger value='models'>
                        {t('Model Call Analytics')}
                      </TabsTrigger>
                    </TabsList>
                    <TabsContent value='consumption'>
                      <Suspense fallback={<Skeleton className='h-72' />}>
                        <ConsumptionChart
                          data={usage.data ?? []}
                          timeGranularity={days === '1' ? 'hour' : 'day'}
                          defaultChartType='area'
                          embedded
                        />
                      </Suspense>
                    </TabsContent>
                    <TabsContent value='models'>
                      <Suspense fallback={<Skeleton className='h-72' />}>
                        <ModelCharts
                          data={usage.data ?? []}
                          timeGranularity={days === '1' ? 'hour' : 'day'}
                          defaultChartTab='proportion'
                          embedded
                        />
                      </Suspense>
                    </TabsContent>
                  </Tabs>
                )}
              </>
            )}
          </section>
          {!hideEmptyRecent && (
            <Card className='gap-4 rounded-2xl shadow-none'>
              <CardHeader className='flex-row items-center justify-between gap-3'>
                <CardTitle>{t('Recent usage')}</CardTitle>
                <Button
                  variant='ghost'
                  size='sm'
                  // 日志筛选使用毫秒且管理员默认查看全站；同时保留当前账号和时间范围。
                  render={
                    <Link
                      to='/usage-logs/$section'
                      params={{ section: 'common' }}
                      search={{
                        username,
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
          )}
        </div>
      </SectionPageLayout.Content>
    </SectionPageLayout>
  )
}
