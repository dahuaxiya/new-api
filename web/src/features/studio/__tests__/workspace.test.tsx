/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

import { ModelStudio } from '..'
import { imageDataURL } from '../api'
import { readHistory, saveHistory } from '../studio-session'

const streamSend = vi.hoisted(() =>
  vi.fn(
    async (
      _payload: unknown,
      _update: (kind: 'content', text: string) => void,
      complete: () => void,
      _error: (text: string) => void,
      _headers: Record<string, string>
    ) => {
      _update('content', 'Received')
      complete()
    }
  )
)
vi.mock('@/features/playground/hooks/use-stream-request', () => ({
  useStreamRequest: () => ({
    sendStreamRequest: streamSend,
    stopStream: vi.fn(),
  }),
}))
const clients: QueryClient[] = []
const price = {
  input: '1',
  output: '2',
  cache_read: '0',
  cache_write: '0',
  cache_write_1h: '0',
}
const png =
  'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aD1sAAAAASUVORK5CYII='

/** Replace only HTTP responses; render the real consent, form and result components. */
function mount(
  ready: boolean,
  options: {
    paused?: boolean
    noPlan?: boolean
    noRoute?: boolean
    userId?: number
    root?: boolean
    secondGroup?: boolean
    automatic?: boolean
  } = {}
) {
  useAuthStore.getState().auth.setUser({
    id: options.userId ?? 1,
    username: 'studio-user',
    role: options.root ? 100 : 1,
  })
  vi.spyOn(api, 'get').mockImplementation(async (url) => {
    let data: unknown = {
      token_id: ready ? 9 : 0,
      defaults: { max_request_cny: '5', daily_budget_cny: '50' },
    }
    if (url.endsWith('/catalog')) {
      data = {
        enabled: !options.paused,
        version: 1,
        products: options.noPlan
          ? []
          : [
              {
                id: 'standard',
                name: 'Standard',
                models: options.noRoute
                  ? []
                  : [
                      {
                        id: 'text-model',
                        kind: 'text',
                        tiers: [{ up_to: 1000, prices: price }],
                        max_output: 4096,
                      },
                      {
                        id: 'image-model',
                        kind: 'image',
                        tiers: [{ up_to: 1000, prices: price }],
                        max_output: 4096,
                      },
                    ],
              },
              ...(options.secondGroup
                ? [
                    {
                      id: 'premium',
                      name: 'Premium',
                      models: [
                        {
                          id: 'premium-text',
                          kind: 'text',
                          tiers: [{ up_to: 1000, prices: price }],
                          max_output: 4096,
                        },
                      ],
                    },
                  ]
                : []),
            ],
      }
    }
    if (url.endsWith('/pricing')) {
      data = {
        revision: 1,
        policy: {
          studio_current: options.automatic,
          product_id: 'standard',
          accept_floating: true,
          caps: { 'text-model': price, 'image-model': price },
          max_request_cny: '1',
          daily_budget_cny: '5',
        },
      }
    }
    return { data: { success: true, data } }
  })
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  clients.push(client)
  return render(
    <QueryClientProvider client={client}>
      <ModelStudio />
    </QueryClientProvider>
  )
}

afterEach(() => {
  cleanup()
  window.sessionStorage.clear()
  window.localStorage.clear()
  clients.splice(0).forEach((client) => client.clear())
  vi.restoreAllMocks()
  streamSend.mockClear()
  useAuthStore.getState().auth.reset()
})

/** 窄屏回车保留换行，只有显式点击发送才能启动付费对话。 */
it('keeps Enter for new lines on a phone and sends with the button', async () => {
  vi.stubGlobal('innerWidth', 375)
  try {
    mount(false)
    fireEvent.change(await screen.findByLabelText('Model'), {
      target: { value: 'text-model' },
    })
    const prompt = screen.getByLabelText('Prompt')
    fireEvent.change(prompt, { target: { value: 'Mobile draft' } })
    expect(fireEvent.keyDown(prompt, { key: 'Enter' })).toBe(true)
    expect(streamSend).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    await waitFor(() => expect(streamSend).toHaveBeenCalledTimes(1))
  } finally {
    vi.unstubAllGlobals()
  }
})

