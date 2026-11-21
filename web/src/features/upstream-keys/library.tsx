import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import type { RetailBindingChannel } from '@/features/channels/retail-pricing-api'
import { api } from '@/lib/api'
import {
  getServerErrorMessage,
  requireServerSuccess,
} from '@/lib/server-error-message'
import { useAuthStore } from '@/stores/auth-store'

type Entry = {
  owner_site_id: number
  id: number
  name: string
  version: number
}
type Site = RetailBindingChannel & { library_id?: number }
type Library = {
  keys: Entry[]
  channels: Site[] | null
  uses: Record<number, number[]>
  groups: Record<number, string>
  stamp: string
}
type Action =
  | 'transfer_owner'
  | 'assign_owner'
  | 'create'
  | 'rotate'
  | 'delete'
  | 'bind'
  | 'detach'
  | 'import'
  | 'add_route'
type Draft = { action: Action; library: Library; entry?: Entry; site?: Site }
const labels = {
  transfer_owner: 'Change filing site',
  assign_owner: 'Change filing site',
  add_route: 'Add route with saved Key',
  create: 'Add upstream Key',
  rotate: 'Replace upstream Key',
  delete: 'Delete',
  bind: 'Select saved Key',
  detach: 'Detach Key and disable route',
  import: 'Import existing Key',
}
const endpoint = '/api/retail/admin/key-library'

/** Shared metadata query contains no secrets. A change of login identity gets a different cache entry. */
function useLibrary(identity: string) {
  return useQuery({
    queryKey: ['retail-key-library', identity],
    retry: false,
    queryFn: async ({ signal }): Promise<Library> =>
      requireServerSuccess((await api.get(endpoint, { signal })).data).data,
  })
}

/** Shows the exact outbound destination and registered upstream group before changing an existing route. */
function siteLabel(library: Library, id: number) {
  const site = library.channels?.find((row) => row.id === id)
  return site
    ? [`${site.name} #${id}`, site.base_url, library.groups[id]]
        .filter(Boolean)
        .join(' · ')
    : `#${id}`
}

/** File inventory by the root site ID. Two sites may share a URL and a name; legacy rows have ID zero. */
function filingSiteLabel(library: Library, id: number, unfiled: string) {
  if (!id) return unfiled
  const root = library.channels?.find(
    (row) => row.id === id && row.site_id === id
  )
  return root ? `${root.name} #${id}` : `#${id}`
}

