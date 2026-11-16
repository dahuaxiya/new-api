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
} from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

import { ModelStudio } from '..'
import { imageDataURL } from '../api'

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
  } = {}
) {
  useAuthStore.getState().auth.setUser({
    id: 1,
    username: 'studio-user',
    role: options.root ? 100 : 1,
  })
  vi.spyOn(api, 'get').mockImplementation(async (url) => {
    let data: unknown = { token_id: ready ? 9 : 0 }
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
            ],
      }
    }
    if (url.endsWith('/pricing')) {
      data = {
        revision: 1,
        policy: {
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
  useAuthStore.getState().auth.reset()
})

it('keeps sending disabled until a price policy exists and does not create identities on load', async () => {
  const post = vi.spyOn(api, 'post')
  mount(false)
  expect(await screen.findByRole('button', { name: 'Send' })).toBeDisabled()
  expect(
    screen.getByText(
      'Set your budget and price limits before the first request.'
    )
  ).toBeVisible()
  expect(post).not.toHaveBeenCalled()
})

it.each([
  [{ paused: true }, 'Model requests are paused by the site administrator.'],
  [
    { noPlan: true },
    'No model plan is assigned to your account group. Contact the site administrator.',
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

it('rejects URL-only and non-PNG upstream output instead of embedding arbitrary content', () => {
  expect(() =>
    imageDataURL({ data: [{ url: 'javascript:alert(1)' }] })
  ).toThrow()
  expect(() => imageDataURL({ data: [{ b64_json: 'PHN2Zz4=' }] })).toThrow()
  expect(imageDataURL({ data: { b64_json: png } })).toBe(
    `data:image/png;base64,${png}`
  )
})