/** First send needs no policy modal; only the real model request initializes defaults. */
it('sends the first chat directly with its selected group and no setup request', async () => {
  const post = vi.spyOn(api, 'post')
  mount(false)
  fireEvent.change(await screen.findByLabelText('Model'), {
    target: { value: 'text-model' },
  })
  fireEvent.change(screen.getByLabelText('Prompt'), {
    target: { value: 'Hello' },
  })
  fireEvent.keyDown(screen.getByLabelText('Prompt'), {
    key: 'Enter',
    shiftKey: true,
  })
  fireEvent.keyDown(screen.getByLabelText('Prompt'), {
    key: 'Enter',
    isComposing: true,
  })
  fireEvent.keyDown(screen.getByLabelText('Prompt'), {
    key: 'Enter',
    keyCode: 229,
  })
  expect(streamSend).not.toHaveBeenCalled()
  fireEvent.keyDown(screen.getByLabelText('Prompt'), { key: 'Enter' })
  await waitFor(() => expect(streamSend).toHaveBeenCalledTimes(1))
  expect(screen.getByLabelText('Prompt')).toHaveValue('')
  expect(streamSend.mock.calls[0][0]).toMatchObject({
    model: 'text-model',
    messages: [{ role: 'user', content: 'Hello' }],
  })
  expect(streamSend.mock.calls[0][4]).toEqual({
    'X-Retail-Product': 'standard',
  })
  expect(screen.queryByRole('dialog')).toBeNull()
  expect(post).not.toHaveBeenCalled()
})

it.each([
  [{ paused: true }, 'Model requests are paused by the site administrator.'],
  [
    { noPlan: true },
    'No call groups are available yet. Contact the site administrator.',
  ],
  [
    { noRoute: true },
    'No model route is currently available. Contact the site administrator.',
  ],
])(
  'explains the blocking site configuration without creating a spending identity: %s',
  async (options, message) => {
    const post = vi.spyOn(api, 'post')
    mount(false, options)
    expect(await screen.findByRole('status')).toHaveTextContent(message)
    expect(screen.getByRole('button', { name: 'Send' })).toBeDisabled()
    expect(
      screen.queryByText(
        'Set your budget and price limits before the first request.'
      )
    ).toBeNull()
    expect(
      screen.queryByRole('link', { name: 'Configure model pricing' })
    ).toBeNull()
    expect(post).not.toHaveBeenCalled()
  }
)

it('shows the root operator where to configure models without enabling requests', async () => {
  const post = vi.spyOn(api, 'post')
  mount(false, { paused: true, root: true })
  expect(
    await screen.findByRole('link', { name: 'Configure model pricing' })
  ).toHaveAttribute('href', '/channels')
  expect(screen.getByRole('button', { name: 'Send' })).toBeDisabled()
  expect(post).not.toHaveBeenCalled()
})

it('filters models by mode, locks a pending image request and displays its downloadable result', async () => {
  let resolve: ((value: unknown) => void) | undefined
  const post = vi.spyOn(api, 'post').mockImplementation(
    () =>
      new Promise((done) => {
        resolve = done
      })
  )
  mount(true)
  await screen.findByRole('tab', { name: 'Image generation' })
  expect(screen.queryByRole('option', { name: 'image-model' })).toBeNull()
  fireEvent.click(screen.getByRole('tab', { name: 'Image generation' }))
  expect(screen.queryByRole('option', { name: 'text-model' })).toBeNull()
  fireEvent.change(screen.getByLabelText('Model'), {
    target: { value: 'image-model' },
  })
  fireEvent.change(screen.getByLabelText('Prompt'), {
    target: { value: 'A quiet garden' },
  })
  fireEvent.click(screen.getByRole('button', { name: 'Generate image' }))
  expect(await screen.findByRole('button', { name: 'Stop' })).toBeEnabled()
  expect(screen.getByRole('tab', { name: 'Chat' })).toHaveAttribute(
    'aria-disabled',
    'true'
  )
  expect(screen.getByLabelText('Model')).toBeDisabled()
  expect(post).toHaveBeenCalledTimes(1)
  expect(post.mock.calls[0][1]).toMatchObject({
    model: 'image-model',
    prompt: 'A quiet garden',
    n: 1,
    stream: false,
  })
  resolve?.({ data: { data: [{ b64_json: png }] } })
  expect(
    await screen.findByRole('img', { name: 'A quiet garden' })
  ).toHaveAttribute('src', `data:image/png;base64,${png}`)
  expect(screen.getByRole('button', { name: 'Download' })).toHaveAttribute(
    'download'
  )
  await waitFor(() =>
    expect(screen.getByRole('button', { name: 'Generate image' })).toBeEnabled()
  )
})

