import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from '@testing-library/react'
import { Toaster, toast } from 'sonner'
import { afterEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'

import {
  UnifiedAccountNotice,
  UnifiedAccountSignIn,
} from '../components/unified-account'

afterEach(() => {
  cleanup()
  toast.dismiss()
  vi.restoreAllMocks()
})

/** 等待登录事务时显示明确状态且只发一次请求，失败后可重试。 */
it('announces the pending sign-in and restores the action after a failed request', async () => {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      data: { site_sso: { issuer_url: 'https://host.example.test' } },
    },
  })
  let rejectRequest: (error: Error) => void = () => undefined
  const post = vi.spyOn(api, 'post').mockImplementation(
    () =>
      new Promise((_resolve, reject) => {
        rejectRequest = reject
      })
  )
  render(
    <QueryClientProvider client={client}>
      <Toaster />
      <UnifiedAccountSignIn />
    </QueryClientProvider>
  )
  await waitFor(() =>
    expect(screen.getByRole('link', { name: 'Sign up' })).toHaveAttribute(
      'href',
      'https://host.example.test/sign-up'
    )
  )
  const button = screen.getByRole('button', {
    name: 'Continue with unified account',
  })
  button.focus()
  fireEvent.click(button)
  fireEvent.click(button)
  expect(button).toHaveFocus()
  expect(screen.getByRole('button', { name: 'Connecting...' })).toHaveAttribute(
    'aria-disabled',
    'true'
  )
  expect(screen.getByRole('status')).toHaveTextContent('Connecting...')
  expect(post).toHaveBeenCalledTimes(1)
  await act(async () => rejectRequest(new Error('Connection failed')))
  expect(
    await screen.findByRole('button', { name: 'Continue with unified account' })
  ).toBeEnabled()
  fireEvent.click(
    screen.getByRole('button', { name: 'Continue with unified account' })
  )
  expect(post).toHaveBeenCalledTimes(2)
  await act(async () => rejectRequest(new Error('Connection failed')))
  client.clear()
})

/** 返回缓存页面后恢复登录按钮，旧请求失败不能取消新一次登录。 */
it('recovers on persisted pageshow and ignores the previous sign-in result', async () => {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      data: { site_sso: { issuer_url: 'https://host.example.test' } },
    },
  })
  const rejects: ((error: Error) => void)[] = []
  const post = vi.spyOn(api, 'post').mockImplementation(
    () =>
      new Promise((_resolve, reject) => {
        rejects.push(reject)
      })
  )
  render(
    <QueryClientProvider client={client}>
      <Toaster />
      <UnifiedAccountSignIn />
    </QueryClientProvider>
  )
  await screen.findByRole('link', { name: 'Sign up' })
  fireEvent.click(
    screen.getByRole('button', { name: 'Continue with unified account' })
  )
  fireEvent(window, new PageTransitionEvent('pageshow', { persisted: true }))
  expect(
    screen.getByRole('button', { name: 'Continue with unified account' })
  ).toBeEnabled()
  fireEvent.click(
    screen.getByRole('button', { name: 'Continue with unified account' })
  )
  expect(post).toHaveBeenCalledTimes(2)
  await act(async () => rejects[0](new Error('Old request failed')))
  expect(screen.getByRole('button', { name: 'Connecting...' })).toHaveAttribute(
    'aria-disabled',
    'true'
  )
  expect(screen.queryByText('Old request failed')).not.toBeInTheDocument()
  await act(async () => rejects[1](new Error('Current request failed')))
  expect(
    screen.getByRole('button', { name: 'Continue with unified account' })
  ).toBeEnabled()
  client.clear()
})

/** 配置未到达时不产生 undefined 链接，也不能创建登录事务。 */
it('waits for issuer configuration before enabling sign-in and account links', async () => {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  vi.spyOn(api, 'get').mockResolvedValue({ data: { success: true, data: {} } })
  const post = vi.spyOn(api, 'post')
  render(
    <QueryClientProvider client={client}>
      <UnifiedAccountSignIn />
      <UnifiedAccountNotice />
    </QueryClientProvider>
  )
  await waitFor(() => expect(client.isFetching()).toBe(0))
  expect(
    screen.getByRole('button', { name: 'Continue with unified account' })
  ).toBeDisabled()
  expect(
    screen.queryByRole('link', { name: 'Sign up' })
  ).not.toBeInTheDocument()
  expect(
    screen.queryByRole('link', { name: 'Forgot password' })
  ).not.toBeInTheDocument()
  expect(post).not.toHaveBeenCalled()
  expect(
    screen.queryByRole('button', { name: 'Manage unified account' })
  ).not.toBeInTheDocument()
  client.clear()
})
