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
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

import type { User } from '../../types'
import { UserTokensPanel } from '../user-tokens-panel'
import { UsersProvider } from '../users-provider'

const user: User = {
  id: 2,
  username: 'managed-user',
  display_name: '',
  role: 1,
  status: 1,
  quota: 0,
  used_quota: 0,
  request_count: 0,
  group: 'default',
}
const token = {
  id: 9,
  name: 'Codex',
  key: 'test****demo',
  status: 1,
  hosting_seat_id: '',
  created_time: 1,
  accessed_time: 0,
  expired_time: -1,
}
const clients: QueryClient[] = []

/** 只替换网络边界，列表、共享验证和确认对话框使用真实组件。 */
function renderPanel() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  clients.push(client)
  return {
    client,
    ...render(
      <QueryClientProvider client={client}>
        <UsersProvider>
          <UserTokensPanel user={user} onClose={vi.fn()} />
        </UsersProvider>
      </QueryClientProvider>
    ),
  }
}

beforeEach(() => {
  useAuthStore.getState().auth.setUser({ id: 1, username: 'root', role: 100 })
  vi.spyOn(api, 'get').mockImplementation(async (url) => ({
    data: {
      success: true,
      data:
        url === '/api/verify/methods'
          ? {
              scope: 'admin.user.token',
              methods: [{ method: '2fa', available: true }],
              oauth_providers: [],
              password_encryption_enabled: false,
            }
          : { items: [token], total: 1 },
    },
  }))
  vi.spyOn(api, 'post').mockResolvedValue({
    data: {
      success: true,
      data: {
        proof_token: 'test-only-proof',
        method: '2fa',
        scope: 'admin.user.token',
        expires_at: Math.floor(Date.now() / 1000) + 60,
      },
    },
  })
})
afterEach(() => {
  cleanup()
  clients.splice(0).forEach((client) => client.clear())
  useAuthStore.getState().auth.reset()
  vi.restoreAllMocks()
})

/** 验证框必须通过用户输入完成，不能绕过共享安全组件。 */
async function verify() {
  await userEvent.type(
    await screen.findByLabelText('Authenticator code or backup code'),
    '123456'
  )
  await userEvent.click(screen.getByRole('button', { name: 'Verify' }))
}

it('默认掩码，验证后显示且明文不进入查询缓存', async () => {
  const request = vi.spyOn(api, 'request').mockResolvedValue({
    data: { success: true, data: { key: 'sk-fixture-only-secret' } },
  })
  const { client } = renderPanel()
  expect(await screen.findByText('sk-test****demo')).toBeInTheDocument()
  expect(request).not.toHaveBeenCalled()
  await userEvent.click(
    screen.getByRole('button', { name: 'View full API key' })
  )
  expect(request).not.toHaveBeenCalled()
  await verify()
  expect(await screen.findByText('sk-fixture-only-secret')).toBeInTheDocument()
  expect(request).toHaveBeenCalledWith(
    expect.objectContaining({
      url: '/api/user/2/tokens/9/key',
      data: { action: 'view' },
      singleUseAuthorization: true,
      headers: { 'X-Security-Proof': 'test-only-proof' },
    })
  )
  expect(
    JSON.stringify(
      client
        .getQueryCache()
        .getAll()
        .map((query) => query.state.data)
    )
  ).not.toContain('sk-fixture-only-secret')
  await userEvent.click(screen.getByRole('button', { name: 'Hide API key' }))
  expect(screen.queryByText('sk-fixture-only-secret')).not.toBeInTheDocument()
})

