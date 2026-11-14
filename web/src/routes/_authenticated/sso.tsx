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
import { createFileRoute } from '@tanstack/react-router'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { z } from 'zod'

import { Button } from '@/components/ui/button'
import { TitledCard } from '@/components/ui/titled-card'
import {
  SecureVerificationDialog,
  useSecureVerification,
} from '@/features/auth/secure-verification'
import { useStatus } from '@/hooks/use-status'
import { api } from '@/lib/api'
import { handleServerError } from '@/lib/handle-server-error'
import { useAuthStore } from '@/stores/auth-store'

const schema = z.object({
  state: z.string().regex(/^[A-Za-z0-9_-]{43}$/),
  challenge: z.string().regex(/^[A-Za-z0-9_-]{43}$/),
  client_id: z.string().min(1).max(32),
  verify: z.enum(['login', 'verify']),
})

/** 第一方站点授权也要求用户点击，敏感操作使用现有验证弹窗重新认证。 */
function SiteSSOAuthorize() {
  const { t } = useTranslation()
  const search = Route.useSearch()
  const { status } = useStatus()
  const user = useAuthStore((state) => state.auth.user)
  const verification = useSecureVerification()
  const [pending, setPending] = useState(false)
  const sso = status?.site_sso

  /** 证明绑定本次 state 和挑战；任何错误都留在当前页供用户重试。 */
  async function authorize() {
    if (!sso || sso.consumer) return
    setPending(true)
    try {
      const proof =
        search.verify === 'verify'
          ? await verification.requestVerification({
              scope: 'site.sso.authorize',
              context: { state: search.state, challenge: search.challenge },
            })
          : null
      if (search.verify === 'verify' && !proof) return
      const { data } = await api.post('/api/site-sso/authorize', search, {
        headers: proof ? { 'X-Security-Proof': proof.proof_token } : undefined,
      })
      if (
        !data.success ||
        typeof data.data?.redirect_url !== 'string' ||
        new URL(data.data.redirect_url).origin !== sso.client_url
      ) {
        throw new Error('Verification flow expired')
      }
      window.location.replace(data.data.redirect_url)
    } catch (error) {
      handleServerError(error)
    } finally {
      setPending(false)
    }
  }

  return (
    <div className='mx-auto w-full max-w-lg p-6'>
      <TitledCard
        title={t(
          search.verify === 'verify'
            ? 'Verify unified account'
            : 'Continue to API site'
        )}
      >
        <p className='mb-2 font-medium'>
          {user?.display_name || user?.username}
        </p>
        <p className='text-muted-foreground mb-6 text-sm'>
          {t(
            'One account for hosting and API access. Balances, orders and API keys stay separate.'
          )}
        </p>
        <Button
          className='w-full'
          disabled={pending || !sso || sso.consumer}
          onClick={() => void authorize()}
        >
          {t(
            search.verify === 'verify'
              ? 'Verify and continue'
              : 'Continue to API site'
          )}
        </Button>
      </TitledCard>
      <SecureVerificationDialog {...verification.dialogProps} />
    </div>
  )
}

export const Route = createFileRoute('/_authenticated/sso')({
  validateSearch: schema,
  component: SiteSSOAuthorize,
})
