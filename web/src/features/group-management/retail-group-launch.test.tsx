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

import { RetailGroupLaunch } from './retail-group-launch'

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
  useAuthStore.getState().auth.reset()
})

/** 新 Claude Key 自动读取模型。冲突后保留用户输入，重新预览才能提交。 */
it('previews a new Claude credential and retains the draft after a conflicting publish', async () => {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Infinity } },
  })
  client.setQueryData(['status'], { site_mode: 'retail' })
  useAuthStore.getState().auth.setUser({ id: 1, username: 'root', role: 100 })
  const state = {
    version: 12,
    local_groups: [],
    pricing_enabled: true,
    channels: [
      {
        id: 2,
        site_id: 2,
        name: 'Anthropic',
        type: 14,
        base_url: 'https://aixingxing.pro',
      },
    ],
    sale_policy: { margin_bps: 2000 },
    sync: {
      enabled: true,
      snapshot: {
        groups: [{ name: 'Claude', group: 'claude', multiplier: '0.5' }],
      },
    },
  }
  vi.spyOn(api, 'get').mockResolvedValue({
    data: { success: true, data: state },
  })
  const post = vi.spyOn(api, 'post').mockImplementation(async (url, body) => {
    if (url.endsWith('fetch_models')) {
      return { data: { success: true, data: ['known', 'unknown'] } }
    }
    if ((body as { review: string }).review) {
      return {
        data: {
          success: false,
          message: 'Retail pricing changed. Reload before saving.',
        },
      }
    }
    return {
      data: {
        success: true,
        data: {
          version: 12,
          review: 'reviewed',
          rows: [
            { model: 'known', tiers: [] },
            { model: 'unknown', reason: 'official_price_missing_or_expired' },
          ],
        },
      },
    }
  })
  render(
    <QueryClientProvider client={client}>
      <RetailGroupLaunch />
    </QueryClientProvider>
  )
  fireEvent.click(screen.getByRole('button', { name: 'Publish a call group' }))
  fireEvent.change(await screen.findByLabelText('Upstream API Key'), {
    target: { value: 'test-only' },
  })
  fireEvent.change(screen.getByLabelText('Upstream group', { exact: false }), {
    target: { value: 'claude' },
  })
  fireEvent.change(screen.getByLabelText('Call group name'), {
    target: { value: 'claude-normal' },
  })
  fireEvent.click(
    screen.getByRole('button', { name: 'Preview CNY selling prices' })
  )
  await screen.findByText(/unknown.*Official price is unverified or expired/)
  expect(post.mock.calls[1][1]).toMatchObject({
    models: ['known', 'unknown'],
    review: '',
    expected_version: 12,
  })
  fireEvent.click(
    screen.getByRole('button', { name: 'Save and publish group' })
  )
  await screen.findByRole('alert')
  expect(screen.getByLabelText('Call group name')).toHaveValue('claude-normal')
  expect(screen.getByLabelText('Upstream API Key')).toHaveValue('test-only')
  await waitFor(() =>
    expect(
      screen.getByRole('button', { name: 'Preview CNY selling prices' })
    ).toBeEnabled()
  )
  cleanup()
  client.clear()
})
