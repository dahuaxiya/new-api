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
import { useEffect, useMemo, useState } from 'react'
import { useForm, useWatch } from 'react-hook-form'
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
import { Tabs, TabsList, TabsTrigger, TabsContent } from '@/components/ui/tabs'
import { Textarea } from '@/components/ui/textarea'
import { formatNumber } from '@/lib/format'
import { handleServerError } from '@/lib/handle-server-error'
import { useAuthStore } from '@/stores/auth-store'

import {
  generateRetailOfficialDraft,
  getRetailPricing,
  importRetailRates,
  previewRetailPricing,
  publishRetailPricing,
  retailPricingDraftSchema,
  type RetailPriceBook,
  type RetailPricePreview,
  type RetailPricingState,
  type RetailOfficialDraft,
} from '../retail-pricing-api'
import { RetailGroupBindings } from './retail-group-bindings'
import { RetailSupplies } from './retail-supplies'

/** Root-only pricing drawer; query identity follows the login session and failures hide old state. */
export function RetailPricing() {
  const { t } = useTranslation()
  const [open, setOpen] = useState(false)
  const [hasPricingDraft, setHasPricingDraft] = useState(false)
  const [staleDraft, setStaleDraft] = useState(false)
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
                'Users select a group on this site. Its supply route determines the upstream Key and purchase price.'
              )}
            </SheetDescription>
            <a
              href='/upstream-keys'
              target='_blank'
              rel='noopener noreferrer'
              className='text-sm underline'
            >
              {t('Upstream Keys')}
            </a>
          </SheetHeader>
          <div className='flex min-h-0 flex-1 flex-col gap-6 overflow-y-auto p-6'>
            <Tabs defaultValue='routes' className='space-y-3'>
              <TabsList>
                <TabsTrigger value='routes'>{t('Supply routes')}</TabsTrigger>
                <TabsTrigger value='pricing'>{t('Price book')}</TabsTrigger>
              </TabsList>
              {/* Keep both drafts mounted when switching views; only explicit saves write changes. */}
              <TabsContent value='routes' keepMounted>
                {open && (
                  <RetailGroupBindings
                    key={identity}
                    identity={identity}
                    onSaved={() => {
                      // A binding is saved independently; never erase an unsaved tariff draft.
                      if (!hasPricingDraft) {
                        void query.refetch()
                      } else {
                        // Keep local catalog edits, but block previews/publication with old economics.
                        // A deliberate refresh reloads the complete committed version atomically.
                        setStaleDraft(true)
                        toast.info(
                          t(
                            'Bindings saved. Refresh pricing before publishing the existing draft.'
                          )
                        )
                      }
                    }}
                  />
                )}
                <RetailSupplies readOnly />
              </TabsContent>
              <TabsContent value='pricing' keepMounted className='space-y-4'>
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
                      onClick={async () => {
                        const refreshed = await query.refetch()
                        // Failed reads must keep old drafts blocked until committed economics are known.
                        if (refreshed.isSuccess) setStaleDraft(false)
                      }}
                    >
                      {t('Refresh')}
                    </Button>
                    <RetailPricingEditor
                      key={`${identity}:${query.data.version}`}
                      state={query.data}
                      staleDraft={staleDraft}
                      onSaved={() => void query.refetch()}
                      onDirtyChange={setHasPricingDraft}
                    />
                  </>
                )}
              </TabsContent>
            </Tabs>
          </div>
        </SheetContent>
      </Sheet>
    </>
  )
}

