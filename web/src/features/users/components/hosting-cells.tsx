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

import { StatusBadge } from '@/components/status-badge'
import { Progress } from '@/components/ui/progress'
import { HOSTING_PERMANENT_END } from '@/features/gpt-hosting/api'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber } from '@/lib/format'

import { hostingStatusLabels, type HostingSummary } from '../hosting-api'

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
    return (
      <div className='max-w-56 space-y-1 text-sm'>
        <p className='truncate'>
          {v.seats && v.seats.length > 1
            ? t('{{count}} hosted accounts', { count: v.bound_seats || 0 })
            : v.binding?.account_label || reading?.account_label || '—'}
        </p>
        <p className='text-muted-foreground truncate text-xs'>
          {[v.binding?.plan || reading?.plan, v.instance_name]
            .filter(Boolean)
            .join(' · ')}
        </p>
      </div>
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