/** A form keeps the reviewed snapshot until submission. Do not retain write-only input in query or mutation caches. */
function EditKey({
  draft,
  close,
  identity,
}: {
  draft: Draft
  close: () => void
  identity: string
}) {
  const { t } = useTranslation()
  const cache = useQueryClient()
  const { action, entry, site, library } = draft
  const [secret, setSecret] = useState('')
  const [name, setName] = useState(
    action === 'import' ? (site?.name ?? '') : ''
  )
  const [models, setModels] = useState('')
  const [owner, setOwner] = useState('')
  const needsOwner =
    action === 'create' ||
    action === 'assign_owner' ||
    action === 'transfer_owner'
  const filingSites =
    library.channels?.filter(
      (row) =>
        row.id === row.site_id &&
        (action !== 'transfer_owner' || row.id !== entry?.owner_site_id)
    ) ?? []
  const [selected, setSelected] = useState('')
  const [confirmed, setConfirmed] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [conflict, setConflict] = useState(false)
  const uses = entry ? (library.uses[entry.id] ?? []) : []
  let affected: number[] = []
  if (action === 'rotate' && entry) {
    // A historical foreign reference can only be detached. It never receives this replacement.
    affected = (library.channels ?? [])
      .filter(
        (row) => row.site_id === entry.owner_site_id && uses.includes(row.id)
      )
      .map((row) => row.id)
  }
  if (action === 'bind' && site) affected = [site.id]
  const groups = Object.fromEntries(
    affected
      .filter((id) => library.groups[id])
      .map((id) => [id, library.groups[id]])
  )
  const needsConfirmation = Object.keys(groups).length > 0
  /** On conflict, reload the reference list before offering a new confirmation. Never replay a secret-bearing request. */
  async function submit(event: FormEvent) {
    event.preventDefault()
    event.stopPropagation()
    if (busy || conflict) return
    setBusy(true)
    setError('')
    try {
      const result = await api.put(
        endpoint,
        {
          action,
          stamp: library.stamp,
          id:
            action === 'bind' || action === 'add_route'
              ? Number(selected)
              : (entry?.id ?? 0),
          ...(needsOwner && { owner_site_id: Number(owner) }),
          ...(action === 'transfer_owner' && {
            from_site_id: entry?.owner_site_id,
          }),
          channel_id: site?.id ?? 0,
          destination: site?.base_url ?? '',
          name,
          models,
          key: secret,
          confirm_groups: confirmed ? groups : {},
        },
        { singleUseAuthorization: true, skipErrorHandler: true }
      )
      requireServerSuccess(result.data)
      close()
      await Promise.all(
        [
          'retail-key-library',
          'upstream-keys',
          'retail-group-bindings',
          'retail-pricing',
          'retail-supplies',
        ].map((k) => cache.invalidateQueries({ queryKey: [k, identity] }))
      )
    } catch (err) {
      setError(t(getServerErrorMessage(err)))
      if (
        (err as { response?: { status?: number } }).response?.status === 409
      ) {
        setConflict(true)
        await cache.invalidateQueries({
          queryKey: ['retail-key-library', identity],
        })
      }
    } finally {
      setSecret('')
      setBusy(false)
    }
  }
  return (
    <Dialog
      open
      onOpenChange={(v) => {
        if (!v && !busy) close()
      }}
    >
      <DialogContent>
        <DialogHeader>
          <DialogTitle>
            {t(labels[action])}
            {entry && ` · ${entry.name}`}
          </DialogTitle>
        </DialogHeader>
        <form onSubmit={submit} className='space-y-4'>
          {action === 'import' && site ? (
            <dl className='grid grid-cols-[auto_1fr] gap-x-4 gap-y-2'>
              <dt>{t('Filing site')}</dt>
              <dd>
                {filingSiteLabel(library, site.site_id, t('Unfiled Keys'))}
              </dd>
              <dt>{t('Supply route')}</dt>
              <dd className='min-w-0 break-all'>
                {siteLabel(library, site.id)}
              </dd>
            </dl>
          ) : (
            site && <p className='break-all'>{siteLabel(library, site.id)}</p>
          )}
          {action === 'transfer_owner' && entry && (
            <p>
              {t('Current filing site')} ·{' '}
              {filingSiteLabel(library, entry.owner_site_id, t('Unfiled Keys'))}
            </p>
          )}
          {needsOwner && (
            <label className='block'>
              {t(
                action === 'transfer_owner' ? 'New filing site' : 'Filing site'
              )}
              <NativeSelect
                required
                value={owner}
                onChange={(e) => setOwner(e.target.value)}
              >
                <NativeSelectOption value=''>
                  {t('Select a site')}
                </NativeSelectOption>
                {filingSites.map((row) => (
                  <NativeSelectOption key={row.id} value={row.id}>
                    {row.name} #{row.id}
                  </NativeSelectOption>
                ))}
              </NativeSelect>
            </label>
          )}
          {(action === 'create' ||
            action === 'import' ||
            action === 'add_route') && (
            <label className='block'>
              {t('Name')}
              <Input
                required
                maxLength={120}
                value={name}
                onChange={(e) => setName(e.target.value)}
              />
            </label>
          )}
          {(action === 'create' || action === 'rotate') && (
            <label className='block'>
              {t('Upstream API Key')}
              <Input
                required
                type='password'
                autoComplete='new-password'
                maxLength={4096}
                spellCheck={false}
                value={secret}
                onChange={(e) => setSecret(e.target.value)}
              />
            </label>
          )}
          {(action === 'bind' || action === 'add_route') && (
            <label className='block'>
              {t('Upstream API Key')}
              <NativeSelect
                required
                value={selected}
                onChange={(e) => setSelected(e.target.value)}
              >
                <NativeSelectOption value=''>
                  {t('Select saved Key')}
                </NativeSelectOption>
                {library.keys
                  .filter(
                    (k) =>
                      k.owner_site_id > 0 &&
                      k.owner_site_id === site?.site_id &&
                      (action === 'add_route' ||
                        k.id !== site.library_id ||
                        !site.has_key)
                  )
                  .map((k) => (
                    <NativeSelectOption key={k.id} value={k.id}>
                      {filingSiteLabel(
                        library,
                        k.owner_site_id,
                        t('Unfiled Keys')
                      )}{' '}
                      · {k.name} #{k.id}
                    </NativeSelectOption>
                  ))}
              </NativeSelect>
            </label>
          )}
          {action === 'add_route' && (
            <label className='block'>
              {t('Group models')}
              <Input
                value={models}
                maxLength={16000}
                onChange={(e) => setModels(e.target.value)}
              />
            </label>
          )}
          {uses.length > 0 && (
            <div>
              <strong>
                {t(
                  action === 'transfer_owner'
                    ? 'Detach all routes before transferring this Key'
                    : 'Upstream sites'
                )}
              </strong>
              <ul>
                {uses.map((id) => (
                  <li className='break-all' key={id}>
                    {siteLabel(library, id)}
                  </li>
                ))}
              </ul>
            </div>
          )}
          {needsConfirmation && (
            <label className='flex items-center gap-2'>
              <Checkbox
                checked={confirmed}
                onCheckedChange={(v) => setConfirmed(!!v)}
              />
              {t('The replacement Key belongs to this upstream group')} ·{' '}
              {Object.values(groups).join(' / ')}
            </label>
          )}
          {error && <p role='alert'>{error}</p>}
          <DialogFooter>
            <Button
              type='button'
              variant='outline'
              disabled={busy}
              onClick={close}
            >
              {t('Cancel')}
            </Button>
            <Button
              type='submit'
              disabled={
                busy ||
                (needsOwner && !owner) ||
                (action === 'transfer_owner' && uses.length > 0) ||
                (needsConfirmation && !confirmed) ||
                conflict ||
                ((action === 'bind' || action === 'add_route') && !selected)
              }
            >
              {t(labels[action])}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

/** Render each Key once under its custodian. Inline credentials remain on their source sites until imported. */
export function KeyLibrary({ identity }: { identity: string }) {
  const { t } = useTranslation()
  const query = useLibrary(identity)
  const [draft, setDraft] = useState<Draft>()
  const data = query.data
  if (query.isError) {
    return (
      <div role='alert'>
        {t(getServerErrorMessage(query.error))}{' '}
        <Button onClick={() => void query.refetch()}>{t('Retry')}</Button>
      </div>
    )
  }
  if (!data) return <p>{t('Loading...')}</p>
  const old = data.channels?.filter((c) => !c.library_id && c.has_key) ?? []
  // Include inactive owners and unfiled legacy entries so removing a site cannot hide inventory.
  const ownerIds = [
    ...new Set([
      ...(data.channels ?? []).map((row) => row.site_id),
      ...data.keys.map((entry) => entry.owner_site_id || 0),
    ]),
  ].sort((a, b) => a - b)
  return (
    <section className='space-y-3'>
      <Button
        disabled={!data.channels?.some((row) => row.id === row.site_id)}
        onClick={() => setDraft({ action: 'create', library: data })}
      >
        {t(labels.create)}
      </Button>
      {!data.keys.length && <p>{t('No saved Keys')}</p>}
      {ownerIds.map((ownerId) => (
        <section key={ownerId} className='space-y-2'>
          <h2 className='font-medium'>
            {filingSiteLabel(data, ownerId, t('Unfiled Keys'))}
          </h2>
          {data.keys
            .filter((entry) => (entry.owner_site_id || 0) === ownerId)
            .map((entry) => (
              <div
                key={entry.id}
                className='flex flex-wrap items-center justify-between gap-2 border-b py-3'
              >
                <div>
                  <strong>
                    {entry.name} #{entry.id}
                  </strong>
                  <p className='text-muted-foreground text-sm break-all'>
                    {(data.uses[entry.id] ?? [])
                      .map((id) => siteLabel(data, id))
                      .join(' / ') || t('Unused')}
                  </p>
                </div>
                <div className='flex gap-2'>
                  {(data.uses[entry.id] ?? [])
                    .filter(
                      (id) =>
                        !data.channels?.some(
                          (c) =>
                            c.id === id && c.site_id === entry.owner_site_id
                        )
                    )
                    .map((id) => (
                      <Button
                        key={id}
                        size='sm'
                        variant='outline'
                        onClick={() =>
                          setDraft({
                            action: 'detach',
                            library: data,
                            site: { id, name: `#${id}`, base_url: '' } as Site,
                          })
                        }
                      >
                        {t(labels.detach)} #{id}
                      </Button>
                    ))}
                  <Button
                    variant='outline'
                    size='sm'
                    onClick={() =>
                      setDraft({
                        action: entry.owner_site_id
                          ? 'transfer_owner'
                          : 'assign_owner',
                        entry,
                        library: data,
                      })
                    }
                  >
                    {t(labels.assign_owner)}
                  </Button>
                  <Button
                    variant='outline'
                    size='sm'
                    disabled={!entry.owner_site_id}
                    onClick={() =>
                      setDraft({ action: 'rotate', entry, library: data })
                    }
                  >
                    {t(labels.rotate)}
                  </Button>
                  <Button
                    variant='outline'
                    size='sm'
                    disabled={!!data.uses[entry.id]?.length}
                    onClick={() =>
                      setDraft({ action: 'delete', entry, library: data })
                    }
                  >
                    {t('Delete')}
                  </Button>
                </div>
              </div>
            ))}
          {old.some((site) => site.site_id === ownerId) && (
            <h3 className='text-sm font-medium'>{t('Existing site Keys')}</h3>
          )}
          {old
            .filter((site) => site.site_id === ownerId)
            .map((site) => (
              <div
                key={site.id}
                className='flex flex-wrap items-center justify-between gap-2 border-b py-2'
              >
                <span>{siteLabel(data, site.id)}</span>
                <Button
                  size='sm'
                  variant='outline'
                  onClick={() =>
                    setDraft({ action: 'import', site, library: data })
                  }
                >
                  {t(labels.import)}
                </Button>
              </div>
            ))}
        </section>
      ))}
      {draft && (
        <EditKey
          identity={identity}
          draft={draft}
          close={() => setDraft(undefined)}
        />
      )}
    </section>
  )
}

/** Site configuration selects a library ID. The query is shared across all eligible routes in the open editor. */
export function SiteKeyPicker({ channel }: { channel: number }) {
  const identity = useAuthStore(
    (s) => `${s.auth.user?.id}:${s.auth.session?.sid}`
  )
  return <SiteBinding key={identity} identity={identity} channel={channel} />
}

/** Keep the picker hidden for channels outside the server's approved list, including hosted customer accounts. */
function SiteBinding({
  identity,
  channel,
}: {
  identity: string
  channel: number
}) {
  const { t } = useTranslation()
  const query = useLibrary(identity)
  const [draft, setDraft] = useState<Draft>()
  const library = query.data
  const site = library?.channels?.find((c) => c.id === channel)
  if (!library || !site) return null
  return (
    <div className='flex flex-wrap items-center gap-2'>
      {site.library_id && (
        <span>{library.keys.find((k) => k.id === site.library_id)?.name}</span>
      )}
      <Button
        type='button'
        variant='outline'
        size='sm'
        onClick={() =>
          setDraft({
            action: !site.library_id && site.has_key ? 'import' : 'bind',
            site,
            library,
          })
        }
      >
        {!site.library_id && site.has_key ? t(labels.import) : t(labels.bind)}
      </Button>
      {site.allow_new_credential && (
        <Button
          type='button'
          size='sm'
          variant='outline'
          onClick={() => setDraft({ action: 'add_route', site, library })}
        >
          {t(labels.add_route)}
        </Button>
      )}
      {!!site.library_id && (
        <Button
          type='button'
          variant='ghost'
          size='sm'
          onClick={() => setDraft({ action: 'detach', site, library })}
        >
          {t(labels.detach)}
        </Button>
      )}
      {draft && (
        <EditKey
          draft={draft}
          identity={identity}
          close={() => setDraft(undefined)}
        />
      )}
    </div>
  )
}
