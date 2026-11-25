import { queryOptions, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { Globe2, KeyRound, Pencil, Plus } from 'lucide-react'
import {
  useRef,
  useState,
  type FormEvent,
  type ReactNode,
  type RefObject,
} from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { DataTableRowActionMenu } from '@/components/data-table/core/row-action-menu'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { ComboboxInput } from '@/components/ui/combobox-input'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import {
  DropdownMenuItem,
  DropdownMenuSeparator,
} from '@/components/ui/dropdown-menu'
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
  upstream_group?: string
  owner_site_id: number
  id: number
  name: string
  version: number
}
type Site = RetailBindingChannel & { library_id?: number }
type Library = {
  consumers?: Record<number, string[]>
  site_rates?: Record<number, { group: string; multiplier: string }[]>
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
  | 'edit'
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
  edit: 'Edit Key',
  delete: 'Delete',
  bind: 'Select saved Key',
  detach: 'Detach Key and disable route',
  import: 'Import existing Key',
}
const endpoint = '/api/retail/admin/key-library'

/** 列表和按编号打开的编辑器共用本会话的无密钥库存元数据。
 * @param identity 管理员与登录会话标识。
 * @return 只读库存请求；编辑仍需服务器验证版本和影响范围。
 */
function libraryOptions(identity: string) {
  return queryOptions({
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
  finalFocus,
}: {
  draft: Draft
  close: () => void
  identity: string
  finalFocus?: RefObject<HTMLElement | null>
}) {
  const { t } = useTranslation()
  const cache = useQueryClient()
  const { action, entry, site, library } = draft
  const [secret, setSecret] = useState('')
  const [name, setName] = useState(() => {
    if (action === 'edit') return entry?.name ?? ''
    return action === 'import' ? (site?.name ?? '') : ''
  })
  const [models, setModels] = useState('')
  const [owner, setOwner] = useState('')
  const [upstream, setUpstream] = useState(entry?.upstream_group ?? '')
  const editGroup =
    library.site_rates !== undefined &&
    ['create', 'edit', 'import'].includes(action)
  const groupOptions =
    library.site_rates?.[
      Number(owner) || entry?.owner_site_id || site?.site_id || 0
    ] ?? []
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
  if (
    action === 'edit' &&
    (secret || (editGroup && upstream !== entry?.upstream_group)) &&
    entry
  ) {
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
  const changingGroup =
    editGroup &&
    action === 'edit' &&
    upstream.trim() !== (entry?.upstream_group ?? '')
  const consumers = [
    ...new Set(affected.flatMap((id) => library.consumers?.[id] ?? [])),
  ]
  const needsConfirmation =
    Object.keys(groups).length > 0 || (changingGroup && consumers.length > 0)
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
          ...(editGroup && upstream && { upstream_group: upstream.trim() }),
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
        setSecret('')
        setConflict(true)
        await cache.invalidateQueries({
          queryKey: ['retail-key-library', identity],
        })
      }
    } finally {
      // An edit retry must not silently become a rename. The open form alone holds its replacement.
      if (action !== 'edit') setSecret('')
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
      <DialogContent finalFocus={finalFocus}>
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
                onChange={(e) => {
                  setOwner(e.target.value)
                  setUpstream('')
                }}
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
          {editGroup && (
            <label className='block'>
              {t('Upstream group')}
              <ComboboxInput
                aria-label={t('Upstream group')}
                allowCustomValue
                showEmptyCustomValue={false}
                placeholder={t('Select or type...')}
                spellCheck={false}
                options={groupOptions.map((rate) => ({
                  value: rate.group,
                  label: rate.group,
                  description: `${rate.multiplier}×`,
                }))}
                value={upstream}
                onValueChange={(value) => {
                  setUpstream(value)
                  setConfirmed(false)
                }}
              />
            </label>
          )}
          {(action === 'create' ||
            action === 'import' ||
            action === 'edit' ||
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
          {(action === 'create' ||
            (action === 'edit' && !!entry?.owner_site_id)) && (
            <label className='block'>
              {t('Upstream API Key')}
              <Input
                required={action === 'create'}
                placeholder={
                  action === 'edit'
                    ? t('Leave blank to keep the existing key')
                    : undefined
                }
                type='password'
                autoComplete='new-password'
                maxLength={4096}
                spellCheck={false}
                value={secret}
                onChange={(e) => {
                  setSecret(e.target.value)
                  setConfirmed(false)
                }}
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
              {changingGroup ? (
                <span>
                  {t('Confirm')} ·{' '}
                  {entry?.upstream_group ||
                    Object.values(groups).join(' / ') ||
                    '—'}{' '}
                  → {upstream.trim()}
                  {consumers.length > 0 && (
                    <span className='block'>
                      {t('Referenced call groups')} · {consumers.join(' / ')}
                    </span>
                  )}
                </span>
              ) : (
                <span>
                  {t('The replacement Key belongs to this upstream group')} ·{' '}
                  {Object.values(groups).join(' / ')}
                </span>
              )}
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
                (action === 'edit' &&
                  (!name.trim() ||
                    (!secret &&
                      name.trim() === entry?.name &&
                      (!editGroup || upstream === entry?.upstream_group)))) ||
                (editGroup &&
                  (!upstream.trim() ||
                    new TextEncoder().encode(upstream.trim()).length > 128)) ||
                (needsOwner && !owner) ||
                (action === 'transfer_owner' && uses.length > 0) ||
                (needsConfirmation && !confirmed) ||
                conflict ||
                ((action === 'bind' || action === 'add_route') && !selected)
              }
            >
              {action === 'edit' ? t('Save') : t(labels[action])}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

/** The page header shares the inventory query. Pin the snapshot when opening a create form. */
export function AddLibraryKey({ identity }: { identity: string }) {
  const { t } = useTranslation()
  const { data } = useQuery(libraryOptions(identity))
  const [draft, setDraft] = useState<Draft>()
  return (
    <>
      <Button
        disabled={!data?.channels?.some((row) => row.id === row.site_id)}
        onClick={() => data && setDraft({ action: 'create', library: data })}
      >
        <Plus aria-hidden />
        {t(labels.create)}
      </Button>
      {draft && (
        <EditKey
          identity={identity}
          draft={draft}
          close={() => setDraft(undefined)}
        />
      )}
    </>
  )
}

/** 在分组行或路由抽屉中编辑指定库存 Key。按下时才读取库存，并固定本次确认所用的版本。
 * @param props 库存主键和当前会话，渠道编号不能用作库存编号。
 * @return 就地编辑入口；关闭后焦点回到打开它的控件。
 */
export function EditLibraryKeyButton(props: {
  id: number
  identity: string
  label: string
  children: ReactNode
}) {
  const { t } = useTranslation()
  const cache = useQueryClient()
  const [draft, setDraft] = useState<Draft>()
  const [busy, setBusy] = useState(false)
  const trigger = useRef<HTMLButtonElement>(null)
  /** 查询失败或 Key 已删除时不打开其他凭据；管理员可以再次点击刷新。 */
  async function open() {
    if (busy) return
    setBusy(true)
    try {
      const library = await cache.fetchQuery({
        ...libraryOptions(props.identity),
        staleTime: 0,
      })
      const entry = library.keys.find((key) => key.id === props.id)
      // 加载期间可能已切换页签、退出或关闭抽屉。不要在已离开的界面上弹出编辑器。
      if (
        !trigger.current ||
        trigger.current.closest('[hidden],[data-hidden]')
      ) {
        return
      }
      if (!entry) {
        toast.error(t('Key no longer available'))
        return
      }
      setDraft({ action: 'edit', entry, library })
    } catch (error) {
      toast.error(t(getServerErrorMessage(error)))
    } finally {
      setBusy(false)
    }
  }
  return (
    <>
      <Button
        ref={trigger}
        type='button'
        variant='link'
        size='sm'
        className='gm-key-link'
        aria-label={props.label}
        aria-busy={busy}
        aria-haspopup='dialog'
        onClick={() => void open()}
      >
        {props.children}
      </Button>
      {draft && (
        <EditKey
          draft={draft}
          identity={props.identity}
          finalFocus={trigger}
          close={() => setDraft(undefined)}
        />
      )}
    </>
  )
}

/** Keep referenced routes distinct from library IDs. Show a destination inline only when it differs from the site's header. */
function routeSummary(library: Library, id: number, ownerUrl?: string) {
  const route = library.channels?.find((row) => row.id === id)
  if (!route) return `#${id}`
  return [
    `${route.name} #${id}`,
    route.base_url !== ownerUrl && route.base_url,
    library.groups[id],
  ]
    .filter(Boolean)
    .join(' · ')
}

/** Render each Key under its owner, including legacy references that still need an explicit detach. */
export function KeyLibrary({
  identity,
  search = '',
}: {
  identity: string
  search?: string
}) {
  const { t } = useTranslation()
  const query = useQuery(libraryOptions(identity))
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
  const channels = data.channels ?? []
  const term = search.trim().toLowerCase()
  // 库存编号与旧渠道编号可能相同。两类结果仍在各自的区块中，原始凭据不进入搜索。
  const savedKeys = (data.keys ?? []).filter((entry) =>
    [
      entry.name,
      `#${entry.id}`,
      entry.upstream_group,
      ...(data.uses[entry.id] ?? []).flatMap(
        (id) => data.consumers?.[id] ?? []
      ),
    ].some((value) => value?.toLowerCase().includes(term))
  )
  const pending = channels.filter(
    (c) =>
      !c.library_id &&
      c.has_key &&
      [c.name, `#${c.id}`, data.groups[c.id]].some((value) =>
        value?.toLowerCase().includes(term)
      )
  )
  // Include missing owners and unfiled legacy entries even when no selectable root remains.
  const ownerIds = [
    ...new Set([
      ...(term ? pending : channels).map((row) => row.site_id),
      ...savedKeys.map((entry) => entry.owner_site_id || 0),
    ]),
  ].sort((a, b) => a - b)
  return (
    <div className='space-y-5'>
      {!ownerIds.length && (
        <p className='text-muted-foreground py-6 text-sm'>
          {t(term ? 'No results found' : 'No saved Keys')}
        </p>
      )}
      {ownerIds.map((ownerId) => {
        const root = channels.find(
          (c) => c.id === ownerId && c.site_id === ownerId
        )
        const saved = savedKeys.filter(
          (entry) => (entry.owner_site_id || 0) === ownerId
        )
        const legacy = pending.filter((site) => site.site_id === ownerId)
        return (
          <section
            key={ownerId}
            aria-labelledby={`key-site-${ownerId}`}
            className='bg-card text-card-foreground overflow-hidden rounded-xl border shadow-xs'
          >
            <header className='bg-muted/35 flex items-center gap-3 border-b px-4 py-4 sm:px-5'>
              <span className='bg-background text-primary grid size-10 shrink-0 place-items-center rounded-lg border'>
                <Globe2 className='size-5' aria-hidden />
              </span>
              <div className='min-w-0'>
                <h3
                  id={`key-site-${ownerId}`}
                  className='text-base font-semibold wrap-anywhere'
                >
                  {root?.name || (ownerId ? '' : t('Unfiled Keys'))}{' '}
                  {!!ownerId && (
                    <span className='text-muted-foreground font-mono text-xs'>
                      #{ownerId}
                    </span>
                  )}
                </h3>
                {root?.base_url && (
                  <div className='text-muted-foreground text-xs wrap-anywhere'>
                    {root.base_url}
                  </div>
                )}
              </div>
            </header>
            {!!saved.length && (
              <div className='px-4 pt-3 sm:px-5'>
                <div
                  aria-hidden
                  className='text-muted-foreground hidden text-xs sm:ms-7 sm:me-[5.5rem] sm:grid sm:grid-cols-[minmax(0,2fr)_minmax(0,3fr)] sm:gap-4'
                >
                  <span>
                    {t('Inventory Key')} / {t('Upstream group')}
                  </span>
                  <span>
                    {t(
                      data.consumers
                        ? 'Referenced call groups'
                        : 'Supply routes'
                    )}
                  </span>
                </div>
                <ul className='divide-y'>
                  {saved.map((entry) => {
                    const uses = data.uses[entry.id] ?? []
                    const consumers = [
                      ...new Set(
                        uses.flatMap((id) => data.consumers?.[id] ?? [])
                      ),
                    ]
                    return (
                      <li
                        key={entry.id}
                        className='flex items-center gap-3 py-2'
                      >
                        <KeyRound
                          className='text-muted-foreground size-4 shrink-0'
                          aria-hidden
                        />
                        <div className='min-w-0 flex-1 sm:grid sm:grid-cols-[minmax(0,2fr)_minmax(0,3fr)] sm:items-center sm:gap-4'>
                          <div className='text-sm font-medium wrap-anywhere'>
                            {entry.name}{' '}
                            <span className='text-muted-foreground font-mono text-xs'>
                              #{entry.id}
                            </span>
                            {data.site_rates && (
                              <div className='text-muted-foreground text-xs'>
                                <span className='sm:sr-only'>
                                  {t('Upstream group')} ·{' '}
                                </span>
                                {entry.upstream_group ||
                                  t('Select upstream group')}
                              </div>
                            )}
                          </div>
                          <div className='text-muted-foreground text-xs leading-relaxed wrap-anywhere'>
                            {consumers.length > 0 && (
                              <div className='text-foreground mb-0.5 font-medium'>
                                <span className='sm:sr-only'>
                                  {t('Referenced call groups')} ·{' '}
                                </span>
                                {consumers.join(' · ')}
                              </div>
                            )}
                            {uses.length
                              ? uses.map((id) => (
                                  <div key={id}>
                                    {routeSummary(data, id, root?.base_url)}
                                  </div>
                                ))
                              : t('Unused')}
                          </div>
                        </div>
                        <Button
                          size='icon'
                          variant='ghost'
                          aria-label={`${t(labels.edit)} · ${entry.name} #${entry.id}`}
                          title={t(labels.edit)}
                          onClick={() =>
                            setDraft({ action: 'edit', entry, library: data })
                          }
                        >
                          <Pencil aria-hidden />
                        </Button>
                        <DataTableRowActionMenu
                          ariaLabel={`${t('Actions')} · ${entry.name} #${entry.id}`}
                        >
                          <DropdownMenuItem
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
                          </DropdownMenuItem>
                          {/* Hidden or foreign references must still be detachable by their safe numeric IDs. */}
                          {uses
                            .filter(
                              (id) =>
                                !channels.some(
                                  (c) =>
                                    c.id === id &&
                                    c.site_id === entry.owner_site_id
                                )
                            )
                            .map((id) => (
                              <DropdownMenuItem
                                key={id}
                                onClick={() =>
                                  setDraft({
                                    action: 'detach',
                                    library: data,
                                    site: {
                                      id,
                                      name: `#${id}`,
                                      base_url: '',
                                    } as Site,
                                  })
                                }
                              >
                                {t(labels.detach)} #{id}
                              </DropdownMenuItem>
                            ))}
                          <DropdownMenuSeparator />
                          <DropdownMenuItem
                            variant='destructive'
                            disabled={!!uses.length}
                            onClick={() =>
                              setDraft({
                                action: 'delete',
                                entry,
                                library: data,
                              })
                            }
                          >
                            {t('Delete')}
                          </DropdownMenuItem>
                        </DataTableRowActionMenu>
                      </li>
                    )
                  })}
                </ul>
              </div>
            )}
            {!!legacy.length && (
              <div
                className={`px-4 py-3 sm:px-5 ${saved.length ? 'border-t' : ''}`}
              >
                <h4 className='text-muted-foreground mb-1 text-xs font-medium'>
                  {t('Existing site Keys')}
                </h4>
                <ul className='divide-y'>
                  {legacy.map((site) => (
                    <li
                      key={site.id}
                      className='flex items-center justify-between gap-3 py-1.5'
                    >
                      <div className='min-w-0 text-sm wrap-anywhere'>
                        {site.name}{' '}
                        <span className='text-muted-foreground font-mono text-xs'>
                          #{site.id}
                        </span>
                        <div className='text-muted-foreground text-xs'>
                          {[
                            site.base_url !== root?.base_url && site.base_url,
                            data.groups[site.id],
                          ]
                            .filter(Boolean)
                            .join(' · ')}
                        </div>
                      </div>
                      <Button
                        size='sm'
                        variant='outline'
                        aria-label={`${t(labels.import)} · ${site.name} #${site.id}`}
                        onClick={() =>
                          setDraft({ action: 'import', site, library: data })
                        }
                      >
                        {t('Import Key')}
                      </Button>
                    </li>
                  ))}
                </ul>
              </div>
            )}
            {!saved.length && !legacy.length && (
              <p className='text-muted-foreground px-5 py-4 text-sm'>
                {t('No saved Keys')}
              </p>
            )}
          </section>
        )
      })}
      {draft && (
        <EditKey
          identity={identity}
          draft={draft}
          close={() => setDraft(undefined)}
        />
      )}
    </div>
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
  const query = useQuery(libraryOptions(identity))
  const [draft, setDraft] = useState<Draft>()
  const library = query.data
  const site = library?.channels?.find((c) => c.id === channel)
  if (!library || !site) return null
  if (library.site_rates) {
    return (
      <Link className='text-sm underline' to='/groups' search={{ tab: 'keys' }}>
        {t('Upstream Keys')}
      </Link>
    )
  }
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
