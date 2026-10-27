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
import { useTranslation } from 'react-i18next'

import { LongText } from '@/components/long-text'
import { StatusBadge } from '@/components/status-badge'
import { Progress } from '@/components/ui/progress'
import { HOSTING_PERMANENT_END } from '@/features/gpt-hosting/api'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber } from '@/lib/format'

import { hostingStatusLabels, type HostingSummary } from '../hosting-api'

/** 管理员只读展示账号所绑定的槽位及配置出口；未知读取不能被解释为未配置或直连。 */
export function HostingConnection(props: {
  summary: Pick<
    HostingSummary,
    'instance_id' | 'instance_name' | 'slot_id' | 'connection'
  >
  detailed?: boolean
}) {
  const { t } = useTranslation()
  const value = props.summary
  if (!value.slot_id) {
    return (
      <p className='text-muted-foreground text-xs'>{t('Slot not assigned')}</p>
    )
  }
  const connection = value.connection
  let exit = t('Connection details unavailable')
  if (connection?.status === 'unconfigured') {
    exit = t('No outbound connection configured')
  }
  if (connection?.status === 'missing') exit = t('Bound slot not found')
  if (
    connection?.status === 'configured' ||
    connection?.status === 'disabled'
  ) {
    exit = connection.exit_name || connection.exit_id || t('Unknown')
    if (connection.exit_type) exit += ` · ${connection.exit_type.toUpperCase()}`
  }
  return (
    <dl className='grid min-w-0 gap-1 text-xs'>
      <div className='grid min-w-0 grid-cols-[auto_minmax(0,1fr)] gap-x-2'>
        <dt className='text-muted-foreground'>{t('Slot')}</dt>
        <dd className='min-w-0'>
          <LongText className='max-w-72'>
            {value.instance_name || value.instance_id} /{' '}
            {connection?.slot_name || value.slot_id}
          </LongText>
          {(props.detailed ||
            (connection?.slot_name &&
              connection.slot_name !== value.slot_id)) && (
            <LongText className='text-muted-foreground max-w-72 font-mono'>
              {value.slot_id}
            </LongText>
          )}
        </dd>
      </div>
      <div className='grid min-w-0 grid-cols-[auto_minmax(0,1fr)] gap-x-2'>
        <dt className='text-muted-foreground'>{t('Outbound connection')}</dt>
        <dd className='min-w-0'>
          <LongText className='max-w-72'>{exit}</LongText>
          {props.detailed &&
            connection?.exit_id &&
            connection.exit_id !== connection.exit_name && (
              <LongText className='text-muted-foreground max-w-72 font-mono'>
                {connection.exit_id}
              </LongText>
            )}
          {connection?.status === 'disabled' && (
            <p className='text-muted-foreground'>
              {t('Outbound connection disabled')}
            </p>
          )}
        </dd>
      </div>
    </dl>
  )
}

/** 在用户行分别显示账号、期限和官方读数；过期读数不得被解释为新额度。 */
export function HostingCell(props: {
  summary?: HostingSummary
  kind: 'account' | 'term' | 'quota'
}) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const v = props.summary
  if (!v) {
    return (
      <span className='text-muted-foreground text-xs'>
        {t('Hosting status unavailable')}
      </span>
    )
  }
  const reading = v.quota?.bound ? v.quota : undefined
  if (props.kind === 'term') {
    return (
      <div className='space-y-1 text-xs'>
        <StatusBadge
          copyable={false}
          variant={v.status === 'active' ? 'success' : 'neutral'}
          className={v.status === 'undated' ? 'users-undated-badge' : undefined}
          label={t(
            hostingStatusLabels[v.status] || 'Hosting status unavailable'
          )}
        />
        {v.grant && (
          <p className='text-muted-foreground'>
            {`${v.bound_seats ?? (v.slot_id ? 1 : 0)}/${v.seat_count ?? 1}`} ·{' '}
            {v.grant.ends_at === HOSTING_PERMANENT_END
              ? t('Permanent hosting')
              : new Date(v.grant.ends_at * 1000).toLocaleDateString(locale)}
          </p>
        )}
        {v.status === 'undated' && (
          <p className='text-muted-foreground'>
            {t('Existing service continues')}
          </p>
        )}
      </div>
    )
  }
  if (props.kind === 'account') {
    if (v.status === 'restricted' || (!v.slot_id && !v.seats?.length)) {
      return <span className='text-muted-foreground text-xs'>—</span>
    }
    // 每个名额有独立绑定；多账号不能沿用首名额的实例、出口或官方读数。
    const accounts = v.seats && v.seats.length > 1 ? v.seats : [v]
    return (
      <ul className='grid max-w-80 min-w-0 gap-3 text-sm sm:min-w-52'>
        {accounts.map((account, index) => {
          const label =
            account.binding?.account_label ||
            (accounts.length === 1 ? reading?.account_label : undefined) ||
            t('Awaiting account sign-in')
          const plan =
            account.binding?.plan ||
            (accounts.length === 1 ? reading?.plan : undefined)
          return (
            <li
              key={account.grant?.seat_id || index}
              aria-label={label}
              className='grid min-w-0 gap-1.5'
            >
              <div className='flex min-w-0 flex-wrap items-center gap-x-2'>
                <LongText className='max-w-56 font-medium'>{label}</LongText>
                <span className='text-muted-foreground text-xs'>
                  {[
                    account.binding?.platform === 'claude' ? 'Claude' : 'GPT',
                    plan,
                  ]
                    .filter(Boolean)
                    .join(' · ')}
                </span>
              </div>
              <HostingConnection summary={account} />
            </li>
          )
        })}
      </ul>
    )
  }
  const quota = v.quota
  if (!quota?.bound) {
    return (
      <span className='text-muted-foreground text-xs'>
        {v.quota_unavailable ? t('GPT quota could not be loaded') : '—'}
      </span>
    )
  }
  return (
    <div className='space-y-1 text-xs tabular-nums'>
      {quota.windows.map((w) => {
        const valid =
          w.remaining_percent !== null &&
          (!w.reset_at || Date.parse(w.reset_at) > Date.now())
        return (
          <div
            className='users-quota-row'
            key={w.id}
            title={
              w.reset_at
                ? new Date(w.reset_at).toLocaleString(locale)
                : t('GPT reset time unavailable')
            }
          >
            <span className='text-muted-foreground'>{w.id}</span>
            {/* 过期或未返回的额度不画进度条，避免把未知读数误当可用容量。 */}
            {valid && (
              <Progress
                value={w.remaining_percent}
                aria-label={`${w.id} ${t('GPT remaining {{percent}}%', { percent: formatNumber(w.remaining_percent, locale) })}`}
              />
            )}
            <span className='text-right'>
              {valid
                ? `${formatNumber(w.remaining_percent, locale)}%`
                : t('Unknown')}
            </span>
          </div>
        )
      })}
      {quota.stale && (
        <p className='text-muted-foreground'>
          {t('Saved reading may be outdated')}
        </p>
      )}
    </div>
  )
}