/** Replay an URL-only supplier response without spending money on another generation. */
it('loads the completed supplier image as downloadable bytes without credentials or a second generation', async () => {
  const signal = new AbortController().signal
  const url =
    'https://pub-13fd1d3607a441b4a9d232b3a223f5f6.r2.dev/images/completed.png'
  const get = vi
    .spyOn(globalThis, 'fetch')
    .mockResolvedValue(
      new Response(Uint8Array.from(atob(png), (c) => c.charCodeAt(0)))
    )
  await expect(imageDataURL({ data: [{ url }] }, signal)).resolves.toBe(
    `data:image/png;base64,${png}`
  )
  expect(get).toHaveBeenCalledExactlyOnceWith(url, {
    credentials: 'omit',
    redirect: 'error',
    referrerPolicy: 'no-referrer',
    signal,
  })
  for (const untrusted of [
    'javascript:alert(1)',
    'https://example.org/images/a.png',
    url.replace('https://', 'https://user@'),
  ]) {
    await expect(
      imageDataURL({ data: [{ url: untrusted }] }, signal)
    ).rejects.toThrow()
  }
  expect(get).toHaveBeenCalledTimes(1)
  await expect(
    imageDataURL({ data: [{ b64_json: 'PHN2Zz4=' }] }, signal)
  ).rejects.toThrow()
  await expect(imageDataURL({ data: { b64_json: png } }, signal)).resolves.toBe(
    `data:image/png;base64,${png}`
  )
})

/** Each tab sends its own group without overwriting the shared budget policy. */
it('sends a newly selected group without reusing its previous model or saving a policy', async () => {
  const put = vi.spyOn(api, 'put')
  mount(true, { secondGroup: true, automatic: true })
  fireEvent.change(await screen.findByLabelText('Model'), {
    target: { value: 'text-model' },
  })
  fireEvent.change(screen.getByLabelText('Call group'), {
    target: { value: 'premium' },
  })
  expect(screen.getByLabelText('Model')).toHaveValue('')
  fireEvent.change(screen.getByLabelText('Model'), {
    target: { value: 'premium-text' },
  })
  fireEvent.change(screen.getByLabelText('Prompt'), {
    target: { value: 'Hello' },
  })
  fireEvent.click(screen.getByRole('button', { name: 'Send' }))
  await waitFor(() => expect(streamSend).toHaveBeenCalledTimes(1))
  expect(streamSend.mock.calls[0][4]).toEqual({
    'X-Retail-Product': 'premium',
  })
  expect(put).not.toHaveBeenCalled()
})

/** The workspace follows the committed group, including changes made inside the policy dialog. */
it('updates the visible group only after saving and leaves browsing selection unchanged on cancel', async () => {
  vi.spyOn(api, 'post').mockResolvedValue({
    data: { success: true, data: { token_id: 9 } },
  })
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true } })
  mount(true, { secondGroup: true })
  fireEvent.change(await screen.findByLabelText('Call group'), {
    target: { value: 'premium' },
  })
  fireEvent.click(screen.getByRole('button', { name: 'Spending protection' }))
  let dialog = await screen.findByRole('dialog')
  await within(dialog).findByLabelText('Call group')
  fireEvent.click(within(dialog).getByRole('button', { name: 'Close' }))
  await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
  expect(screen.getByLabelText('Call group')).toHaveValue('premium')
  expect(put).not.toHaveBeenCalled()
  fireEvent.click(screen.getByRole('button', { name: 'Spending protection' }))
  dialog = await screen.findByRole('dialog')
  fireEvent.change(await within(dialog).findByLabelText('Call group'), {
    target: { value: 'standard' },
  })
  expect(within(dialog).queryByText('Advanced model price limits')).toBeNull()
  expect(within(dialog).queryByRole('switch')).toBeNull()
  fireEvent.click(within(dialog).getByRole('button', { name: 'Save changes' }))
  await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
  expect(screen.getByLabelText('Call group')).toHaveValue('standard')
  expect(screen.getByLabelText('Model')).toHaveValue('')
  expect(put).toHaveBeenCalledWith(
    '/api/retail/keys/9/pricing',
    expect.objectContaining({
      policy: expect.objectContaining({
        product_id: 'standard',
        studio_current: true,
        caps: {},
        max_request_cny: '1',
        daily_budget_cny: '5',
      }),
    })
  )
})

