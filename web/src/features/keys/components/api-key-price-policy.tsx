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
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
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
import {
  getRetailCatalog,
  getRetailKeyPolicy,
  putRetailKeyPolicy,
  putRetailKeyGroup,
  retailKeyPolicySchema,
  type RetailCatalog,
  type RetailKeyPolicy,
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
  const queryClient = useQueryClient()
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
      title={
        props.title ?? t(props.studio ? 'API key price limits' : 'Call group')
      }
      description={t(
        props.studio
          ? 'Price changes apply automatically. Your request limit, daily budget and wallet balance still apply.'
          : 'Choose a call group. Usage is charged to your balance at its current prices.'
      )}
    >
      {query.isPending && <LoadingState inline message={t('Loading...')} />}
      {query.isError && (
        <ErrorState
          title={t('Unable to load retail pricing')}
          onRetry={() => void query.refetch()}
        />
      )}
      {query.data &&
        !query.isError &&
        (props.studio ? (
          <StudioBudgetEditor
            key={`${identity}:${props.tokenId}:${query.data.saved.revision}:${props.initialProductId ?? ''}`}
            tokenId={props.tokenId}
            initialProductId={props.initialProductId}
            catalog={query.data.catalog}
            saved={query.data.saved}
            onSaved={(productId) => {
              props.onSaved?.(productId)
              void queryClient.invalidateQueries({
                queryKey: ['retail-key-pricing', identity, props.tokenId],
              })
              props.onOpenChange(false)
            }}
          />
        ) : (
          <CallGroupEditor
            key={`${identity}:${props.tokenId}:${query.data.saved.revision}`}
            tokenId={props.tokenId}
            catalog={query.data.catalog}
            saved={query.data.saved}
            onSaved={(productId) => {
              props.onSaved?.(productId)
              void queryClient.invalidateQueries({
                queryKey: ['retail-key-pricing', identity, props.tokenId],
              })
              props.onOpenChange(false)
            }}
          />
        ))}
    </Dialog>
  )
}

/** Edit the Studio budget using the selected group’s published prices.
 * @param props Verified catalog, saved spending limits and the group selected in Studio.
 * @return A budget form that preserves the saved revision for concurrent edits.
 */
function StudioBudgetEditor(props: {
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
      product_id: props.initialProductId || props.saved.policy.product_id,
      // Unit prices come from the server on each request, including newly published models.
      studio_current: true,
      accept_floating: true,
      caps: {},
      max_request_cny: props.saved.policy.max_request_cny,
      daily_budget_cny: props.saved.policy.daily_budget_cny,
    },
  })
  const policy = form.watch()
  /** Preserve one complete policy draft when changing groups or spending limits. */
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
  return (
    <form
      className="space-y-5 p-1"
      inert={mutation.isPending ? true : undefined}
      aria-busy={mutation.isPending}
      onSubmit={form.handleSubmit((submitted) => mutation.mutate(submitted))}
    >
      {!props.catalog.enabled && (
        <p className="text-muted-foreground text-sm">
          {t('New priced requests are currently paused')}
        </p>
      )}
      <div className="space-y-2">
        <Label htmlFor="key-price-product">{t('Call group')}</Label>
        <NativeSelect
          id="key-price-product"
          value={policy.product_id || ''}
          onChange={(e) => {
            setPolicy({
              ...policy,
              product_id: e.target.value,
            })
          }}
        >
          <NativeSelectOption value="">
            {t('Select a call group')}
          </NativeSelectOption>
          {props.catalog.products.map((item) => (
            <NativeSelectOption value={item.id} key={item.id}>
              {item.name}
            </NativeSelectOption>
          ))}
        </NativeSelect>
      </div>
      <div className="grid gap-4 sm:grid-cols-2">
        <div className="space-y-2">
          <Label htmlFor="key-max-request">
            {t('Maximum request charge (CNY)')}
          </Label>
          <Input
            id="key-max-request"
            inputMode="decimal"
            value={policy.max_request_cny}
            onChange={(e) =>
              setPolicy({ ...policy, max_request_cny: e.target.value })
            }
          />
        </div>
        <div className="space-y-2">
          <Label htmlFor="key-daily-budget">
            {t('Daily budget (CNY, UTC)')}
          </Label>
          <Input
            id="key-daily-budget"
            inputMode="decimal"
            value={policy.daily_budget_cny}
            onChange={(e) =>
              setPolicy({ ...policy, daily_budget_cny: e.target.value })
            }
          />
        </div>
      </div>
      <p className="text-muted-foreground text-sm">
        {t(
          'The wallet reserves the maximum context and output cost, then returns the unused amount after settlement.'
        )}
      </p>
      {Object.keys(form.formState.errors).length > 0 && (
        <p role="alert" className="text-destructive text-sm">
          {t(
            'Enter nonnegative prices with up to eight decimals and positive budgets'
          )}
        </p>
      )}
      <Button
        type="submit"
        disabled={
          mutation.isPending ||
          !product ||
          !policy.max_request_cny ||
          !policy.daily_budget_cny
        }
      >
        {t('Save changes')}
      </Button>
    </form>
  )
}

/** Change a key's group with optimistic concurrency; no customer price or budget values are synthesized.
 * @param props Owner-scoped key, visible catalog, saved revision and completion callback.
 * @return A group selector and save action.
 */
function CallGroupEditor(props: {
  tokenId: number
  catalog: RetailCatalog
  saved: { revision: number; policy: RetailKeyPolicy }
  onSaved: (productId: string) => void
}) {
  const { t } = useTranslation()
  const [productId, setProductId] = useState(
    props.saved.policy.product_id || ''
  )
  const mutation = useMutation({
    mutationFn: (selected: string) =>
      putRetailKeyGroup(props.tokenId, props.saved.revision, selected),
    onSuccess: (_, selected) => {
      toast.success(t('Saved successfully'))
      props.onSaved(selected)
    },
    onError: (error) => handleServerError(error),
  })
  return (
    <form
      className="space-y-5 p-1"
      onSubmit={(event) => {
        event.preventDefault()
        if (
          !mutation.isPending &&
          props.catalog.products.some((product) => product.id === productId)
        ) {
          mutation.mutate(productId)
        }
      }}
    >
      <div className="space-y-2">
        <Label htmlFor="key-call-group">{t('Call group')}</Label>
        <NativeSelect
          id="key-call-group"
          value={productId}
          disabled={mutation.isPending}
          onChange={(event) => setProductId(event.target.value)}
        >
          <NativeSelectOption value="">
            {t('Select a call group')}
          </NativeSelectOption>
          {props.catalog.products.map((product) => (
            <NativeSelectOption key={product.id} value={product.id}>
              {product.name}
            </NativeSelectOption>
          ))}
        </NativeSelect>
      </div>
      {props.catalog.products.length === 0 && (
        <p role="status">
          {t('No call groups are available. Contact the administrator.')}
        </p>
      )}
      <Button
        type="submit"
        disabled={
          mutation.isPending ||
          !props.catalog.products.some((product) => product.id === productId)
        }
      >
        {t('Save')}
      </Button>
    </form>
  )
}
