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
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import {
  sideDrawerContentClassName,
  sideDrawerHeaderClassName,
} from '@/components/drawer-layout'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import {
  Sheet,
  SheetContent,
  SheetTitle,
  SheetHeader,
} from '@/components/ui/sheet'
import { fetchModels } from '@/features/channels/api'
import { SellingPricePreview } from '@/features/channels/components/retail-pricing'
import {
  getRetailGroupBindings,
  saveRetailSiteFeed,
  type RetailSiteFeed,
  getRetailPricing,
  launchRetailGroup,
  type RetailPriceBook,
  type RetailBindingState,
  type RetailLaunchInput,
  type RetailLaunchReview,
  type RetailLaunchRoute,
} from '@/features/channels/retail-pricing-api'
import { useStatus } from '@/hooks/use-status'
import { formatNumber } from '@/lib/format'
import { ROLE } from '@/lib/roles'
import { getServerErrorMessage } from '@/lib/server-error-message'
import { useAuthStore } from '@/stores/auth-store'

/** 入口只在中转站管理后台显示。会话切换时销毁包含未保存 Key 的表单。 */
export function RetailGroupLaunch() {
  const { t } = useTranslation()
  const { status } = useStatus()
  const { user, session } = useAuthStore((s) => s.auth)
  const [open, setOpen] = useState(false)
  const [editing, setEditing] = useState<PublishedGroupEdit>()
  if (status?.site_mode !== 'retail' || user?.role !== ROLE.SUPER_ADMIN) {
    return null
  }
  return (
    <Sheet open={open} onOpenChange={setOpen}>
      <Button
        className='self-start'
        onClick={() => {
          setEditing(undefined)
          setOpen(true)
        }}
      >
        {t('Publish a call group')}
      </Button>
      <PublishedGroups
        identity={`${user.id}:${session?.sid}`}
        onEdit={(draft) => {
          setEditing(draft)
          setOpen(true)
        }}
      />
      <SheetContent className={sideDrawerContentClassName('sm:max-w-2xl')}>
        <SheetHeader className={sideDrawerHeaderClassName()}>
          <SheetTitle>
            {t(editing ? 'Edit call group routing' : 'Publish a call group')}
          </SheetTitle>
        </SheetHeader>
        {open && (
          <LaunchForm
            key={`${user.id}:${session?.sid}:${editing?.version ?? 'new'}`}
            identity={`${user.id}:${session?.sid}`}
            editing={editing}
            onReload={setEditing}
            onPublished={() => setOpen(false)}
          />
        )}
      </SheetContent>
    </Sheet>
  )
}

type PublishedProduct = RetailPriceBook['products'][number]
type PublishedGroupEdit = {
  product: PublishedProduct
  version: number
  bindings: RetailPriceBook['group_bindings']
}

/** 保存打开时该组的绑定，区分同组被修改和无关的全站价格更新。 */
function groupEditSnapshot(
  product: PublishedProduct,
  book: RetailPriceBook,
  version: number
): PublishedGroupEdit {
  return {
    product,
    version,
    bindings:
      book.group_bindings?.filter(
        (b) => b.local_group === product.binding_group
      ) ?? [],
  }
}

/** 页面列表和重名检查使用同一份服务端价表，避免把账号分组当作已上架的线路。 */
function usePublishedPricing(identity: string) {
  return useQuery({
    queryKey: ['retail-pricing', identity],
    queryFn: ({ signal }) => getRetailPricing(signal),
    retry: false,
  })
}

/** 历史手工价表可能没有绑定记录，仍显示实际保存的线路 ID。 */
function productRouteLabel(
  book: RetailPriceBook,
  product: RetailPriceBook['products'][number]
) {
  return product.routes
    .map((route) => {
      const binding = book.group_bindings?.find(
        (b) =>
          b.channel_id === route.channel_id &&
          b.local_group === product.binding_group
      )
      return [`#${route.channel_id}`, binding?.upstream_group]
        .filter(Boolean)
        .join(' · ')
    })
    .join(' / ')
}

