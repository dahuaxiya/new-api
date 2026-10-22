import { createFileRoute, redirect } from '@tanstack/react-router'

import { UserUsageOverview } from '@/features/user-usage'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

export const Route = createFileRoute('/_authenticated/user-usage/')({
  beforeLoad: () => {
    const { auth } = useAuthStore.getState()
    if (!auth.user || auth.user.role < ROLE.ADMIN) {
      throw redirect({ to: '/403' })
    }
  },
  component: UserUsageOverview,
})
