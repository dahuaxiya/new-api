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
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { SignUpForm } from '../sign-up-form'

Object.defineProperty(document, 'elementFromPoint', {
  configurable: true,
  value: () => null,
})

const mocks = vi.hoisted(() => ({
  sendCode: vi.fn(),
}))

vi.mock('@/hooks/use-status', () => ({
  useStatus: () => ({
    status: {
      email_verification: true,
      register_enabled: true,
    },
    loading: false,
  }),
}))

vi.mock('@/features/auth/hooks/use-turnstile', () => ({
  useTurnstile: () => ({
    isTurnstileEnabled: false,
    turnstileSiteKey: '',
    turnstileToken: '',
    setTurnstileToken: vi.fn(),
    validateTurnstile: () => true,
  }),
}))

vi.mock('@/features/auth/hooks/use-email-verification', () => ({
  useEmailVerification: () => ({
    isSending: false,
    secondsLeft: 0,
    isActive: false,
    sendCode: mocks.sendCode,
  }),
}))

vi.mock('@/features/auth/hooks/use-auth-redirect', () => ({
  useAuthRedirect: () => ({
    redirectToLogin: vi.fn(),
    handleLoginResult: vi.fn(),
  }),
}))

vi.mock('@/features/auth/components/oauth-providers', () => ({
  OAuthProviders: () => null,
}))

beforeEach(() => {
  mocks.sendCode.mockReset()
  mocks.sendCode.mockResolvedValue(true)
})

describe('sign-up flow', () => {
  it('starts with email only and moves to verification after sending a code', async () => {
    render(<SignUpForm />)
    const user = userEvent.setup()
    const email = screen.getByRole('combobox', { name: 'Email' })

    expect(screen.queryByLabelText('Username')).not.toBeInTheDocument()
    await user.type(email, 'alice@qq.com')
    await user.click(
      screen.getByRole('button', { name: /Send verification code/ })
    )

    expect(mocks.sendCode).toHaveBeenCalledWith('alice@qq.com')
    expect(await screen.findByText('alice@qq.com')).toBeVisible()
    expect(
      screen.getByRole('textbox', {
        name: '6-digit email verification code',
      })
    ).toBeVisible()
  })

  it('continues after six digits and keeps the verified email when editing it', async () => {
    render(<SignUpForm />)
    const user = userEvent.setup()

    await user.type(
      screen.getByRole('combobox', { name: 'Email' }),
      'alice@qq.com'
    )
    await user.click(
      screen.getByRole('button', { name: /Send verification code/ })
    )

    const code = await screen.findByRole('textbox', {
      name: '6-digit email verification code',
    })
    await user.type(code, '246810')
    await waitFor(() => expect(screen.getByLabelText('Username')).toBeVisible())

    await user.click(screen.getByRole('button', { name: 'Change' }))
    expect(screen.getByRole('combobox', { name: 'Email' })).toHaveValue(
      'alice@qq.com'
    )
  })
})
