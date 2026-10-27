import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  act,
  fireEvent,
  render,
  renderHook,
  screen,
  waitFor,
} from '@testing-library/react'
import { useState } from 'react'
import { beforeEach, expect, test, vi } from 'vitest'

import { useEmailVerification } from '@/features/auth/hooks/use-email-verification'
import { api } from '@/lib/api'

import { SettingsPageProvider } from '../../components/settings-page-context'
import { REGISTRATION_ABUSE_DEFAULTS } from '../lib/registration-abuse-schema'
import { RegistrationAbuseSection } from '../registration-abuse-section'

/** Provide the real shared save toolbar to exercise the complete form submission. */
function Fixture() {
  const [container, setContainer] = useState<HTMLDivElement | null>(null)
  return (
    <>
      <div ref={setContainer} />
      <SettingsPageProvider actionsContainer={container}>
        <RegistrationAbuseSection
          value={JSON.stringify(REGISTRATION_ABUSE_DEFAULTS)}
        />
      </SettingsPageProvider>
    </>
  )
}

/** Render with retries disabled so failed writes retain edits without background mutations. */
function renderSettings(): void {
  const client = new QueryClient({
    defaultOptions: { mutations: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <Fixture />
    </QueryClientProvider>
  )
}

beforeEach(() => {
  vi.spyOn(api, 'put').mockResolvedValue({ data: { success: true } })
})

test('saves a complete policy in one request and keeps saved values on the form', async () => {
  renderSettings()
  const cooldown = screen.getByRole('spinbutton', {
    name: 'Resend interval (seconds)',
  })
  expect(cooldown).toHaveValue(60)
  fireEvent.change(cooldown, { target: { value: '90' } })
  const save = screen.getByRole('button', {
    name: 'Save registration and email limits',
  })
  await waitFor(() => expect(save).toBeEnabled())
  fireEvent.click(save)
  await waitFor(() =>
    expect(api.put).toHaveBeenCalledExactlyOnceWith('/api/option/', {
      key: 'RegistrationAbusePolicy',
      value: JSON.stringify({
        ...REGISTRATION_ABUSE_DEFAULTS,
        resend_seconds: 90,
      }),
    })
  )
  await waitFor(() => expect(save).toBeDisabled())
  expect(cooldown).toHaveValue(90)
})

test('rejects zero, fractions and inconsistent windows before submitting', async () => {
  renderSettings()
  const input = screen.getByRole('spinbutton', {
    name: 'Emails per IP per hour',
  })
  for (const value of ['0', '1.5', '31']) {
    fireEvent.change(input, { target: { value } })
    await waitFor(() => expect(input).toHaveAttribute('aria-invalid', 'true'))
    expect(
      screen.getByRole('button', { name: 'Save registration and email limits' })
    ).toBeDisabled()
  }
  expect(api.put).not.toHaveBeenCalled()
})

test('failed persistence keeps edited values available for retry', async () => {
  vi.spyOn(api, 'put').mockResolvedValue({
    data: {
      success: false,
      message: 'Database error, please contact the administrator',
    },
  })
  renderSettings()
  const input = screen.getByRole('spinbutton', {
    name: 'Emails per address per day',
  })
  fireEvent.change(input, { target: { value: '4' } })
  const save = screen.getByRole('button', {
    name: 'Save registration and email limits',
  })
  await waitFor(() => expect(save).toBeEnabled())
  fireEvent.click(save)
  await waitFor(() => expect(api.put).toHaveBeenCalledTimes(1))
  await waitFor(() => expect(save).toBeEnabled())
  expect(input).toHaveValue(4)
})

test('registration countdown follows the server cooldown instead of sixty seconds', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({
    data: { success: true, data: { resend_seconds: 90 } },
  })
  const { result } = renderHook(() => useEmailVerification())
  await act(async () => {
    expect(await result.current.sendCode('test@example.com')).toBe(true)
  })
  expect(result.current.secondsLeft).toBe(90)
  expect(result.current.isActive).toBe(true)
})
