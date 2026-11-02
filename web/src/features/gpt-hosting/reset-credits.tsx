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
import { useMutation, useQuery } from '@tanstack/react-query'
import { RefreshCw } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import {
  Popover,
  PopoverContent,
  PopoverTitle,
  PopoverTrigger,
} from '@/components/ui/popover'
import {
  SecureVerificationDialog,
  useSecureVerification,
} from '@/features/auth/secure-verification'
import { toIntlLocale } from '@/i18n/languages'

import { postHosting, type HostingResetCredits as ResetCredits } from './api'

/**
 * 展示当前名额的官方重置次数；切换名额时由调用方卸载，禁止复用验证状态。
 * @param props 会话标识、名额编号和服务端返回的绑定编号。
 * @return 当前账号的查询及使用界面。
 */
export function HostingResetCredits({
  identity,
  seatId,
  bindingId,
}: {
  identity: string
  seatId: string
  bindingId: string
}): React.JSX.Element {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const verification = useSecureVerification()
  const [confirm, setConfirm] = useState(false)
  const [outcome, setOutcome] = useState<'reset' | 'not_used' | 'unknown'>()
  const mounted = useRef(true)
  useEffect(() => {
    mounted.current = true
    return () => {
      mounted.current = false
    }
  }, [])
  const query = useQuery({
    queryKey: ['gpt-hosting', 'reset-credits', identity, seatId, bindingId],
    queryFn: () =>
      postHosting<ResetCredits>('reset-credits/query', {}, undefined, seatId),
    retry: false,
    staleTime: 30_000,
  })
  const useCredit = useMutation({
    mutationFn: async () => {
      const verified = await verification.requestVerification({
        scope: 'gpt.hosting.manage',
        context: { action: 'reset', binding_id: bindingId },
      })
      // 用户切换名额后，旧验证不能继续提交到已离开的账号。
      if (!verified || !mounted.current) return undefined
      try {
        return await postHosting<ResetCredits>(
          'reset-credits/use',
          {},
          verified.proof_token,
          seatId
        )
      } catch {
        // 浏览器超时也可能发生在服务端消费之后，不能提示失败并允许立即重试。
        return { outcome: 'unknown' as const }
      }
    },
    retry: false,
    gcTime: 0,
    onSuccess: async (result) => {
      if (!result || !mounted.current) return
      setOutcome(result.outcome || 'unknown')
      await query.refetch()
    },
  })
  const blockedUntil = query.data?.blocked_until || 0
  const blocked = blockedUntil * 1000 > Date.now()
  const unknownWithoutFreshStatus =
    outcome === 'unknown' && (query.isError || query.isFetching)
  const formattedBlock = blocked
    ? new Date(blockedUntil * 1000).toLocaleString(locale)
    : ''
  // 官方卡片没有公开稳定编号；合并相同展示字段，避免列表刷新时依赖数组下标。
  const groups = new Map<
    string,
    { item: ResetCredits['items'][number]; count: number }
  >()
  for (const item of query.data?.items || []) {
    const key = JSON.stringify([
      item.label,
      item.starts_at,
      item.expires_at,
      item.resets_left,
      item.redeemable,
    ])
    const group = groups.get(key)
    if (group) group.count++
    else groups.set(key, { item, count: 1 })
  }
  return (
    <section
      aria-label={t('Reset Credits')}
      className='space-y-2 border-t pt-3'
    >
      <div className='flex flex-wrap items-center justify-between gap-x-4 gap-y-2'>
        <div className='flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1'>
          <h2 className='text-sm font-medium'>{t('Reset Credits')}</h2>
          {query.data && !query.isError && (
            <>
              <p className='text-muted-foreground text-sm'>
                {t('Available')}:{' '}
                <span className='text-foreground font-medium tabular-nums'>
                  {query.data.available_count}
                </span>
              </p>
              {query.data.items.length > 0 && (
                <Popover>
                  <PopoverTrigger render={<Button variant='ghost' size='xs' />}>
                    {t('Details')}
                  </PopoverTrigger>
                  <PopoverContent
                    align='start'
                    className='max-h-64 w-80 max-w-[calc(100vw-2rem)] overflow-y-auto p-3'
                  >
                    <PopoverTitle>{t('Reset Credits')}</PopoverTitle>
                    <ul className='text-muted-foreground space-y-2 text-sm'>
                      {[...groups.entries()].map(([key, { item, count }]) => (
                        <li key={key}>
                          <p className='text-foreground font-medium'>
                            {item.label || t('Reset Credits')}
                            {' · '}
                            {item.resets_left * count}
                            {' · '}
                            {item.redeemable
                              ? t('Available')
                              : t('Not available')}
                          </p>
                          {item.starts_at && (
                            <p>
                              {t('Starts at')}:{' '}
                              {new Date(item.starts_at).toLocaleString(locale)}
                            </p>
                          )}
                          {item.expires_at && (
                            <p>
                              {t('Expires at')}:{' '}
                              {new Date(item.expires_at).toLocaleString(locale)}
                            </p>
                          )}
                        </li>
                      ))}
                    </ul>
                  </PopoverContent>
                </Popover>
              )}
            </>
          )}
        </div>
        <div className='flex items-center gap-1'>
          {query.data && !query.isError && (
            <Button
              variant='outline'
              size='sm'
              disabled={
                query.data.available_count <= 0 ||
                blocked ||
                unknownWithoutFreshStatus ||
                useCredit.isPending ||
                query.isFetching
              }
              onClick={() => setConfirm(true)}
            >
              {t('Use reset credit')}
            </Button>
          )}
          <Button
            variant='ghost'
            size='icon-sm'
            aria-label={t('Refresh')}
            disabled={query.isFetching || useCredit.isPending}
            onClick={() => query.refetch()}
          >
            <RefreshCw
              aria-hidden='true'
              className={
                query.isFetching
                  ? 'animate-spin motion-reduce:animate-none'
                  : undefined
              }
            />
          </Button>
        </div>
      </div>
      {query.isPending && (
        <p role='status' className='text-muted-foreground text-sm'>
          {t('Loading...')}
        </p>
      )}
      {query.isError && (
        <p role='alert' className='text-destructive text-sm'>
          {query.error.message}
        </p>
      )}
      {(blocked || outcome === 'unknown') && (
        <Alert>
          <AlertDescription>
            {blocked
              ? t(
                  'The previous reset could not be confirmed. Try again after {{time}}.',
                  { time: formattedBlock }
                )
              : t(
                  'The reset result could not be confirmed. Refresh the status before trying again.'
                )}
          </AlertDescription>
        </Alert>
      )}
      {outcome === 'reset' && (
        <p role='status' className='text-sm'>
          {t('Reset credit used successfully.')}
        </p>
      )}
      {outcome === 'not_used' && (
        <p role='status' className='text-sm'>
          {t('No reset credit was used. Check the current account status.')}
        </p>
      )}
      <ConfirmDialog
        open={confirm}
        onOpenChange={setConfirm}
        title={t('Use reset credit')}
        desc={t(
          'Use one official reset credit for this account? Used reset credits cannot be restored.'
        )}
        confirmText={t('Use reset credit')}
        handleConfirm={() => {
          setConfirm(false)
          useCredit.mutate()
        }}
      />
      <SecureVerificationDialog {...verification.dialogProps} />
    </section>
  )
}
