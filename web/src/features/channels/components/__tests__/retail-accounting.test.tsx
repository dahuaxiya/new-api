import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

import { RetailAccounting } from '../retail-accounting'

const clients: QueryClient[] = []

/** 每个场景独立设置身份与站点；只在 HTTP 边界提供账本响应。 */
function setup(role = 100, site = 'retail') {
  useAuthStore.getState().auth.setUser({ id: 1, username: 'root', role })
  const client = new QueryClient({
    defaultOptions: {
      queries: { retry: false, staleTime: Infinity },
      mutations: { retry: false },
    },
  })
  client.setQueryData(['status'], { site_mode: site })
  clients.push(client)
  return render(
    <QueryClientProvider client={client}>
      <RetailAccounting />
    </QueryClientProvider>
  )
}

/** 两类游标同时出现，用来捕获误把另一账本向后翻页的问题。 */
function response(id = 'req-first', age = 90000) {
  return {
    data: {
      success: true,
      data: {
        charges: [
          {
            id,
            user_id: 8,
            token_id: 3,
            reserved: 1000,
            actual: 0,
            status: 'reserved',
            updated_at: Date.now() / 1000 - age,
          },
        ],
        attempts: [
          {
            id: 4,
            request_id: 'req-attempt',
            channel_id: 2,
            state: 'failed',
            status_code: 403,
            cost_state: 'unknown',
          },
        ],
        costs: [],
        next: { charge_before: 100, charge_id: id, attempt_before: 4 },
      },
    },
  }
}

afterEach(() => {
  cleanup()
  clients.splice(0).forEach((client) => client.clear())
  vi.restoreAllMocks()
  useAuthStore.getState().auth.reset()
  localStorage.clear()
})

/** 普通用户及托管站都不加载运营账本。 */
it.each([
  [1, 'retail'],
  [100, 'hosting'],
])('does not expose accounting for role %s on %s', (role, site) => {
  const get = vi.spyOn(api, 'get')
  setup(role as number, site as string)
  expect(
    screen.queryByRole('button', { name: 'Retail accounting' })
  ).not.toBeInTheDocument()
  expect(get).not.toHaveBeenCalled()
})

/** 翻页和请求筛选来自真实按钮交互，不共享三个账本的游标。 */
it('paginates each ledger independently and resets cursors for exact lookup', async () => {
  const get = vi.spyOn(api, 'get').mockResolvedValue(response())
  setup()
  fireEvent.click(screen.getByRole('button', { name: 'Retail accounting' }))
  await screen.findByRole('table', { name: 'Customer charges' })
  fireEvent.click(screen.getByRole('button', { name: 'Next page' }))
  await waitFor(() =>
    expect(get).toHaveBeenLastCalledWith(
      '/api/retail/admin/accounting',
      expect.objectContaining({
        params: {
          request_id: undefined,
          charge_before: 100,
          charge_id: 'req-first',
        },
      })
    )
  )
  fireEvent.click(screen.getByRole('tab', { name: 'Upstream attempts' }))
  await screen.findByText('Unknown cost')
  expect(get).toHaveBeenLastCalledWith(
    '/api/retail/admin/accounting',
    expect.objectContaining({ params: { request_id: undefined } })
  )
  fireEvent.click(screen.getByRole('button', { name: 'Next page' }))
  await waitFor(() =>
    expect(get).toHaveBeenLastCalledWith(
      '/api/retail/admin/accounting',
      expect.objectContaining({
        params: { request_id: undefined, attempt_before: 4 },
      })
    )
  )
  fireEvent.click(screen.getByRole('tab', { name: 'Customer charges' }))
  fireEvent.change(screen.getByLabelText('Exact request ID'), {
    target: { value: 'req-exact' },
  })
  fireEvent.click(screen.getByRole('button', { name: 'Search' }))
  await waitFor(() =>
    expect(get).toHaveBeenLastCalledWith(
      '/api/retail/admin/accounting',
      expect.objectContaining({ params: { request_id: 'req-exact' } })
    )
  )
  await waitFor(() =>
    expect(screen.getByRole('button', { name: 'Next page' })).toBeDisabled()
  )
})