/** 读取正式价表，供管理员核对实际发布的调用分组和线路。账号倍率表可能没有同名项。 */
function PublishedGroups({
  identity,
  onEdit,
}: {
  identity: string
  onEdit: (draft: PublishedGroupEdit) => void
}) {
  const { t } = useTranslation()
  // 和价表共用按登录会话隔离的缓存，保存成功后由上架表单触发重新读取。
  const query = usePublishedPricing(identity)
  const supply = useQuery({
    queryKey: ['retail-group-bindings', identity],
    queryFn: ({ signal }) => getRetailGroupBindings(signal),
    retry: false,
  })
  const saved = query.data
  const book = saved?.book
  return (
    <section aria-label={t('Published call groups')} className='space-y-2'>
      <h3 className='font-medium'>{t('Published call groups')}</h3>
      {query.isError && (
        <Button variant='outline' onClick={() => void query.refetch()}>
          {t('Retry')}
        </Button>
      )}
      {!query.isError && !book && <p>{t('Loading...')}</p>}
      {!query.isError && book && !book.products?.length && <p>{t('None')}</p>}
      {!query.isError && book && !!book.products?.length && (
        <dl className='grid gap-2 text-sm'>
          {book.products.map((product) => (
            <div
              key={product.id}
              className='flex flex-wrap items-baseline gap-x-3'
            >
              <dt className='font-medium'>{product.name}</dt>
              <dd className='text-muted-foreground'>
                {supply.data?.inventory_keys
                  ? product.routes
                      .map((route) => {
                        const ch = supply.data.rate_channels?.find(
                          (c) => c.id === route.channel_id
                        )
                        const key = supply.data.inventory_keys?.find(
                          (k) => k.id === ch?.library_id
                        )
                        return key
                          ? `${key.name} · ${key.upstream_group}`
                          : productRouteLabel(book, {
                              ...product,
                              routes: [route],
                            })
                      })
                      .join(' / ')
                  : productRouteLabel(book, product)}
                {!supply.data?.inventory_keys &&
                  product.rate_source_channel_id && (
                    <span className='ms-2'>
                      {t('Selling multiplier source')}: #
                      {product.rate_source_channel_id}
                    </span>
                  )}
              </dd>
              {product.public && product.binding_group && (
                <Button
                  size='sm'
                  variant='ghost'
                  onClick={() =>
                    onEdit(groupEditSnapshot(product, book, saved.version))
                  }
                >
                  {t('Edit call group routing')}
                </Button>
              )}
            </div>
          ))}
        </dl>
      )}
    </section>
  )
}

