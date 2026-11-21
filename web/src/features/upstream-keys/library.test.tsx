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

import { KeyLibrary, SiteKeyPicker } from './library'

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

/** A root can create a spare credential under an approved site without configuring a route or pricing group. */
it('creates an unused Key under a site without a group', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      data: {
        keys: [],
        channels: [{ id: 7, site_id: 7, name: 'Example' }],
        uses: {},
        groups: {},
        stamp: 's',
      },
    },
  })
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true, data: { id: 1 } } })
  render(
    <QueryClientProvider client={new QueryClient()}>
      <KeyLibrary identity='root' />
    </QueryClientProvider>
  )
  fireEvent.click(
    await screen.findByRole('button', { name: 'Add upstream Key' })
  )
  fireEvent.change(screen.getByLabelText('Filing site'), {
    target: { value: '7' },
  })
  fireEvent.change(screen.getByLabelText('Name'), {
    target: { value: 'Spare' },
  })
  fireEvent.change(screen.getByLabelText('Upstream API Key'), {
    target: { value: 'test-value' },
  })
  fireEvent.click(
    within(screen.getByRole('dialog')).getByRole('button', {
      name: 'Add upstream Key',
    })
  )
  await waitFor(() =>
    expect(put).toHaveBeenCalledWith(
      '/api/retail/admin/key-library',
      expect.objectContaining({
        action: 'create',
        name: 'Spare',
        channel_id: 0,
        owner_site_id: 7,
        key: 'test-value',
      }),
      expect.anything()
    )
  )
})

/** Selecting a saved Key sends only the ID, pinned destination and confirmed upstream identity. */
it('binds an existing credential from site settings', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      data: {
        keys: [{ id: 3, name: 'Spare', version: 1, owner_site_id: 8 }],
        channels: [
          {
            id: 7,
            site_id: 7,
            name: 'Example',
            base_url: 'https://up.example',
          },
          { id: 8, site_id: 8, name: 'Other' },
        ],
        uses: {},
        groups: { 7: 'fast' },
        stamp: 's',
      },
    },
  })
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true } })
  render(
    <QueryClientProvider client={new QueryClient()}>
      <SiteKeyPicker channel={7} />
    </QueryClientProvider>
  )
  fireEvent.click(
    await screen.findByRole('button', { name: 'Select saved Key' })
  )
  expect(
    screen.getByRole('option', { name: 'Other #8 · Spare #3' })
  ).toBeInTheDocument()
  fireEvent.change(screen.getByRole('combobox'), { target: { value: '3' } })
  fireEvent.click(screen.getByRole('checkbox'))
  fireEvent.click(
    within(screen.getByRole('dialog')).getByRole('button', {
      name: 'Select saved Key',
    })
  )
  await waitFor(() =>
    expect(put).toHaveBeenCalledWith(
      '/api/retail/admin/key-library',
      expect.objectContaining({
        id: 3,
        channel_id: 7,
        destination: 'https://up.example',
        action: 'bind',
        confirm_groups: { 7: 'fast' },
        key: '',
      }),
      expect.anything()
    )
  )
})
