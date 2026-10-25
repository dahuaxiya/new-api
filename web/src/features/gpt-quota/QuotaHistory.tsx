import { useQuery } from '@tanstack/react-query'
import { lazy, Suspense, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { EmptyState } from '@/components/empty-state'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { api } from '@/lib/api'
import { requireServerSuccess } from '@/lib/server-error-message'

import { quotaHistorySchema } from './history'

const Chart = lazy(() => import('./QuotaHistoryChart'))

/** Fetch only self history; session-scoped caching prevents account-switch flashes. */
export function QuotaHistory(props: {
  seatId?: string
  userId: number
  sessionId: string | undefined
}) {
  const { t } = useTranslation()
  const [hours, setHours] = useState(24)
  const query = useQuery({
    queryKey: [
      'gpt-quota-history',
      props.userId,
      props.sessionId,
      props.seatId,
      hours,
    ],
    queryFn: async ({ signal }) => {
      const response = await api.get('/api/gpt-quota/self/history', {
        signal,
        params: { hours, ...(props.seatId ? { seat_id: props.seatId } : {}) },
      })
      return quotaHistorySchema.parse(requireServerSuccess(response.data).data)
    },
    staleTime: 30_000,
    gcTime: 0,
    refetchInterval: 60_000,
    retry: 1,
    meta: { errorToast: false },
  })
  const points = query.data?.points ?? []
  const hasValues = points.some(
    (row) => row.remaining_5h !== null || row.remaining_7d !== null
  )

  return (
    <section
      className='min-w-0 space-y-3 border-t pt-4'
      aria-label={t('Quota history')}
    >
      <div className='flex flex-wrap items-center justify-between gap-2'>
        <h3 className='text-sm font-medium'>{t('Quota history')}</h3>
        <NativeSelect
          aria-label={t('Quota history range')}
          value={hours}
          onChange={(event) => setHours(Number(event.target.value))}
        >
          <NativeSelectOption value={24}>
            {t('Last 24 hours')}
          </NativeSelectOption>
          <NativeSelectOption value={168}>
            {t('Last 7 days')}
          </NativeSelectOption>
          <NativeSelectOption value={720}>
            {t('Last 30 days')}
          </NativeSelectOption>
        </NativeSelect>
      </div>
      <p className='text-muted-foreground text-xs leading-relaxed'>
        {t(
          'Official remaining percentages, recorded every 5 minutes and retained for 30 days. Gaps and resets are not connected.'
        )}
      </p>
      <div className='flex flex-wrap gap-4 text-xs'>
        <span className='flex items-center gap-1.5'>
          <span className='size-2 rounded-full bg-blue-600' />
          {t('GPT 5-hour window')}
        </span>
        <span className='flex items-center gap-1.5'>
          <span className='size-2 rounded-full bg-teal-600' />
          {t('GPT 7-day window')}
        </span>
      </div>
      {query.isPending && (
        <LoadingState inline message={t('Loading quota history...')} />
      )}
      {query.isError && (
        <ErrorState
          className='min-h-0 py-4'
          title={t('Quota history could not be loaded')}
          description={t('Please retry. Existing API access is unaffected.')}
          onRetry={() => void query.refetch()}
        />
      )}
      {!query.isError && query.data && !hasValues && (
        <EmptyState
          className='min-h-32 py-5'
          title={t('No quota history in this range')}
          description={t(
            'History starts when recording is enabled. Earlier readings cannot be recovered.'
          )}
        />
      )}
      {!query.isError && hasValues && (
        <Suspense
          fallback={
            <LoadingState inline message={t('Loading quota history...')} />
          }
        >
          <Chart points={points} />
        </Suspense>
      )}
      {hasValues && points.length === 1 && (
        <p className='text-muted-foreground text-xs'>
          {t(
            'One reading recorded. A line will appear after more readings arrive.'
          )}
        </p>
      )}
    </section>
  )
}
