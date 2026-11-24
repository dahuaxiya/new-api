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

import {
  isIssuerFrame,
  issuerOrigin,
  retailOrigin,
} from '@/features/auth/lib/embedded-login'
import { SignUp } from '@/features/auth/sign-up'
import type { SystemStatus } from '@/features/auth/types'
import { statusQueryOptions } from '@/lib/status-query'
import { useAuthStore } from '@/stores/auth-store'

export const Route = createFileRoute('/(auth)/sign-up')({
  component: SignUp,
  validateSearch: z.object({ aff: z.string().optional().catch(undefined) }),
  beforeLoad: async ({ context, search, preload }) => {
    if (preload) return
    // Old public links keep their referral code. Only top-level visits migrate;
    // redirecting the issuer frame as well would recursively embed the retail page.
    if (window.location.origin === issuerOrigin && window === window.top) {
      const url = new URL('/sign-up', retailOrigin)
      if (search.aff) url.searchParams.set('aff', search.aff)
      throw redirect({ href: url.href, replace: true, reloadDocument: true })
    }
    const { auth } = useAuthStore.getState()

    // 如果已经有用户信息，说明已登录，注册页对其无意义，跳转到 dashboard
    if (auth.user) {
      if (isIssuerFrame()) {
        window.parent.postMessage({ type: 'luobo.form.sign-in' }, retailOrigin)
        return { returningToLogin: true }
      }
      throw redirect({ to: '/dashboard' })
    }
    if (window.location.origin === retailOrigin && window.top === window) {
      const status = (await context.queryClient.fetchQuery(
        statusQueryOptions
      )) as SystemStatus | null
      return {
        embeddedRegistration:
          status?.site_sso?.consumer &&
          status.site_sso.issuer_url === issuerOrigin,
      }
    }
  },
})
