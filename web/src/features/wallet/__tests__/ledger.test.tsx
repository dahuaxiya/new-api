import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  createMemoryHistory,
  createRootRoute,
  createRouter,
  RouterProvider,
} from '@tanstack/react-router'
import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

import { WalletLedger } from '../wallet-ledger'

/** 用户只看到一次消费，能展开看退回，并从余额卡定位跨日未结算请求。 */
it('shows the final charge once and opens all outstanding holds from the balance card', async () => {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const auth = useAuthStore.getState().auth
  useAuthStore.setState({
    auth: { ...auth, user: { id: 1, username: 'tester', role: 1 } },
  })
  const common = {
    after: 93611394,
    at: 1795575907,
    source: 5,
    key: 'request',
    reference: 'request',
  }
  vi.spyOn(api, 'get').mockImplementation(async (_, config) => ({
    data: {
      success: true,
      data: {
        available: 93611394,
        today: 9350,
        pending: 73050000,
        quota_per_unit: 500000,
        more: false,
        events: [
          config?.params.view === 'pending'
            ? {
                ...common,
                kind: 'pending',
                delta: -73050000,
                held: 73050000,
                actual: null,
              }
            : {
                ...common,
                kind: 'settle',
                delta: -9350,
                actual: 9350,
                held: 75288,
              },
        ],
      },
    },
  }))
  const router = createRouter({
    routeTree: createRootRoute({ component: WalletLedger }),
    history: createMemoryHistory(),
  })
  const page = render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
  const user = userEvent.setup()
  try {
    await user.click(
      await screen.findByRole('button', { name: /Spending.*-¥0\.0187/ })
    )
    const sheet = await screen.findByRole('dialog')
    // 详情按「预留 − 退回 = 实扣」排列，运算符和金额要一起出现，用户才能直接对上账。
    expect(within(sheet).getByText('¥0.150576')).toBeVisible()
    expect(within(sheet).getByText('− ¥0.131876')).toBeVisible()
    expect(within(sheet).getByText('= ¥0.0187')).toBeVisible()
    expect(within(sheet).getByText('¥0.0187')).toBeVisible()
    await user.keyboard('{Escape}')
    await user.click(
      screen.getByRole('button', { name: 'Awaiting settlement ¥146.10' })
    )
    expect(
      await screen.findByText('Outstanding holds across all dates')
    ).toBeVisible()
    expect(
      await screen.findByRole('button', {
        name: /Awaiting settlement.*¥146\.1.*¥187\.22/,
      })
    ).toBeVisible()
    expect(
      screen.queryByRole('button', { name: /Spending.*-¥0\.0187/ })
    ).toBeNull()
  } finally {
    page.unmount()
    client.clear()
    useAuthStore.setState({ auth })
  }
})
