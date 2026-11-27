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
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { GitFork, KeyRound, Pencil, Search, X } from 'lucide-react'
import { useMemo, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { StaticDataTable } from '@/components/data-table'
import { EmptyState } from '@/components/empty-state'
import { ErrorState } from '@/components/error-state'
import { Button } from '@/components/ui/button'
import {
  InputGroup,
  InputGroupAddon,
  InputGroupButton,
  InputGroupInput,
} from '@/components/ui/input-group'
import {
  Popover,
  PopoverTrigger,
  PopoverContent,
} from '@/components/ui/popover'
import { Skeleton } from '@/components/ui/skeleton'
import { Switch } from '@/components/ui/switch'
import {
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { updateChannelStatus } from '@/features/channels/api'
import {
  CHANNEL_STATUS,
  CHANNEL_STATUS_LABELS,
} from '@/features/channels/constants'
import { channelsQueryKeys } from '@/features/channels/lib/channel-actions'
import {
  getRetailGroupBindings,
  type RetailBindingState,
  type RetailPriceBook,
} from '@/features/channels/retail-pricing-api'
import { EditLibraryKeyButton } from '@/features/upstream-keys/library'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber } from '@/lib/format'
import { handleServerError } from '@/lib/handle-server-error'
import { requireServerSuccess } from '@/lib/server-error-message'

type Product = RetailPriceBook['products'][number]
type DirectoryProps = {
  identity: string
  multipliers?: Record<string, string> | null
  book?: RetailPriceBook
  supply?: RetailBindingState
  priceError: boolean
  supplyError: boolean
  retryPrices: () => void
  retrySupply: () => void
  publishButton: ReactNode
  onEdit: (product: Product, trigger: HTMLElement) => void
}

/** 按渠道号合并各类供货资料；列表展示与写后核对使用同一份状态来源。
 * @param supply 当前管理员可见的供货资料，加载失败时无法判断状态。
 * @return 包含停用线路的渠道索引。
 */
function routeChannels(supply?: RetailBindingState) {
  return new Map(
    [
      ...(supply?.channels ?? []),
      ...(supply?.routing_channels ?? []),
      ...(supply?.rate_channels ?? []),
    ].map((channel) => [channel.id, channel])
  )
}

/** 从价表取实际请求线路，从渠道取配置开关。定价来源可以不接请求。
 * @param book 正式价表，旧商品可能没有库存编号或绑定记录。
 * @param supply 本次读取的供货配置。未返回的渠道保持未知。
 * @return 按原有商品及线路顺序排列的显示资料。
 */
function directoryGroups(book?: RetailPriceBook, supply?: RetailBindingState) {
  const channels = routeChannels(supply)
  const keys = new Map(supply?.inventory_keys?.map((key) => [key.id, key]))
  const bindings = new Map(
    book?.group_bindings?.map((item) => [
      `${item.local_group}/${item.channel_id}`,
      item.upstream_group,
    ])
  )
  return (book?.products ?? []).map((product) => {
    const routes = (product.routes ?? []).map((route) => {
      const channel = channels.get(route.channel_id)
      const keyId = route.library_id || channel?.library_id
      const key = keyId ? keys.get(keyId) : undefined
      const upstream =
        key?.upstream_group ||
        bindings.get(`${product.binding_group}/${route.channel_id}`)
      // 旧记录的渠道号和库存 Key 编号来自两张表，不能用渠道号冒充 Key。
      return {
        channelId: route.channel_id,
        keyId,
        keyName: key?.name,
        upstream,
        status: channel?.status ?? CHANNEL_STATUS.UNKNOWN,
      }
    })
    const anchor = channels.get(product.rate_source_channel_id ?? 0)
    const sourceID = product.rate_source_key_id || anchor?.library_id
    const sourceKey = sourceID ? keys.get(sourceID) : undefined
    return { product, routes, sourceID, sourceKey }
  })
}

type GroupRow = ReturnType<typeof directoryGroups>[number]

/** 只允许更改服务端已识别的开关状态，包括重新启用自动停用的线路。
 * @param status 服务端状态，未知或缺失时禁用操作。
 * @return 管理员是否可以设置该线路的启停状态。
 */
function canChangeStatus(status?: number): boolean {
  return (
    status === CHANNEL_STATUS.ENABLED ||
    status === CHANNEL_STATUS.MANUAL_DISABLED ||
    status === CHANNEL_STATUS.AUTO_DISABLED
  )
}

type SwitchTarget = { channelId: number; enabled: boolean }
type SwitchControls = {
  pendingId?: number
  onToggle: (target: SwitchTarget) => void
}

/** 同一组的多条线路共享组名和编辑按钮，手机上每条线路的状态仍贴着自己的 Key。
 * @param props 商品、对应线路及操作入口。非公开或没有绑定组的商品仅展示。
 * @return 该商品的表格行组，空线路也保留商品。
 */
function ProductRows(props: {
  group: GroupRow
  identity: string
  factor?: string
  controls: SwitchControls
  onEdit: DirectoryProps['onEdit']
}) {
  const { t, i18n } = useTranslation()
  const { product, routes, sourceID, sourceKey } = props.group
  const anchor = product.rate_source_channel_id
  const pricingOnly =
    !!anchor && !routes.some((route) => route.channelId === anchor)
  const exact = props.factor
  // 循环小数可有 24 位。列表只缩短显示，精确值原样保留在可点击的详情里。
  const short =
    exact &&
    new Intl.NumberFormat(
      toIntlLocale(i18n.resolvedLanguage || i18n.language),
      { maximumSignificantDigits: 6, useGrouping: false }
    ).format(Number(exact))
  const lines = routes.length ? routes : [undefined]
  return (
    <TableBody className='gm-product'>
      {lines.map((route, index) => (
        <TableRow className='gm-row' key={route?.channelId ?? 'empty'}>
          {index === 0 && (
            <TableHead
              className='gm-group'
              scope='rowgroup'
              rowSpan={lines.length}
            >
              <span className='gm-group-name'>{product.name}</span>
              {product.binding_group &&
                product.binding_group !== product.name && (
                  <small>{product.binding_group}</small>
                )}
            </TableHead>
          )}
          {index === 0 && (
            <TableCell className='gm-rate' rowSpan={lines.length}>
              <span className='gm-rate-label'>{t('Selling multiplier')} </span>
              {anchor ? (
                <Popover>
                  <PopoverTrigger
                    render={
                      <Button
                        variant='ghost'
                        size='sm'
                        className='gm-rate-trigger'
                        aria-label={`${t('Selling multiplier')} · ${product.name} · ${exact ? `${exact}×` : t('Rate unavailable')}`}
                      />
                    }
                  >
                    {exact ? `${short}×` : t('Rate unavailable')}
                  </PopoverTrigger>
                  <PopoverContent className='w-auto max-w-72 space-y-2 text-sm'>
                    <div>
                      <span className='text-muted-foreground'>
                        {t('Selling multiplier')}
                      </span>{' '}
                      <strong className='tabular-nums'>
                        {exact ? `${exact}×` : t('Rate unavailable')}
                      </strong>
                    </div>
                    <div>
                      {t('Selling multiplier source')}:{' '}
                      {sourceID
                        ? `${sourceKey?.name ?? t('Inventory Key')} #${sourceID}`
                        : `${t('Channel')} #${anchor}`}
                    </div>
                    {pricingOnly && (
                      <div className='text-muted-foreground'>
                        {t('Pricing only')}
                      </div>
                    )}
                  </PopoverContent>
                </Popover>
              ) : (
                <span className='gm-variable'>{t('Route-based rate')}</span>
              )}
              {pricingOnly && (
                <small className='gm-rate-origin'>
                  {sourceID ? (
                    <EditLibraryKeyButton
                      identity={props.identity}
                      id={sourceID}
                      label={`${t('Edit Key')} · ${t('Selling multiplier source')} #${sourceID}`}
                    >
                      {t('Pricing only')} · #{sourceID}
                    </EditLibraryKeyButton>
                  ) : (
                    <>
                      {t('Pricing only')} · {t('Channel')} #{anchor}
                    </>
                  )}
                </small>
              )}
            </TableCell>
          )}
          <TableCell className='gm-key'>
            {route ? (
              <div className='gm-binding'>
                {route.keyId ? (
                  <EditLibraryKeyButton
                    id={route.keyId}
                    identity={props.identity}
                    label={`${t('Edit Key')} · ${route.keyName || t('Inventory Key')} #${route.keyId}`}
                  >
                    <span className='gm-key-id'>
                      <KeyRound aria-hidden />#{route.keyId}
                    </span>
                    {route.keyName && <span>{route.keyName}</span>}
                  </EditLibraryKeyButton>
                ) : (
                  <span className='gm-legacy'>
                    {t('Channel')} #{route.channelId}
                  </span>
                )}
              </div>
            ) : (
              <span className='text-muted-foreground'>
                {t('No routes configured')}
              </span>
            )}
          </TableCell>
          <TableCell className='gm-upstream'>
            {route?.upstream || '—'}
          </TableCell>
          <TableCell className='gm-state'>
            {route ? (
              <span className='inline-flex items-center gap-2 whitespace-nowrap'>
                <Switch
                  checked={route.status === CHANNEL_STATUS.ENABLED}
                  disabled={
                    props.controls.pendingId !== undefined ||
                    !canChangeStatus(route.status)
                  }
                  aria-busy={props.controls.pendingId === route.channelId}
                  aria-label={`${t('Configured route status')} · ${product.name} · #${route.channelId}`}
                  onCheckedChange={(enabled) =>
                    props.controls.onToggle({
                      channelId: route.channelId,
                      enabled,
                    })
                  }
                />
                <span className='text-muted-foreground text-xs'>
                  {props.controls.pendingId === route.channelId
                    ? t('Saving...')
                    : t(
                        CHANNEL_STATUS_LABELS[
                          route.status as keyof typeof CHANNEL_STATUS_LABELS
                        ] ?? 'Unknown'
                      )}
                </span>
              </span>
            ) : (
              '—'
            )}
          </TableCell>
          {index === 0 && (
            <TableCell className='gm-action' rowSpan={lines.length}>
              {product.public && product.binding_group && (
                <Button
                  variant='ghost'
                  size='sm'
                  aria-label={`${t('Edit call group routing')} · ${product.name}`}
                  onClick={(event) =>
                    props.onEdit(product, event.currentTarget)
                  }
                >
                  <Pencil aria-hidden />
                  <span>{t('Edit')}</span>
                </Button>
              )}
            </TableCell>
          )}
        </TableRow>
      ))}
    </TableBody>
  )
}

/** 展示已发布的调用分组。搜索命中一条线路时保留整组，方便对照备用线路。
 * @param props 正式价表与库存元数据分别加载，失败时可单独重试。
 * @return 可搜索的路由目录和上架入口。
 */
export function PublishedGroupDirectory(props: DirectoryProps) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const [search, setSearch] = useState('')
  const groups = useMemo(
    () => directoryGroups(props.book, props.supply),
    [props.book, props.supply]
  )
  const client = useQueryClient()
  const [confirm, setConfirm] = useState<SwitchTarget | null>(null)
  const statusKey = ['retail-group-bindings', props.identity]
  const change = useMutation({
    retry: false,
    mutationFn: async (target: SwitchTarget) => {
      const status = target.enabled
        ? CHANNEL_STATUS.ENABLED
        : CHANNEL_STATUS.MANUAL_DISABLED
      requireServerSuccess(await updateChannelStatus(target.channelId, status))
      // 状态接口的 success 不能区分重复设置和未能持久化；必须回读真实线路。
      // 取消点击前发起的读，避免它覆盖这次变更，或被 fetchQuery 复用为验收结果。
      await client.cancelQueries({ queryKey: statusKey, exact: true })
      const saved = await client.fetchQuery({
        queryKey: statusKey,
        queryFn: ({ signal }) => getRetailGroupBindings(signal),
        staleTime: 0,
        retry: false,
      })
      if (routeChannels(saved).get(target.channelId)?.status !== status) {
        throw new Error('Failed to update channel')
      }
    },
    onSuccess: (_, target) =>
      toast.success(
        t(
          target.enabled
            ? 'Channel enabled successfully'
            : 'Channel disabled successfully'
        )
      ),
    onError: (error) => {
      handleServerError(error, t('Failed to update channel'))
      // 网络异常时写入结果不确定；恢复以服务端为准，不自行翻转或重试写请求。
      void client.invalidateQueries({ queryKey: statusKey, exact: true })
    },
    onSettled: () => {
      for (const key of [
        channelsQueryKeys.all,
        ['retail-supplies'],
        ['retail-key-library'],
        ['upstream-keys'],
        ['retail-catalog'],
        ['retail-studio'],
      ]) {
        void client.invalidateQueries({ queryKey: key })
      }
    },
  })
  // 一个渠道可能供多个分组使用。即使列表已搜索过滤，确认框也要列出所有引用它的分组。
  const consumers = new Map<number, Set<string>>()
  for (const group of groups) {
    for (const route of group.routes) {
      const names = consumers.get(route.channelId) ?? new Set<string>()
      names.add(group.product.name)
      consumers.set(route.channelId, names)
    }
  }
  const controls: SwitchControls = {
    pendingId: change.isPending ? change.variables.channelId : undefined,
    onToggle: (target) => {
      if (change.isPending) return
      if (!target.enabled || (consumers.get(target.channelId)?.size ?? 0) > 1) {
        setConfirm(target)
      } else change.mutate(target)
    },
  }
  const query = search.trim().toLowerCase()
  const visible = groups.filter(
    ({ product, routes, sourceID, sourceKey }) =>
      !query ||
      [
        product.name,
        product.binding_group,
        sourceID ? `#${sourceID}` : undefined,
        sourceKey?.name,
        sourceKey?.upstream_group,
        ...routes.flatMap((line) => [
          line.keyName,
          line.upstream,
          line.keyId ? `#${line.keyId}` : `#${line.channelId}`,
        ]),
      ].some((value) => value?.toLowerCase().includes(query))
  )
  return (
    <section className='gm-directory' aria-label={t('Published call groups')}>
      {confirm && (
        <ConfirmDialog
          open
          onOpenChange={(open) => {
            if (!open) setConfirm(null)
          }}
          title={`${t(confirm.enabled ? 'Enable' : 'Disable')} · ${t('Channel')} #${confirm.channelId}`}
          desc={t('This changes the route for these call groups: {{groups}}.', {
            groups: [...(consumers.get(confirm.channelId) ?? [])].join(', '),
          })}
          confirmText={t(confirm.enabled ? 'Enable' : 'Disable')}
          destructive={!confirm.enabled}
          handleConfirm={() => {
            setConfirm(null)
            // 对话框打开期间可能发生后台刷新；消失、未知或已达目标的线路无需再写。
            const latest = routeChannels(props.supply).get(
              confirm.channelId
            )?.status
            if (
              change.isPending ||
              !consumers.has(confirm.channelId) ||
              !canChangeStatus(latest)
            ) {
              return
            }
            if ((latest === CHANNEL_STATUS.ENABLED) !== confirm.enabled) {
              change.mutate(confirm)
            }
          }}
        />
      )}
      <header className='gm-directory-head'>
        <div className='gm-directory-title'>
          <span className='gm-directory-mark' aria-hidden>
            <GitFork />
          </span>
          <div>
            <div className='gm-heading-line'>
              <h3>{t('Published call groups')}</h3>
              {props.book && (
                <span className='gm-count'>
                  {formatNumber(groups.length, locale)}
                </span>
              )}
            </div>
            <p>
              {t(
                'Each call group uses the upstream group assigned to its Key.'
              )}
            </p>
          </div>
        </div>
        {!props.priceError && groups.length > 0 && (
          <div className='gm-tools'>
            <InputGroup className='gm-search'>
              <InputGroupAddon>
                <Search aria-hidden />
              </InputGroupAddon>
              <InputGroupInput
                value={search}
                onChange={(e) => setSearch(e.target.value)}
                aria-label={t('Find a group or Key')}
                placeholder={t('Find a group or Key')}
              />
              {search && (
                <InputGroupAddon align='inline-end'>
                  <InputGroupButton
                    size='icon-xs'
                    aria-label={t('Clear search')}
                    onClick={() => setSearch('')}
                  >
                    <X aria-hidden />
                  </InputGroupButton>
                </InputGroupAddon>
              )}
            </InputGroup>
            {query && (
              <span role='status' className='gm-results'>
                {formatNumber(visible.length, locale)} /{' '}
                {formatNumber(groups.length, locale)}
              </span>
            )}
          </div>
        )}
        <div className='gm-publish'>{props.publishButton}</div>
      </header>
      {props.priceError && (
        <ErrorState
          title={t('Failed to load pricing')}
          onRetry={props.retryPrices}
        />
      )}
      {!props.priceError && !props.book && (
        <div
          role='status'
          aria-label={t('Loading...')}
          className='grid gap-3 p-5'
        >
          {[1, 2, 3].map((row) => (
            <Skeleton key={row} className='h-9 motion-reduce:animate-none' />
          ))}
        </div>
      )}
      {!props.priceError && props.book && (
        <>
          {props.supplyError && (
            <div className='gm-notice' role='alert'>
              {t('Key and route details could not be loaded.')}
              <Button variant='outline' size='sm' onClick={props.retrySupply}>
                {t('Retry')}
              </Button>
            </div>
          )}
          {visible.length ? (
            <StaticDataTable
              className='gm-table-wrap'
              tableClassName='gm-table'
              tableProps={{ 'aria-label': t('Published call groups') }}
            >
              <TableHeader>
                <TableRow>
                  <TableHead>{t('Call group')}</TableHead>
                  <TableHead>{t('Selling multiplier')}</TableHead>
                  <TableHead>{t('Inventory Key')}</TableHead>
                  <TableHead>{t('Upstream group')}</TableHead>
                  <TableHead>{t('Configured route status')}</TableHead>
                  <TableHead>
                    <span className='sr-only'>{t('Actions')}</span>
                  </TableHead>
                </TableRow>
              </TableHeader>
              {visible.map((group) => (
                <ProductRows
                  key={group.product.id}
                  group={group}
                  controls={controls}
                  identity={props.identity}
                  factor={props.multipliers?.[group.product.id]}
                  onEdit={props.onEdit}
                />
              ))}
            </StaticDataTable>
          ) : (
            <EmptyState
              className='min-h-40'
              icon={query ? Search : GitFork}
              title={t(query ? 'No results found' : 'No published call groups')}
              action={
                query ? (
                  <Button
                    variant='outline'
                    size='sm'
                    onClick={() => setSearch('')}
                  >
                    {t('Clear search')}
                  </Button>
                ) : undefined
              }
            />
          )}
        </>
      )}
    </section>
  )
}
