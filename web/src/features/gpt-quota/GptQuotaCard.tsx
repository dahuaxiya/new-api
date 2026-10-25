import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { isAxiosError } from 'axios'
import { useTranslation } from 'react-i18next'
import type { z } from 'zod'

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
import { toIntlLocale } from '@/i18n/languages'
import { api } from '@/lib/api'
import { requireServerSuccess } from '@/lib/server-error-message'
import { useAuthStore } from '@/stores/auth-store'

import { WindowReading } from './quota-reading'
import { QuotaHistory } from './QuotaHistory'
import { quotaSchema } from './schema'

/** Fetch the signed-in user's snapshot through the existing session-aware client. */
async function fetchQuota(
  signal: AbortSignal,
  seatId?: string
): Promise<z.infer<typeof quotaSchema>> {
  const response = await api.get('/api/gpt-quota/self', {
    signal,
    params: seatId ? { seat_id: seatId } : undefined,
  })
  const result = requireServerSuccess(response.data)
  return quotaSchema.parse(result.data)
}

/** Compose the native user card with per-session caching and no slot selector. */
export function GptQuotaCard(props: { seatId?: string } = {}) {
  const userId = useAuthStore((state) => state.auth.user?.id)
  const sessionId = useAuthStore((state) => state.auth.session?.sid)
  // Remount mutation state on account/session changes; an old in-flight refresh
  // must never write its response into the next person's query cache.
  if (!userId) return null
  return (
    <SessionQuotaCard
      key={`${userId}:${sessionId ?? ''}:${props.seatId || ''}`}
      seatId={props.seatId}
      userId={userId}
      sessionId={sessionId}
    />
  )
}

/** Keep refresh callbacks and feedback scoped to the session that started them. */
function SessionQuotaCard(props: {
  seatId?: string
  userId: number
  sessionId: string | undefined
}) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const queryClient = useQueryClient()
  const queryKey = ['gpt-quota', props.userId, props.sessionId, props.seatId]
  const refresh = useMutation({
    mutationFn: async () => {
      const response = await api.post(
        '/api/gpt-quota/self/refresh',
        {},
        {
          timeout: 90_000,
          params: props.seatId ? { seat_id: props.seatId } : undefined,
        }
      )
      return quotaSchema.parse(requireServerSuccess(response.data).data)
    },
    // Cancel an older display read so it cannot overwrite the fresh probe result.
    onMutate: () => queryClient.cancelQueries({ queryKey }),
    onSuccess: (value) => {
      queryClient.setQueryData(queryKey, value)
      void queryClient.invalidateQueries({
        queryKey: ['gpt-quota-history', props.userId, props.sessionId],
      })
    },
    retry: false,
    meta: { errorToast: false },
  })
  // Never reuse one person's quota when another account logs in in the same tab.
  const query = useQuery({
    queryKey,
    queryFn: ({ signal }) => fetchQuota(signal, props.seatId),
    enabled: !refresh.isPending,
    staleTime: 15_000,
    gcTime: 0,
    refetchInterval: 30_000,
    retry: 1,
    meta: { errorToast: false },
  })
  const data = query.data
  if (data && !data.bound) return null

  return (
    <Card className='shrink-0' aria-label={t('GPT subscription quota')}>
      <CardHeader>
        <div className='flex flex-wrap items-center justify-between gap-2'>
          <CardTitle>{t('GPT subscription quota')}</CardTitle>
          <Button
            variant='outline'
            size='sm'
            disabled={query.isPending || refresh.isPending}
            onClick={() => refresh.mutate()}
          >
            {refresh.isPending
              ? t('Querying official quota...')
              : t('Refresh official quota')}
          </Button>
        </div>
        <CardDescription>
          {t(
            'GPT quota belongs to your linked account; it is separate from the site balance.'
          )}
        </CardDescription>
      </CardHeader>
      <CardContent className='space-y-3'>
        {refresh.isError && (
          <p role='alert' className='text-destructive text-sm'>
            {isAxiosError(refresh.error) &&
            refresh.error.response?.status === 429
              ? t('Please wait 30 seconds before refreshing quota again')
              : t(
                  'Official quota refresh failed. Previous readings are retained; please try again later.'
                )}
          </p>
        )}
        {refresh.isSuccess && (
          <p role='status' className='text-muted-foreground text-sm'>
            {t('Official quota updated')}
          </p>
        )}
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
        {!query.isPending && (
          <QuotaHistory
            seatId={props.seatId}
            userId={props.userId}
            sessionId={props.sessionId}
          />
        )}
      </CardContent>
    </Card>
  )
}