/** A fresh mount reads the last selection and completed turns; New conversation clears only its history. */
it('resumes a premium conversation after remount and starts a new one with the same model', async () => {
  const options = { secondGroup: true }
  const first = mount(true, options)
  fireEvent.change(await screen.findByLabelText('Call group'), {
    target: { value: 'premium' },
  })
  fireEvent.change(screen.getByLabelText('Model'), {
    target: { value: 'premium-text' },
  })
  fireEvent.change(screen.getByLabelText('Prompt'), {
    target: { value: 'Remember Ada' },
  })
  fireEvent.click(screen.getByRole('button', { name: 'Send' }))
  await screen.findByText('Remember Ada', { selector: 'p' })
  first.unmount()
  const second = mount(true, options)
  expect(await screen.findByLabelText('Call group')).toHaveValue('premium')
  expect(screen.getByLabelText('Model')).toHaveValue('premium-text')
  expect(screen.getByText('Remember Ada')).toBeInTheDocument()
  fireEvent.change(screen.getByLabelText('Prompt'), {
    target: { value: 'Who?' },
  })
  fireEvent.click(screen.getByRole('button', { name: 'Send' }))
  await waitFor(() => expect(streamSend).toHaveBeenCalledTimes(2))
  expect(streamSend.mock.calls[1][0]).toMatchObject({
    messages: [
      { role: 'user', content: 'Remember Ada' },
      { role: 'assistant', content: 'Received' },
      { role: 'user', content: 'Who?' },
    ],
  })
  fireEvent.click(screen.getByRole('button', { name: 'New conversation' }))
  second.unmount()
  mount(true, options)
  expect(await screen.findByLabelText('Model')).toHaveValue('premium-text')
  expect(screen.queryByText('Remember Ada')).toBeNull()
})

/** Failed submissions stay editable while failed turns are excluded from later requests. */
it('restores a failed prompt and retries only on the next manual send', async () => {
  streamSend.mockImplementationOnce(async (_p, _u, _c, error) =>
    error('Temporary failure')
  )
  mount(true)
  fireEvent.change(await screen.findByLabelText('Model'), {
    target: { value: 'text-model' },
  })
  fireEvent.change(screen.getByLabelText('Prompt'), {
    target: { value: 'Retry me' },
  })
  fireEvent.click(screen.getByRole('button', { name: 'Send' }))
  expect(await screen.findByText('Temporary failure')).toBeVisible()
  expect(screen.getByLabelText('Prompt')).toHaveValue('Retry me')
  fireEvent.click(screen.getByRole('button', { name: 'Send' }))
  await waitFor(() => expect(streamSend).toHaveBeenCalledTimes(2))
  expect(streamSend.mock.calls[1][0]).toMatchObject({
    messages: [{ role: 'user', content: 'Retry me' }],
  })
})

/** Leaving mid-generation preserves the prompt for its owner without sending it a second time. */
it('restores an interrupted message for its owner only', async () => {
  streamSend.mockImplementationOnce(async () => {})
  const first = mount(true)
  fireEvent.change(await screen.findByLabelText('Model'), {
    target: { value: 'text-model' },
  })
  fireEvent.change(screen.getByLabelText('Prompt'), {
    target: { value: 'Private message' },
  })
  fireEvent.click(screen.getByRole('button', { name: 'Send' }))
  await screen.findByRole('button', { name: 'Stop' })
  expect(screen.getByLabelText('Prompt')).toHaveValue('')
  first.unmount()
  const other = mount(true, { userId: 2 })
  expect(await screen.findByLabelText('Model')).toHaveValue('')
  expect(screen.queryByText('Private message')).toBeNull()
  other.unmount()
  mount(true)
  expect(await screen.findByText('Private message')).toBeVisible()
  expect(screen.getByRole('alert')).toBeVisible()
  expect(streamSend).toHaveBeenCalledTimes(1)
})

