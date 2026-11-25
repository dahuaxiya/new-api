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
import { GitFork, KeyRound, Pencil, Search, X } from 'lucide-react'
import { useMemo, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import { StaticDataTable } from '@/components/data-table'
import { EmptyState } from '@/components/empty-state'
import { ErrorState } from '@/components/error-state'
import { StatusBadge } from '@/components/status-badge'
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
import {
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import {
  CHANNEL_STATUS,
  CHANNEL_STATUS_LABELS,
} from '@/features/channels/constants'
import type {
  RetailBindingState,
  RetailPriceBook,
} from '@/features/channels/retail-pricing-api'
import { EditLibraryKeyButton } from '@/features/upstream-keys/library'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber } from '@/lib/format'

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

/** 从价表取实际请求线路，从渠道取配置开关。定价来源可以不接请求。
 * @param book 正式价表，旧商品可能没有库存编号或绑定记录。
 * @param supply 本次读取的供货配置。未返回的渠道保持未知。
 * @return 按原有商品及线路顺序排列的显示资料。
 */
function directoryGroups(book?: RetailPriceBook, supply?: RetailBindingState) {
  // 报价渠道包括已停用线路。旧接口只返回 channels 或 routing_channels。
  const channels = new Map(
    [
      ...(supply?.channels ?? []),
      ...(supply?.routing_channels ?? []),
      ...(supply?.rate_channels ?? []),
    ].map((channel) => [channel.id, channel])
  )
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

/** 状态只描述该线路的后台开关，包括自动停用和未知值。
 * @param props 后端返回的渠道状态码。
 * @return 带文本的线路状态标识。
 */
function RouteStatus(props: { status: number }) {
  const { t } = useTranslation()
  const label =
    CHANNEL_STATUS_LABELS[props.status as keyof typeof CHANNEL_STATUS_LABELS] ??
    'Unknown'
  let tone: 'neutral' | 'success' | 'warning' = 'neutral'
  if (props.status === CHANNEL_STATUS.ENABLED) tone = 'success'
  if (
    props.status === CHANNEL_STATUS.AUTO_DISABLED ||
    props.status === CHANNEL_STATUS.MANUAL_DISABLED
  ) {
    tone = 'warning'
  }
  return (
    <StatusBadge
      className='gm-status'
      data-tone={tone}
      label={t(label)}
      type='text'
      showDot
      copyable={false}
      variant={tone}
    />
  )
}

/** 同一组的多条线路共享组名和编辑按钮，手机上每条线路的状态仍贴着自己的 Key。
 * @param props 商品、对应线路及操作入口。非公开或没有绑定组的商品仅展示。
 * @return 该商品的表格行组，空线路也保留商品。
 */
function ProductRows(props: {
  group: GroupRow
  identity: string
  factor?: string
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
            {route ? <RouteStatus status={route.status} /> : '—'}
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
