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
  return <SiteSSOAuthorize search={Route.useSearch()} />
}

export const Route = createFileRoute('/_authenticated/sso')({
  validateSearch: schema,
  beforeLoad: async ({ search, context, preload }) => {
    const nonce = switchNonce()
    const user = useAuthStore.getState().auth.user
    if (preload || search.verify !== 'login' || !nonce || !user) return
    const status = (await context.queryClient.fetchQuery(
      statusQueryOptions
    )) as SystemStatus | null
    const sso = status?.site_sso
    if (!sso || sso.consumer || !consumeSiteSwitch(nonce, user.id)) return
    let url: string
    try {
      url = await approveSiteSwitch(
        { ...search, verify: 'login' },
        sso.client_url
      )
    } catch (error) {
      // 自动步骤只执行一次，失败后显示原有手动授权与换号入口。
      handleServerError(error)
      return
    }
    throw redirect({ href: url, replace: true, reloadDocument: true })
  },
  component: SiteSSOPage,
})
