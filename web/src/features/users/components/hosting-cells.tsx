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
          label={t(
            hostingStatusLabels[v.status] || 'Hosting status unavailable'
          )}
        />
        {v.grant && (
          <p className='text-muted-foreground'>
            {v.slot_id ? '1/1' : '0/1'} ·{' '}
            {new Date(v.grant.ends_at * 1000).toLocaleDateString(locale)}
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
      <div className='max-w-48 space-y-1 text-xs'>
        <p className='truncate'>
          {v.binding?.account_label || reading?.account_label || '—'}
        </p>
        <p className='text-muted-foreground truncate'>
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
          <p
            key={w.id}
            title={
              w.reset_at
                ? new Date(w.reset_at).toLocaleString(locale)
                : t('GPT reset time unavailable')
            }
          >
            {w.id} ·{' '}
            {valid
              ? `${formatNumber(w.remaining_percent, locale)}%`
              : t('Unknown')}
          </p>
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
