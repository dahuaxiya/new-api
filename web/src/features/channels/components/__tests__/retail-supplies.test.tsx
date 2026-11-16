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

it('adds a disabled group under one site without copying a Key or asking for the address', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      data: {
        state: 'ready',
        group: 'retail',
        items: [
          {
            channel_id: 1,
            site_id: 0,
            name: 'Xingxing',
            base_url: 'https://aixingxing.pro',
            models: 'model-a',
            kind: 'reseller',
            status: 'eligible',
          },
          {
            channel_id: 2,
            site_id: 1,
            name: 'Existing group',
            base_url: 'https://aixingxing.pro',
            kind: 'reseller',
            status: 'disabled',
          },
        ],
      },
    },
  })
  const post = vi
    .spyOn(api, 'post')
    .mockResolvedValue({ data: { success: true, data: { channel_id: 3 } } })
  renderSupplies()
  fireEvent.click(screen.getByRole('button', { name: 'Supply overview' }))
  await screen.findByText('Existing group')
  expect(screen.getAllByText('https://aixingxing.pro')).toHaveLength(1)
  // Expanded forms must scroll at full height instead of shrinking and clipping site cards.
  expect(
    screen.getByText('Xingxing').closest('[data-slot="card"]')
  ).toHaveClass('shrink-0')
  fireEvent.click(screen.getByRole('button', { name: 'Add supply group' }))
  expect(screen.getByLabelText('Group API Key')).toHaveValue('')
  expect(screen.getByLabelText('Group models')).toHaveValue('model-a')
  expect(screen.queryByLabelText(/base url|address/i)).not.toBeInTheDocument()
  fireEvent.change(screen.getByLabelText('Supply group name'), {
    target: { value: 'Pro' },
  })
  fireEvent.click(screen.getByRole('button', { name: 'Create supply group' }))
  await waitFor(() =>
    expect(post).toHaveBeenCalledWith('/api/retail/admin/supplies/groups', {
      site_id: 1,
      name: 'Pro',
      key: '',
      models: 'model-a',
    })
  )
  await waitFor(() =>
    expect(screen.queryByLabelText('Group API Key')).not.toBeInTheDocument()
  )
})

it('keeps a failed group draft open and clears its credential instead of reporting success', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      data: {
        state: 'ready',
        group: 'retail',
        items: [
          {
            channel_id: 1,
            name: 'Xingxing',
            base_url: 'https://aixingxing.pro',
            models: '',
            kind: 'reseller',
            status: 'eligible',
          },
        ],
      },
    },
  })
  vi.spyOn(api, 'post').mockResolvedValue({
    data: { success: false, message: 'Site unavailable' },
  })
  renderSupplies()
  fireEvent.click(screen.getByRole('button', { name: 'Supply overview' }))
  fireEvent.click(
    await screen.findByRole('button', { name: 'Add supply group' })
  )
  fireEvent.change(screen.getByLabelText('Supply group name'), {
    target: { value: 'Pro' },
  })
  fireEvent.change(screen.getByLabelText('Group API Key'), {
    target: { value: 'synthetic-secret' },
  })
  fireEvent.click(screen.getByRole('button', { name: 'Create supply group' }))
  await waitFor(() =>
    expect(screen.getByLabelText('Group API Key')).toHaveValue('')
  )
  expect(screen.getByLabelText('Supply group name')).toHaveValue('Pro')
  fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
  expect(screen.queryByLabelText('Group API Key')).not.toBeInTheDocument()
})

it('does not retain supplier credentials inside cached Axios failures', async () => {
  vi.spyOn(api, 'post').mockRejectedValue({
    response: { data: { success: false, message: 'Site unavailable' } },
    config: { data: 'sensitive-key-only-in-request' },
  })
  vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      data: {
        state: 'ready',
        group: 'retail',
        items: [
          {
            channel_id: 1,
            name: 'Site',
            kind: 'reseller',
            status: 'eligible',
            base_url: 'https://aixingxing.pro',
          },
        ],
      },
    },
  })
  renderSupplies()
  fireEvent.click(screen.getByRole('button', { name: 'Supply overview' }))
  fireEvent.click(
    await screen.findByRole('button', { name: 'Add supply group' })
  )
  fireEvent.change(screen.getByLabelText('Supply group name'), {
    target: { value: 'Pro' },
  })
  fireEvent.change(screen.getByLabelText('Group API Key'), {
    target: { value: 'sensitive-key-only-in-request' },
  })
  fireEvent.click(screen.getByRole('button', { name: 'Create supply group' }))
  await waitFor(() =>
    expect(screen.getByLabelText('Group API Key')).toHaveValue('')
  )
  const failures = clients
    .at(-1)
    ?.getMutationCache()
    .getAll()
    .map((mutation) => mutation.state.error)
  expect(failures?.[0]).toHaveProperty('message', 'Site unavailable')
  expect(JSON.stringify(failures)).not.toContain(
    'sensitive-key-only-in-request'
  )
})
