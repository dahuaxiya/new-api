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
import { useEffect, useState } from 'react'
import { Link, useRouteContext, useSearch } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'

import { useStatus } from '@/hooks/use-status'

import { AuthLayout } from '../auth-layout'
import { TermsFooter } from '../components/terms-footer'
import { Button } from '@/components/ui/button'
import { UserAuthForm } from './components/user-auth-form'

/** 身份站展示真实表单。中转站仅在自动登录交接失败后才显示重试按钮。 */
export function SignIn() {
  const { t } = useTranslation()
  const { redirect, recovery } = useSearch({ from: '/(auth)/sign-in' })
  const { status } = useStatus()
  const routeContext = useRouteContext({ from: '/(auth)/sign-in' })
  const signInFailure = 'signInFailure' in routeContext ? routeContext.signInFailure : undefined
  const retryAt = signInFailure?.retryAt ?? 0
  const [now, setNow] = useState(Date.now)
  const waiting = Math.max(0, Math.ceil((retryAt - now) / 1000))
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

  return (
    <AuthLayout retailEntrance={status?.site_mode === 'retail'}>
      <div className='w-full space-y-8'>
        <div className='space-y-2'>
          <h2 className='text-center text-2xl font-semibold tracking-tight sm:text-left'>
            {t('Sign in')}
          </h2>
          {!status?.site_sso?.consumer &&
            !status?.self_use_mode_enabled &&
            status?.register_enabled !== false && (
              <p className='text-muted-foreground text-left text-sm sm:text-base'>
                {t("Don't have an account?")}{' '}
                <Link
                  to='/sign-up'
                  className='hover:text-primary font-medium underline underline-offset-4'
                >
                  {t('Sign up')}
                </Link>
                .
              </p>
            )}
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

        <TermsFooter
          variant='sign-in'
          status={status}
          className='text-center'
        />
      </div>
    </AuthLayout>
  )
}
