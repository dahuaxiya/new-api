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
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Dialog } from '@/components/dialog'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { Switch } from '@/components/ui/switch'
import {
  getRetailCatalog,
  getRetailKeyPolicy,
  putRetailKeyPolicy,
  retailKeyPolicySchema,
  type RetailCatalog,
  type RetailKeyPolicy,
  type RetailPrices,
} from '@/features/channels/retail-pricing-api'
import { handleServerError } from '@/lib/handle-server-error'
import { useAuthStore } from '@/stores/auth-store'

/** Load an owner's price policy and catalog only while the key dialog is open. */
export function ApiKeyPricePolicy(props: {
  open: boolean
  tokenId: number
  title?: string
  studio?: boolean
  initialProductId?: string
  onSaved?: (productId: string) => void
  onOpenChange: (open: boolean) => void
}) {
  const { t } = useTranslation()
  const identity = useAuthStore(
    (s) => `${s.auth.user?.id}:${s.auth.session?.sid}`
  )
  const query = useQuery({
    queryKey: ['retail-key-pricing', identity, props.tokenId],
    queryFn: async ({ signal }) => {
      const [catalog, saved] = await Promise.all([
        getRetailCatalog(signal),
        getRetailKeyPolicy(props.tokenId, signal),
      ])
      return { catalog, saved }
    },
    enabled: props.open && props.tokenId > 0,
    retry: false,
  })
  return (
    <Dialog
      open={props.open}
      onOpenChange={props.onOpenChange}
      title={props.title ?? t('API key price limits')}
      description={t(
        props.studio
          ? 'Price changes apply automatically. Your request limit, daily budget and wallet balance still apply.'
          : 'Prices follow upstream changes within your CNY ceilings. Requests above a ceiling pause before reaching the upstream.'
      )}
    >
      {query.isPending && <LoadingState inline message={t('Loading...')} />}
      {query.isError && (
        <ErrorState
          title={t('Unable to load retail pricing')}
          onRetry={() => void query.refetch()}
        />
      )}
      {query.data && !query.isError && (
        <PricePolicyEditor
          key={`${identity}:${props.tokenId}:${query.data.saved.revision}:${props.initialProductId ?? ''}`}
          studio={props.studio}
          tokenId={props.tokenId}
          initialProductId={props.initialProductId}
          catalog={query.data.catalog}
          saved={query.data.saved}
          onSaved={(productId) => {
            props.onSaved?.(productId)
            void query.refetch()
            props.onOpenChange(false)
          }}
        />
      )}
    </Dialog>
  )
}

