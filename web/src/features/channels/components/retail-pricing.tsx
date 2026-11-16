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
import { zodResolver } from '@hookform/resolvers/zod'
import { useMutation, useQuery } from '@tanstack/react-query'
import { useEffect, useState } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ConfirmDialog } from '@/components/confirm-dialog'
import {
  sideDrawerContentClassName,
  sideDrawerHeaderClassName,
} from '@/components/drawer-layout'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'
import { formatNumber } from '@/lib/format'
import { handleServerError } from '@/lib/handle-server-error'
import { useAuthStore } from '@/stores/auth-store'

import {
  getRetailPricing,
  importRetailRates,
  previewRetailPricing,
  publishRetailPricing,
  retailPricingDraftSchema,
  type RetailPriceBook,
  type RetailPricePreview,
  type RetailPricingState,
} from '../retail-pricing-api'
import { RetailGroupBindings } from './retail-group-bindings'
import { RetailPublicRatesPreview } from './retail-public-rates-preview'

/** Root-only pricing drawer; query identity follows the login session and failures hide old state. */
export function RetailPricing() {
  const { t } = useTranslation()
  const [open, setOpen] = useState(false)
  const [hasPricingDraft, setHasPricingDraft] = useState(false)
  const identity = useAuthStore(
    (s) => `${s.auth.user?.id}:${s.auth.session?.sid}`
  )
  const query = useQuery({
    queryKey: ['retail-pricing', identity],
    queryFn: ({ signal }) => getRetailPricing(signal),
    enabled: open,
    retry: false,
  })
  return (
    <>
      <Button variant='outline' onClick={() => setOpen(true)}>
        {t('Retail pricing')}
      </Button>
      <Sheet open={open} onOpenChange={setOpen}>
        <SheetContent className={sideDrawerContentClassName('sm:max-w-3xl')}>
          <SheetHeader className={sideDrawerHeaderClassName()}>
            <SheetTitle>{t('Retail pricing')}</SheetTitle>
            <SheetDescription>
              {t(
                'Official prices and upstream multipliers are maintained separately. Expired prices pause new requests.'
              )}
            </SheetDescription>
          </SheetHeader>
          <div className='flex min-h-0 flex-1 flex-col gap-6 overflow-y-auto p-6'>
            {open && (
              <RetailGroupBindings
                key={identity}
                identity={identity}
                onSaved={() => {
                  // A binding is saved independently; never erase an unsaved tariff draft.
                  if (!hasPricingDraft) {
                    void query.refetch()
                  } else {
                    toast.info(
                      t(
                        'Bindings saved. Refresh pricing before publishing the existing draft.'
                      )
                    )
                  }
                }}
              />
            )}
            {open && (
              <details>
                <summary className='text-muted-foreground cursor-pointer text-sm'>
                  {t('Public multiplier reference')}
                </summary>
                <div className='mt-3'>
                  <RetailPublicRatesPreview
                    key={identity}
                    identity={identity}
                  />
                </div>
              </details>
            )}
            {query.isPending && (
              <LoadingState inline message={t('Loading...')} />
            )}
            {query.isError && (
              <ErrorState
                title={t('Unable to load retail pricing')}
                onRetry={() => void query.refetch()}
              />
            )}
            {query.data && !query.isError && (
              <>
                <Button
                  variant='ghost'
                  className='self-end'
                  disabled={query.isFetching}
                  onClick={() => void query.refetch()}
                >
                  {t('Refresh')}
                </Button>
                <RetailPricingEditor
                  key={`${identity}:${query.data.version}`}
                  state={query.data}
                  onSaved={() => void query.refetch()}
                  onDirtyChange={setHasPricingDraft}
                />
              </>
            )}
          </div>
        </SheetContent>
      </Sheet>
    </>
  )
}

