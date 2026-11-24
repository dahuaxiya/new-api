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
import { Link, useRouteContext, useSearch } from '@tanstack/react-router'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { isIssuerFrame } from '@/features/auth/lib/embedded-login'
import { useStatus } from '@/hooks/use-status'

import { AuthLayout } from '../auth-layout'
import { TermsFooter } from '../components/terms-footer'
import { EmbeddedLogin } from './components/embedded-login'
import { UserAuthForm } from './components/user-auth-form'

/** 身份站展示真实表单。中转站仅在自动登录交接失败后才显示重试按钮。 */
export function SignIn() {
  const { t } = useTranslation()
  const { redirect, recovery } = useSearch({ from: '/(auth)/sign-in' })
  const { status } = useStatus()
  const routeContext = useRouteContext({ from: '/(auth)/sign-in' })
  const signInFailure =
    'signInFailure' in routeContext ? routeContext.signInFailure : undefined
  const retryAt = signInFailure?.retryAt ?? 0
  const [now, setNow] = useState(Date.now)
  const waiting = Math.max(0, Math.ceil((retryAt - now) / 1000))
  const callback = new URL(redirect ?? '/', window.location.origin)
  const retailHandoff =
    status?.site_mode !== 'retail' &&
    callback.pathname === '/sso' &&
    callback.searchParams.get('client_id') === 'retail'
  useEffect(() => {
    if (!retryAt || retryAt <= Date.now()) return
    // 后台标签页可能暂停计时；按绝对时间计算，回来时仍显示正确的剩余时间。
    const timer = window.setInterval(() => {
      const current = Date.now()
      setNow(current)
      if (current >= retryAt) window.clearInterval(timer)
    }, 1000)
    return () => window.clearInterval(timer)
  }, [retryAt])

  if ('embeddedLoginUrl' in routeContext && routeContext.embeddedLoginUrl) {
    return (
      <AuthLayout retailEntrance>
        <EmbeddedLogin
          key={routeContext.embeddedLoginUrl}
          url={routeContext.embeddedLoginUrl}
        />
      </AuthLayout>
    )
  }

  return (
    <AuthLayout retailEntrance retailHandoff={retailHandoff}>
      <div className='w-full space-y-8'>
        <div className='space-y-2'>
          <h2 className='text-center text-2xl font-semibold tracking-tight sm:text-left'>
            {retailHandoff || status?.site_mode === 'retail'
              ? t('Sign in to the relay')
              : t('Sign in')}
          </h2>
        </div>

        {status?.site_sso?.consumer && recovery !== 'operator' ? (
          <div className='space-y-3'>
            <p role='status' className='text-muted-foreground text-sm'>
              {signInFailure?.limited
                ? t('Too many sign-in attempts. Please try again later.')
                : t('Could not start sign-in. Please try again.')}
            </p>
            <Button
              disabled={waiting > 0}
              onClick={() => window.location.reload()}
            >
              {waiting > 0
                ? t('Retry in {{time}}', {
                    time: `${Math.floor(waiting / 60)}:${String(waiting % 60).padStart(2, '0')}`,
                  })
                : t('Retry')}
            </Button>
          </div>
        ) : (
          <UserAuthForm redirectTo={redirect} />
        )}

        {!status?.site_sso?.consumer &&
          !status?.self_use_mode_enabled &&
          status?.register_enabled !== false && (
            <p className='text-muted-foreground border-t pt-5 text-center text-sm'>
              {t("Don't have an account?")}{' '}
              <Link
                to='/sign-up'
                target={isIssuerFrame() ? '_top' : undefined}
                className='text-primary inline-flex min-h-11 items-center font-medium hover:underline'
              >
                {t('Sign up')}
              </Link>
            </p>
          )}

        <TermsFooter
          variant='sign-in'
          status={status}
          className='text-center'
        />
      </div>
    </AuthLayout>
  )
}
