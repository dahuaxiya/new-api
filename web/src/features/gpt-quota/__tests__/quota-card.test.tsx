import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import i18next from 'i18next'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import zhResources from '@/i18n/locales/zh.json'
import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

import { GptQuotaCard } from '../GptQuotaCard'

let client: QueryClient
const fixture = {
  bound: true,
  account_label: 'Alice',
  plan: 'pro',
  stale: false,
  observed_at: '2026-10-21T13:00:00Z',
  last_checked_at: '2026-10-21T13:00:00Z',
  sync_status: 'ok',
  poll_seconds: 300,
  windows: [
    { id: '5h', used_percent: null, remaining_percent: null, reset_at: null },
    {
      id: '7d',
      used_percent: 56,
      remaining_percent: 44,
      reset_at: '2099-01-01T00:00:00Z',
    },
  ],
}

beforeEach(() => {
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  useAuthStore.setState(useAuthStore.getInitialState(), true)
  useAuthStore.getState().auth.setUser({ id: 2, username: 'alice', role: 1 })
})
afterEach(async () => {
  await i18next.changeLanguage('en')
  i18next.removeResourceBundle('zhCN', 'translation')
  cleanup()
  client.clear()
  vi.restoreAllMocks()
})

/** Mount with the real auth store and query cache; mock only the HTTP boundary. */
function mount() {
  return render(
    <QueryClientProvider client={client}>
      <GptQuotaCard />
    </QueryClientProvider>
  )
}

describe('official quota display', () => {
  it('shows a missing window as unknown and a known window as remaining percentage', async () => {
    vi.spyOn(api, 'get').mockResolvedValue({
      data: { success: true, data: fixture },
    })
    mount()
    expect(
      await screen.findByText('GPT upstream has not returned this window')
    ).toBeTruthy()
    expect(screen.getByText('GPT remaining 44%')).toBeTruthy()
    expect(screen.getByRole('progressbar').getAttribute('aria-valuenow')).toBe(
      '56'
    )
  })

  it('does not show a full available quota when the observed window has expired', async () => {
    vi.spyOn(api, 'get').mockResolvedValue({
      data: {
        success: true,
        data: {
          ...fixture,
          windows: [
            fixture.windows[0],
            { ...fixture.windows[1], reset_at: '2000-01-01T00:00:00Z' },
          ],
        },
      },
    })
    mount()
    expect(
      await screen.findByText('GPT window expired; awaiting a fresh reading')
    ).toBeTruthy()
    expect(screen.queryByRole('progressbar')).toBeNull()
  })

  it('removes the previous account reading immediately while the next account loads', async () => {
    const get = vi
      .spyOn(api, 'get')
      .mockResolvedValueOnce({ data: { success: true, data: fixture } })
    mount()
    await screen.findByText('Alice · PRO')
    get.mockImplementation(() => new Promise(() => {}))
    act(() =>
      useAuthStore.getState().auth.setUser({ id: 3, username: 'bob', role: 1 })
    )
    await waitFor(() => expect(screen.queryByText('Alice · PRO')).toBeNull())
    expect(
      screen
        .getByRole('button', { name: 'Refresh official quota' })
        .hasAttribute('disabled')
    ).toBe(true)
  })

  it('marks failed synchronization as dated data without hiding a last-known reading', async () => {
    vi.spyOn(api, 'get').mockResolvedValue({
      data: {
        success: true,
        data: { ...fixture, stale: true, sync_status: 'error' },
      },
    })
    mount()
    const status = await screen.findByRole('status')
    expect(
      within(status).getByText(
        'GPT reading may be outdated; awaiting the next successful sync'
      )
    ).toBeTruthy()
    expect(screen.getByText('GPT remaining 44%')).toBeTruthy()
  })

  it('hides quota for an unbound administrator instead of exposing another user', async () => {
    vi.spyOn(api, 'get').mockResolvedValue({
      data: { success: true, data: { bound: false } },
    })
    mount()
    await waitFor(() =>
      expect(screen.queryByLabelText('GPT subscription quota')).toBeNull()
    )
  })
})

