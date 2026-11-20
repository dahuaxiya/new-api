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
import { readHistory, saveHistory, type Conversation } from '../studio-session'

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
const imageSend = vi.fn(
  async (_entry: unknown) => `data:image/png;base64,${png}`
)
const server = new Map<string, Conversation>()
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
    loseFirstAck?: boolean
    maxOutput?: number
    paused?: boolean
    noPlan?: boolean
    noRoute?: boolean
    userId?: number
    root?: boolean
    secondGroup?: boolean
    automatic?: boolean
    basicImage?: boolean
  } = {}
) {
  useAuthStore.getState().auth.setUser({
    id: options.userId ?? 1,
    username: 'studio-user',
    role: options.root ? 100 : 1,
  })
  const prefix = `${options.userId ?? 1}/`
  const oldPost = vi.mocked(api.post).getMockImplementation?.()
  vi.spyOn(api, 'post').mockImplementation(async (url, body, config) => {
    if (!url.includes('/runs/')) {
      return oldPost ? oldPost(url, body, config) : { data: { success: true } }
    }
    const payload =
      body instanceof FormData ? JSON.parse(String(body.get('intent'))) : body
    const { record, parent, maxTokens } = payload as {
      record: Conversation
      parent?: string
      maxTokens: number
    }
    if (server.has(prefix + record.id)) return { data: { success: true } }
    const previous = parent && server.get(prefix + parent)
    if (parent) server.delete(prefix + parent)
    const copy = structuredClone(record)
    if (previous && copy.mode === 'image') {
      copy.entries = [...previous.entries, ...copy.entries]
    }
    server.set(prefix + copy.id, copy)
    const entry = copy.entries.at(-1)
    if (!entry) throw new Error('Expected a submitted turn')
    if (copy.mode === 'image') {
      if (body instanceof FormData) {
        entry.reference = `/api/retail/studio/media/${copy.id}/reference`
      }
      void imageSend(entry).then((image) => {
        entry.image = image
        entry.complete = true
        copy.running = false
      })
    } else {
      const messages = copy.entries
        .filter((e) => e === entry || (e.complete && !e.error))
        .flatMap((e) =>
          e === entry
            ? [{ role: 'user', content: e.prompt }]
            : [
                { role: 'user', content: e.prompt },
                { role: 'assistant', content: e.content },
              ]
        )
      await streamSend(
        { messages, model: entry.model, max_tokens: maxTokens },
        (_, chunk) => {
          entry.content += chunk
        },
        () => {
          entry.complete = true
          copy.running = false
        },
        (msg) => {
          entry.error = msg
          copy.running = false
        },
        { 'X-Retail-Product': copy.product }
      )
    }
    if (options.loseFirstAck) {
      throw new Error('Connection lost after acceptance')
    }
    return { data: { success: true } }
  })
  vi.spyOn(api, 'put').mockImplementation(async (url, body, config) => {
    if (url.endsWith('/pricing')) return { data: { success: true } }
    if (!url.includes('/history/')) throw new Error('Unexpected PUT')
    const row: Conversation = JSON.parse(
      new TextDecoder().decode(body as ArrayBuffer)
    )
    if (config?.params.parent) server.delete(prefix + config.params.parent)
    server.set(prefix + row.id, row)
    return { data: { success: true } }
  })
  vi.spyOn(api, 'delete').mockImplementation(async (url) => {
    server.delete(prefix + url.split('/').at(-1))
    return { data: { success: true } }
  })
  vi.spyOn(api, 'get').mockImplementation(async (url) => {
    if (url.includes('/runs/')) {
      const row = server.get(prefix + url.split('/').at(-1))
      if (!row) {
        throw Object.assign(new Error('missing'), {
          response: { status: 404 },
        })
      }
      return {
        data: row.running
          ? {
              running: true,
              content: '',
              reasoning: '',
              nextContent: 0,
              nextReasoning: 0,
            }
          : { running: false, record: row },
      }
    }
    if (url.includes('/studio/media/')) {
      return {
        data: new Blob([Uint8Array.from(atob(png), (c) => c.charCodeAt(0))], {
          type: 'image/png',
        }),
      }
    }
    if (url.endsWith('/history')) {
      return {
        data: {
          success: true,
          data: [...server.entries()]
            .filter(([key]) => key.startsWith(prefix))
            .map(([, row]) => ({ ...row, title: row.entries[0].prompt })),
        },
      }
    }
    if (url.includes('/history/')) {
      return { data: server.get(prefix + url.split('/').at(-1)) }
    }
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
                        max_output: options.maxOutput ?? 4096,
                      },
                      {
                        id: 'image-model',
                        reference_image: true,
                        kind: 'image',
                        tiers: [{ up_to: 1000, prices: price }],
                        max_output: 4096,
                      },
                      ...(options.basicImage
                        ? [
                            {
                              id: 'basic-image',
                              kind: 'image',
                              tiers: [{ up_to: 1000, prices: price }],
                              max_output: 4096,
                            },
                          ]
                        : []),
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
  server.clear()
  window.sessionStorage.clear()
  window.localStorage.clear()
  clients.splice(0).forEach((client) => client.clear())
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
  streamSend.mockClear()
  imageSend.mockClear()
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
    max_tokens: 4096,
    messages: [{ role: 'user', content: 'Hello' }],
  })
  expect(streamSend.mock.calls[0][4]).toEqual({
    'X-Retail-Product': 'standard',
  })
  expect(screen.queryByRole('dialog')).toBeNull()
  expect(post.mock.calls.every(([url]) => url.includes('/runs/'))).toBe(true)
})

