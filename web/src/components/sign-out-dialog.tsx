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
import { useQueryClient } from '@tanstack/react-query'
import { useNavigate } from '@tanstack/react-router'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { logout } from '@/features/auth/api'
import { sanitizeAuthRedirect } from '@/features/auth/lib/auth-redirect'
import { clearAuthenticatedClientState } from '@/lib/auth-session'
import { signedOutRoute } from '@/lib/signed-out-route'
import { handleServerError } from '@/lib/handle-server-error'

interface SignOutDialogProps {
  redirectTo?: string
  open: boolean
  onOpenChange: (open: boolean) => void
}

/** Logout always revokes the server session before clearing local state or entering another account. */
export function SignOutDialog(props: SignOutDialogProps) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const [isSigningOut, setIsSigningOut] = useState(false)

  /** Preserve only a same-origin return path; failed logout leaves the current identity intact. */
  const handleSignOut = async () => {
    setIsSigningOut(true)
    try {
      const response = await logout()
      if (!response.success) {
        handleServerError(response, t('Failed to sign out session'))
        return
      }

      const destination = signedOutRoute(queryClient)
      clearAuthenticatedClientState(queryClient)
      toast.success(t('Signed out'))
      if (destination === '/') {
        void navigate({ to: '/', replace: true })
        return
      }
      // 身份站主动换号时仍需保留原授权地址。
      const redirect = sanitizeAuthRedirect(
        props.redirectTo,
        window.location.origin
      )
      void navigate({
        to: '/sign-in',
        search: redirect ? { redirect } : {},
        replace: true,
      })
    } catch (error: unknown) {
      handleServerError(error, t('Failed to sign out session'))
    } finally {
      setIsSigningOut(false)
    }
  }

  return (
    <ConfirmDialog
      open={props.open}
      onOpenChange={props.onOpenChange}
      title={t('Sign out')}
      desc={t(
        'Are you sure you want to sign out? You will need to sign in again to access your account.'
      )}
      confirmText={t('Sign out')}
      handleConfirm={handleSignOut}
      isLoading={isSigningOut}
      className='sm:max-w-sm'
    />
  )
}