/** 同组新建后关页重开，两段聊天均可取回；只在用户继续发送时提交原分组上下文。 */
it('reopens an archived conversation in its original group after closing the tab', async () => {
  const options = { secondGroup: true }
  const tab = mount(true, options)
  fireEvent.change(await screen.findByLabelText('Call group'), {
    target: { value: 'premium' },
  })
  fireEvent.change(screen.getByLabelText('Model'), {
    target: { value: 'premium-text' },
  })
  for (const prompt of ['First topic', 'Second topic']) {
    fireEvent.change(screen.getByLabelText('Prompt'), {
      target: { value: prompt },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    await screen.findByText('Received')
    fireEvent.click(screen.getByRole('button', { name: 'New conversation' }))
  }
  tab.unmount()
  sessionStorage.clear()
  const other = mount(true, { userId: 2 })
  fireEvent.click(await screen.findByRole('button', { name: 'Chat history' }))
  expect(await screen.findByText('No saved conversations yet.')).toBeVisible()
  other.unmount()
  mount(true, options)
  fireEvent.click(await screen.findByRole('button', { name: 'Chat history' }))
  const list = within(await screen.findByRole('dialog'))
  expect(list.getByRole('button', { name: /Second topic/ })).toBeVisible()
  fireEvent.click(list.getByRole('button', { name: /First topic/ }))
  expect(
    await screen.findByText('First topic', { selector: 'p' })
  ).toBeVisible()
  expect(screen.getByLabelText('Call group')).toHaveValue('premium')
  expect(screen.getByLabelText('Model')).toHaveValue('premium-text')
  expect(streamSend).toHaveBeenCalledTimes(2)
  fireEvent.change(screen.getByLabelText('Prompt'), {
    target: { value: 'Continue' },
  })
  fireEvent.click(screen.getByRole('button', { name: 'Send' }))
  await waitFor(() => expect(streamSend).toHaveBeenCalledTimes(3))
  expect(streamSend.mock.calls[2][0]).toMatchObject({
    messages: [
      { role: 'user', content: 'First topic' },
      { role: 'assistant', content: 'Received' },
      { role: 'user', content: 'Continue' },
    ],
  })
  expect(readHistory('studio.v1:1')).toHaveLength(2)
})

/** 清理最久未更新的会话；单条超额不能删除已有历史，同源分支也不能相互覆盖。 */
it('bounds retained history and keeps separate continuations of the same version', async () => {
  const key = 'studio.v1:test'
  const now = Date.now()
  const turn = {
    id: 1,
    model: 'm',
    prompt: 'message',
    content: 'reply',
    reasoning: '',
    complete: true,
  }
  const base = {
    id: 'parent',
    product: 'group',
    updatedAt: now,
    entries: [turn],
  }
  await saveHistory(key, base)
  await Promise.all(
    ['left', 'right'].map((id) => saveHistory(key, { ...base, id }, base.id))
  )
  expect(
    readHistory(key)
      .map((row) => row.id)
      .sort()
  ).toEqual(['left', 'right'])
  for (let i = 1; i <= 20; i++) {
    await saveHistory(key, { ...base, id: String(i), updatedAt: now + i })
  }
  expect(readHistory(key)).toHaveLength(20)
  expect(readHistory(key).at(-1)?.id).toBe('1')
  await expect(
    saveHistory(key, {
      ...base,
      id: 'big',
      updatedAt: now + 25,
      entries: [{ ...turn, content: 'x'.repeat(5 * 1024 * 1024) }],
    })
  ).rejects.toThrow()
  expect(readHistory(key)).toHaveLength(20)
  vi.spyOn(Date, 'now').mockReturnValue(now + 31 * 24 * 60 * 60 * 1000)
  expect(readHistory(key)).toHaveLength(0)
})

/** 升级尚未关闭的旧标签后，即使关页也能从历史中打开当时的消息。 */
it('archives legacy tab transcripts without replaying them', async () => {
  sessionStorage.setItem(
    'studio.v1:1',
    JSON.stringify({ mode: 'text', product: 'standard', model: 'text-model' })
  )
  sessionStorage.setItem(
    'studio.v1:1:chat:standard',
    JSON.stringify([
      {
        id: 1,
        prompt: 'Before upgrade',
        model: 'text-model',
        reasoning: '',
        content: 'Original reply',
        complete: true,
      },
    ])
  )
  const first = mount(true)
  await screen.findByText('Original reply')
  first.unmount()
  sessionStorage.clear()
  mount(true)
  fireEvent.click(await screen.findByRole('button', { name: 'Chat history' }))
  fireEvent.click(await screen.findByRole('button', { name: /Before upgrade/ }))
  expect(await screen.findByText('Original reply')).toBeVisible()
  expect(streamSend).not.toHaveBeenCalled()
})