/** 预览获取当前版本和新 Key 的首份模型清单。发布仅提交用户刚审阅的快照，冲突时保留输入。 */
function LaunchForm({
  identity,
  editing: editSnapshot,
  onReload,
  onPublished,
}: {
  identity: string
  editing?: PublishedGroupEdit
  onReload: (latest: PublishedGroupEdit) => void
  onPublished: () => void
}) {
  const { t } = useTranslation()
  const client = useQueryClient()
  const editing = editSnapshot?.product
  const state = useQuery({
    queryKey: ['retail-group-bindings', identity],
    queryFn: ({ signal }) => getRetailGroupBindings(signal),
    retry: false,
    refetchOnWindowFocus: false,
  })
  const pricing = usePublishedPricing(identity)
  const initialAnchor =
    editing?.rate_source_channel_id ??
    pricing.data?.book.group_bindings?.find(
      (binding) =>
        binding.local_group === editing?.binding_group &&
        editing.routes.some((route) => route.channel_id === binding.channel_id)
    )?.channel_id
  const [site, setSite] = useState(0)
  const [credentialChoice, setCredentialChoice] = useState<number | null>(
    initialAnchor ?? null
  )
  const [upstreamChoice, setUpstream] = useState<string>()
  const [group, setGroup] = useState(editing?.binding_group ?? '')
  const [key, setKey] = useState('')
  const [margin, setMargin] = useState(
    editing?.margin_bps === undefined ? '' : String(editing.margin_bps / 100)
  )
  // 存量组预填原线路。调整定价来源需要明确选择新的绑定，旧线路仍可保留为备用。
  const [useSourceRoute, setUseSourceRoute] = useState(
    !editing ||
      !initialAnchor ||
      editing.routes.some((route) => route.channel_id === initialAnchor)
  )
  const [routeDrafts, setRouteDrafts] = useState<RetailLaunchRoute[]>(
    () =>
      editing?.routes
        .filter((r) => r.channel_id !== initialAnchor)
        .map((route) => ({
          channel_id: route.channel_id,
          upstream_group:
            pricing.data?.book.group_bindings?.find(
              (b) =>
                b.local_group === editing.binding_group &&
                b.channel_id === route.channel_id
            )?.upstream_group ?? '',
          rate_id: route.rate_id,
        })) ?? []
  )
  const [conflict, setConflict] = useState<PublishedGroupEdit>()
  const [busy, setBusy] = useState(false)
  const [failure, setFailure] = useState('')
  const [preview, setPreview] = useState<{
    input: RetailLaunchInput
    siteName: string
    credentialName: string
    result: RetailLaunchReview
  } | null>(null)
  const data = state.data
  const rateChannels = data?.rate_channels ?? data?.channels ?? []
  const sites = rateChannels.filter((c) => c.site_id === c.id)
  const anchorChannel = rateChannels.find((c) => c.id === initialAnchor)
  const selected = sites.find(
    (c) => c.id === (site || anchorChannel?.site_id || sites[0]?.id)
  )
  const upstream =
    upstreamChoice ??
    (initialAnchor ? data?.credential_groups?.[initialAnchor] : undefined) ??
    ''
  const routingChannels = data?.routing_channels ?? data?.channels ?? []
  const legacySites = new Set(data?.channels.map((c) => c.site_id))
  /** 只在已登记的星行渠道上兼容旧价表，外站同名组必须使用本人的站点快照。 */
  function ratesFor(siteId?: number) {
    if (!siteId) return []
    return (
      data?.site_feeds?.[siteId]?.snapshot.groups ??
      (legacySites.has(siteId) ? data?.sync.snapshot?.groups : undefined) ??
      []
    )
  }
  const rates = ratesFor(selected?.id)
  const sourceFeed = selected && data?.site_feeds?.[selected.id]
  const sourceReady = sourceFeed
    ? !sourceFeed.last_error
    : !!(selected && legacySites.has(selected.id) && data?.sync.enabled)
  const credentials =
    rateChannels.filter((c) => {
      const pinned = data?.credential_groups?.[c.id]
      return (
        c.site_id === selected?.id &&
        ((!useSourceRoute && pinned === upstream) ||
          (c.has_key && c.status === 1)) &&
        (!pinned || pinned === upstream)
      )
    }) ?? []
  const matching = credentials.filter(
    (c) => data?.credential_groups?.[c.id] === upstream
  )
  // 只自动选唯一已绑定的 Key。未绑定的旧 Key 须由管理员选择，不能猜它在上游的用途。
  let defaultChannel: number | null =
    matching[0]?.id ?? (selected?.allow_new_credential === false ? null : 0)
  if (matching.length > 1) defaultChannel = null
  const channel = credentialChoice ?? defaultChannel
  const credential = credentials.find((c) => c.id === channel)
  const book = pricing.data?.book
  const duplicate = book?.products?.find(
    (p) =>
      p.id !== editing?.id &&
      (p.name === group.trim() || p.binding_group === group.trim())
  )
  const additionalRoutes = routeDrafts
    .filter((route) => route.channel_id !== channel)
    .map((route) => {
      const upstreamGroup =
        data?.credential_groups?.[route.channel_id] ||
        route.upstream_group.trim()
      const siteId = routingChannels.find(
        (c) => c.id === route.channel_id
      )?.site_id
      const sharedRate =
        siteId && book?.site_group_rates?.[siteId]?.[upstreamGroup]
      return {
        ...route,
        upstream_group: upstreamGroup,
        rate_id: ratesFor(siteId).length ? '' : sharedRate || route.rate_id,
      }
    })
  const canPreview = !!(
    selected &&
    group.trim() &&
    !duplicate &&
    rates.some((r) => r.group === upstream) &&
    (credential || (channel === 0 && key.trim())) &&
    (useSourceRoute || additionalRoutes.length > 0) &&
    additionalRoutes.every(
      (route) =>
        routingChannels.some((c) => c.id === route.channel_id) &&
        route.upstream_group &&
        (ratesFor(
          routingChannels.find((c) => c.id === route.channel_id)?.site_id
        ).some((r) => r.group === route.upstream_group) ||
          route.rate_id)
    ) &&
    (margin === '' || (Number(margin) >= 0 && Number(margin) <= 95))
  )
  /** 只在服务器事务成功后关闭表单；部分缺价时仍告诉操作者具体跳过的模型。 */
  function published(groupName: string, rows: RetailLaunchReview['rows']) {
    for (const prefix of [
      'retail-catalog',
      'retail-pricing',
      'retail-group-bindings',
      'retail-studio',
    ]) {
      void client.invalidateQueries({ queryKey: [prefix] })
    }
    toast.success(
      `${t(editing ? 'Saved' : 'Call group published')} · ${groupName}`
    )
    const skipped = rows.filter((r) => r.reason).map((r) => r.model)
    if (skipped.length) {
      toast.message(`${t('Skipped models')} · ${skipped.join(', ')}`)
    }
    onPublished()
  }
  /** 读取新 Key 时只使用选中站点的协议和地址。后台验证并计算可售范围，浏览器不推算价格。 */
  async function submit(apply = false) {
    if (!selected || busy) return
    setBusy(true)
    setFailure('')
    try {
      if (preview) {
        await launchRetailGroup(preview.input, preview.result.review)
        published(preview.input.local_group, preview.result.rows)
        return
      }
      if (!canPreview || channel === null) return
      // 预览始终使用新价表。仅其他分组或价格发生变化时可沿用草稿；同组变化需管理员重新核对。
      let version: number
      if (editSnapshot) {
        const latest = await pricing.refetch({ throwOnError: true })
        const saved = latest.data
        const product = saved?.book.products.find(
          (p) => p.id === editSnapshot.product.id
        )
        if (!saved || !product) {
          throw new Error('Retail pricing changed. Reload before saving.')
        }
        const current = groupEditSnapshot(product, saved.book, saved.version)
        if (
          current.version !== editSnapshot.version &&
          (JSON.stringify(current.product) !==
            JSON.stringify(editSnapshot.product) ||
            JSON.stringify(current.bindings) !==
              JSON.stringify(editSnapshot.bindings))
        ) {
          setConflict(current)
          throw new Error(
            'Group routing changed. Reload the saved group to continue.'
          )
        }
        version = saved.version
      } else {
        version = (await getRetailGroupBindings()).version
      }
      const input: RetailLaunchInput = {
        expected_version: version,
        local_group: group.trim(),
        site_id: selected.id,
        channel_id: channel,
        upstream_group: upstream,
        product_id: editing?.id,
        additional_routes: additionalRoutes,
        pricing_only: !useSourceRoute,
      }
      if (margin !== '') input.margin_bps = Math.round(Number(margin) * 100)
      if (!channel) {
        const response = await fetchModels({
          type: selected.type,
          base_url: selected.base_url,
          key: key.trim(),
        })
        if (!response.success) throw new Error(getServerErrorMessage(response))
        if (!response.data?.length) throw new Error(t('No models found'))
        input.key = key.trim()
        input.models = [...new Set(response.data)].sort()
      }
      const result = await launchRetailGroup(
        apply ? { ...input, apply: true } : input
      )
      if (apply && result.applied) {
        published(input.local_group, result.rows)
        return
      }
      setPreview({
        input,
        result,
        siteName: selected.name,
        credentialName: credential
          ? `${credential.name} · #${credential.id}`
          : t('New upstream Key'),
      })
    } catch (error) {
      setPreview(null)
      setFailure(t(getServerErrorMessage(error)))
    } finally {
      setBusy(false)
    }
  }
  const reasons: Record<string, string> = {
    official_price_missing_or_expired:
      'Official price is unverified or expired',
    model_mapping_conflict:
      'Channel model mapping differs from the price route',
  }
  const actionLabel = editing ? 'Save' : 'Save and publish group'
  if (data?.inventory_keys) {
    return (
      <InventoryLaunchForm
        identity={identity}
        state={data}
        editing={editSnapshot}
        onReload={onReload}
        onPublished={onPublished}
      />
    )
  }
  return (
    <div className='min-h-0 flex-1 space-y-4 overflow-auto p-6'>
      {state.isError && (
        <Button onClick={() => void state.refetch()}>{t('Retry')}</Button>
      )}
      {!data ? (
        <p>{t('Loading group bindings...')}</p>
      ) : (
        <>
          {!preview && (
            <fieldset
              disabled={busy}
              className='grid gap-4 sm:grid-cols-2'
              onChange={() => {
                setPreview(null)
                setFailure('')
              }}
            >
              <label>
                {t('Supply site')}
                <NativeSelect
                  value={selected?.id ?? 0}
                  onChange={(e) => {
                    setSite(Number(e.target.value))
                    setCredentialChoice(null)
                    setUpstream('')
                    setKey('')
                  }}
                >
                  {sites.map((s) => (
                    <NativeSelectOption key={s.id} value={s.id}>
                      {s.name} · #{s.id}
                    </NativeSelectOption>
                  ))}
                </NativeSelect>
              </label>
              {selected && (
                <SiteFeedEditor
                  key={`${selected.id}/${data.site_feeds?.[selected.id]?.url ?? ''}`}
                  site={selected.id}
                  baseUrl={selected.base_url}
                  feed={data.site_feeds?.[selected.id]}
                  initialOpen={!rates.length}
                  version={data.version}
                  legacyFollow={
                    legacySites.has(selected.id) && data.sync.enabled
                  }
                  onSaved={async () => {
                    await Promise.all([
                      state.refetch({ throwOnError: true }),
                      pricing.refetch({ throwOnError: true }),
                    ])
                  }}
                />
              )}
              <label>
                {t('Selling multiplier source')}
                <NativeSelect
                  value={upstream}
                  onChange={(e) => {
                    setSite(selected?.id ?? 0)
                    setUpstream(e.target.value)
                    setCredentialChoice(null)
                    setKey('')
                  }}
                >
                  <NativeSelectOption value=''>
                    {t('Select group')}
                  </NativeSelectOption>
                  {rates.map((r) => (
                    <NativeSelectOption key={r.group} value={r.group}>
                      {r.name || r.group} · {r.multiplier}×
                    </NativeSelectOption>
                  ))}
                </NativeSelect>
              </label>
              {upstream && (
                <label>
                  {t('Supply credential')}
                  <NativeSelect
                    value={channel ?? ''}
                    onChange={(e) => {
                      setCredentialChoice(Number(e.target.value))
                      setKey('')
                    }}
                  >
                    {channel === null && (
                      <NativeSelectOption value='' disabled>
                        {t('Select a Key')}
                      </NativeSelectOption>
                    )}
                    {credentials.map((c) => (
                      <NativeSelectOption key={c.id} value={c.id}>
                        {c.name} · #{c.id} ·{' '}
                        {data.credential_groups?.[c.id] || t('Unbound Key')}
                      </NativeSelectOption>
                    ))}
                    {selected?.allow_new_credential !== false && (
                      <NativeSelectOption value={0}>
                        {t('New upstream Key')}
                      </NativeSelectOption>
                    )}
                  </NativeSelect>
                </label>
              )}
              {upstream && channel === 0 && (
                <label>
                  {t('Upstream API Key')}
                  <Input
                    type='password'
                    autoComplete='new-password'
                    value={key}
                    onChange={(e) => setKey(e.target.value)}
                  />
                </label>
              )}
              <label>
                {t('Call group name')}
                <Input
                  readOnly={!!editing}
                  list='launch-local-groups'
                  value={group}
                  onChange={(e) => setGroup(e.target.value)}
                />
                <datalist id='launch-local-groups'>
                  {data.local_groups
                    .filter(
                      (g) =>
                        !book?.products?.some(
                          (p) => p.name === g || p.binding_group === g
                        )
                    )
                    .map((g) => (
                      <option key={g} value={g} />
                    ))}
                </datalist>
              </label>
              <label>
                {t('Target margin (%)')}
                <Input
                  type='number'
                  min={0}
                  max={95}
                  step={0.01}
                  value={margin}
                  placeholder={t('Site default ({{value}}%)', {
                    value: data.sale_policy.margin_bps / 100,
                  })}
                  onChange={(e) => setMargin(e.target.value)}
                />
              </label>
              <label className='flex items-center gap-2 sm:col-span-2'>
                <Checkbox
                  checked={useSourceRoute}
                  onCheckedChange={(value) => setUseSourceRoute(value === true)}
                />
                {t('Route requests to the selling multiplier source')}
              </label>
            </fieldset>
          )}
          {!preview && (
            <fieldset disabled={busy} className='space-y-3'>
              <legend className='mb-2 font-medium'>
                {t('Additional upstream routes')}
              </legend>
              {additionalRoutes.map((route) => {
                const option = routingChannels.find(
                  (c) => c.id === route.channel_id
                )
                const routeRates = ratesFor(option?.site_id)
                const usesPublicRate = !!routeRates.length
                const pinned = data.credential_groups?.[route.channel_id]
                return (
                  <div
                    key={route.channel_id}
                    className='grid gap-2 rounded-md border p-3 sm:grid-cols-2'
                  >
                    <div className='flex items-center justify-between gap-2 sm:col-span-2'>
                      <span>
                        {option?.name ?? t('Unavailable or expired')} · #
                        {route.channel_id}
                      </span>
                      <Button
                        variant='ghost'
                        size='sm'
                        onClick={() =>
                          setRouteDrafts((drafts) =>
                            drafts.filter(
                              (r) => r.channel_id !== route.channel_id
                            )
                          )
                        }
                      >
                        {t('Remove')}
                      </Button>
                    </div>
                    <label>
                      {t('Upstream group')}
                      {usesPublicRate ? (
                        <NativeSelect
                          value={route.upstream_group}
                          disabled={!!pinned}
                          onChange={(e) =>
                            setRouteDrafts((drafts) =>
                              drafts.map((r) =>
                                r.channel_id === route.channel_id
                                  ? {
                                      ...r,
                                      upstream_group: e.target.value,
                                      rate_id: '',
                                    }
                                  : r
                              )
                            )
                          }
                        >
                          <NativeSelectOption value=''>
                            {t('Select group')}
                          </NativeSelectOption>
                          {pinned &&
                            !routeRates.some((r) => r.group === pinned) && (
                              <NativeSelectOption value={pinned}>
                                {pinned} · {t('Unavailable')}
                              </NativeSelectOption>
                            )}
                          {routeRates.map((r) => (
                            <NativeSelectOption key={r.group} value={r.group}>
                              {r.name || r.group} · {r.multiplier}×
                            </NativeSelectOption>
                          ))}
                        </NativeSelect>
                      ) : (
                        <Input
                          value={route.upstream_group}
                          readOnly={!!pinned}
                          onChange={(e) =>
                            setRouteDrafts((drafts) =>
                              drafts.map((r) =>
                                r.channel_id === route.channel_id
                                  ? { ...r, upstream_group: e.target.value }
                                  : r
                              )
                            )
                          }
                        />
                      )}
                    </label>
                    {!usesPublicRate && (
                      <label>
                        {t('Procurement rate')}
                        <NativeSelect
                          value={route.rate_id}
                          disabled={
                            !!(
                              option &&
                              book?.site_group_rates?.[option.site_id]?.[
                                route.upstream_group
                              ]
                            )
                          }
                          onChange={(e) =>
                            setRouteDrafts((drafts) =>
                              drafts.map((r) =>
                                r.channel_id === route.channel_id
                                  ? { ...r, rate_id: e.target.value }
                                  : r
                              )
                            )
                          }
                        >
                          <NativeSelectOption value=''>
                            {t('Select rate')}
                          </NativeSelectOption>
                          {book?.rates
                            .filter(
                              (rate) => rate.valid_until > data.server_time
                            )
                            .map((rate) => (
                              <NativeSelectOption key={rate.id} value={rate.id}>
                                {rate.id} · {rate.multiplier}×
                              </NativeSelectOption>
                            ))}
                        </NativeSelect>
                      </label>
                    )}
                  </div>
                )
              })}
              <NativeSelect
                aria-label={t('Add upstream route')}
                value=''
                onChange={(e) => {
                  const id = Number(e.target.value)
                  if (id) {
                    setRouteDrafts((drafts) => [
                      ...drafts,
                      { channel_id: id, upstream_group: '', rate_id: '' },
                    ])
                  }
                }}
              >
                <NativeSelectOption value=''>
                  {t('Add upstream route')}
                </NativeSelectOption>
                {routingChannels
                  .filter(
                    (c) =>
                      c.id !== channel &&
                      !routeDrafts.some((r) => r.channel_id === c.id)
                  )
                  .map((c) => (
                    <NativeSelectOption key={c.id} value={c.id}>
                      {c.name} · #{c.id}
                    </NativeSelectOption>
                  ))}
              </NativeSelect>
            </fieldset>
          )}
          {duplicate && book && !preview && (
            <p role='alert'>
              {group.trim()} · {t('This call group is already published')} ·{' '}
              {productRouteLabel(book, duplicate)}
            </p>
          )}
          {failure && <p role='alert'>{failure}</p>}
          {conflict && (
            <Button disabled={busy} onClick={() => onReload(conflict)}>
              {t('Reload saved bindings')}
            </Button>
          )}
          {(!data.pricing_enabled || !sourceReady) && (
            <p>
              {t(
                'Enable retail sales and load the upstream rates before publishing'
              )}
            </p>
          )}
          {preview && (
            <div aria-live='polite' className='space-y-3'>
              <h3 className='font-semibold'>{preview.input.local_group}</h3>
              <dl className='grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-sm'>
                <dt>{t('Supply site')}</dt>
                <dd>{preview.siteName}</dd>
                <dt>{t('Upstream group')}</dt>
                <dd>{preview.input.upstream_group}</dd>
                <dt>{t('Supply credential')}</dt>
                <dd>{preview.credentialName}</dd>
                {preview.result.sale_multiplier && (
                  <>
                    <dt>{t('Selling multiplier')}</dt>
                    <dd>{preview.result.sale_multiplier}×</dd>
                  </>
                )}
                {!!preview.input.additional_routes?.length && (
                  <>
                    <dt>{t('Additional upstream routes')}</dt>
                    <dd>
                      {preview.input.additional_routes
                        .map(
                          (route) =>
                            `${routingChannels.find((c) => c.id === route.channel_id)?.name ?? ''} · #${route.channel_id}`
                        )
                        .join(' / ')}
                    </dd>
                  </>
                )}
                <dt>{t('Models to publish')}</dt>
                <dd>
                  {formatNumber(
                    preview.result.rows.filter(
                      (r) => r.tiers?.length && !r.reason
                    ).length
                  )}
                </dd>
              </dl>
              {preview.result.unavailable_channels?.map((id) => (
                <p key={id} role='alert'>
                  {id === 0 ? t('New upstream Key') : `#${id}`} ·{' '}
                  {t(
                    'No eligible models. Remove this route or update its supply and pricing.'
                  )}
                </p>
              ))}
              {preview.result.rows
                .filter((r) => r.reason)
                .map((r) => (
                  <p key={r.model}>
                    {r.model} ·{' '}
                    {t(reasons[r.reason ?? ''] ?? 'Unavailable or expired')}
                  </p>
                ))}
              {preview.result.rows.some((row) => row.channels?.length) && (
                <details className='space-y-2 rounded-md border p-3 text-sm'>
                  <summary>{t('Eligible routes by model')}</summary>
                  <dl className='space-y-1'>
                    {preview.result.rows
                      .filter((row) => row.channels?.length)
                      .map((row) => (
                        <div
                          key={row.model}
                          className='flex flex-wrap justify-between gap-x-2'
                        >
                          <dt>{row.model}</dt>
                          <dd>
                            {row.channels
                              ?.map((id) =>
                                id === 0 ? t('New upstream Key') : `#${id}`
                              )
                              .join(' / ')}
                          </dd>
                        </div>
                      ))}
                  </dl>
                </details>
              )}
              {preview.result.review && (
                <SellingPricePreview
                  rows={preview.result.rows
                    .filter((r) => r.tiers)
                    .map((r) => ({ ...r, product: group, available: true }))}
                />
              )}
            </div>
          )}
          <div className='bg-background sticky bottom-0 flex flex-wrap gap-2 border-t py-3'>
            {!preview && (
              <Button
                variant='outline'
                disabled={
                  busy || !canPreview || !data.pricing_enabled || !sourceReady
                }
                onClick={() => void submit()}
              >
                {t('Preview CNY selling prices')}
              </Button>
            )}
            {preview && (
              <Button
                variant='outline'
                disabled={busy}
                onClick={() => setPreview(null)}
              >
                {t('Back')}
              </Button>
            )}
            <Button
              disabled={
                busy ||
                (!preview && !canPreview) ||
                !data.pricing_enabled ||
                !sourceReady ||
                (!!preview && !preview.result.review)
              }
              onClick={() => void submit(true)}
            >
              {t(busy ? 'Loading...' : actionLabel)}
            </Button>
          </div>
        </>
      )}
    </div>
  )
}