/** 刷新失败时不继续把旧账单当作当前状态，重试后重新显示。 */
it('hides stale accounting after a refresh failure and supports retry', async () => {
  const get = vi.spyOn(api, 'get').mockResolvedValue(response())
  setup()
  fireEvent.click(screen.getByRole('button', { name: 'Retail accounting' }))
  await screen.findByText('req-first')
  get.mockRejectedValueOnce(new Error('unavailable'))
  fireEvent.click(screen.getByRole('button', { name: 'Refresh' }))
  await screen.findByText('Unable to load resources')
  expect(screen.queryByText('req-first')).not.toBeInTheDocument()
  fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
  await screen.findByText('req-first')
})

/** 不到 24 小时的预留不能在页面上提前释放。 */
it('disables release for a recent reservation', async () => {
  vi.spyOn(api, 'get').mockResolvedValue(response('req-active', 60))
  setup()
  fireEvent.click(screen.getByRole('button', { name: 'Retail accounting' }))
  expect(
    await screen.findByRole('button', { name: 'Release reservation' })
  ).toBeDisabled()
})

/** 释放失败保留原因和弹窗；成功前不改显示金额，成功才重新读取账本。 */
it('keeps review details on release failure and submits the observed reservation', async () => {
  vi.spyOn(api, 'get').mockResolvedValue(response())
  const post = vi
    .spyOn(api, 'post')
    .mockResolvedValueOnce({
      data: { success: false, message: 'Reservation changed' },
    })
    .mockResolvedValue({ data: { success: true } })
  setup()
  fireEvent.click(screen.getByRole('button', { name: 'Retail accounting' }))
  fireEvent.click(
    await screen.findByRole('button', { name: 'Release reservation' })
  )
  expect(screen.getByRole('button', { name: 'Confirm release' })).toBeDisabled()
  fireEvent.change(screen.getByLabelText('Review reason (5–500 characters)'), {
    target: { value: 'Verified no upstream charge' },
  })
  await waitFor(() =>
    expect(
      screen.getByRole('button', { name: 'Confirm release' })
    ).toBeEnabled()
  )
  fireEvent.click(screen.getByRole('button', { name: 'Confirm release' }))
  await waitFor(() =>
    expect(post).toHaveBeenCalledWith(
      '/api/retail/admin/charges/req-first/refund',
      { expected_reserved: 1000, reason: 'Verified no upstream charge' }
    )
  )
  await waitFor(() =>
    expect(
      screen.getByRole('button', { name: 'Confirm release' })
    ).toBeEnabled()
  )
  expect(screen.getByLabelText('Review reason (5–500 characters)')).toHaveValue(
    'Verified no upstream charge'
  )
  fireEvent.click(screen.getByRole('button', { name: 'Confirm release' }))
  await waitFor(() =>
    expect(
      screen.queryByRole('button', { name: 'Confirm release' })
    ).not.toBeInTheDocument()
  )
})

/** 身份变化立即卸载旧账单及确认操作，不能让新用户看到之前的运营记录。 */
it('clears the open ledger when the signed-in user changes', async () => {
  vi.spyOn(api, 'get').mockResolvedValue(response())
  setup()
  fireEvent.click(screen.getByRole('button', { name: 'Retail accounting' }))
  await screen.findByText('req-first')
  act(() =>
    useAuthStore
      .getState()
      .auth.setUser({ id: 2, username: 'next-root', role: 100 })
  )
  expect(screen.queryByText('req-first')).not.toBeInTheDocument()
  expect(
    screen.getByRole('button', { name: 'Retail accounting' })
  ).toBeInTheDocument()
})
