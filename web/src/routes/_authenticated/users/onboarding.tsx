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

import { Onboarding } from '@/features/onboarding'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

export const Route = createFileRoute('/_authenticated/users/onboarding')({
  beforeLoad: () => {
    // 开户会同时创建用户、渠道与分组，按后端 RootAuth 限制为超级管理员。
    if (useAuthStore.getState().auth.user?.role !== ROLE.SUPER_ADMIN) {
      throw redirect({ to: '/403' })
    }
  },
  validateSearch: z.object({
    request_id: z.uuid().optional().catch(undefined),
  }),
  component: OnboardingRoute,
})

/**
 * 只传递非敏感操作编号，浏览器历史中不保留用户密码或上游凭据。
 * @return 携带当前查询编号的开户页面。
 */
function OnboardingRoute(): React.JSX.Element {
  return <Onboarding requestId={Route.useSearch().request_id} />
}