/** 本站组只持有库存选择。站点、采购分组、模型清单均由服务端按 Key 解析。 */
function InventoryLaunchForm({
  identity,
  state,
  editing,
  onReload,
  onPublished,
}: {
  identity: string
  state: RetailBindingState
  editing?: PublishedGroupEdit
  onReload: (latest: PublishedGroupEdit) => void
  onPublished: () => void
}) {
  const { t } = useTranslation()
  const client = useQueryClient()
  const product = editing?.product
  const channels = state.rate_channels ?? []
  const keys = (state.inventory_keys ?? []).filter(
    (key) =>
      key.upstream_group &&
      channels.some((channel) => channel.id === key.owner_site_id)
  )
  const anchor =
    product?.rate_source_channel_id || product?.routes[0]?.channel_id
  const keyFor = (channelID?: number) =>
    channels.find((channel) => channel.id === channelID)?.library_id || 0
  const [primary, setPrimary] = useState(
    () => product?.rate_source_key_id || keyFor(anchor)
  )
  const [extras, setExtras] = useState<number[]>(
    () =>
      product?.routes
        .filter((route) => route.channel_id !== anchor)
        .map((route) => route.library_id || keyFor(route.channel_id)) ?? []
  )
  const [usePrimary, setUsePrimary] = useState(
    !product || product.routes.some((route) => route.channel_id === anchor)
  )
  const [name, setName] = useState(product?.binding_group ?? '')
  const [margin, setMargin] = useState(
    product?.margin_bps === undefined ? '' : String(product.margin_bps / 100)
  )
  const [busy, setBusy] = useState(false)
  const [failure, setFailure] = useState('')
  const [review, setReview] = useState<{
    input: RetailLaunchInput
    result: RetailLaunchReview
  }>()
  const selected = keys.find((key) => key.id === primary)
  const site = channels.find(
    (channel) => channel.id === selected?.owner_site_id
  )
  const feed = site && state.site_feeds?.[site.id]
  const rate = (
    feed?.snapshot ??
    (site && state.channels.some((channel) => channel.site_id === site.id)
      ? state.sync.snapshot
      : undefined)
  )?.groups.find((rate) => rate.group === selected?.upstream_group)
  const valid =
    !!name.trim() &&
    !!selected &&
    !!rate &&
    (usePrimary || extras.length > 0) &&
    extras.every(
      (id) => id !== 0 && id !== primary && keys.some((key) => key.id === id)
    ) &&
    new Set(extras).size === extras.length &&
    (margin === '' ||
      (Number.isFinite(Number(margin)) &&
        Number(margin) >= 0 &&
        Number(margin) <= 95))
  /** 每次提交都复核草稿版本。其他组变更可以重新报价，同组被修改则先回读再编辑。 */
  async function submit(publish: boolean) {
    if (busy || !valid) return
    setBusy(true)
    setFailure('')
    try {
      let input: RetailLaunchInput
      if (review) input = review.input
      else {
        const latest = await getRetailPricing(new AbortController().signal)
        if (editing && latest.version !== editing.version) {
          const current = latest.book.products.find(
            (p) => p.id === editing.product.id
          )
          if (!current) {
            throw new Error('Retail pricing changed. Reload before saving.')
          }
          const fresh = groupEditSnapshot(current, latest.book, latest.version)
          if (
            JSON.stringify(fresh.product) !== JSON.stringify(editing.product) ||
            JSON.stringify(fresh.bindings) !== JSON.stringify(editing.bindings)
          ) {
            onReload(fresh)
            toast.error(
              t('Group routing changed. Reload the saved group to continue.')
            )
            return
          }
        }
        input = {
          expected_version: latest.version,
          library_id: primary,
          additional_key_ids: extras,
          local_group: name.trim(),
          pricing_only: !usePrimary,
          product_id: product?.id,
          ...(margin !== '' && {
            margin_bps: Math.round(Number(margin) * 100),
          }),
        }
      }
      const result = await launchRetailGroup(
        publish && !review ? { ...input, apply: true } : input,
        review?.result.review || ''
      )
      if (!result.applied) {
        setReview({ input, result })
        return
      }
      await Promise.all(
        [
          'retail-group-bindings',
          'retail-key-library',
          'retail-pricing',
          'upstream-keys',
          'retail-catalog',
          'retail-studio',
        ].map((key) => client.invalidateQueries({ queryKey: [key] }))
      )
      toast.success(t('Saved'))
      onPublished()
    } catch (error) {
      setReview(undefined)
      setFailure(t(getServerErrorMessage(error)))
    } finally {
      setBusy(false)
    }
  }
  return (
    <div className='min-h-0 flex-1 space-y-5 overflow-auto p-6'>
      <fieldset className='grid gap-4' disabled={busy || !!review}>
        <label>
          {t('Call group name')}
          <Input
            value={name}
            readOnly={!!product}
            onChange={(e) => setName(e.target.value)}
          />
        </label>
        <label>
          {t('Upstream API Key')}
          <NativeSelect
            value={primary}
            onChange={(e) => setPrimary(Number(e.target.value))}
          >
            <NativeSelectOption value={0}>
              {t('Select a Key')}
            </NativeSelectOption>
            {keys.map((key) => (
              <NativeSelectOption key={key.id} value={key.id}>
                {key.name} · #{key.id} · {key.upstream_group}
              </NativeSelectOption>
            ))}
          </NativeSelect>
        </label>
        {selected && (
          <dl className='grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-sm'>
            <dt>{t('Supply site')}</dt>
            <dd>{site?.name}</dd>
            <dt>{t('Upstream group')}</dt>
            <dd>{selected.upstream_group}</dd>
            <dt>{t('Procurement rate')}</dt>
            <dd>{rate ? `${rate.multiplier}×` : t('Unavailable')}</dd>
          </dl>
        )}
        <a className='text-sm underline' href='/upstream-keys'>
          {t('Upstream Keys')}
        </a>
        <label>
          {t('Target margin (%)')}
          <Input
            type='number'
            min={0}
            max={95}
            value={margin}
            placeholder={String(state.sale_policy.margin_bps / 100)}
            onChange={(e) => setMargin(e.target.value)}
          />
        </label>
        <details open={extras.length > 0 || !usePrimary || undefined}>
          <summary>{t('Additional upstream routes')}</summary>
          <label className='flex items-center gap-2 py-2'>
            <Checkbox
              checked={usePrimary}
              onCheckedChange={(v) => setUsePrimary(v === true)}
            />
            {t('Route requests to the selling multiplier source')}
          </label>
          {extras.map((id) => (
            <div key={id} className='flex items-center justify-between'>
              <span>
                {keys.find((key) => key.id === id)?.name || t('Unavailable')}
              </span>
              <Button
                variant='ghost'
                onClick={() => setExtras(extras.filter((item) => item !== id))}
              >
                {t('Remove')}
              </Button>
            </div>
          ))}
          <NativeSelect
            aria-label={t('Add upstream route')}
            value=''
            onChange={(e) => setExtras([...extras, Number(e.target.value)])}
          >
            <NativeSelectOption value=''>
              {t('Select a Key')}
            </NativeSelectOption>
            {keys
              .filter((key) => key.id !== primary && !extras.includes(key.id))
              .map((key) => (
                <NativeSelectOption key={key.id} value={key.id}>
                  {key.name} · {key.upstream_group}
                </NativeSelectOption>
              ))}
          </NativeSelect>
        </details>
      </fieldset>
      {site && !review && (
        <SiteFeedEditor
          key={site.id}
          site={site.id}
          baseUrl={site.base_url}
          feed={feed}
          version={state.version}
          initialOpen={false}
          legacyFollow={state.sync.enabled}
          onSaved={async () => {
            await client.invalidateQueries({
              queryKey: ['retail-group-bindings', identity],
            })
          }}
        />
      )}
      {failure && <p role='alert'>{failure}</p>}
      {review && (
        <div aria-live='polite' className='space-y-2'>
          <p>
            {t('Selling multiplier')} · {review.result.sale_multiplier}×
          </p>
          <SellingPricePreview
            rows={review.result.rows
              .filter((row) => row.tiers)
              .map((row) => ({ ...row, product: name, available: true }))}
          />
          {review.result.rows
            .filter((row) => row.reason)
            .map((row) => (
              <p key={row.model} className='text-muted-foreground text-sm'>
                {row.model} · {t('Unavailable')}
              </p>
            ))}
        </div>
      )}
      <div className='flex flex-wrap gap-2'>
        <Button
          variant='outline'
          disabled={busy || (!review && !valid)}
          onClick={() => (review ? setReview(undefined) : void submit(false))}
        >
          {t(review ? 'Back' : 'Preview CNY selling prices')}
        </Button>
        <Button
          disabled={
            busy ||
            !valid ||
            !state.pricing_enabled ||
            (!!review && !review.result.review)
          }
          onClick={() => void submit(true)}
        >
          {busy
            ? t('Loading...')
            : t(product ? 'Save' : 'Save and publish group')}
        </Button>
      </div>
    </div>
  )
}

