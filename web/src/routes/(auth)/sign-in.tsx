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
import { z } from 'zod'

import { sanitizeAuthRedirect } from '@/features/auth/lib/auth-redirect'
import { startSiteSwitch, switchNonce } from '@/features/auth/lib/site-switch'
import { SignIn } from '@/features/auth/sign-in'
import type { SystemStatus } from '@/features/auth/types'
import { resolveAuthentication } from '@/lib/auth-session'
import { handleServerError } from '@/lib/handle-server-error'
import { statusQueryOptions } from '@/lib/status-query'
import { useAuthStore } from '@/stores/auth-store'

const searchSchema = z.object({
  redirect: z.string().optional(),
})

export const Route = createFileRoute('/(auth)/sign-in')({
  component: SignIn,
  validateSearch: searchSchema,
  beforeLoad: async ({ search, context, preload }) => {
    const nonce = switchNonce()
    if (nonce && !preload) {
      const status = (await context.queryClient.fetchQuery(
        statusQueryOptions
      )) as SystemStatus | null
      if (status?.site_sso?.consumer) {
        let url: string
        try {
          // 托管站可能刚刚换过账号，不能直接复用中转站残留的另一份登录态。
          url = await startSiteSwitch(status.site_sso.issuer_url, nonce)
        } catch (error) {
          handleServerError(error)
          return
        }
        throw redirect({ href: url, replace: true, reloadDocument: true })
      }
    }
    // 根 guard 可能因为没有会话提示而跳过了 refresh。此处必须回源确认，
    // 否则持有有效 Refresh Cookie 的用户会被要求重新输入密码。
    await resolveAuthentication()

    const { auth } = useAuthStore.getState()

    // 如果已经有用户信息，说明已登录
    if (auth.user) {
      const target =
        sanitizeAuthRedirect(search?.redirect, window.location.origin) ??
        '/dashboard'
      throw redirect({ href: target, replace: true })
    }
  },
})
