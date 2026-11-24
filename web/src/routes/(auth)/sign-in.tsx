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
import axios from 'axios'
import { z } from 'zod'

import { sanitizeAuthRedirect } from '@/features/auth/lib/auth-redirect'
import { issuerOrigin, retailOrigin } from '@/features/auth/lib/embedded-login'
import { startSiteLogin, switchNonce } from '@/features/auth/lib/site-switch'
import { SignIn } from '@/features/auth/sign-in'
import type { SystemStatus } from '@/features/auth/types'
import { resolveAuthentication } from '@/lib/auth-session'
import { statusQueryOptions } from '@/lib/status-query'
import { useAuthStore } from '@/stores/auth-store'

const searchSchema = z.object({
  redirect: z.string().optional(),
  recovery: z.literal('operator').optional(),
})

/** 只把可公开的失败类别和服务端等待时间交给界面，避免直接展示身份服务的响应。 */
function signInFailure(error: unknown) {
  const response = axios.isAxiosError(error) ? error.response : undefined
  const seconds = Number(response?.headers?.['retry-after'])
  const limited = response?.status === 429
  return {
    signInFailure: {
      limited,
      retryAt:
        limited && Number.isFinite(seconds) && seconds > 0
          ? Date.now() + seconds * 1000
          : 0,
    },
  }
}

export const Route = createFileRoute('/(auth)/sign-in')({
  component: SignIn,
  validateSearch: searchSchema,
  beforeLoad: async ({ search, context, preload }) => {
    // 鼠标悬停预取不能消耗登录接口限额，也不应发起会话刷新。
    if (preload) return
    const nonce = switchNonce()
    if (nonce) {
      const status = (await context.queryClient.fetchQuery(
        statusQueryOptions
      )) as SystemStatus | null
      if (status?.site_sso?.consumer) {
        let url: string
        try {
          // 托管站可能刚刚换过账号，不能直接复用中转站残留的另一份登录态。
          url = await startSiteLogin(
            status.site_sso.issuer_url,
            '/dashboard/overview',
            nonce
          )
        } catch (error) {
          return signInFailure(error)
        }
        throw redirect({ href: url, replace: true, reloadDocument: true })
      }
    }
    // 根 guard 可能因为没有会话提示而跳过了 refresh。此处必须回源确认，
    // 否则持有有效 Refresh Cookie 的用户会被要求重新输入密码。
    const session = await resolveAuthentication()
    // 刷新已被限流时再创建登录事务只会重复消耗同一配额。应急入口仍可手动登录。
    if (
      session.kind === 'transient_error' &&
      search.recovery !== 'operator' &&
      axios.isAxiosError(session.error) &&
      session.error.response?.status === 429
    ) {
      return signInFailure(session.error)
    }

    const { auth } = useAuthStore.getState()

    // 如果已经有用户信息，说明已登录
    if (auth.user) {
      const target =
        sanitizeAuthRedirect(search?.redirect, window.location.origin) ??
        '/dashboard/overview'
      throw redirect({ href: target, replace: true })
    }

    // 路由预加载不是登录意图；管理员的本机应急入口也不依赖身份站。
    if (search.recovery === 'operator') return
    const status = (await context.queryClient.fetchQuery(
      statusQueryOptions
    )) as SystemStatus | null
    if (!status?.site_sso?.consumer) return
    const target =
      sanitizeAuthRedirect(search.redirect, window.location.origin) ??
      '/dashboard/overview'
    let href: string
    try {
      href = await startSiteLogin(status.site_sso.issuer_url, target)
    } catch (error) {
      // 无法建立事务时才展示手动重试入口，避免自动刷新造成循环。
      return signInFailure(error)
    }
    if (
      location.origin === retailOrigin &&
      status.site_sso.issuer_url === issuerOrigin &&
      window.top === window
    ) {
      return { embeddedLoginUrl: href }
    }
    throw redirect({ href, replace: true, reloadDocument: true })
  },
})
