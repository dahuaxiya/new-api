import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  createMemoryHistory,
  createRootRoute,
  createRouter,
  RouterProvider,
} from '@tanstack/react-router'
import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import i18next from 'i18next'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import zhResources from '@/i18n/locales/zh.json'
import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

import { UserUsageOverview } from '..'

let client: QueryClient
const users = [
  {
    id: 2,
    username: 'alice',
    display_name: 'Alice',
    group: 'alice',
    role: 1,
    status: 1,
    quota: 500000,
    used_quota: 10000,
    request_count: 9,
  },
  {
    id: 3,
    username: 'bob',
    display_name: 'Bob',
    group: 'bob',
    role: 1,
    status: 1,
    quota: 1000000,
    used_quota: 0,
    request_count: 0,
  },
]
const quota = {
  bound: true,
  account_label: 'Alice',
  plan: 'plus',
  stale: false,
  observed_at: '2026-10-22T03:00:00Z',
  last_checked_at: null,
  sync_status: 'ok',
  poll_seconds: 300,
  windows: [
    { id: '5h', used_percent: null, remaining_percent: null, reset_at: null },
    {
      id: '7d',
      used_percent: 60,
      remaining_percent: 40,
      reset_at: '2099-01-01T00:00:00Z',
    },
  ],
}

beforeEach(() => {
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  useAuthStore.setState(useAuthStore.getInitialState(), true)
  useAuthStore.getState().auth.setUser({ id: 1, username: 'admin', role: 100 })
})
afterEach(async () => {
  cleanup()
  client.clear()
  vi.restoreAllMocks()
  await i18next.changeLanguage('en')
  i18next.removeResourceBundle('zhCN', 'translation')
})

/** Only replace HTTP responses; router, session store and query cache remain real. */
function mockAPI(failedUsage = false) {
  return vi.spyOn(api, 'get').mockImplementation(async (url) => {
    if (url === '/api/user/') {
      return {
        data: {
          success: true,
          data: { items: users, total: 2, page: 1, page_size: 10 },
        },
      }
    }
    if (url === '/api/data/users') {
      return {
        data: {
          success: !failedUsage,
          data: [
            { username: 'alice', count: 2, token_used: 100, quota: 1000 },
            { username: 'alice', count: 1, token_used: 150, quota: 1000 },
            {
              username: 'not-on-page',
              count: 999,
              token_used: 999999,
              quota: 999999,
            },
          ],
        },
      }
    }
    return {
      data: {
        success: true,
        data: url === '/api/gpt-quota/users/2' ? quota : { bound: false },
      },
    }
  })
}

/** Mount in an actual router so log links retain their username and period search. */
function mount() {
  const root = createRootRoute({ component: UserUsageOverview })
  const router = createRouter({
    routeTree: root,
    history: createMemoryHistory({ initialEntries: ['/'] }),
  })
  return render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
}

it('matches summaries to users, preserves unknown windows, and links to scoped logs', async () => {
  mockAPI()
  mount()
  const alice = await screen.findByLabelText('Usage for Alice')
  await within(alice).findByText('GPT remaining 40%')
  expect(within(alice).getByText('250')).toBeTruthy()
  expect(within(alice).getByText('3')).toBeTruthy()
  expect(
    within(alice).getByText('GPT upstream has not returned this window')
  ).toBeTruthy()
  const href =
    within(alice)
      .getByRole('link', { name: 'View user logs' })
      .getAttribute('href') ?? ''
  const params = new URL(href, 'https://example.com').searchParams
  expect(params.get('username')).toBe('alice')
  expect(Number(params.get('endTime')) - Number(params.get('startTime'))).toBe(
    7 * 86400 * 1000
  )
  const bob = screen.getByLabelText('Usage for Bob')
  expect(within(bob).queryByText('250')).toBeNull()
  expect(await within(bob).findByText('No linked quota account')).toBeTruthy()
  expect(
    within(bob).getByText(
      'No saved usage summary for this period; check usage logs for recent requests.'
    )
  ).toBeTruthy()
})

it('does not expose administrator data or send requests for an ordinary user', async () => {
  const get = mockAPI()
  useAuthStore.getState().auth.setUser({ id: 2, username: 'alice', role: 1 })
  mount()
  await screen.findByText('Access denied')
  expect(get).not.toHaveBeenCalled()
})

it('removes loaded admin data immediately when the signed-in account changes', async () => {
  const get = mockAPI()
  mount()
  await screen.findByText('GPT remaining 40%')
  get.mockClear()
  act(() =>
    useAuthStore.getState().auth.setUser({ id: 3, username: 'bob', role: 1 })
  )
  await screen.findByText('Access denied')
  expect(screen.queryByLabelText('Usage for Alice')).toBeNull()
  expect(screen.queryByText('GPT remaining 40%')).toBeNull()
  expect(get).not.toHaveBeenCalled()
})

it('reports failed aggregation as unavailable while leaving balances and official quota visible', async () => {
  mockAPI(true)
  mount()
  const alice = await screen.findByLabelText('Usage for Alice')
  await within(alice).findByText('Site usage summary unavailable')
  expect(within(alice).queryByText('250')).toBeNull()
  expect(await within(alice).findByText('GPT remaining 40%')).toBeTruthy()
  expect(within(alice).getByText('Site balance')).toBeTruthy()
})

it('refreshes saved data without sending a probe and supports a different period', async () => {
  const get = mockAPI()
  const post = vi.spyOn(api, 'post')
  mount()
  await screen.findByText('GPT remaining 40%')
  get.mockClear()
  fireEvent.click(screen.getByRole('button', { name: 'Refresh display' }))
  await waitFor(() =>
    expect(
      get.mock.calls.some(([url]) => url === '/api/gpt-quota/users/2')
    ).toBe(true)
  )
  expect(post).not.toHaveBeenCalled()
  const user = userEvent.setup()
  await user.click(screen.getByRole('combobox', { name: 'Usage period' }))
  await user.click(await screen.findByRole('option', { name: 'Last 24 hours' }))
  await waitFor(() => {
    const calls = get.mock.calls.filter(([url]) => url === '/api/data/users')
    const params = calls.at(-1)?.[1]?.params
    expect(params.end_timestamp - params.start_timestamp).toBe(86400)
  })
})

it('renders the administrator page with the deployed Chinese translation bundle', async () => {
  i18next.addResourceBundle('zhCN', 'translation', zhResources.translation)
  await i18next.changeLanguage('zhCN')
  mockAPI()
  mount()
  expect(await screen.findByText('用户用量总览')).toBeTruthy()
  expect(await screen.findByText('剩余 40%')).toBeTruthy()
  expect(screen.getAllByText('站内余额')).toHaveLength(2)
})
