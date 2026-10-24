import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'

import { useDataTable } from '@/components/data-table'
import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

import type { HostingSummary } from '../../hosting-api'
import type { User } from '../../types'
import { HostingCell } from '../hosting-cells'
import { HostingTermNotice } from '../hosting-term-notice'
import { useUsersColumns } from '../users-columns'
import { UsersMobileCard } from '../users-mobile-card'
import { UsersProvider, useUsers } from '../users-provider'

const clients: QueryClient[] = []
/** 每个用例创建独立查询缓存和管理员状态，测试结束时清理。 */
function renderWorkspace(children: React.ReactNode) {
  useAuthStore.getState().auth.setUser({ id: 1, username: 'root', role: 100 })
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  clients.push(client)
  return render(
    <QueryClientProvider client={client}>
      <UsersProvider>{children}</UsersProvider>
    </QueryClientProvider>
  )
}

afterEach(() => {
  cleanup()
  clients.splice(0).forEach((c) => c.clear())
  useAuthStore.getState().auth.reset()
  vi.restoreAllMocks()
})

it('未设期限提示使用服务端总数，点击后交给列表切换筛选', async () => {
  const get = vi
    .spyOn(api, 'get')
    .mockResolvedValue({
      data: { success: true, data: { total: 27, items: [{ id: 2 }] } },
    })
  const show = vi.fn()
  renderWorkspace(<HostingTermNotice onShowUndated={show} />)
  expect(
    await screen.findByText('27 users have no hosting term')
  ).toBeInTheDocument()
  expect(get.mock.calls[0][0]).toContain('hosting=undated')
  fireEvent.click(screen.getByRole('button', { name: 'View and set terms' }))
  expect(show).toHaveBeenCalledOnce()
})

it('没有未设期限用户时不显示需要处理的提示', async () => {
  const get = vi
    .spyOn(api, 'get')
    .mockResolvedValue({
      data: { success: true, data: { total: 0, items: [] } },
    })
  renderWorkspace(<HostingTermNotice onShowUndated={vi.fn()} />)
  await waitFor(() => expect(get).toHaveBeenCalledOnce())
  expect(screen.queryByRole('complementary')).not.toBeInTheDocument()
})

it('统计请求失败时不显示错误的全站数量', async () => {
  const get = vi.spyOn(api, 'get').mockRejectedValue(new Error('unavailable'))
  renderWorkspace(<HostingTermNotice onShowUndated={vi.fn()} />)
  await waitFor(() => expect(get).toHaveBeenCalledOnce())
  expect(screen.queryByRole('complementary')).not.toBeInTheDocument()
})

/** 使用真实用户列和管理上下文，验证手机卡片未丢失管理入口。 */
function MobileFixture() {
  const columns = useUsersColumns()
  const user: User = {
    id: 2,
    username: 'user-with-a-long-name-for-mobile',
    display_name: 'Example',
    role: 1,
    status: 1,
    group: 'default',
    quota: 100,
    used_quota: 0,
    request_count: 0,
    hosting: { user_id: 2, status: 'undated', source: 'legacy' },
  }
  const { table } = useDataTable({
    data: [user],
    columns,
    initialColumnVisibility: { role: false, group: false },
  })
  const { currentRow, open } = useUsers()
  return (
    <>
      <UsersMobileCard row={table.getRowModel().rows[0]} />
      <output>
        {open === 'update' ? `Editing user ${currentRow?.id}` : ''}
      </output>
    </>
  )
}

it('手机卡片显示真实状态并通过原管理入口选中正确用户', () => {
  renderWorkspace(<MobileFixture />)
  expect(
    screen.getByRole('article', { name: 'user-with-a-long-name-for-mobile' })
  ).toBeInTheDocument()
  expect(screen.getByText('Hosting term not set')).toBeInTheDocument()
  fireEvent.click(screen.getByRole('button', { name: /^Manage$/ }))
  expect(screen.getByText('Editing user 2')).toBeInTheDocument()
})

it('过期和未知官方读数不绘制可用额度条，已知零余额仍显示 0%', () => {
  const summary = {
    user_id: 2,
    status: 'active',
    quota: {
      bound: true,
      windows: [
        { id: '5h', remaining_percent: 0, reset_at: '2099-01-01T00:00:00Z' },
        { id: '7d', remaining_percent: 98, reset_at: '2000-01-01T00:00:00Z' },
      ],
    },
  } as HostingSummary
  const view = render(<HostingCell kind='quota' summary={summary} />)
  expect(screen.getAllByRole('progressbar')).toHaveLength(1)
  expect(screen.getByText('0%')).toBeInTheDocument()
  expect(screen.getByText('Unknown')).toBeInTheDocument()
  view.rerender(<HostingCell kind='quota' />)
  expect(screen.queryByRole('progressbar')).not.toBeInTheDocument()
  expect(screen.getByText('Hosting status unavailable')).toBeInTheDocument()
})
