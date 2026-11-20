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
  getRetailPricing,
  launchRetailGroup,
  type RetailLaunchInput,
  type RetailLaunchReview,
} from '@/features/channels/retail-pricing-api'
import { useStatus } from '@/hooks/use-status'
import { ROLE } from '@/lib/roles'
import { getServerErrorMessage } from '@/lib/server-error-message'
import { useAuthStore } from '@/stores/auth-store'

/** 入口只在中转站管理后台显示。会话切换时销毁包含未保存 Key 的表单。 */
export function RetailGroupLaunch() {
  const { t } = useTranslation()
  const { status } = useStatus()
  const { user, session } = useAuthStore((s) => s.auth)
  const [open, setOpen] = useState(false)
  if (status?.site_mode !== 'retail' || user?.role !== ROLE.SUPER_ADMIN) {
    return null
  }
  return (
    <Sheet open={open} onOpenChange={setOpen}>
      <Button className='self-start' onClick={() => setOpen(true)}>
        {t('Publish a call group')}
      </Button>
      <PublishedGroups identity={`${user.id}:${session?.sid}`} />
      <SheetContent className={sideDrawerContentClassName('sm:max-w-2xl')}>
        <SheetHeader className={sideDrawerHeaderClassName()}>
          <SheetTitle>{t('Publish a call group')}</SheetTitle>
        </SheetHeader>
        {open && (
          <LaunchForm
            key={`${user.id}:${session?.sid}`}
            identity={`${user.id}:${session?.sid}`}
            onPublished={() => setOpen(false)}
          />
        )}
      </SheetContent>
    </Sheet>
  )
}

