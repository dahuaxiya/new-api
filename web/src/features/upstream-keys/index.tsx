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
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { ChevronRight, KeyRound, RefreshCw, Route, Search } from 'lucide-react'
import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogFooter,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import {
  InputGroup,
  InputGroupAddon,
  InputGroupInput,
} from '@/components/ui/input-group'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import type { RetailBindingChannel } from '@/features/channels/retail-pricing-api'
import { api } from '@/lib/api'
import {
  requireServerSuccess,
  getServerErrorMessage,
} from '@/lib/server-error-message'

import { credentialUses, credentialCoverage, type Inventory } from './coverage'
import { AddLibraryKey, KeyLibrary } from './library'

type Action = 'enable' | 'disable'
type Draft = { action: Action; row: RetailBindingChannel; inventory: Inventory }
const titles = {
  enable: 'Enable',
  disable: 'Disable',
}

/** 弹窗使用打开时的版本，后台刷新不能悄悄更换待确认的影响范围。离开弹窗即清空密钥。 */
function CredentialDialog({
  draft,
  close,
  saved,
}: {
  draft: Draft
  close: () => void
  saved: () => Promise<void>
}) {
  const { t } = useTranslation()
  const { action, row, inventory } = draft
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const uses = credentialUses(inventory, row.id)
  /** 提交单项动作。网络失败不重试携带密钥的请求，也不把 Axios 错误或密钥存进共享缓存。 */
  async function submit(e: FormEvent) {
    e.preventDefault()
    if (busy) return
    setBusy(true)
    setError('')
    try {
      const payload = {
        version: inventory.version,
        stamp: inventory.stamp,
        site_id: row.site_id,
        id: row.id,
        action,
      }
      const result = await api.put('/api/retail/admin/credentials', payload, {
        singleUseAuthorization: true,
        skipErrorHandler: true,
      })
      requireServerSuccess(result.data)
    } catch (err) {
      setBusy(false)
      if (
        (err as { response?: { status: number } })?.response?.status === 409
      ) {
        close()
        toast.error(t('Retail pricing changed. Reload before saving.'))
        await saved()
        return
      }
      setError(t(getServerErrorMessage(err)))
      return
    }
    close()
    await saved()
  }
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !busy) close()
      }}
    >
      <DialogContent className='max-h-[90dvh] overflow-y-auto'>
        <DialogHeader>
          <DialogTitle>
            {t(titles[action])} · {row.name}
          </DialogTitle>
        </DialogHeader>
        <form className='space-y-4' onSubmit={submit}>
          <section className='space-y-2'>
            <h3 className='font-medium'>{t('Referenced call groups')}</h3>
            {!uses.products.length && !uses.bindings.length && (
              <p>{t('None')}</p>
            )}
            {uses.products.map((p) => {
              const { candidates, missing } = credentialCoverage(
                p,
                row.id,
                inventory.channels ?? [],
                inventory.targets
              )
              return (
                <div key={p.id} className='rounded-md border p-2 text-sm'>
                  <strong>{p.binding_group || p.name}</strong>
                  {p.rate_source_channel_id === row.id && (
                    <div>{t('Selling multiplier source')}</div>
                  )}
                  {(p.routes ?? []).some((r) => r.channel_id === row.id) && (
                    <>
                      <div>{t('Supply route')}</div>
                      <div>
                        {t('Configured alternatives')}:{' '}
                        {candidates.join(' · ') || t('None')}
                      </div>
                      {missing.length > 0 && (
                        <div>
                          {t('Models without an alternative')}:{' '}
                          {missing.join(', ')}
                        </div>
                      )}
                    </>
                  )}
                </div>
              )
            })}
            {uses.bindings.map((b) => (
              <p key={b.local_group}>
                {b.local_group} · {t('Supply binding')}
              </p>
            ))}
            {uses.products.length > 0 && (
              <p className='text-muted-foreground text-sm'>
                {t(
                  'Coverage reflects configuration only. Upstream availability may vary.'
                )}
              </p>
            )}
          </section>
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
            <Button type='submit' disabled={busy}>
              {t(titles[action])}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

