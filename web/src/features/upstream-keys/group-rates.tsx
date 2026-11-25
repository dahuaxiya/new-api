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
import { useId, useState } from 'react'
import { useTranslation } from 'react-i18next'

import {
  Combobox,
  ComboboxInput,
  ComboboxContent,
  ComboboxEmpty,
  ComboboxItem,
  ComboboxList,
  useComboboxAnchor,
} from '@/components/ui/combobox'

import type { indexConsumerPrices } from './coverage'

type GroupOption = { group: string; multiplier: string }

/** 以明示的采购或售价口径展示倍率。保留服务端的小数精度，同时容纳旧商品的文字报价。
 * @param props 报价口径、未经取整的十进制字符串以及缺少固定报价时的可选说明。
 * @return 可见标签和数值，不将缺价显示为零或一倍。
 */
export function RateValue(props: {
  kind: 'purchase' | 'sale'
  value?: string
  fallback?: string
  id?: string
}) {
  const { t } = useTranslation()
  return (
    <span id={props.id} className='gm-rate-value' data-kind={props.kind}>
      <span>
        {t(
          props.kind === 'purchase'
            ? 'Purchase multiplier'
            : 'Selling multiplier'
        )}
      </span>{' '}
      <b>
        {typeof props.value === 'string' && props.value
          ? `${props.value}×`
          : props.fallback || t('Rate unavailable')}
      </b>
    </span>
  )
}

/** 给每个被该库存引用的本站分组附上正式售价，包括仅用作定价来源的 Key。
 * @param props 库存主键、实际线路、消费者名称和本次价表索引；旧商品可能没有库存主键。
 * @return 按消费者排列的报价。绑定尚未上架时仍显示该分组。
 */
export function KeyConsumerRates(props: {
  names: string[]
  keyId: number
  channels: number[]
  prices?: ReturnType<typeof indexConsumerPrices>
  loading: boolean
}) {
  const { t } = useTranslation()
  return (
    <div className='gm-consumer-rates'>
      {props.names.map((name) => {
        // 名字可相同，备用线路也可能引用该 Key。按实际线路和库存主键对照，避免将同名商品错配给这行。
        const matches =
          props.prices
            ?.get(name)
            ?.filter(
              ({ product }) =>
                (!!product.rate_source_channel_id &&
                  props.channels.includes(product.rate_source_channel_id) &&
                  (!product.rate_source_key_id ||
                    product.rate_source_key_id === props.keyId)) ||
                product.routes?.some(
                  (route) =>
                    props.channels.includes(route.channel_id) &&
                    (!route.library_id || route.library_id === props.keyId)
                )
            ) ?? []
        return (
          <div key={name} className='gm-consumer-group'>
            <strong>{name}</strong>{' '}
            {matches.length ? (
              matches.map(({ product, multiplier }) => (
                <span key={product.id}>
                  {matches.length > 1 && (
                    <span>{product.name || product.id} · </span>
                  )}
                  <RateValue
                    kind='sale'
                    value={
                      product.rate_source_channel_id ? multiplier : undefined
                    }
                    fallback={
                      !product.rate_source_channel_id
                        ? t('Route-based rate')
                        : undefined
                    }
                  />
                </span>
              ))
            ) : (
              <RateValue
                kind='sale'
                fallback={props.loading ? t('Loading...') : undefined}
              />
            )}
          </div>
        )
      })}
    </div>
  )
}

/** 管理员可搜索候选或手填未收录的分组。弹层由站内 Combobox 锚定，自由输入只提交组名。
 * @param props 本站点候选、当前编辑值和更新草稿的回调。
 * @return 常显采购倍率和可用键盘操作的上游分组输入框。
 */
export function UpstreamGroupField(props: {
  value: string
  options: GroupOption[]
  onChange: (value: string) => void
}) {
  const { t } = useTranslation()
  const inputId = useId()
  const rateId = useId()
  const anchor = useComboboxAnchor()
  const [term, setTerm] = useState('')
  const [open, setOpen] = useState(false)
  const selected = props.options.find(
    (option) => option.group === props.value.trim()
  )
  return (
    <div className='gm-upstream-field'>
      <div className='gm-upstream-field-head'>
        <label htmlFor={inputId}>{t('Upstream group')}</label>
        <RateValue id={rateId} kind='purchase' value={selected?.multiplier} />
      </div>
      <Combobox<GroupOption>
        items={props.options}
        open={open}
        value={selected ?? null}
        inputValue={props.value}
        itemToStringLabel={(option) => option.group}
        isItemEqualToValue={(a, b) => a.group === b.group}
        filter={(option) =>
          option.group.toLowerCase().includes(term.trim().toLowerCase())
        }
        onOpenChange={(nextOpen) => {
          setOpen(nextOpen)
          if (!nextOpen) setTerm('')
        }}
        onInputValueChange={(value, details) => {
          // 关闭列表或匹配候选时组件也会要求回填显示值。只有实际输入能修改手填草稿。
          if (details.reason === 'input-change') {
            // 没有候选时也打开可退出的空态；Esc 只关闭下拉层，不能丢弃整个编辑表单。
            setOpen(true)
            setTerm(value)
            props.onChange(value)
          }
        }}
        onValueChange={(option) => {
          if (option) {
            props.onChange(option.group)
            setTerm('')
          }
        }}
      >
        <div ref={anchor}>
          <ComboboxInput
            id={inputId}
            aria-describedby={rateId}
            aria-label={t('Upstream group')}
            triggerAriaLabel={t('Select upstream group')}
            spellCheck={false}
            onKeyDown={(event) => {
              // 手填分组时回车只结束这次输入，不能顺带提交整个 Key 表单。
              if (
                open &&
                event.key === 'Enter' &&
                !event.currentTarget.getAttribute('aria-activedescendant') &&
                !event.nativeEvent.isComposing
              ) {
                event.preventDefault()
                setOpen(false)
                setTerm('')
              }
            }}
            placeholder={t('Select or type...')}
            className='w-full'
          />
        </div>
        <ComboboxContent anchor={anchor} className='gm-group-options'>
          {/* 空态也属于候选层。按 Esc 应只关闭这层，保留弹窗内的手填值。 */}
          <ComboboxEmpty>
            {t(
              'No published rate for this group. You can still enter its name.'
            )}
          </ComboboxEmpty>
          <ComboboxList className='empty:hidden'>
            {(option: GroupOption) => (
              <ComboboxItem key={option.group} value={option}>
                <span className='min-w-0 flex-1 wrap-anywhere'>
                  {option.group}
                </span>
                <span className='shrink-0 font-semibold tabular-nums'>
                  <span className='sr-only'>{t('Purchase multiplier')} </span>
                  {option.multiplier}×
                </span>
              </ComboboxItem>
            )}
          </ComboboxList>
        </ComboboxContent>
      </Combobox>
    </div>
  )
}