/** 跟随与读取分开提交。暂停保留最近一次已保存的倍率，网络故障时也能停止轮询。 */
function SiteFeedEditor({
  site,
  baseUrl,
  feed,
  version,
  legacyFollow,
  initialOpen,
  onSaved,
}: {
  site: number
  baseUrl: string
  feed?: RetailSiteFeed
  version: number
  initialOpen: boolean
  legacyFollow: boolean
  onSaved: () => Promise<void>
}) {
  const { t } = useTranslation()
  const [url, setUrl] = useState(() => {
    if (feed) return feed.url
    try {
      return new URL('/api/public/rates', baseUrl).href
    } catch {
      return ''
    }
  })
  const follow = feed?.follow ?? legacyFollow
  const [pending, setPending] = useState(false)
  const [error, setError] = useState('')
  /** 使用展示时的版本号。冲突时重新取得最新版本，由管理员决定是否重新提交。 */
  async function save(nextFollow?: boolean) {
    if (pending) return
    setPending(true)
    setError('')
    try {
      await saveRetailSiteFeed(
        site,
        version,
        nextFollow === false && feed ? feed.url : url.trim(),
        nextFollow
      )
      await onSaved()
    } catch (e) {
      setError(t(getServerErrorMessage(e)))
      await onSaved().catch(() => {})
    } finally {
      setPending(false)
    }
  }
  const [expanded, setExpanded] = useState(initialOpen)
  return (
    <details
      open={expanded}
      onToggle={(e) => setExpanded(e.currentTarget.open)}
      className='sm:col-span-2'
    >
      <summary>{t('Upstream rate feed')}</summary>
      <div className='mt-2 grid gap-2'>
        <label>
          {t('Upstream rate feed URL')}
          <Input
            type='url'
            value={url}
            disabled={pending}
            onChange={(e) => setUrl(e.target.value)}
          />
        </label>
        <div className='flex flex-wrap items-center gap-3'>
          <Button
            type='button'
            variant='outline'
            size='sm'
            disabled={pending || !url.trim()}
            onClick={() => void save()}
          >
            {t('Load upstream groups')}
          </Button>
          <label className='flex items-center gap-2'>
            <Checkbox
              checked={follow}
              disabled={pending || !url.trim()}
              onCheckedChange={(checked) => void save(checked === true)}
            />
            {t('Automatically follow upstream multipliers')}
          </label>
        </div>
        {(error || feed?.last_error) && (
          <p role='alert'>{error || t(feed?.last_error ?? '')}</p>
        )}
      </div>
    </details>
  )
}