/** Keep drafts local and publish only the exact book that the operator has previewed. */
function RetailPricingEditor(props: {
  state: RetailPricingState
  onSaved: () => void
  onDirtyChange: (dirty: boolean) => void
}) {
  const { t } = useTranslation()
  const form = useForm({
    resolver: zodResolver(retailPricingDraftSchema),
    defaultValues: {
      book: {
        ...props.state.book,
        wallet_cny_per_unit: props.state.book.wallet_cny_per_unit || '',
        receipt_ratio: props.state.book.receipt_ratio || '1',
      },
      models: JSON.stringify(props.state.book.models ?? [], null, 2),
      products: JSON.stringify(props.state.book.products ?? [], null, 2),
      rates: JSON.stringify(props.state.book.rates ?? [], null, 2),
    },
  })
  const { book, models, products, rates } = form.watch()
  const [preview, setPreview] = useState<{
    draft: string
    rows: RetailPricePreview[]
  } | null>(null)
  const [confirm, setConfirm] = useState<'book' | 'rates' | null>(null)
  const draft = JSON.stringify({ book, models, products, rates })
  const [initialDraft] = useState(draft)
  const onDirtyChange = props.onDirtyChange
  useEffect(() => {
    onDirtyChange(draft !== initialDraft)
  }, [draft, initialDraft, onDirtyChange])
  const previewValid = preview?.draft === draft
  const mutation = useMutation({
    mutationFn: async (action: 'preview' | 'book' | 'rates') => {
      // A parse failure stays in this form; it cannot turn an incomplete import into a successful publication.
      const next: RetailPriceBook = {
        ...book,
        models: JSON.parse(models),
        products: JSON.parse(products),
        rates: JSON.parse(rates),
      }
      if (action === 'preview') {
        setPreview({ draft, rows: await previewRetailPricing(next) })
        return
      }
      if (!previewValid) {
        throw new Error(t('Preview the current draft before publishing'))
      }
      if (action === 'rates') {
        await importRetailRates(props.state.version, next.rates)
      } else {
        await publishRetailPricing(props.state.version, next)
      }
      setConfirm(null)
      toast.success(t('Saved successfully'))
      props.onSaved()
    },
    onError: (error) => handleServerError(error),
  })
  // Keep exact CNY decimal strings in the preview; wallet display FX is unrelated.
  const numberFields = [
    { key: 'margin_bps', label: t('Target margin (%)') },
    { key: 'fee_bps', label: t('Payment fee (%)') },
    { key: 'buffer_bps', label: t('Cost buffer (%)') },
  ] as const
  return (
    <>
      <div className='flex flex-wrap items-center justify-between gap-3'>
        <Badge variant={book.enabled ? 'secondary' : 'outline'}>
          {t('Price version')} {props.state.version}
        </Badge>
        <div className='flex items-center gap-2'>
          <Switch
            id='retail-pricing-enabled'
            checked={book.enabled}
            onCheckedChange={(enabled) =>
              form.setValue('book.enabled', enabled)
            }
          />
          <Label htmlFor='retail-pricing-enabled'>
            {t('Accept new priced requests')}
          </Label>
        </div>
      </div>
      <p className='text-muted-foreground text-sm'>
        {t(
          'USD uses the supplier numeric 1:1 CNY convention. CNY prices are used directly. Publishing requires users to configure price limits.'
        )}
      </p>
      <div className='grid gap-4 sm:grid-cols-2'>
        <div className='space-y-2'>
          <Label htmlFor='retail-wallet-rate'>
            {t('CNY per existing wallet unit')}
          </Label>
          <Input
            id='retail-wallet-rate'
            {...form.register('book.wallet_cny_per_unit')}
            inputMode='decimal'
          />
        </div>
        <div className='space-y-2'>
          <Label htmlFor='retail-receipts'>
            {t('Cash received per CNY of credit')}
          </Label>
          <Input
            id='retail-receipts'
            {...form.register('book.receipt_ratio')}
            inputMode='decimal'
          />
        </div>
        {numberFields.map((field) => (
          <div className='space-y-2' key={field.key}>
            <Label htmlFor={field.key}>{field.label}</Label>
            <Input
              id={field.key}
              type='number'
              min={0}
              step='0.01'
              value={book[field.key] / 100}
              onChange={(e) =>
                form.setValue(
                  `book.${field.key}`,
                  Math.round(Number(e.target.value) * 100)
                )
              }
            />
          </div>
        ))}
      </div>
      <div className='space-y-2'>
        <Label htmlFor='retail-rates'>
          {t('Upstream group multipliers (JSON)')}
        </Label>
        <p className='text-muted-foreground text-sm'>
          {t(
            'Import a complete group snapshot with id, multiplier, verified_at and valid_until. Times use Unix seconds; validity is at most seven days.'
          )}
        </p>
        <Input
          type='file'
          accept='.json,application/json'
          aria-label={t('Import multiplier file')}
          onChange={async (e) => {
            const file = e.target.files?.[0]
            if (!file) return
            if (file.size > 128 * 1024) {
              toast.error(t('Multiplier file must be smaller than 128 KB'))
              return
            }
            try {
              form.setValue('rates', await file.text())
              setPreview(null)
            } catch (error) {
              handleServerError(error)
            }
            e.target.value = ''
          }}
        />
        <Textarea
          id='retail-rates'
          {...form.register('rates')}
          rows={6}
          spellCheck={false}
          className='font-mono text-xs'
        />
      </div>
      <details className='space-y-4'>
        <summary className='cursor-pointer text-sm font-medium'>
          {t('Official catalog and product access')}
        </summary>
        <p className='text-muted-foreground text-sm'>
          {t(
            'Models require official_model, currency, source, verified_at, valid_until, max_output and tiers. Products bind authorized user_groups to channel_id, rate_id and model mappings.'
          )}
        </p>
        <div className='space-y-2'>
          <Label htmlFor='retail-models'>
            {t('Official model catalog (JSON)')}
          </Label>
          <Textarea
            id='retail-models'
            {...form.register('models')}
            rows={10}
            spellCheck={false}
            className='font-mono text-xs'
          />
        </div>
        <div className='space-y-2'>
          <Label htmlFor='retail-products'>
            {t('Sales products and access (JSON)')}
          </Label>
          <Textarea
            id='retail-products'
            {...form.register('products')}
            rows={8}
            spellCheck={false}
            className='font-mono text-xs'
          />
        </div>
      </details>
      <div className='flex flex-wrap gap-2'>
        <Button
          variant='outline'
          disabled={mutation.isPending}
          onClick={form.handleSubmit(() => mutation.mutate('preview'))}
        >
          {t('Preview CNY selling prices')}
        </Button>
        <Button
          disabled={mutation.isPending || !previewValid}
          onClick={() => setConfirm('book')}
        >
          {t('Publish price book')}
        </Button>
        <Button
          variant='outline'
          disabled={
            mutation.isPending ||
            !previewValid ||
            props.state.version === 0 ||
            JSON.stringify(book) !==
              JSON.stringify({
                ...props.state.book,
                wallet_cny_per_unit: props.state.book.wallet_cny_per_unit || '',
                receipt_ratio: props.state.book.receipt_ratio || '1',
              }) ||
            models !== JSON.stringify(props.state.book.models ?? [], null, 2) ||
            products !==
              JSON.stringify(props.state.book.products ?? [], null, 2)
          }
          onClick={() => setConfirm('rates')}
        >
          {t('Update multipliers only')}
        </Button>
      </div>
      {Object.keys(form.formState.errors).length > 0 && (
        <p role='alert' className='text-destructive text-sm'>
          {t('Import valid JSON arrays before previewing')}
        </p>
      )}
      {previewValid && (
        <div className='space-y-4' aria-live='polite'>
          <p className='text-sm font-medium'>
            {t('Selling prices · CNY per million tokens')}
          </p>
          {preview.rows.length === 0 && (
            <p className='text-muted-foreground text-sm'>
              {t('No products configured')}
            </p>
          )}
          {preview.rows.map((row) => (
            <div
              key={`${row.product}:${row.model}`}
              className='space-y-2 border-b pb-4'
            >
              <p className='text-sm'>
                {row.product} · {row.model}
              </p>
              {!row.available && (
                <Badge variant='warning'>{t('Unavailable or expired')}</Badge>
              )}
              {row.tiers?.map((tier) => (
                <div
                  key={tier.up_to}
                  className='grid grid-cols-2 gap-2 text-sm sm:grid-cols-3'
                >
                  <span>
                    {t('Context limit')}: {formatNumber(tier.up_to)}
                  </span>
                  <span>
                    {t('Input')}: ¥{tier.prices.input}
                  </span>
                  <span>
                    {t('Output')}: ¥{tier.prices.output}
                  </span>
                  <span>
                    {t('Cache read')}: ¥{tier.prices.cache_read}
                  </span>
                  <span>
                    {t('Cache write')}: ¥{tier.prices.cache_write}
                  </span>
                  <span>
                    {t('Cache write (1h)')}: ¥{tier.prices.cache_write_1h}
                  </span>
                </div>
              ))}
            </div>
          ))}
        </div>
      )}
      <ConfirmDialog
        open={confirm !== null}
        onOpenChange={(open) => !open && setConfirm(null)}
        title={t('Publish new prices?')}
        desc={t(
          'New requests use this version. Existing requests keep their accepted price. Disabled or expired pricing pauses new requests.'
        )}
        confirmText={t('Publish')}
        isLoading={mutation.isPending}
        disabled={!previewValid}
        handleConfirm={() => confirm && mutation.mutate(confirm)}
      />
    </>
  )
}