it('取消验证不读取密钥，关闭详情中止未完成的请求', async () => {
  let finish: (value: unknown) => void = () => {}
  const request = vi.spyOn(api, 'request').mockImplementation(
    () =>
      new Promise((resolve) => {
        finish = resolve
      })
  )
  const view = renderPanel()
  await userEvent.click(
    await screen.findByRole('button', { name: 'View full API key' })
  )
  await userEvent.click(await screen.findByRole('button', { name: 'Cancel' }))
  expect(request).not.toHaveBeenCalled()
  await userEvent.click(
    screen.getByRole('button', { name: 'View full API key' })
  )
  await verify()
  await waitFor(() => expect(request).toHaveBeenCalledOnce())
  const signal = request.mock.calls[0][0].signal
  view.unmount()
  expect(signal?.aborted).toBe(true)
  await act(async () => {
    finish({ data: { success: true, data: { key: 'sk-late-fixture' } } })
  })
  expect(screen.queryByText('sk-late-fixture')).not.toBeInTheDocument()
})

it('停用必须先确认并验证，再发送指定密钥的停用动作', async () => {
  const request = vi
    .spyOn(api, 'request')
    .mockResolvedValue({ data: { success: true, data: { status: 2 } } })
  renderPanel()
  await userEvent.click(await screen.findByRole('button', { name: 'Disable' }))
  expect(await screen.findByText('Disable API key?')).toBeInTheDocument()
  expect(request).not.toHaveBeenCalled()
  await userEvent.click(
    within(screen.getByRole('alertdialog')).getByRole('button', {
      name: 'Disable',
    })
  )
  await verify()
  await waitFor(() =>
    expect(request).toHaveBeenCalledWith(
      expect.objectContaining({
        method: 'PATCH',
        url: '/api/user/2/tokens/9/status',
        data: { action: 'disable' },
      })
    )
  )
})

it('请求失败可以重试，空列表显示提示', async () => {
  const get = vi.mocked(api.get)
  get.mockRejectedValueOnce(new Error('offline'))
  renderPanel()
  expect(await screen.findByText('Failed to load API keys')).toBeInTheDocument()
  get.mockResolvedValueOnce({
    data: { success: true, data: { items: [], total: 0 } },
  })
  await userEvent.click(screen.getByRole('button', { name: 'Retry' }))
  expect(await screen.findByText('No API keys found')).toBeInTheDocument()
})

it('切到后台和更改筛选后隐藏明文', async () => {
  vi.spyOn(api, 'request').mockResolvedValue({
    data: { success: true, data: { key: 'sk-fixture-only-secret' } },
  })
  renderPanel()
  await userEvent.click(
    await screen.findByRole('button', { name: 'View full API key' })
  )
  await verify()
  await screen.findByText('sk-fixture-only-secret')
  vi.spyOn(document, 'hidden', 'get').mockReturnValue(true)
  fireEvent(document, new Event('visibilitychange'))
  expect(screen.queryByText('sk-fixture-only-secret')).not.toBeInTheDocument()
  vi.spyOn(document, 'hidden', 'get').mockReturnValue(false)
  await userEvent.click(
    screen.getByRole('button', { name: 'View full API key' })
  )
  await verify()
  await screen.findByText('sk-fixture-only-secret')
  fireEvent.change(
    screen.getByRole('textbox', { name: 'Search API key names' }),
    { target: { value: 'Code' } }
  )
  expect(screen.queryByText('sk-fixture-only-secret')).not.toBeInTheDocument()
})

it('状态筛选器显示可读标签，窄屏卡片明确标注最后使用时间', async () => {
  vi.spyOn(window, 'matchMedia').mockImplementation((query) => ({
    matches: query.includes('max-width: 640px'),
    media: query,
    onchange: null,
    addListener: vi.fn(),
    removeListener: vi.fn(),
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
    dispatchEvent: vi.fn(),
  }))
  renderPanel()
  await screen.findByText('sk-test****demo')
  expect(
    screen.getByRole('combobox', { name: 'API key status' })
  ).toHaveTextContent('All statuses')
  expect(screen.queryByRole('table')).not.toBeInTheDocument()
  expect(screen.getByText('Last Used: Never used')).toBeInTheDocument()
})
