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
import { useTranslation } from 'react-i18next'

import { HOSTING_PERMANENT_END } from '@/features/gpt-hosting/api'
import { getAllLogs } from '@/features/usage-logs/api'
import { getAuditLogs } from '@/features/usage-logs/audit/api'
import { usageLogSchema } from '@/features/usage-logs/data/schema'
import { buildQuotaAuditOperation } from '@/features/usage-logs/lib/quota-audit-operation'
import { toIntlLocale } from '@/i18n/languages'
import { hasPermission } from '@/lib/admin-permissions'
import { formatQuotaWithCurrency } from '@/lib/currency'
import { requireServerSuccess } from '@/lib/server-error-message'
import { useAuthStore } from '@/stores/auth-store'

import type { User } from '../types'
import { useUsers } from './users-provider'

/** 在用户详情读取最近记录；只展示结构化金额、期限和原因，不渲染任何上游请求内容或凭据。 */
export function HostingUserActivity(props: { user: User }) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const identity = useAuthStore(
    (s) => `${s.auth.user?.id}:${s.auth.session?.sid}`
  )
  const canAudit = useAuthStore((s) =>
    hasPermission(s.auth.user, 'audit', 'read')
  )
  const { refreshTrigger } = useUsers()
  const query = useQuery({
    queryKey: ['hosting-activity', identity, props.user.id, refreshTrigger],
    queryFn: async () => {
      const [usage, wallet, audit] = await Promise.all([
        getAllLogs({ username: props.user.username, type: 2, page_size: 5 }),
        getAllLogs({ username: props.user.username, type: 1, page_size: 5 }),
        canAudit
          ? getAuditLogs('all', {
              username: props.user.username,
              category: 'operation',
              p: 1,
              page_size: 10,
            })
          : Promise.resolve({ items: [] }),
      ])
      return {
        usage: usageLogSchema
          .array()
          .parse(requireServerSuccess(usage).data?.items || []),
        wallet: usageLogSchema
          .array()
          .parse(requireServerSuccess(wallet).data?.items || []),
        audit: audit.items || [],
      }
    },
    retry: false,
  })
  return (
    <section className='space-y-4 border-b p-6'>
      <h3 className='font-semibold'>{t('Recent usage and changes')}</h3>
      {query.isPending && (
        <p className='text-muted-foreground text-sm'>{t('Loading...')}</p>
      )}
      {query.isError && (
        <p role='alert' className='text-destructive text-sm'>
          {t('Failed to load audit records')}
        </p>
      )}
      {query.data && (
        <>
          <h4 className='text-sm font-medium'>{t('Hosting changes')}</h4>
          {query.data.audit
            .filter((row) =>
              String(
                (row.other?.op as { action?: string })?.action || row.action
              ).startsWith('hosting.')
            )
            .map((row) => {
              const op = row.other?.op as
                | { action?: string; params?: Record<string, unknown> }
                | undefined
              const params = op?.params || {}
              const admin = row.other?.admin_info as
                | { admin_username?: string }
                | undefined
              let endLabel = t('Permanent hosting')
              if (params.ends_at !== HOSTING_PERMANENT_END) {
                endLabel =
                  typeof params.ends_at === 'number'
                    ? new Date(params.ends_at * 1000).toLocaleString(locale)
                    : '—'
              }
              return (
                <div
                  key={row.event_id}
                  className='space-y-1 border-l-2 pl-3 text-xs'
                >
                  <p>
                    {new Date(row.created_at * 1000).toLocaleString(locale)} ·{' '}
                    {admin?.admin_username || row.username}
                  </p>
                  <p>
                    {t(
                      op?.action === 'hosting.takeover'
                        ? 'Existing account taken over'
                        : 'Hosting entitlement saved'
                    )}
                  </p>
                  <p>
                    {typeof params.starts_at === 'number'
                      ? new Date(params.starts_at * 1000).toLocaleString(locale)
                      : '—'}{' '}
                    → {endLabel}
                  </p>
                  {typeof params.note === 'string' && (
                    <p className='break-words'>{params.note}</p>
                  )}
                </div>
              )
            })}
          {!query.data.audit.some((row) =>
            String(
              (row.other?.op as { action?: string })?.action || row.action
            ).startsWith('hosting.')
          ) && (
            <p className='text-muted-foreground text-xs'>
              {t('No hosting changes recorded')}
            </p>
          )}
          <h4 className='text-sm font-medium'>{t('Site balance changes')}</h4>
          {query.data.wallet.map((row) => {
            let op:
              | { action?: string; params?: Record<string, unknown> }
              | undefined
            try {
              op = JSON.parse(row.other || '{}').op
            } catch {
              /* 历史非 JSON 日志只显示日期，不猜测金额。 */
            }
            const summary = buildQuotaAuditOperation(
              op?.action || '',
              op?.params || {},
              true,
              t
            )
            return (
              <div key={row.id} className='space-y-1 border-l-2 pl-3 text-xs'>
                <p>{new Date(row.created_at * 1000).toLocaleString(locale)}</p>
                <p>{summary?.description || t('Not recorded')}</p>
                {typeof op?.params?.reason === 'string' && (
                  <p>{op.params.reason}</p>
                )}
              </div>
            )
          })}
          {!query.data.wallet.length && (
            <p className='text-muted-foreground text-xs'>
              {t('No balance changes recorded')}
            </p>
          )}
          <h4 className='text-sm font-medium'>{t('Recent requests')}</h4>
          {query.data.usage.map((row) => (
            <div
              key={row.id}
              className='flex flex-wrap justify-between gap-2 text-xs'
            >
              <span>
                {new Date(row.created_at * 1000).toLocaleString(locale)} ·{' '}
                {row.model_name}
              </span>
              <span>{formatQuotaWithCurrency(row.quota)}</span>
            </div>
          ))}
          {!query.data.usage.length && (
            <p className='text-muted-foreground text-xs'>
              {t('No recent requests')}
            </p>
          )}
        </>
      )}
    </section>
  )
}
