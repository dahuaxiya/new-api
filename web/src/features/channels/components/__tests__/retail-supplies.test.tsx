import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

import { RetailSupplies } from '../retail-supplies'

const clients: QueryClient[] = []

/** 使用真实抽屉和查询生命周期，仅在 HTTP 边界提供供货响应。 */
function renderSupplies() {
  useAuthStore.getState().auth.setUser({ id: 1, username: 'root', role: 100 })
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  clients.push(client)
  render(
    <QueryClientProvider client={client}>
      <RetailSupplies />
    </QueryClientProvider>
  )
}

afterEach(() => {
  cleanup()
  clients.splice(0).forEach((client) => client.clear())
  vi.restoreAllMocks()
  useAuthStore.getState().auth.reset()
})

it('opens on demand and distinguishes eligible, disabled and isolated channels', async () => {
  const get = vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      data: {
        state: 'ready',
        group: 'retail',
        items: [
          {
            channel_id: 1,
            name: 'Owned',
            kind: 'subscription',
            status: 'eligible',
          },
          {
            channel_id: 2,
            name: 'Backup',
            kind: 'reseller',
            status: 'disabled',
          },
          {
            channel_id: 3,
            name: 'Customer',
            kind: 'subscription',
            status: 'excluded',
          },
        ],
      },
    },
  })
  renderSupplies()
  expect(get).not.toHaveBeenCalled()
  fireEvent.click(screen.getByRole('button', { name: 'Supply overview' }))
  expect(await screen.findByText('Owned')).toBeInTheDocument()
  expect(screen.getByText('Eligible for routing')).toBeInTheDocument()
  expect(screen.getByText('Channel disabled')).toBeInTheDocument()
  expect(
    screen.getByText('Excluded by supply isolation rules')
  ).toBeInTheDocument()
  expect(
    screen.getByText(
      'Routing eligibility does not verify upstream connectivity, remaining quota or model availability.'
    )
  ).toBeInTheDocument()
})

it('hides stale eligibility after refresh fails and lets the operator retry', async () => {
  const get = vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      data: { state: 'disabled', group: '', items: [] },
    },
  })
  renderSupplies()
  fireEvent.click(screen.getByRole('button', { name: 'Supply overview' }))
  expect(
    await screen.findByText('No supply channels registered')
  ).toBeInTheDocument()
  get.mockRejectedValueOnce(new Error('unavailable'))
  fireEvent.click(screen.getByRole('button', { name: 'Refresh' }))
  expect(
    await screen.findByText('Unable to load resources')
  ).toBeInTheDocument()
  expect(screen.queryByText('Retail sales disabled')).not.toBeInTheDocument()
  fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
  await waitFor(() =>
    expect(
      screen.queryByText('Unable to load resources')
    ).not.toBeInTheDocument()
  )
  expect(screen.getByText('Retail sales disabled')).toBeInTheDocument()
})