/** Guard the real locale structure, which differs from the empty test fallback bundle. */
it('renders the new quota card in Chinese using the deployed locale bundle', async () => {
  i18next.addResourceBundle('zhCN', 'translation', zhResources.translation)
  await i18next.changeLanguage('zhCN')
  vi.spyOn(api, 'get').mockResolvedValue({
    data: { success: true, data: fixture },
  })
  mount()
  expect(await screen.findByText('剩余 44%')).toBeTruthy()
  expect(screen.getByLabelText('GPT 订阅额度')).toBeTruthy()
})

/** Manual refresh must POST once, show progress, and replace the displayed sample. */
it('queries official quota only on click and prevents duplicate clicks while pending', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({
    data: { success: true, data: fixture },
  })
  let complete!: (value: unknown) => void
  const post = vi.spyOn(api, 'post').mockImplementation(
    () =>
      new Promise((resolve) => {
        complete = resolve
      })
  )
  mount()
  await screen.findByText('GPT remaining 44%')
  expect(post).not.toHaveBeenCalled()
  fireEvent.click(
    screen.getByRole('button', { name: 'Refresh official quota' })
  )
  const pending = await screen.findByRole('button', {
    name: 'Querying official quota...',
  })
  expect(pending.hasAttribute('disabled')).toBe(true)
  fireEvent.click(pending)
  await waitFor(() => expect(post).toHaveBeenCalledTimes(1))
  expect(post).toHaveBeenCalledWith(
    '/api/gpt-quota/self/refresh',
    {},
    { timeout: 90_000 }
  )
  act(() =>
    complete({
      data: {
        success: true,
        data: {
          ...fixture,
          windows: [
            fixture.windows[0],
            { ...fixture.windows[1], used_percent: 60, remaining_percent: 40 },
          ],
        },
      },
    })
  )
  expect(await screen.findByText('GPT remaining 40%')).toBeTruthy()
  expect(await screen.findByText('Official quota updated')).toBeTruthy()
})

/** Upstream failures keep the previous official reading and expose a local retry hint. */
it.each([503, 429])(
  'retains the last reading after refresh status %s',
  async (status) => {
    vi.spyOn(api, 'get').mockResolvedValue({
      data: { success: true, data: fixture },
    })
    vi.spyOn(api, 'post').mockRejectedValue({
      isAxiosError: true,
      response: { status },
    })
    mount()
    await screen.findByText('GPT remaining 44%')
    fireEvent.click(
      screen.getByRole('button', { name: 'Refresh official quota' })
    )
    const alert = await screen.findByRole('alert')
    expect(alert.textContent).toContain(
      status === 429
        ? 'Please wait 30 seconds'
        : 'Previous readings are retained'
    )
    expect(screen.getByText('GPT remaining 44%')).toBeTruthy()
  }
)

/** Account changes during a probe must not attach the old result or feedback to the new user. */
it('isolates a refresh that finishes after switching accounts', async () => {
  vi.spyOn(api, 'get')
    .mockResolvedValueOnce({ data: { success: true, data: fixture } })
    .mockResolvedValue({
      data: { success: true, data: { ...fixture, account_label: 'Bob' } },
    })
  let complete!: (value: unknown) => void
  vi.spyOn(api, 'post').mockImplementation(
    () =>
      new Promise((resolve) => {
        complete = resolve
      })
  )
  mount()
  await screen.findByText('Alice · PRO')
  fireEvent.click(
    screen.getByRole('button', { name: 'Refresh official quota' })
  )
  await waitFor(() => expect(complete).toBeDefined())
  act(() =>
    useAuthStore.getState().auth.setUser({ id: 3, username: 'bob', role: 1 })
  )
  await screen.findByText('Bob · PRO')
  await act(async () => complete({ data: { success: true, data: fixture } }))
  expect(screen.queryByText('Alice · PRO')).toBeNull()
  expect(screen.queryByText('Official quota updated')).toBeNull()
  expect(screen.getByText('Bob · PRO')).toBeTruthy()
})