/** A lost acknowledgement reuses the same ID, even when the model can accept a larger default. */
it('uses 10240 output tokens and recovers a lost acknowledgement without a second generation', async () => {
  mount(false, { loseFirstAck: true, maxOutput: 16384 })
  fireEvent.change(await screen.findByLabelText('Model'), {
    target: { value: 'text-model' },
  })
  fireEvent.change(screen.getByLabelText('Prompt'), {
    target: { value: 'Lost acknowledgement' },
  })
  fireEvent.click(screen.getByRole('button', { name: 'Send' }))
  await waitFor(
    () =>
      expect(
        vi.mocked(api.post).mock.calls.filter(([url]) => url.includes('/runs/'))
      ).toHaveLength(2),
    { timeout: 4000 }
  )
  expect(streamSend).toHaveBeenCalledTimes(1)
  expect(streamSend.mock.calls[0][0]).toMatchObject({ max_tokens: 10240 })
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
  let resolve: ((value: string) => void) | undefined
  imageSend.mockImplementationOnce(
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
  expect(imageSend).toHaveBeenCalledTimes(1)
  expect(imageSend.mock.calls[0][0]).toMatchObject({
    model: 'image-model',
    prompt: 'A quiet garden',
  })
  resolve?.(`data:image/png;base64,${png}`)
  expect(
    await screen.findByRole(
      'img',
      { name: 'A quiet garden' },
      { timeout: 3500 }
    )
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
  expect(put.mock.calls.some(([url]) => url.endsWith('/pricing'))).toBe(false)
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

/** 标签存储被清除时从服务器恢复当前对话；主动新建后的空白状态也能在重开时恢复。 */
it('recovers the same conversation after tab storage loss until the user starts a new one', async () => {
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
  await screen.findByText('Received')
  first.unmount()
  sessionStorage.clear()
  const second = mount(true, options)
  expect(await screen.findByLabelText('Call group')).toHaveValue('premium')
  expect(screen.getByLabelText('Model')).toHaveValue('premium-text')
  expect(screen.getByText('Remember Ada')).toBeInTheDocument()
  expect(streamSend).toHaveBeenCalledTimes(1)
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
  const newConversation = screen.getByRole('button', {
    name: 'New conversation',
  })
  await waitFor(() => expect(newConversation).toBeEnabled())
  fireEvent.click(newConversation)
  second.unmount()
  sessionStorage.clear()
  mount(true, options)
  expect(await screen.findByLabelText('Model')).toHaveValue('premium-text')
  expect(screen.queryByText('Remember Ada')).toBeNull()
  expect(streamSend).toHaveBeenCalledTimes(2)
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
  expect(screen.getByRole('button', { name: 'Stop' })).toBeVisible()
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
  localStorage.clear()
  const other = mount(true, { userId: 2 })
  fireEvent.click(await screen.findByRole('button', { name: 'Studio history' }))
  expect(await screen.findByText('No history yet.')).toBeVisible()
  other.unmount()
  mount(true, options)
  fireEvent.click(await screen.findByRole('button', { name: 'Studio history' }))
  const list = within(await screen.findByRole('dialog'))
  expect(
    await list.findByRole('button', { name: /^Second topic/ })
  ).toBeVisible()
  fireEvent.click(list.getByRole('button', { name: /^First topic/ }))
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
  expect(server.size).toBe(2)
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
  fireEvent.click(await screen.findByRole('button', { name: 'Studio history' }))
  fireEvent.click(
    await screen.findByRole('button', { name: /^Before upgrade/ })
  )
  expect(await screen.findByText('Original reply')).toBeVisible()
  expect(streamSend).not.toHaveBeenCalled()
})

/** 参考图随提示词进入已发送消息，完成生成和重新打开历史都不会把它放回草稿。 */
it('previews and removes a reference before sending a single edit', async () => {
  vi.spyOn(URL, 'createObjectURL').mockImplementation((file) =>
    file instanceof File ? 'blob:studio-preview' : 'blob:studio-saved-reference'
  )
  let finishImage: ((value: string) => void) | undefined
  imageSend.mockImplementationOnce(
    () =>
      new Promise((resolve) => {
        finishImage = resolve
      })
  )
  const first = mount(true)
  fireEvent.click(await screen.findByRole('tab', { name: 'Image generation' }))
  fireEvent.change(screen.getByLabelText('Model'), {
    target: { value: 'image-model' },
  })
  const upload = screen.getByLabelText('Reference image')
  const file = new File(
    [Uint8Array.from(atob(png), (c) => c.charCodeAt(0))],
    'ref.png',
    { type: 'image/png' }
  )
  fireEvent.change(upload, { target: { files: [file] } })
  await screen.findByRole('img', { name: 'Reference image' })
  fireEvent.click(
    screen.getByRole('button', { name: 'Remove reference image' })
  )
  expect(screen.queryByRole('img', { name: 'Reference image' })).toBeNull()
  fireEvent.change(upload, { target: { files: [file] } })
  await screen.findByRole('img', { name: 'Reference image' })
  expect(imageSend).not.toHaveBeenCalled()
  fireEvent.change(screen.getByLabelText('Prompt'), {
    target: { value: 'Draw in blue' },
  })
  fireEvent.click(screen.getByRole('button', { name: 'Generate image' }))
  const results = within(screen.getByRole('log', { name: 'Results' }))
  const preview = await results.findByRole('img', { name: 'Reference image' })
  expect(results.getByText('Draw in blue').parentElement).toContainElement(
    preview
  )
  expect(preview).toHaveAttribute('src', 'blob:studio-preview')
  expect(screen.getByLabelText('Prompt')).toHaveValue('')
  expect(
    screen.queryByRole('button', { name: 'Remove reference image' })
  ).toBeNull()
  expect(imageSend).toHaveBeenCalledExactlyOnceWith(
    expect.objectContaining({
      reference: expect.stringMatching(/\/studio\/media\/.+\/reference$/),
    })
  )
  finishImage?.(`data:image/png;base64,${png}`)
  await results.findByRole('img', { name: 'Draw in blue' })
  await waitFor(() =>
    expect(
      results.getByRole('img', { name: 'Reference image' })
    ).toHaveAttribute('src', 'blob:studio-saved-reference')
  )
  expect(
    screen.queryByRole('button', { name: 'Remove reference image' })
  ).toBeNull()
  first.unmount()
  sessionStorage.clear()
  localStorage.clear()
  mount(true)
  fireEvent.click(await screen.findByRole('button', { name: 'Studio history' }))
  fireEvent.click(await screen.findByRole('button', { name: /^Draw in blue/ }))
  expect(
    await screen.findByRole('img', { name: 'Draw in blue' })
  ).toHaveAttribute('src', `data:image/png;base64,${png}`)
  expect(
    await within(screen.getByRole('log', { name: 'Results' })).findByRole(
      'img',
      {
        name: 'Reference image',
      }
    )
  ).toHaveAttribute('src', 'blob:studio-saved-reference')
  expect(
    screen.queryByRole('button', { name: 'Remove reference image' })
  ).toBeNull()
  expect(imageSend).toHaveBeenCalledTimes(1)
})

/** 上游收到校验后的实际像素尺寸和输出设置。 */
it('submits custom image dimensions, quality, format and compression', async () => {
  mount(true)
  fireEvent.click(await screen.findByRole('tab', { name: 'Image generation' }))
  fireEvent.change(screen.getByLabelText('Model'), {
    target: { value: 'image-model' },
  })
  fireEvent.change(screen.getByLabelText('Image size'), {
    target: { value: 'custom' },
  })
  fireEvent.change(screen.getByLabelText('Custom resolution'), {
    target: { value: '1920x1088' },
  })
  fireEvent.change(screen.getByLabelText('Quality'), {
    target: { value: 'auto' },
  })
  fireEvent.change(screen.getByLabelText('Output format'), {
    target: { value: 'webp' },
  })
  fireEvent.change(screen.getByLabelText('Output compression (%)'), {
    target: { value: '82' },
  })
  fireEvent.change(screen.getByLabelText('Prompt'), {
    target: { value: 'A wide garden' },
  })
  fireEvent.click(screen.getByRole('button', { name: 'Generate image' }))

  await screen.findByRole('img', { name: 'A wide garden' })
  expect(api.post).toHaveBeenCalledWith(
    expect.stringContaining('/runs/'),
    expect.objectContaining({
      size: '1920x1088',
      quality: 'auto',
      outputFormat: 'webp',
      outputCompression: 82,
    }),
    expect.anything()
  )
})

/** 手机上在底部弹层改参数，关闭后同一份表单仍按新设置发送。 */
it('keeps mobile image settings outside the composer and uses them after closing', async () => {
  vi.stubGlobal('innerWidth', 360)
  mount(true)
  fireEvent.click(await screen.findByRole('tab', { name: 'Image generation' }))
  fireEvent.change(screen.getByLabelText('Model'), {
    target: { value: 'image-model' },
  })
  const prompt = screen.getByLabelText('Prompt')
  const trigger = screen.getByRole('button', { name: 'Advanced Settings' })
  fireEvent.click(trigger)
  const sheet = await screen.findByRole('dialog', {
    name: 'Advanced Settings',
  })
  expect(prompt.closest('form')).not.toContainElement(sheet)
  fireEvent.change(within(sheet).getByLabelText('Image size'), {
    target: { value: '864x1536' },
  })
  fireEvent.click(within(sheet).getByRole('button', { name: 'Close' }))
  await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())

  fireEvent.change(prompt, { target: { value: 'Tall garden' } })
  fireEvent.click(screen.getByRole('button', { name: 'Generate image' }))
  await screen.findByRole('img', { name: 'Tall garden' })
  expect(api.post).toHaveBeenCalledWith(
    expect.stringContaining('/runs/'),
    expect.objectContaining({ size: '864x1536' }),
    expect.anything()
  )
})

/** 切换模型后，界面和实际请求都使用新模型支持的选项。 */
it('resets unsupported options when switching to a basic image model', async () => {
  mount(true, { basicImage: true })
  fireEvent.click(await screen.findByRole('tab', { name: 'Image generation' }))
  const model = screen.getByLabelText('Model')
  fireEvent.change(model, { target: { value: 'image-model' } })
  const size = screen.getByLabelText('Image size')
  const quality = screen.getByLabelText('Quality')
  fireEvent.change(size, { target: { value: 'custom' } })
  fireEvent.change(quality, { target: { value: 'auto' } })
  fireEvent.change(screen.getByLabelText('Output format'), {
    target: { value: 'jpeg' },
  })

  fireEvent.change(model, { target: { value: 'basic-image' } })
  expect(size).toHaveValue('1024x1024')
  expect(quality).toHaveValue('medium')
  expect(screen.queryByLabelText('Custom resolution')).toBeNull()
  expect(screen.queryByLabelText('Output format')).toBeNull()

  fireEvent.change(screen.getByLabelText('Prompt'), {
    target: { value: 'Basic image' },
  })
  fireEvent.click(screen.getByRole('button', { name: 'Generate image' }))
  await screen.findByRole('img', { name: 'Basic image' })
  expect(api.post).toHaveBeenCalledWith(
    expect.stringContaining('/runs/'),
    expect.objectContaining({
      size: '1024x1024',
      quality: 'medium',
      outputFormat: 'png',
    }),
    expect.anything()
  )
})

/** 50 MiB 边界内直接发送文件字节，且超过上限时不提交。 */
it('uploads original file bytes as multipart and blocks files over 50 MiB', async () => {
  vi.spyOn(URL, 'createObjectURL').mockReturnValue('blob:original-reference')
  mount(true)
  fireEvent.click(await screen.findByRole('tab', { name: 'Image generation' }))
  fireEvent.change(screen.getByLabelText('Model'), {
    target: { value: 'image-model' },
  })
  const signature = Uint8Array.from(atob(png), (c) => c.charCodeAt(0))
  const upload = screen.getByLabelText('Reference image')
  const tooLarge = new File([new Uint8Array(50 * 1024 * 1024 + 1)], 'large.png')
  fireEvent.change(upload, { target: { files: [tooLarge] } })
  await screen.findByRole('alert')
  expect(screen.getByRole('button', { name: 'Generate image' })).toBeDisabled()

  const file = new File(
    [signature, new Uint8Array(6 * 1024 * 1024)],
    'reference.png',
    { type: 'image/png' }
  )
  fireEvent.change(upload, { target: { files: [file] } })
  expect(
    await screen.findByRole('img', { name: 'Reference image' })
  ).toHaveAttribute('src', 'blob:original-reference')
  fireEvent.change(screen.getByLabelText('Prompt'), {
    target: { value: 'Edit original' },
  })
  fireEvent.click(screen.getByRole('button', { name: 'Generate image' }))
  await screen.findByRole('img', { name: 'Edit original' })

  const sent = vi
    .mocked(api.post)
    .mock.calls.find(([url]) => url.includes('/runs/'))?.[1]
  expect(sent).toBeInstanceOf(FormData)
  const outgoing = (sent as FormData).get('image') as File
  expect(outgoing.size).toBe(file.size)
  expect(await outgoing.arrayBuffer()).toEqual(await file.arrayBuffer())
})

/** A prior chat reply can be inserted without opening its session or losing the current draft. */
it('quotes an archived chat reply into the current prompt', async () => {
  const id = 'aaaaaaaa-1111-2222-3333-bbbbbbbbbbbb'
  server.set(`1/${id}`, {
    id,
    product: 'standard',
    mode: 'text',
    updatedAt: Date.now(),
    entries: [
      {
        id: 1,
        prompt: 'Archived question',
        model: 'text-model',
        content: 'Archived answer',
        reasoning: 'Internal notes',
        complete: true,
      },
    ],
  })
  mount(true)
  fireEvent.click(
    await screen.findByRole('button', { name: 'New conversation' })
  )
  fireEvent.change(screen.getByLabelText('Prompt'), {
    target: { value: 'Summarize that' },
  })
  fireEvent.click(screen.getByRole('button', { name: 'Quote from history' }))
  fireEvent.click(
    await screen.findByRole('button', { name: /Archived question/ })
  )
  fireEvent.click(
    await screen.findByRole('button', { name: /Quote reply.*Archived answer/ })
  )
  expect(screen.getByLabelText('Prompt')).toHaveValue(
    '> Archived answer\n\nSummarize that'
  )
  expect(streamSend).not.toHaveBeenCalled()
  fireEvent.click(screen.getByRole('button', { name: 'Send' }))
  await waitFor(() => expect(streamSend).toHaveBeenCalledTimes(1))
  expect(streamSend.mock.calls[0][0]).toMatchObject({
    messages: [
      { role: 'user', content: '> Archived answer\n\nSummarize that' },
    ],
  })
})

/** An archived result is read as authenticated media and submitted as an edit attachment. */
it('uses a saved result as the next image reference', async () => {
  vi.spyOn(URL, 'createObjectURL').mockReturnValue('blob:saved-result')
  const id = 'aaaaaaaa-1111-2222-3333-bbbbbbbbbbbb'
  const media = `/api/retail/studio/media/${id}/result`
  server.set(`1/${id}`, {
    id,
    product: 'standard',
    mode: 'image',
    updatedAt: Date.now(),
    entries: [
      {
        id: 1,
        prompt: 'A city at noon',
        model: 'image-model',
        content: '',
        reasoning: '',
        complete: true,
        image: media,
      },
    ],
  })
  mount(true)
  fireEvent.click(await screen.findByRole('tab', { name: 'Image generation' }))
  fireEvent.change(screen.getByLabelText('Model'), {
    target: { value: 'image-model' },
  })
  fireEvent.change(screen.getByLabelText('Prompt'), {
    target: { value: 'Change it to dusk' },
  })
  fireEvent.click(screen.getByRole('button', { name: 'Quote from history' }))
  fireEvent.click(await screen.findByRole('button', { name: /A city at noon/ }))
  const option = await screen.findByRole('button', { name: /Quote image/ })
  await waitFor(() => expect(option).toBeEnabled())
  fireEvent.click(option)
  expect(screen.getByLabelText('Prompt')).toHaveValue('Change it to dusk')
  await screen.findByRole('img', { name: 'Reference image' })
  expect(imageSend).not.toHaveBeenCalled()
  fireEvent.click(screen.getByRole('button', { name: 'Generate image' }))
  await screen.findByRole('img', { name: 'Change it to dusk' })
  const payload = vi
    .mocked(api.post)
    .mock.calls.find(([url]) => url.includes('/runs/'))?.[1]
  expect(payload).toBeInstanceOf(FormData)
  const photo = (payload as FormData).get('image') as File
  expect(new Uint8Array(await photo.arrayBuffer())).toEqual(
    Uint8Array.from(atob(png), (char) => char.charCodeAt(0))
  )
})

/** 粘贴文件同时带有链接或错误 MIME 时，保留文字草稿并按实际 WebP 格式上传原图。 */
it('pastes a reference image without replacing the prompt or intercepting text', async () => {
  vi.spyOn(URL, 'createObjectURL').mockReturnValue('blob:clipboard-reference')
  mount(true)
  const prompt = await screen.findByLabelText('Prompt')
  const bytes = Uint8Array.from(
    atob('UklGRiIAAABXRUJQVlA4IBYAAAAwAQCdASoBAAEAAUAmJaQAA3AA/vuUAAA='),
    (char) => char.charCodeAt(0)
  )
  const image = new File([bytes], 'clipboard.png', { type: 'image/png' })
  const clipboardData = {
    files: [image],
    getData: () => 'https://example.com/image.png',
  }

  expect(fireEvent.paste(prompt, { clipboardData })).toBe(true)
  fireEvent.change(screen.getByLabelText('Model'), {
    target: { value: 'text-model' },
  })
  expect(fireEvent.paste(prompt, { clipboardData })).toBe(false)
  await screen.findByRole('img', { name: 'Reference image' })
  fireEvent.change(prompt, { target: { value: 'Describe this picture' } })
  fireEvent.click(screen.getByRole('button', { name: 'Send' }))
  await waitFor(() => expect(streamSend).toHaveBeenCalledOnce())
  const chatUpload = vi
    .mocked(api.post)
    .mock.calls.filter(([url]) => url.includes('/runs/'))
    .at(-1)?.[1]
  expect(chatUpload).toBeInstanceOf(FormData)
  expect((chatUpload as FormData).get('image')).toBeInstanceOf(Blob)
  expect(
    screen.queryByRole('button', { name: 'Remove reference image' })
  ).toBeNull()
  fireEvent.click(screen.getByRole('tab', { name: 'Image generation' }))
  fireEvent.change(screen.getByLabelText('Model'), {
    target: { value: 'image-model' },
  })
  expect(fireEvent.paste(prompt, { clipboardData: { files: [] } })).toBe(true)
  fireEvent.change(prompt, { target: { value: 'Make the background blue' } })
  expect(fireEvent.paste(prompt, { clipboardData })).toBe(false)
  await screen.findByRole('img', { name: 'Reference image' })
  expect(prompt).toHaveValue('Make the background blue')
  expect(imageSend).not.toHaveBeenCalled()

  fireEvent.click(screen.getByRole('button', { name: 'Generate image' }))
  await screen.findByRole('img', { name: 'Make the background blue' })
  const payload = vi
    .mocked(api.post)
    .mock.calls.filter(([url]) => url.includes('/runs/'))
    .at(-1)?.[1]
  expect(payload).toBeInstanceOf(FormData)
  const reference = (payload as FormData).get('image') as File
  expect(reference.type).toBe('image/webp')
  expect(new Uint8Array(await reference.arrayBuffer())).toEqual(bytes)
})

/** The second image request continues the first record; only New conversation clears its parent. */
it('keeps successive generations together until the user starts a new conversation', async () => {
  mount(true)
  fireEvent.click(await screen.findByRole('tab', { name: 'Image generation' }))
  fireEvent.change(screen.getByLabelText('Model'), {
    target: { value: 'image-model' },
  })
  for (const prompt of ['Draw a tree', 'Make the leaves red']) {
    fireEvent.change(screen.getByLabelText('Prompt'), {
      target: { value: prompt },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Generate image' }))
    await screen.findByRole('img', { name: prompt })
  }
  expect(screen.getByRole('img', { name: 'Draw a tree' })).toBeVisible()
  expect(server.size).toBe(1)
  expect([...server.values()][0].entries.map((e) => e.prompt)).toEqual([
    'Draw a tree',
    'Make the leaves red',
  ])
  fireEvent.click(screen.getByRole('button', { name: 'New conversation' }))
  expect(screen.queryByRole('img', { name: 'Draw a tree' })).toBeNull()
})
