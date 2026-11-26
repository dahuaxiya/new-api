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

import { Button } from '@/components/ui/button'
import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
  SheetTrigger,
} from '@/components/ui/sheet'
import type { RetailCatalog } from '@/features/channels/retail-pricing-api'
import { getLobeIcon } from '@/lib/lobe-icon'

import { retailModelBrand } from '../lib/retail-model-brand'
import { RetailModelCard } from './retail-model-card'
import { SearchBar } from './search-bar'

type Product = RetailCatalog['products'][number]

/** Show the same authorized options in the desktop sidebar and mobile sheet.
 * @param props Groups in catalog order, active group, and selection callback.
 * @return A named list with one button per available calling group.
 */
function RetailGroupNav(props: {
  products: Product[]
  group: string
  onPick: (group: string) => void
}) {
  const { t } = useTranslation()
  return (
    <nav aria-label={t('Call groups')}>
      <ul className='space-y-1'>
        {props.products.map((product) => {
          const factor = groupSaleFactor(product)
          // Mixed or empty groups have no single company identity.
          const brands = new Set(
            product.models.map(
              (model) => retailModelBrand(model.official_model)?.[1]
            )
          )
          const brand =
            brands.size === 1 ? brands.values().next().value : undefined
          return (
            <li key={product.id}>
              <Button
                variant='ghost'
                aria-current={product.id === props.group ? 'true' : undefined}
                onClick={() => props.onPick(product.id)}
                className='aria-current:bg-muted h-auto min-h-11 w-full justify-start gap-2.5 rounded-xl px-2.5 py-2 text-start whitespace-normal aria-current:font-semibold'
              >
                <span className='shrink-0' aria-hidden='true'>
                  {getLobeIcon(brand, 18)}
                </span>
                <span className='min-w-0 flex-1'>
                  <span className='block [overflow-wrap:anywhere]'>{product.name}</span>
                  <span className='text-muted-foreground block text-xs font-normal'>
                    {product.models.length} {t('Models')}
                  </span>
                </span>
                {factor && (
                  <span className='text-primary shrink-0 rounded-md bg-primary/5 px-1.5 py-0.5 text-xs font-semibold whitespace-nowrap tabular-nums'>
                    <span className='sr-only'>{t('Effective sale factor')} </span>
                    {factor}
                  </span>
                )}
              </Button>
            </li>
          )
        })}
      </ul>
    </nav>
  )
}

/** Summarize the sale factors for the full group, even when search hides models.
 * @param product Selected catalog group with all its quotable models.
 * @return One factor or the range; omit the summary when an older response lacks a factor.
 */
function groupSaleFactor(product: Product) {
  let min = Infinity
  let max = 0
  // A group may use different pricing routes by model. Do not imply a single
  // rate unless all of the quoted models agree.
  for (const model of product.models) {
    const factor = Number(model.sale_factor)
    if (!Number.isFinite(factor) || factor <= 0) {
      return undefined
    }
    min = Math.min(min, factor)
    max = Math.max(max, factor)
  }
  if (max === 0) {
    return undefined
  }
  const low = Number(min.toPrecision(5))
  const high = Number(max.toPrecision(5))
  const estimate = low !== min || high !== max ? '≈' : ''
  return low === high ? `${estimate}×${low}` : `${estimate}×${low}–×${high}`
}

/** Browse and search the model prices returned for the signed-in user.
 * @param props Catalog groups already restricted by the server.
 * @return Desktop sidebar, mobile group sheet, and the selected group's cards.
 */
export function RetailGroupPrices(props: { products: Product[] }) {
  const { t } = useTranslation()
  const [group, setGroup] = useState('')
  const [search, setSearch] = useState('')
  const [sheet, setSheet] = useState(false)
  // A catalog refresh can remove a group. Resolve selection against the current response.
  const selected =
    props.products.find((product) => product.id === group) ??
    props.products.find((product) => product.models.length > 0) ??
    props.products[0]
  if (!selected) {
    return <p role='status'>{t('No products available for this account')}</p>
  }

  const saleFactor = groupSaleFactor(selected)
  const keyword = search.trim().toLowerCase()
  const models = selected.models.filter((model) =>
    model.id.toLowerCase().includes(keyword)
  )

  /** Close the mobile sheet and clear search only when the group changes.
   * @param id ID of a group from the current catalog.
   */
  function pickGroup(id: string) {
    if (id !== selected.id) {
      setGroup(id)
      setSearch('')
    }
    setSheet(false)
  }

  return (
    <div className='grid items-start gap-6 border-t pt-6 md:grid-cols-[13rem_minmax(0,1fr)] lg:grid-cols-[14rem_minmax(0,1fr)] lg:gap-8'>
      <aside className='sticky top-20 hidden min-w-0 border-e pe-4 md:block'>
        <h2 className='text-muted-foreground mb-2 px-2.5 text-xs font-medium'>
          {t('Call groups')}
        </h2>
        <div className='max-h-[65dvh] overflow-y-auto p-1'>
          <RetailGroupNav
            products={props.products}
            group={selected.id}
            onPick={pickGroup}
          />
        </div>
      </aside>
      <section aria-label={selected.name} className='min-w-0'>
        <div className='mb-4 flex flex-wrap items-center justify-between gap-3'>
          <h2 className='flex min-w-0 flex-wrap items-baseline gap-2 text-lg font-semibold tracking-tight [overflow-wrap:anywhere]'>
            {selected.name}
            <span className='text-muted-foreground border-s ps-2 text-xs font-normal tabular-nums'>
              {models.length} {t('Models')}
            </span>
            {saleFactor && (
              <span className='inline-flex items-baseline gap-1.5 rounded-md border border-primary/20 bg-primary/5 px-2 py-1 leading-none whitespace-nowrap'>
                <span className='text-muted-foreground text-xs leading-none font-medium tracking-normal'>
                  {t('Effective sale factor')}
                </span>{' '}
                <strong className='text-primary text-base leading-none font-semibold tracking-tight tabular-nums'>
                  {saleFactor}
                </strong>
              </span>
            )}
          </h2>
          <div className='flex w-full items-center gap-2 md:w-auto'>
            <Sheet open={sheet} onOpenChange={setSheet}>
              <SheetTrigger
                render={
                  <Button
                    variant='outline'
                    className='h-10 shrink-0 md:hidden'
                  />
                }
              >
                {t('Select a group')}
              </SheetTrigger>
              <SheetContent side='left' className='max-w-xs gap-0'>
                <SheetHeader>
                  <SheetTitle>{t('Call groups')}</SheetTitle>
                </SheetHeader>
                <div className='overflow-y-auto p-3'>
                  <RetailGroupNav
                    products={props.products}
                    group={selected.id}
                    onPick={pickGroup}
                  />
                </div>
              </SheetContent>
            </Sheet>
            <SearchBar
              value={search}
              onChange={setSearch}
              onClear={() => setSearch('')}
              className='min-w-0 flex-1 md:w-56'
            />
          </div>
        </div>
        {models.length ? (
          <div className='grid grid-cols-[repeat(auto-fill,minmax(min(100%,19rem),1fr))] items-stretch gap-4 xl:gap-5'>
            {models.map((model) => (
              <RetailModelCard
                key={`${selected.id}/${model.id}`}
                model={model}
              />
            ))}
          </div>
        ) : (
          <p
            role='status'
            className='text-muted-foreground rounded-xl border p-5 text-sm'
          >
            {selected.models.length
              ? t('No models found')
              : t('No models have a current verified price')}
          </p>
        )}
      </section>
    </div>
  )
}
