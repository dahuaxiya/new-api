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
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Field, FieldLabel } from '@/components/ui/field'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { toIntlLocale } from '@/i18n/languages'
import { useAuthStore } from '@/stores/auth-store'

import { getHosting, HOSTING_PERMANENT_END, type HostingSelf } from './api'
import { HostingStore } from './store'

/** 账单入口沿用当前登录会话；换用户或链接目标时重建选择状态，避免沿用其他账号的名额。 */
export function HostingBilling(props: { seatId?: string }): React.JSX.Element {
  const user = useAuthStore((s) => s.auth.user)
  const session = useAuthStore((s) => s.auth.session?.sid)
  const identity = `${user?.id}:${session}`
  return (
    <BillingSession
      key={`${identity}:${props.seatId || ''}`}
      identity={identity}
      initialSeatId={props.seatId}
    />
  )
}

/** 从本人名额列表选择续费对象；显式目标查询失败时不回退到首名额，防止给错账号续费。 */
function BillingSession(props: {
  identity: string
  initialSeatId?: string
}): React.JSX.Element {
  const { t, i18n } = useTranslation()
  const [seatId, setSeatId] = useState(props.initialSeatId || '')
  const query = useQuery({
    queryKey: ['gpt-hosting', 'self', props.identity, seatId],
    queryFn: ({ signal }) => getHosting<HostingSelf>('self', signal, seatId),
    refetchInterval: 15_000,
  })
  if (query.isPending) return <LoadingState inline />
  if (query.isError) return <ErrorState onRetry={() => query.refetch()} />
  const data = query.data
  return (
    <div className='space-y-4'>
      {!!data.seats?.length && (
        <Field>
          <FieldLabel htmlFor='billing-hosting-seat'>
            {t('Hosting seat')}
          </FieldLabel>
          <NativeSelect
            id='billing-hosting-seat'
            value={data.grant?.seat_id || ''}
            onChange={(event) => setSeatId(event.target.value)}
          >
            {data.seats.map((seat, index) => (
              <NativeSelectOption
                key={seat.grant?.seat_id}
                value={seat.grant?.seat_id}
              >
                {t('Seat {{number}}', { number: index + 1 })} ·{' '}
                {seat.binding?.account_label || t('Not connected')} ·{' '}
                {seat.binding?.platform === 'claude' ? 'Claude' : 'GPT'}
              </NativeSelectOption>
            ))}
          </NativeSelect>
          {data.grant && (
            <p className='text-muted-foreground text-sm'>
              {t('Expires')}:{' '}
              {data.grant.ends_at === HOSTING_PERMANENT_END
                ? t('Permanent hosting')
                : new Date(data.grant.ends_at * 1000).toLocaleString(
                    toIntlLocale(i18n.resolvedLanguage || i18n.language)
                  )}
            </p>
          )}
        </Field>
      )}
      {/* 切换名额时卸载旧支付弹窗，不能把已打开的续费表单转给另一个账号。 */}
      <HostingStore
        key={data.grant?.seat_id || 'new'}
        identity={props.identity}
        seat={data}
        platform={data.binding?.platform || 'codex'}
      />
    </div>
  )
}