/** Edit bounded spending rules without resetting an existing policy or ordinary API key consent. */
function PricePolicyEditor(props: {
  studio?: boolean
  tokenId: number
  initialProductId?: string
  catalog: RetailCatalog
  saved: { revision: number; policy: RetailKeyPolicy }
  onSaved: (productId: string) => void
}) {
  const { t } = useTranslation()
  const form = useForm<RetailKeyPolicy>({
    resolver: zodResolver(retailKeyPolicySchema),
    defaultValues: {
      ...props.saved.policy,
      caps: props.saved.policy.caps ?? {},
      max_request_cny: props.saved.policy.max_request_cny || '',
      daily_budget_cny: props.saved.policy.daily_budget_cny || '',
      // Selecting another call group is only a draft. Reuse budget values, but
      // require fresh ceilings and consent before it can replace the saved policy.
      ...(props.initialProductId &&
      props.initialProductId !== props.saved.policy.product_id
        ? {
            product_id: props.initialProductId,
            caps: {},
            accept_floating: Boolean(
              props.studio && props.saved.policy.studio_current
            ),
          }
        : {}),
    },
  })
  const policy = form.watch()
  /** Preserve one complete policy draft when changing product or a model's nested ceilings. */
  const setPolicy = (next: RetailKeyPolicy) => form.reset(next)
  const product = props.catalog.products.find(
    (item) => item.id === policy.product_id
  )
  const mutation = useMutation({
    mutationFn: (submitted: RetailKeyPolicy) =>
      putRetailKeyPolicy(props.tokenId, props.saved.revision, submitted),
    onSuccess: (_, submitted) => {
      toast.success(t('Saved successfully'))
      props.onSaved(submitted.product_id)
    },
    onError: (error) => handleServerError(error),
  })
  const fields = [
    { key: 'input', label: t('Input') },
    { key: 'output', label: t('Output') },
    { key: 'cache_read', label: t('Cache read') },
    { key: 'cache_write', label: t('Cache write') },
    { key: 'cache_write_1h', label: t('Cache write (1h)') },
  ] as const
  return (
    <form
      className='space-y-5 p-1'
      onSubmit={form.handleSubmit((submitted) => mutation.mutate(submitted))}
    >
      {!props.catalog.enabled && (
        <p className='text-muted-foreground text-sm'>
          {t('New priced requests are currently paused')}
        </p>
      )}
      <div className='space-y-2'>
        <Label htmlFor='key-price-product'>{t('Call group')}</Label>
        <NativeSelect
          id='key-price-product'
          value={policy.product_id || ''}
          onChange={(e) =>
            setPolicy({
              ...policy,
              product_id: e.target.value,
              caps: {},
              accept_floating: Boolean(policy.studio_current),
            })
          }
        >
          <NativeSelectOption value=''>
            {t('Select a call group')}
          </NativeSelectOption>
          {props.catalog.products.map((item) => (
            <NativeSelectOption value={item.id} key={item.id}>
              {item.name}
            </NativeSelectOption>
          ))}
        </NativeSelect>
      </div>
      {props.studio && (
        <div className='space-y-2'>
          <div className='flex items-center gap-3'>
            <Switch
              id='studio-current-prices'
              checked={Boolean(policy.studio_current)}
              onCheckedChange={(studio_current) =>
                setPolicy({ ...policy, studio_current, accept_floating: true })
              }
            />
            <Label htmlFor='studio-current-prices'>
              {t('Use current prices within my budget')}
            </Label>
          </div>
        </div>
      )}
      {product && !policy.studio_current && (
        <details open={!props.studio} className='space-y-4'>
          <summary className='cursor-pointer text-sm font-medium'>
            {t('Advanced model price limits')}
          </summary>
          <Button
            type='button'
            variant='outline'
            onClick={() => {
              const caps: Record<string, RetailPrices> = {}
              for (const model of product.models) {
                const values = {} as RetailPrices
                // Keep the server's decimal string: String(1e-8) is not an accepted money value.
                for (const field of fields) {
                  values[field.key] = model.tiers.reduce(
                    (largest, tier) =>
                      Number(tier.prices[field.key]) > Number(largest)
                        ? tier.prices[field.key]
                        : largest,
                    '0'
                  )
                }
                caps[model.id] = values
              }
              setPolicy({ ...policy, caps })
            }}
          >
            {t('Use current prices as ceilings')}
          </Button>
          {product.models.length === 0 && (
            <p className='text-muted-foreground text-sm'>
              {t('No models have a current verified price')}
            </p>
          )}
          {product.models.map((model, modelIndex) => (
            <div key={model.id} className='space-y-3 border-b pb-4'>
              <p className='text-sm font-medium'>
                {model.id} · {t('CNY per million tokens')}
              </p>
              <div className='grid grid-cols-2 gap-3 sm:grid-cols-3'>
                {fields.map((field) => (
                  <div key={field.key} className='space-y-2'>
                    <Label htmlFor={`price-cap-${modelIndex}-${field.key}`}>
                      {field.label}
                    </Label>
                    <Input
                      id={`price-cap-${modelIndex}-${field.key}`}
                      inputMode='decimal'
                      value={policy.caps[model.id]?.[field.key] ?? ''}
                      onChange={(e) =>
                        setPolicy({
                          ...policy,
                          caps: {
                            ...policy.caps,
                            [model.id]: {
                              ...policy.caps[model.id],
                              [field.key]: e.target.value,
                            },
                          },
                        })
                      }
                    />
                  </div>
                ))}
              </div>
            </div>
          ))}
        </details>
      )}
      <div className='grid gap-4 sm:grid-cols-2'>
        <div className='space-y-2'>
          <Label htmlFor='key-max-request'>
            {t('Maximum request charge (CNY)')}
          </Label>
          <Input
            id='key-max-request'
            inputMode='decimal'
            value={policy.max_request_cny}
            onChange={(e) =>
              setPolicy({ ...policy, max_request_cny: e.target.value })
            }
          />
        </div>
        <div className='space-y-2'>
          <Label htmlFor='key-daily-budget'>
            {t('Daily budget (CNY, UTC)')}
          </Label>
          <Input
            id='key-daily-budget'
            inputMode='decimal'
            value={policy.daily_budget_cny}
            onChange={(e) =>
              setPolicy({ ...policy, daily_budget_cny: e.target.value })
            }
          />
        </div>
      </div>
      <p className='text-muted-foreground text-sm'>
        {t(
          'The wallet reserves the maximum context and output cost, then returns the unused amount after settlement.'
        )}
      </p>
      {!policy.studio_current && (
        <div className='flex items-start gap-3'>
          <Switch
            id='key-accept-floating'
            checked={policy.accept_floating || false}
            onCheckedChange={(accept_floating) =>
              setPolicy({ ...policy, accept_floating })
            }
          />
          <Label htmlFor='key-accept-floating'>
            {t('Accept automatic price changes within these limits')}
          </Label>
        </div>
      )}
      {Object.keys(form.formState.errors).length > 0 && (
        <p role='alert' className='text-destructive text-sm'>
          {t(
            'Enter nonnegative prices with up to eight decimals and positive budgets'
          )}
        </p>
      )}
      <Button
        type='submit'
        disabled={
          mutation.isPending ||
          !policy.accept_floating ||
          !product ||
          (!policy.studio_current && Object.keys(policy.caps).length === 0) ||
          !policy.max_request_cny ||
          !policy.daily_budget_cny
        }
      >
        {t('Save price limits')}
      </Button>
    </form>
  )
}