/** 读取正式价表，供管理员核对实际发布的调用分组和线路。账号倍率表可能没有同名项。 */
function PublishedGroups({ identity }: { identity: string }) {
  const { t } = useTranslation()
  // 和价表共用按登录会话隔离的缓存，保存成功后由上架表单触发重新读取。
  const query = useQuery({
    queryKey: ['retail-pricing', identity],
    queryFn: ({ signal }) => getRetailPricing(signal),
    retry: false,
  })
  const book = query.data?.book
  return (
    <section aria-label={t('Published call groups')} className='space-y-2'>
      <h3 className='font-medium'>{t('Published call groups')}</h3>
      {query.isError ? (
        <Button variant='outline' onClick={() => void query.refetch()}>
          {t('Retry')}
        </Button>
      ) : !book ? (
        <p>{t('Loading...')}</p>
      ) : !book.products?.length ? (
        <p>{t('None')}</p>
      ) : (
        <dl className='grid gap-2 text-sm'>
          {book.products.map((product) => (
            <div
              key={product.id}
              className='flex flex-wrap items-baseline gap-x-3'
            >
              <dt className='font-medium'>{product.name}</dt>
              <dd className='text-muted-foreground'>
                {product.routes
                  .map((route) => {
                    // 历史手工方案可能只有线路 ID；只显示这条线路已保存的上游分组。
                    const binding = book.group_bindings?.find(
                      (b) =>
                        b.channel_id === route.channel_id &&
                        b.local_group === product.binding_group
                    )
                    return [`#${route.channel_id}`, binding?.upstream_group]
                      .filter(Boolean)
                      .join(' · ')
                  })
                  .join(' / ')}
              </dd>
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
  onPublished,
}: {
  identity: string
  onPublished: () => void
}) {
  const { t } = useTranslation()
  const client = useQueryClient()
  const state = useQuery({
    queryKey: ['retail-group-bindings', identity],
    queryFn: ({ signal }) => getRetailGroupBindings(signal),
    retry: false,
    refetchOnWindowFocus: false,
  })
  const [site, setSite] = useState(0)
  const [channel, setChannel] = useState(0)
  const [upstream, setUpstream] = useState('')
  const [group, setGroup] = useState('')
  const [key, setKey] = useState('')
  const [margin, setMargin] = useState('')
  const [busy, setBusy] = useState(false)
  const [failure, setFailure] = useState('')
  const [preview, setPreview] = useState<{
    input: RetailLaunchInput
    result: RetailLaunchReview
  } | null>(null)
  const data = state.data
  const sites = data?.channels.filter((c) => c.site_id === c.id) ?? []
  const selected = sites.find((c) => c.id === (site || sites[0]?.id))
  const pinned = data?.credential_groups?.[channel]
  const upstreamGroup = pinned || upstream
  const rates = data?.sync.snapshot?.groups ?? []
  const canPreview = !!(
    selected &&
    group.trim() &&
    upstreamGroup &&
    (channel || key.trim()) &&
    (margin === '' || (Number(margin) >= 0 && Number(margin) <= 95))
  )
  /** 读取新 Key 时只使用选中站点的协议和地址。后台验证并计算可售范围，浏览器不推算价格。 */
  async function submit() {
    if (!selected || busy) return
    setBusy(true)
    setFailure('')
    try {
      if (preview) {
        await launchRetailGroup(preview.input, preview.result.review)
        for (const prefix of [
          'retail-catalog',
          'retail-pricing',
          'retail-group-bindings',
          'retail-studio',
        ]) {
          void client.invalidateQueries({ queryKey: [prefix] })
        }
        toast.success(t('Call group published'))
        onPublished()
        return
      }
      const latest = await getRetailGroupBindings()
      const input: RetailLaunchInput = {
        expected_version: latest.version,
        local_group: group.trim(),
        site_id: selected.id,
        channel_id: channel,
        upstream_group: upstreamGroup,
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
      const result = await launchRetailGroup(input)
      setPreview({ input, result })
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
  const actionLabel = preview
    ? 'Save and publish group'
    : 'Preview CNY selling prices'
  return (
    <div className='min-h-0 flex-1 space-y-4 overflow-auto p-6'>
      {state.isError && (
        <Button onClick={() => void state.refetch()}>{t('Retry')}</Button>
      )}
      {!data ? (
        <p>{t('Loading group bindings...')}</p>
      ) : (
        <>
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
                  setChannel(0)
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
            <label>
              {t('Supply credential')}
              <NativeSelect
                value={channel}
                onChange={(e) => {
                  setChannel(Number(e.target.value))
                  setUpstream('')
                  setKey('')
                }}
              >
                <NativeSelectOption value={0}>
                  {t('New upstream Key')}
                </NativeSelectOption>
                {data.channels
                  .filter(
                    (c) =>
                      c.site_id === selected?.id && c.has_key && c.status === 1
                  )
                  .map((c) => (
                    <NativeSelectOption key={c.id} value={c.id}>
                      {c.name} · #{c.id}
                    </NativeSelectOption>
                  ))}
              </NativeSelect>
            </label>
            {!channel && (
              <label className='sm:col-span-2'>
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
              {t('Upstream group')}
              <NativeSelect
                disabled={!!pinned}
                value={upstreamGroup}
                onChange={(e) => setUpstream(e.target.value)}
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
            <label>
              {t('Call group name')}
              <Input
                list='launch-local-groups'
                value={group}
                onChange={(e) => setGroup(e.target.value)}
              />
              <datalist id='launch-local-groups'>
                {data.local_groups.map((g) => (
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
          </fieldset>
          {failure && <p role='alert'>{failure}</p>}
          {(!data.pricing_enabled || !data.sync.enabled) && (
            <p>
              {t(
                'Enable retail sales and automatic upstream rates before publishing a group'
              )}
            </p>
          )}
          {preview && (
            <div aria-live='polite' className='space-y-3'>
              {preview.result.rows
                .filter((r) => r.reason)
                .map((r) => (
                  <p key={r.model}>
                    {r.model} ·{' '}
                    {t(reasons[r.reason ?? ''] ?? 'Unavailable or expired')}
                  </p>
                ))}
              {preview.result.review && (
                <SellingPricePreview
                  rows={preview.result.rows
                    .filter((r) => r.tiers)
                    .map((r) => ({ ...r, product: group, available: true }))}
                />
              )}
            </div>
          )}
          <Button
            disabled={
              busy ||
              !canPreview ||
              !data.pricing_enabled ||
              !data.sync.enabled ||
              (!!preview && !preview.result.review)
            }
            onClick={() => void submit()}
          >
            {t(busy ? 'Loading...' : actionLabel)}
          </Button>
        </>
      )}
    </div>
  )
}
