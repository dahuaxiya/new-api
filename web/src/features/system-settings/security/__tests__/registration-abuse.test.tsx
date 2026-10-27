import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  act,
  fireEvent,
  render,
  renderHook,
  screen,
  waitFor,
} from '@testing-library/react'
import { createInstance } from 'i18next'
import { useState } from 'react'
import { beforeEach, expect, test, vi } from 'vitest'

import { useEmailVerification } from '@/features/auth/hooks/use-email-verification'
import en from '@/i18n/locales/en.json'
import fr from '@/i18n/locales/fr.json'
import ja from '@/i18n/locales/ja.json'
import ru from '@/i18n/locales/ru.json'
import viLocale from '@/i18n/locales/vi.json'
import zhTW from '@/i18n/locales/zh-TW.json'
import zh from '@/i18n/locales/zh.json'
import { api } from '@/lib/api'

import { SettingsPageProvider } from '../../components/settings-page-context'
import {
  REGISTRATION_ABUSE_DEFAULTS,
  REGISTRATION_ABUSE_GROUPS,
} from '../lib/registration-abuse-schema'
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

test('all seven locales load abuse settings from the real translation namespace', async () => {
  const resources = { en, zh, zhTW, fr, ru, ja, vi: viLocale }
  const instance = createInstance()
  await instance.init({
    resources,
    lng: 'zh',
    fallbackLng: false,
    keySeparator: false,
  })
  const keys = [
    'Registration and email abuse protection',
    'Save registration and email limits',
    ...REGISTRATION_ABUSE_GROUPS.flatMap((group) => [
      group.title,
      group.description,
      ...group.fields.map((field) => field.label),
    ]),
  ]
  for (const lng of Object.keys(resources)) {
    await instance.changeLanguage(lng)
    for (const key of keys) {
      expect(instance.exists(key), `${lng}: ${key}`).toBe(true)
    }
  }
  await instance.changeLanguage('zh')
  expect(instance.t('Registration and email abuse protection')).toBe(
    '注册与邮件防刷'
  )
  expect(instance.t('Resend interval (seconds)')).toBe('重发间隔（秒）')
})
