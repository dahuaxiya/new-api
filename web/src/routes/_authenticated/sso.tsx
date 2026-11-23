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
import { createFileRoute, redirect } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { useStatus } from '@/hooks/use-status'
import { z } from 'zod'

import {
  consumeIssuerLogin,
  mayRestartIssuerLogin,
} from '@/features/auth/lib/issuer-login-intent'
import { SiteSSOAuthorize } from '@/features/auth/components/site-sso-authorize'
import {
  approveSiteSwitch,
  consumeSiteSwitch,
  switchNonce,
} from '@/features/auth/lib/site-switch'
import type { SystemStatus } from '@/features/auth/types'
import { handleServerError } from '@/lib/handle-server-error'
import { statusQueryOptions } from '@/lib/status-query'
import { useAuthStore } from '@/stores/auth-store'

const schema = z.object({
  state: z.string().regex(/^[A-Za-z0-9_-]{43}$/),
  challenge: z.string().regex(/^[A-Za-z0-9_-]{43}$/),
  client_id: z.string().min(1).max(32),
  verify: z.enum(['login', 'verify']),
})

/** Keep route validation ahead of the authorization UI; only validated state/challenge reach the issuer. */
function SiteSSOPage() {
  const search = Route.useSearch()
  const { t } = useTranslation()
  const { status } = useStatus()
  if (search.verify === 'verify') return <SiteSSOAuthorize search={search} />
  // 普通登录成功时路由已跳转。若浏览器存储或授权失败，只允许从配置的消费站重建事务。
  const client = status?.site_sso?.client_url
  return (
    <div role='alert' className='mx-auto max-w-md space-y-4 p-6'>
      <p>{t('Verification flow expired')}</p>
      {client && (
        <Button render={<a href={new URL('/sign-in', client).href} />}>
          {t('Sign in')}
        </Button>
      )}
    </div>
  )
}

export const Route = createFileRoute('/_authenticated/sso')({
  validateSearch: schema,
  beforeLoad: async ({ search, context, preload }) => {
    const nonce = switchNonce()
    const user = useAuthStore.getState().auth.user
    if (preload || search.verify !== 'login' || !user) return
    const status = (await context.queryClient.fetchQuery(
      statusQueryOptions
    )) as SystemStatus | null
    const sso = status?.site_sso
    if (!sso || sso.consumer) return
    const intended = nonce
      ? consumeSiteSwitch(nonce, user.id)
      : await consumeIssuerLogin(sso.client_url, search.state, search.challenge)
    if (!intended) {
      // 遗留页面或书签重新建立浏览器绑定，不能自动签发外来 URL 指定的事务。
      if (!nonce && mayRestartIssuerLogin())
        throw redirect({
          href: new URL('/sign-in', sso.client_url).href,
          replace: true,
          reloadDocument: true,
        })
      return
    }
    let url: string
    try {
      url = await approveSiteSwitch(
        { ...search, verify: 'login' },
        sso.client_url
      )
    } catch (error) {
      // 自动步骤只执行一次，失败后从消费站重新发起事务。
      handleServerError(error)
      return
    }
    throw redirect({ href: url, replace: true, reloadDocument: true })
  },
  component: SiteSSOPage,
})
