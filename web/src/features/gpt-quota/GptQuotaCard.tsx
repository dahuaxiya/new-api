import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { z } from 'zod'

import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { Progress } from '@/components/ui/progress'
import { toIntlLocale } from '@/i18n/languages'
import { api } from '@/lib/api'
import { formatNumber } from '@/lib/format'
import { requireServerSuccess } from '@/lib/server-error-message'
import { useAuthStore } from '@/stores/auth-store'

const windowSchema = z.object({
  id: z.enum(['5h', '7d']),
  used_percent: z.number().min(0).max(100).nullable(),
  remaining_percent: z.number().min(0).max(100).nullable(),
  reset_at: z.string().datetime({ offset: true }).nullable(),
})
const quotaSchema = z.discriminatedUnion('bound', [
  z.object({ bound: z.literal(false) }),
  z.object({
    bound: z.literal(true),
    account_label: z.string(),
    plan: z.string().nullable(),
    windows: z.array(windowSchema).length(2),
    observed_at: z.string().datetime({ offset: true }).nullable(),
    last_checked_at: z.string().datetime({ offset: true }).nullable(),
    sync_status: z.enum(['ok', 'pending', 'error']),
    stale: z.boolean(),
    poll_seconds: z.number(),
  }),
])
type QuotaWindow = z.infer<typeof windowSchema>

/** Fetch the signed-in user's snapshot through the existing session-aware client. */
async function fetchQuota(
  signal: AbortSignal
): Promise<z.infer<typeof quotaSchema>> {
  const response = await api.get('/api/gpt-quota/self', { signal })
  const result = requireServerSuccess(response.data)
  return quotaSchema.parse(result.data)
}

/** Render an official window; expired observations never imply a fresh full quota. */
function WindowReading(props: { window: QuotaWindow; now: number }) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const window = props.window
  const expired =
    window.reset_at !== null && Date.parse(window.reset_at) <= props.now
  const known = window.used_percent !== null && !expired
  const label =
    window.id === '5h' ? t('GPT 5-hour window') : t('GPT 7-day window')

  return (
    <section
      className='bg-muted/30 min-w-0 rounded-xl border p-4'
      aria-label={label}
    >
      <h3 className='mb-2 text-sm font-medium'>{label}</h3>
      {known ? (
        <>
          <p className='mb-2 text-2xl font-semibold tabular-nums'>
            {t('GPT remaining {{percent}}%', {
              percent: formatNumber(window.remaining_percent, locale),
            })}
          </p>
          <Progress
            value={window.used_percent}
            aria-label={t('GPT used {{percent}}%', {
              percent: formatNumber(window.used_percent, locale),
            })}
          />
          <p className='text-muted-foreground mt-2 text-xs'>
            {t('GPT used {{percent}}%', {
              percent: formatNumber(window.used_percent, locale),
            })}
          </p>
        </>
      ) : (
        <p className='py-2 text-base font-medium'>
          {expired
            ? t('GPT window expired; awaiting a fresh reading')
            : t('GPT upstream has not returned this window')}
        </p>
      )}
      <p className='text-muted-foreground mt-3 text-xs leading-relaxed'>
        {window.reset_at
          ? t('GPT resets at {{time}}', {
              time: new Date(window.reset_at).toLocaleString(locale, {
                timeZoneName: 'short',
              }),
            })
          : t('GPT reset time unavailable')}
      </p>
    </section>
  )
}

/** Compose the native user card with per-session caching and no slot selector. */
export function GptQuotaCard() {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const userId = useAuthStore((state) => state.auth.user?.id)
  const sessionId = useAuthStore((state) => state.auth.session?.sid)
  // Never reuse one person's quota when another account logs in in the same tab.
  const query = useQuery({
    queryKey: ['gpt-quota', userId, sessionId],
    queryFn: ({ signal }) => fetchQuota(signal),
    enabled: Boolean(userId),
    staleTime: 0,
    gcTime: 0,
    refetchInterval: 30_000,
    retry: 1,
    meta: { errorToast: false },
  })
  const data = query.data
  if (!userId || (data && !data.bound)) return null

  return (
    <Card className='shrink-0' aria-label={t('GPT subscription quota')}>
      <CardHeader>
        <div className='flex flex-wrap items-center justify-between gap-2'>
          <CardTitle>{t('GPT subscription quota')}</CardTitle>
          <Button
            variant='outline'
            size='sm'
            disabled={query.isFetching}
            onClick={() => void query.refetch()}
          >
            {t('Refresh quota display')}
          </Button>
        </div>
        <CardDescription>
          {t(
            'GPT quota belongs to your linked account; it is separate from the site balance.'
          )}
        </CardDescription>
      </CardHeader>
      <CardContent className='space-y-3'>
        {query.isPending && (
          <LoadingState inline message={t('Loading GPT quota...')} />
        )}
        {query.isError && (
          <ErrorState
            className='min-h-0 py-3'
            title={t('GPT quota could not be loaded')}
            description={t('Please retry. Existing API access is unaffected.')}
            onRetry={() => void query.refetch()}
          />
        )}
        {!query.isError && data?.bound && (
          <>
            <p className='text-sm font-medium'>
              {data.account_label}
              {data.plan ? ` · ${data.plan.toUpperCase()}` : ''}
            </p>
            {(data.stale || data.sync_status !== 'ok') && (
              <p role='status' className='text-muted-foreground text-sm'>
                {data.sync_status === 'pending'
                  ? t('GPT initial synchronization is in progress')
                  : t(
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
            <p className='text-muted-foreground text-xs leading-relaxed'>
              {t('GPT official reading: {{time}}', {
                time: data.observed_at
                  ? new Date(data.observed_at).toLocaleString(locale, {
                      timeZoneName: 'short',
                    })
                  : t('Not available'),
              })}
              {' · '}
              {t(
                'GPT syncs every 5 minutes; displayed times use your local timezone.'
              )}
            </p>
            <a
              className='text-muted-foreground text-xs underline'
              href='/quota-source.tar.gz'
            >
              {t('Quota integration source code')}
            </a>
          </>
        )}
      </CardContent>
    </Card>
  )
}