/** Keep drafts local and publish only the exact book that the operator has previewed. */
function RetailPricingEditor(props: {
  state: RetailPricingState
  staleDraft: boolean
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
  const [book, models, products, rates] = useWatch({
    control: form.control,
    name: ['book', 'models', 'products', 'rates'],
  })
  const [preview, setPreview] = useState<{
    draft: string
    rows: RetailPricePreview[]
    book: RetailPriceBook
  } | null>(null)
  const [confirm, setConfirm] = useState<'book' | 'rates' | null>(null)
  const [generated, setGenerated] = useState<RetailOfficialDraft | null>(null)
  const draft = JSON.stringify({ book, models, products, rates })
  const [initialDraft] = useState(draft)
  const onDirtyChange = props.onDirtyChange
  useEffect(() => {
    onDirtyChange(draft !== initialDraft)
  }, [draft, initialDraft, onDirtyChange])
  const previewValid = !props.staleDraft && preview?.draft === draft
  const catalog = useMemo(() => {
    try {
      const parsedModels: RetailPriceBook['models'] = JSON.parse(models)
      const parsedProducts: RetailPriceBook['products'] = JSON.parse(products)
      if (!Array.isArray(parsedModels) || !Array.isArray(parsedProducts)) {
        return null
      }
      return {
        models: parsedModels.length,
        products: parsedProducts.length,
        groups: [
          ...new Set(parsedProducts.flatMap((item) => item?.user_groups ?? [])),
        ],
        hasPublic: parsedProducts.some((item) => item?.public),
        callGroups: parsedProducts.map((item) => item.name),
      }
    } catch {
      return null
    }
  }, [models, products])
  // A successful empty preview is a setup draft, not a publishable price book.
  // Pausing an existing book is still allowed when all current routes have expired.
  const canPublish = Boolean(
    previewValid &&
    catalog?.models &&
    catalog.products &&
    (!book.enabled || preview?.rows.some((row) => row.available))
  )
  const mutation = useMutation({
    mutationFn: async (
      action: 'generate' | 'preview' | 'review' | 'book' | 'rates'
    ) => {
      if (action === 'generate') {
        const proposal = await generateRetailOfficialDraft(props.state.version)
        // Only the model catalog and routes enter the local draft. Generation
        // cannot change the sales switch, economics or consent on the server.
        form.setValue(
          'models',
          JSON.stringify(proposal.book.models ?? [], null, 2)
        )
        form.setValue(
          'products',
          JSON.stringify(proposal.book.products ?? [], null, 2)
        )
        setGenerated(proposal)
        setPreview(null)
        return
      }
      // A parse failure stays in this form; it cannot turn an incomplete import into a successful publication.
      const next: RetailPriceBook = {
        ...book,
        models: JSON.parse(models),
        products: JSON.parse(products),
        rates: JSON.parse(rates),
      }
      if (action === 'preview' || action === 'review') {
        const rows = await previewRetailPricing(next)
        // Bind the review to its submitted snapshot. Edits while the request is
        // running invalidate confirmation instead of publishing unseen changes.
        setPreview({ draft, rows, book: next })
        if (action === 'review') setConfirm('book')
        return
      }
      if (!previewValid || !preview) {
        throw new Error(t('Preview the current draft before publishing'))
      }
      if (action === 'rates') {
        await importRetailRates(props.state.version, next.rates)
      } else {
        if (!canPublish) {
          throw new Error(
            t(
              'Cannot publish an empty price book. Generate official prices and routes first.'
            )
          )
        }
        await publishRetailPricing(props.state.version, preview.book)
      }
      setConfirm(null)
      toast.success(
        t(
          next.enabled
            ? 'Price book published for new requests'
            : 'Price book saved; model requests remain paused'
        )
      )
      props.onSaved()
    },
    onError: (error) => handleServerError(error),
  })
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
      <section
        className='space-y-3 rounded-xl border p-4'
        aria-label={t('Model catalog setup')}
      >
        <div className='flex flex-wrap items-center justify-between gap-3'>
          <h3 className='text-sm font-medium'>{t('Model catalog setup')}</h3>
          <Button
            variant='outline'
            disabled={
              mutation.isPending || props.staleDraft || draft !== initialDraft
            }
            onClick={() => mutation.mutate('generate')}
          >
            {t('Generate official prices and routes')}
          </Button>
        </div>
        <p className='text-muted-foreground text-sm'>
          {t(
            'Uses saved supply bindings and verified official model IDs. Existing prices are preserved. No file upload is needed.'
          )}
        </p>
        <p className='text-sm' aria-live='polite'>
          {t('{{models}} models · {{products}} plans', {
            models: catalog?.models ?? 0,
            products: catalog?.products ?? 0,
          })}
        </p>
        <p className='text-muted-foreground text-sm'>
          {t('Call groups')}: {catalog?.callGroups.join(', ') || t('None')}
        </p>
        {catalog?.hasPublic && (
          <p className='text-muted-foreground text-sm'>
            {t(
              'Public call groups can be selected by every registered user. No account group change is needed.'
            )}
          </p>
        )}
        {props.state.account_group &&
          !catalog?.hasPublic &&
          !catalog?.groups.includes(props.state.account_group) && (
            <p className='text-sm text-amber-700 dark:text-amber-400'>
              {t(
                'Your account is in {{group}} and has no plan in this draft. Assign an account to an authorized group in User management, or change the saved supply binding.',
                { group: props.state.account_group }
              )}
            </p>
          )}
        {!book.enabled && (
          <p className='text-muted-foreground text-sm'>
            {t(
              'Model requests remain paused. Enable new priced requests and preview before publishing.'
            )}
          </p>
        )}
        {draft !== initialDraft && (
          <p className='text-muted-foreground text-xs'>
            {t(
              'Refresh saved pricing before generating another proposal. Unsaved edits are kept until you refresh.'
            )}
          </p>
        )}
        {generated && (
          <div className='space-y-2' aria-live='polite'>
            <p className='text-sm'>
              {t(
                'Proposal generated locally. Preview the selling prices, then publish to apply it.'
              )}
            </p>
            {generated.excluded.length > 0 && (
              <details>
                <summary className='cursor-pointer text-sm'>
                  {t('Skipped {{count}} model entries', {
                    count: generated.excluded.length,
                  })}
                </summary>
                <ul className='text-muted-foreground mt-2 space-y-1 text-xs'>
                  {generated.excluded.map((item) => (
                    <li key={`${item.channel_id}:${item.model}:${item.reason}`}>
                      #{item.channel_id} · {item.model || t('Supply channel')} ·{' '}
                      {item.reason === 'product_access_conflict' &&
                        t('Existing product access differs from this binding')}
                      {item.reason === 'model_mapping_conflict' &&
                        t('Channel model mapping differs from the price route')}
                      {item.reason !== 'product_access_conflict' &&
                        item.reason !== 'model_mapping_conflict' &&
                        t(
                          item.reason === 'supply_unavailable'
                            ? 'Supply is disabled or missing credentials'
                            : 'Official price is unverified or expired'
                        )}
                    </li>
                  ))}
                </ul>
              </details>
            )}
          </div>
        )}
      </section>
      <details className='space-y-3'>
        <summary className='cursor-pointer text-sm font-medium'>
          {t('Advanced settings')}
        </summary>
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
        </div>
        <details className='space-y-3'>
          <summary className='cursor-pointer text-sm font-medium'>
            {t('Manual multipliers · other suppliers')}
          </summary>
          <p className='text-muted-foreground text-sm'>
            {t(
              'Only for suppliers without automatic following. Bound Xingxing channels do not need this file or JSON.'
            )}
          </p>
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
          <Button
            variant='outline'
            disabled={
              mutation.isPending ||
              !previewValid ||
              props.state.version === 0 ||
              JSON.stringify(book) !==
                JSON.stringify({
                  ...props.state.book,
                  wallet_cny_per_unit:
                    props.state.book.wallet_cny_per_unit || '',
                  receipt_ratio: props.state.book.receipt_ratio || '1',
                }) ||
              models !==
                JSON.stringify(props.state.book.models ?? [], null, 2) ||
              products !==
                JSON.stringify(props.state.book.products ?? [], null, 2)
            }
            onClick={() => setConfirm('rates')}
          >
            {t('Update multipliers only')}
          </Button>
        </details>
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
      </details>
      <div className='flex flex-wrap gap-2'>
        {props.staleDraft && (
          <p role='alert' className='text-muted-foreground w-full text-sm'>
            {t(
              'Bindings saved. Refresh pricing before publishing the existing draft.'
            )}
          </p>
        )}
        <Button
          variant='outline'
          disabled={mutation.isPending || props.staleDraft}
          onClick={form.handleSubmit(() => mutation.mutate('preview'))}
        >
          {t('Preview CNY selling prices')}
        </Button>
        <Button
          disabled={
            mutation.isPending ||
            props.staleDraft ||
            !catalog?.models ||
            !catalog.products
          }
          onClick={form.handleSubmit(() => mutation.mutate('review'))}
        >
          {t('Publish price book')}
        </Button>
      </div>
      {Object.keys(form.formState.errors).length > 0 && (
        <p role='alert' className='text-destructive text-sm'>
          {t('Import valid JSON arrays before previewing')}
        </p>
      )}
      {previewValid && confirm === null && (
        <SellingPricePreview rows={preview.rows} />
      )}
      <ConfirmDialog
        className='data-[size=default]:max-w-[calc(100vw-2rem)] data-[size=default]:sm:max-w-3xl'
        open={confirm !== null}
        onOpenChange={(open) => !open && setConfirm(null)}
        title={t('Publish new prices?')}
        desc={t(
          'New requests use this version. Existing requests keep their accepted price. Disabled or expired pricing pauses new requests.'
        )}
        confirmText={t('Publish')}
        isLoading={mutation.isPending}
        disabled={confirm === 'book' ? !canPublish : !previewValid}
        handleConfirm={() => confirm && mutation.mutate(confirm)}
      >
        <div className='max-h-[55dvh] overflow-y-auto'>
          {preview && <SellingPricePreview rows={preview.rows} />}
          {(!previewValid || (confirm === 'book' && !canPublish)) && (
            <p role='alert' className='text-destructive text-sm'>
              {t(
                'Prices cannot be published. Close this review, check the draft and available routes, then try again.'
              )}
            </p>
          )}
        </div>
      </ConfirmDialog>
    </>
  )
}

/** Show the full calculated tariff both in the optional preview and the publication review. */
export function SellingPricePreview(props: { rows: RetailPricePreview[] }) {
  const { t } = useTranslation()
  return (
    <div className='space-y-4' aria-live='polite'>
      <p className='text-sm font-medium'>
        {t('Selling prices · CNY per million tokens')}
      </p>
      {props.rows.length === 0 && (
        <p className='text-muted-foreground text-sm'>
          {t('No products configured')}
        </p>
      )}
      {props.rows.map((row) => (
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
  )
}
