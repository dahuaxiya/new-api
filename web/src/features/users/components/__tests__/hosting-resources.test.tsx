import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  cleanup,
  fireEvent,
  render,
  screen,
  within,
} from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

import { HostingResources } from '../hosting-resources'

const clients: QueryClient[] = []
/** 资源抽屉使用真实组件，仅模拟面板汇总与开户选项的 HTTP 响应。 */
function renderResources(instances: unknown[]) {
  useAuthStore.getState().auth.setUser({ id: 1, username: 'root', role: 100 })
  vi.spyOn(api, 'get').mockImplementation(async (url) => ({
    data: {
      success: true,
      data:
        url === '/api/gpt-hosting/admin'
          ? { grants: [], instances }
          : { instances: [], models: [] },
    },
  }))
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  clients.push(client)
  render(
    <QueryClientProvider client={client}>
      <HostingResources open onOpenChange={() => {}} />
    </QueryClientProvider>
  )
}
afterEach(() => {
  cleanup()
  clients.splice(0).forEach((client) => client.clear())
  vi.restoreAllMocks()
  useAuthStore.getState().auth.reset()
})

it('shows draining capacity while preventing new inventory publication', async () => {
  renderResources([
    {
      id: 'node',
      name: 'Node A',
      available: true,
      draining: true,
      slots: [
        {
          id: 'new',
          name: 'Empty new',
          platform: 'codex',
          empty: true,
          exit_ready: true,
          managed: true,
          enabled: false,
          state: 'inventory',
        },
        {
          id: 'published',
          name: 'Published',
          platform: 'codex',
          empty: true,
          exit_ready: true,
          managed: true,
          enabled: true,
          state: 'inventory',
        },
      ],
    },
  ])
  expect(await screen.findByText('New assignments stopped')).toBeInTheDocument()
  expect(
    screen.getByRole('button', { name: 'Allow self-service assignment' })
  ).toBeDisabled()
  expect(
    screen.getByRole('button', { name: 'Stop new assignments' })
  ).toBeEnabled()
  const total = screen.getByText('Total slots').parentElement
  if (!total) throw new Error('Total slots must have a definition container')
  expect(within(total).getByText('2')).toBeInTheDocument()
  expect(
    screen.getByText(
      'Slot counts are snapshots, not available stock. Orders and account reservations are checked again during assignment.'
    )
  ).toBeInTheDocument()
})

it('does not present unavailable node capacity as zero', async () => {
  renderResources([
    {
      id: 'node',
      name: 'Offline',
      available: false,
      draining: false,
      slots: [],
    },
  ])
  expect(await screen.findByText('Instance unavailable')).toBeInTheDocument()
  expect(screen.queryByText('Total slots')).not.toBeInTheDocument()
  expect(screen.queryByText('No slots yet')).not.toBeInTheDocument()
})

it('explains how to register the first node when the registry is empty', async () => {
  renderResources([])
  expect(
    await screen.findByText('No hosting nodes registered')
  ).toBeInTheDocument()
  expect(
    screen.getByText(
      'Register a node in the server configuration before preparing slots.'
    )
  ).toBeInTheDocument()
})

it('hides stale capacity when refreshing the registry fails', async () => {
  renderResources([{ id: 'node', name: 'Node A', available: true, slots: [] }])
  expect(await screen.findByText('Total slots')).toBeInTheDocument()
  vi.mocked(api.get).mockRejectedValue(new Error('unavailable'))
  fireEvent.click(screen.getByRole('button', { name: 'Refresh' }))
  expect(
    await screen.findByText('Unable to load resources')
  ).toBeInTheDocument()
  expect(screen.queryByText('Total slots')).not.toBeInTheDocument()
  expect(screen.queryByText('Accepting new accounts')).not.toBeInTheDocument()
})