/** 按所属站点管理库存；供货线路的启停与更换库存 Key 分开确认。
 * @param props 当前管理员会话，由分组工作区在切换身份时重新挂载。
 * @return 可检索的库存和折叠的线路管理。
 */
export function InventoryPanel({ identity }: { identity: string }) {
  const { t } = useTranslation()
  const cache = useQueryClient()
  const [filter, setFilter] = useState('')
  const [keySearch, setKeySearch] = useState('')
  const [routesOpen, setRoutesOpen] = useState(false)
  const [site, setSite] = useState('')
  const [status, setStatus] = useState('')
  const [draft, setDraft] = useState<Draft>()
  const query = useQuery({
    queryKey: ['upstream-keys', identity],
    enabled: routesOpen,
    retry: false,
    queryFn: async ({ signal }): Promise<Inventory> =>
      requireServerSuccess(
        (await api.get('/api/retail/admin/credentials', { signal })).data
      ).data,
  })
  const data = query.data
  const channels = data?.channels ?? []
  const sites = [...new Set(channels.map((c) => c.site_id))]
  const unknown = new Set<number>()
  for (const id of [
    ...Object.keys(data?.groups ?? {}).map(Number),
    ...(data?.bindings ?? []).map((b) => b.channel_id),
    ...(data?.products ?? []).flatMap((p) => [
      p.rate_source_channel_id ?? 0,
      ...(p.routes ?? []).map((r) => r.channel_id),
    ]),
  ]) {
    if (id && !channels.some((c) => c.id === id)) unknown.add(id)
  }
  /** 各入口共享服务端状态；保存后使本会话下其他表单的读取缓存失效。 */
  async function refresh() {
    await Promise.all(
      [
        'upstream-keys',
        'retail-key-library',
        'retail-pricing',
        'retail-group-bindings',
      ].map((k) => cache.invalidateQueries({ queryKey: [k, identity] }))
    )
  }
  return (
    <section className='gm-inventory space-y-4' aria-label={t('Upstream Keys')}>
      <header className='gm-key-toolbar'>
        <h3>
          <KeyRound aria-hidden />
          {t('Inventory Key')}
        </h3>
        <InputGroup className='gm-key-search'>
          <InputGroupAddon>
            <Search aria-hidden />
          </InputGroupAddon>
          <InputGroupInput
            aria-label={t('Find a group or Key')}
            placeholder={t('Find a group or Key')}
            value={keySearch}
            onChange={(event) => setKeySearch(event.target.value)}
          />
        </InputGroup>
        <div className='flex items-center gap-1'>
          <AddLibraryKey identity={identity} />
          <Button
            variant='outline'
            size='icon'
            aria-label={t('Refresh')}
            title={t('Refresh')}
            onClick={() => void refresh()}
          >
            <RefreshCw aria-hidden />
          </Button>
        </div>
      </header>
      <KeyLibrary identity={identity} search={keySearch} />
      <details
        className='group bg-card rounded-lg border'
        onToggle={(event) => setRoutesOpen(event.currentTarget.open)}
      >
        <summary className='flex cursor-pointer list-none items-center gap-3 px-4 py-4 text-sm font-semibold sm:px-5 [&::-webkit-details-marker]:hidden'>
          <Route className='text-muted-foreground size-4' aria-hidden />
          {t('Supply routes')}
          <ChevronRight
            className='text-muted-foreground ml-auto size-4 group-open:rotate-90'
            aria-hidden
          />
        </summary>
        <div className='space-y-3 border-t p-4 sm:p-5'>
          <div className='flex flex-wrap gap-2'>
            <Input
              className='w-full sm:w-60'
              aria-label={t('Search')}
              placeholder={t('Search by Key name or upstream group')}
              value={filter}
              onChange={(e) => setFilter(e.target.value)}
            />
            <NativeSelect
              aria-label={t('Upstream site')}
              value={site}
              onChange={(e) => setSite(e.target.value)}
            >
              <NativeSelectOption value=''>{t('All sites')}</NativeSelectOption>
              {sites.map((id) => (
                <NativeSelectOption key={id} value={id}>
                  {channels.find((c) => c.id === id)?.name ?? `#${id}`}
                </NativeSelectOption>
              ))}
            </NativeSelect>
            <NativeSelect
              aria-label={t('Status')}
              value={status}
              onChange={(e) => setStatus(e.target.value)}
            >
              <NativeSelectOption value=''>
                {t('All statuses')}
              </NativeSelectOption>
              <NativeSelectOption value='enabled'>
                {t('Enabled')}
              </NativeSelectOption>
              <NativeSelectOption value='disabled'>
                {t('Disabled')}
              </NativeSelectOption>
            </NativeSelect>
          </div>
          {query.isPending && <p>{t('Loading...')}</p>}
          {query.isError && (
            <p role='alert'>{t(getServerErrorMessage(query.error))}</p>
          )}
          {data && !sites.length && <p>{t('None')}</p>}
          {data &&
            sites
              .filter((id) => !site || String(id) === site)
              .map((id) => {
                const root = channels.find((c) => c.id === id)
                const rows = channels.filter(
                  (c) =>
                    c.site_id === id &&
                    (!status || (c.status === 1) === (status === 'enabled')) &&
                    [c.name, `#${c.id}`, data.groups[c.id] ?? t('Unassigned')]
                      .join(' ')
                      .toLowerCase()
                      .includes(filter.trim().toLowerCase())
                )
                return (
                  <section key={id} className='rounded-lg border p-4'>
                    <div className='flex flex-wrap items-center justify-between gap-2'>
                      <h2 className='font-semibold'>
                        {root?.name ?? `#${id}`}
                      </h2>
                    </div>
                    {!rows.length && <p className='py-3'>{t('None')}</p>}
                    {rows.map((row) => {
                      const uses = credentialUses(data, row.id)
                      const names = [
                        ...new Set([
                          ...uses.products.map(
                            (p) => p.binding_group || p.name
                          ),
                          ...uses.bindings.map((b) => b.local_group),
                        ]),
                      ]
                      return (
                        <div
                          key={row.id}
                          className='mt-3 flex flex-wrap items-center justify-between gap-3 border-t pt-3'
                        >
                          <div>
                            <strong>{row.name}</strong>{' '}
                            <span className='text-muted-foreground'>
                              #{row.id}
                            </span>
                            <div className='text-sm'>
                              {data.groups[row.id] || t('Unassigned')} ·{' '}
                              {t(row.status === 1 ? 'Enabled' : 'Disabled')}
                            </div>
                            <div className='text-muted-foreground text-sm'>
                              {t('Referenced call groups')}:{' '}
                              {names.join(' · ') || t('None')}
                            </div>
                          </div>
                          <div className='flex gap-2'>
                            <Button
                              variant='outline'
                              size='sm'
                              onClick={() =>
                                setDraft({
                                  action:
                                    row.status === 1 ? 'disable' : 'enable',
                                  row,
                                  inventory: data,
                                })
                              }
                            >
                              {t(row.status === 1 ? 'Disable' : 'Enable')}
                            </Button>
                          </div>
                        </div>
                      )
                    })}
                  </section>
                )
              })}
          {!!unknown.size && (
            <p>
              {t('Unavailable credential references')}:{' '}
              {[...unknown].map((id) => `#${id}`).join(', ')}
            </p>
          )}
        </div>
      </details>
      {draft && (
        <CredentialDialog
          draft={draft}
          close={() => setDraft(undefined)}
          saved={refresh}
        />
      )}
    </section>
  )
}
