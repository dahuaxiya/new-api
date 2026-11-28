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
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import type { RetailCatalog } from '@/features/channels/retail-pricing-api'
import { toIntlLocale } from '@/i18n/languages'
import { getLobeIcon } from '@/lib/lobe-icon'

import { retailModelBrand } from '../lib/retail-model-brand'

type RetailOffer = RetailCatalog['products'][number]['models'][number]

/** Show base and billed unit prices for the same context tier.
 * @param props Offer from the retail catalog. Legacy responses may have only selling prices.
 * @return A model card with a keyboard-accessible context selector when applicable.
 */
export function RetailModelCard(props: { model: RetailOffer }) {
  const { t, i18n } = useTranslation()
  const [context, setContext] = useState<number>()
  const model = props.model
  const locale = toIntlLocale(i18n.resolvedLanguage)
  const format = new Intl.NumberFormat(locale)
  const image = model.kind === 'image'
  const tier =
    model.tiers.find((entry) => entry.up_to === context) ?? model.tiers[0]
  // Match by the actual context ceiling so a missing tier never shows a price
  // from a different context band, including after a price book update.
  const base = model.base_price?.tiers.find(
    (entry) => entry.up_to === tier?.up_to
  )
  const currency = model.base_price?.currency
  const icon = retailModelBrand(model.official_model)?.[2]
  const fields = [
    ['input', t('Input')],
    ['output', t('Output')],
    ['cache_read', t('Cache read')],
    ['cache_write', t('Cache write')],
    ['cache_write_1h', t('Cache write (1h)')],
  ] as const
  const tiers = model.tiers.map((entry, index) => ({
    value: entry.up_to,
    label: `${index ? `${format.format(model.tiers[index - 1].up_to + 1)} – ` : '≤ '}${format.format(entry.up_to)}`,
  }))

  return (
    <article
      aria-label={model.id}
      className='border-border bg-card text-card-foreground min-w-0 rounded-2xl border p-4 shadow-xs sm:p-5 xl:p-6'
    >
      <div className='flex items-center gap-3'>
        <span className='shrink-0' aria-hidden='true'>
          {getLobeIcon(icon, 34)}
        </span>
        <div className='min-w-0'>
          <h3 className='text-xl font-semibold tracking-tight [overflow-wrap:anywhere]'>
            {model.id}
          </h3>
          <p className='text-muted-foreground text-xs'>
            {model.video ? t('Video generation') : null}
            {!model.video && (image ? t('Image') : t('Text'))}
          </p>
        </div>
      </div>
      {model.video && (
        <dl className='mt-4 grid grid-cols-[1fr_auto] gap-3 tabular-nums'>
          {Object.entries(model.video.prices).map(([resolution, price]) => (
            <div className='contents' key={resolution}>
              <dt>{resolution}</dt>
              <dd>
                ¥{price} / {t('second')}
              </dd>
            </div>
          ))}
          <dt>{t('Duration (seconds)')}</dt>
          <dd>1–{model.video.max_duration}</dd>
        </dl>
      )}
      {!model.video &&
        (tier ? (
          <>
            <div className='my-4 border-y py-2.5' aria-live='polite'>
              <table
                aria-label={t('Per million tokens')}
                className='w-full table-fixed tabular-nums'
              >
                <thead className='text-muted-foreground border-b text-xs'>
                  <tr>
                    <th
                      scope='col'
                      className='w-[31%] py-2 text-start font-medium'
                    >
                      {t('Usage')}
                    </th>
                    {currency && (
                      <th
                        scope='col'
                        className='px-1 py-2 text-end font-medium'
                      >
                        {model.base_price?.official
                          ? t('Official price')
                          : t('Base Price')}{' '}
                        <span className='ms-1 inline-block text-[10px]'>
                          {currency}
                        </span>
                      </th>
                    )}
                    <th scope='col' className='py-2 ps-1 text-end font-medium'>
                      {t('Group price')}{' '}
                      <span className='ms-1 inline-block text-[10px]'>
                        {model.currency}
                      </span>
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {fields
                    .filter(
                      ([key]) =>
                        !key.startsWith('cache_write') ||
                        Number(tier.prices[key]) > 0 ||
                        Number(base?.prices[key]) > 0
                    )
                    .map(([key, label]) => (
                      <tr key={key}>
                        <th
                          scope='row'
                          className='text-muted-foreground py-1.5 text-start text-xs font-medium'
                        >
                          {label}
                        </th>
                        {currency && (
                          <td className='text-muted-foreground px-1 text-end text-sm [overflow-wrap:anywhere]'>
                            {base
                              ? `${currency === 'USD' ? '$' : '¥'}${base.prices[key]}`
                              : '—'}
                          </td>
                        )}
                        <td className='py-1.5 ps-1 text-end text-base font-semibold tracking-tight [overflow-wrap:anywhere]'>
                          ¥{tier.prices[key]}
                        </td>
                      </tr>
                    ))}
                </tbody>
              </table>
            </div>
            <dl className='text-muted-foreground grid gap-2 text-xs'>
              <div className='flex flex-wrap items-center justify-between gap-2'>
                <dt>{t('Context')}</dt>
                <dd className='text-foreground min-w-0 tabular-nums'>
                  {tiers.length > 1 ? (
                    <Select
                      items={tiers}
                      value={tier.up_to}
                      onValueChange={(value) => setContext(value ?? undefined)}
                    >
                      <SelectTrigger
                        size='sm'
                        aria-label={`${model.id} ${t('Context')}`}
                        className='max-w-full text-xs'
                      >
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent align='end' alignItemWithTrigger={false}>
                        {tiers.map((item) => (
                          <SelectItem key={item.value} value={item.value}>
                            {item.label}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                  ) : (
                    tiers[0]?.label
                  )}
                </dd>
              </div>
              {!image && (
                <div className='flex justify-between gap-2'>
                  <dt>{t('Max output')}</dt>
                  <dd className='text-foreground tabular-nums'>
                    {format.format(model.max_output)} Token
                  </dd>
                </div>
              )}
            </dl>
          </>
        ) : (
          <p className='text-muted-foreground mt-4 text-sm'>
            {t('No models have a current verified price')}
          </p>
        ))}
    </article>
  )
}
