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

const streamSend = vi.hoisted(() =>
  vi.fn(
    async (
      _payload: unknown,
      _update: unknown,
      complete: () => void,
      _error: unknown,
      _headers: Record<string, string>
    ) => complete()
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
    root?: boolean
    secondGroup?: boolean
    automatic?: boolean
  } = {}
) {
  useAuthStore.getState().auth.setUser({
    id: 1,
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
  clients.splice(0).forEach((client) => client.clear())
  vi.restoreAllMocks()
  streamSend.mockClear()
  useAuthStore.getState().auth.reset()
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
  expect(streamSend.mock.calls[0][4]).toEqual({ 'X-Retail-Product': 'premium' })
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
  fireEvent.click(within(dialog).getByText('Advanced model price limits'))
  fireEvent.click(
    within(dialog).getByRole('button', {
      name: 'Use current prices as ceilings',
    })
  )
  fireEvent.click(
    within(dialog).getByRole('switch', {
      name: 'Accept automatic price changes within these limits',
    })
  )
  fireEvent.click(
    within(dialog).getByRole('button', { name: 'Save price limits' })
  )
  await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
  expect(screen.getByLabelText('Call group')).toHaveValue('standard')
  expect(screen.getByLabelText('Model')).toHaveValue('')
  expect(put).toHaveBeenCalledWith(
    '/api/retail/keys/9/pricing',
    expect.objectContaining({
      policy: expect.objectContaining({ product_id: 'standard' }),
    })
  )
})
