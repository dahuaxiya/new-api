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

import { AddLibraryKey, KeyLibrary, SiteKeyPicker } from './library'

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
      <AddLibraryKey identity='root' />
    </QueryClientProvider>
  )
  const add = await screen.findByRole('button', { name: 'Add upstream Key' })
  await waitFor(() => expect(add).toBeEnabled())
  fireEvent.click(add)
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
        keys: [
          { id: 3, name: 'Spare', version: 1, owner_site_id: 7 },
          { id: 4, name: 'Foreign', version: 1, owner_site_id: 8 },
          { id: 5, name: 'Unfiled', version: 1, owner_site_id: 0 },
        ],
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
    screen.getByRole('option', { name: 'Example #7 · Spare #3' })
  ).toBeInTheDocument()
  expect(screen.queryByRole('option', { name: /Foreign|Unfiled/ })).toBeNull()
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

/** Importing a child shows its root's identity separately from the route and keeps custody server-derived. */
it('identifies the filing site before importing a child credential', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      data: {
        keys: [],
        channels: [
          { id: 7, site_id: 7, name: 'Root' },
          { id: 8, site_id: 7, name: 'Fast', has_key: true },
        ],
        groups: {},
        uses: {},
        stamp: 's',
      },
    },
  })
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true } })
  render(
    <QueryClientProvider client={new QueryClient()}>
      <SiteKeyPicker channel={8} />
    </QueryClientProvider>
  )
  fireEvent.click(
    await screen.findByRole('button', { name: 'Import existing Key' })
  )
  const dialog = within(screen.getByRole('dialog'))
  expect(dialog.getByText('Filing site').nextElementSibling).toHaveTextContent(
    'Root #7'
  )
  expect(dialog.getByText('Supply route').nextElementSibling).toHaveTextContent(
    'Fast #8'
  )
  fireEvent.click(dialog.getByRole('button', { name: 'Import existing Key' }))
  await waitFor(() => expect(put).toHaveBeenCalled())
  expect(put.mock.calls[0][1]).toMatchObject({
    action: 'import',
    channel_id: 8,
  })
  expect(put.mock.calls[0][1]).not.toHaveProperty('owner_site_id')
})

/** A transfer submits the reviewed source and the selected new root; the current root is not a destination. */
it('transfers an unused Key to a different root', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      data: {
        keys: [{ id: 3, name: 'Spare', owner_site_id: 7 }],
        channels: [
          { id: 7, site_id: 7, name: 'A' },
          { id: 8, site_id: 8, name: 'B' },
        ],
        groups: {},
        uses: {},
        stamp: 's',
      },
    },
  })
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true } })
  render(
    <QueryClientProvider client={new QueryClient()}>
      <KeyLibrary identity='root' />
    </QueryClientProvider>
  )
  fireEvent.keyDown(
    await screen.findByRole('button', { name: 'Actions · Spare #3' }),
    { key: 'ArrowDown' }
  )
  fireEvent.click(
    await screen.findByRole('menuitem', { name: 'Change filing site' })
  )
  const dialog = within(screen.getByRole('dialog'))
  expect(dialog.queryByRole('option', { name: 'A #7' })).toBeNull()
  fireEvent.change(dialog.getByLabelText('New filing site'), {
    target: { value: '8' },
  })
  fireEvent.click(dialog.getByRole('button', { name: 'Change filing site' }))
  await waitFor(() =>
    expect(put).toHaveBeenCalledWith(
      '/api/retail/admin/key-library',
      expect.objectContaining({
        action: 'transfer_owner',
        from_site_id: 7,
        owner_site_id: 8,
        id: 3,
      }),
      expect.anything()
    )
  )
})

/** Same-host roots keep separate inventories, and a hidden route still prevents deleting its Key. */
it('separates sites sharing an address and preserves the actions for a referenced Key', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      data: {
        keys: [{ id: 3, name: 'Spare', owner_site_id: 7 }],
        channels: [
          { id: 7, site_id: 7, name: 'A', base_url: 'https://example.test' },
          {
            id: 8,
            site_id: 8,
            name: 'B',
            base_url: 'https://example.test',
            has_key: true,
          },
        ],
        groups: {},
        uses: { 3: [99] },
        stamp: 'v1',
      },
    },
  })
  render(
    <QueryClientProvider client={new QueryClient()}>
      <KeyLibrary identity='root' />
    </QueryClientProvider>
  )
  const first = await screen.findByRole('region', { name: 'A #7' })
  const other = screen.getByRole('region', { name: 'B #8' })
  expect(
    within(other).getByRole('button', { name: 'Import existing Key · B #8' })
  ).toBeEnabled()
  fireEvent.keyDown(
    within(first).getByRole('button', { name: 'Actions · Spare #3' }),
    { key: 'ArrowDown' }
  )
  expect(
    await screen.findByRole('menuitem', { name: 'Delete' })
  ).toHaveAttribute('aria-disabled', 'true')
  expect(
    screen.getByRole('menuitem', { name: 'Detach Key and disable route #99' })
  ).toBeInTheDocument()
})

/** Empty secret means rename only. Entering a replacement requires confirming the route group. */
it('edits the saved name and optionally replaces the secret on the same Key', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      data: {
        keys: [{ id: 3, owner_site_id: 7, name: 'Spare' }],
        channels: [{ id: 7, site_id: 7, name: 'Root' }],
        uses: { 3: [7] },
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
      <KeyLibrary identity='root' />
    </QueryClientProvider>
  )
  fireEvent.click(
    await screen.findByRole('button', { name: 'Edit Key · Spare #3' })
  )
  expect(screen.getByLabelText('Upstream API Key')).toHaveValue('')
  expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled()
  fireEvent.change(screen.getByLabelText('Name'), {
    target: { value: 'Claude' },
  })
  expect(screen.queryByRole('checkbox')).toBeNull()
  fireEvent.click(screen.getByRole('button', { name: 'Save' }))
  await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
  expect(put.mock.calls[0][1]).toMatchObject({
    action: 'edit',
    id: 3,
    name: 'Claude',
    key: '',
    confirm_groups: {},
  })
  fireEvent.click(screen.getByRole('button', { name: 'Edit Key · Spare #3' }))
  fireEvent.change(screen.getByLabelText('Upstream API Key'), {
    target: { value: 'replacement' },
  })
  expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled()
  fireEvent.click(screen.getByRole('checkbox'))
  fireEvent.click(screen.getByRole('button', { name: 'Save' }))
  await waitFor(() => expect(put).toHaveBeenCalledTimes(2))
  expect(put.mock.calls[1][1]).toMatchObject({
    action: 'edit',
    id: 3,
    name: 'Spare',
    key: 'replacement',
    confirm_groups: { 7: 'fast' },
  })
})
