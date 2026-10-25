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
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import {
  sideDrawerContentClassName,
  sideDrawerHeaderClassName,
} from '@/components/drawer-layout'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Field, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { Switch } from '@/components/ui/switch'
import {
  SecureVerificationDialog,
  useSecureVerification,
} from '@/features/auth/secure-verification'
import { handleServerError } from '@/lib/handle-server-error'
import { useAuthStore } from '@/stores/auth-store'

import {
  getHostingStore,
  postHosting,
  type HostingAdminStore,
  type HostingPlan,
} from './api'
import { HostingOrderList } from './store-orders'

/** 套餐与订单复用用户管理抽屉；支付密钥仍在既有支付配置中管理，页面不读取密钥。 */
export function HostingStoreAdmin(props: {
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const { t } = useTranslation()
  const identity = useAuthStore(
    (s) => `${s.auth.user?.id}:${s.auth.session?.sid}`
  )
  const query = useQuery({
    queryKey: ['hosting-store-admin', identity],
    queryFn: ({ signal }) =>
      getHostingStore<HostingAdminStore>('admin/store', 1, signal),
    enabled: props.open,
  })
  return (
    <Sheet open={props.open} onOpenChange={props.onOpenChange}>
      <SheetContent className={sideDrawerContentClassName('sm:max-w-3xl')}>
        <SheetHeader className={sideDrawerHeaderClassName()}>
          <SheetTitle>{t('Hosting plans and orders')}</SheetTitle>
          <SheetDescription>
            {t(
              'Prices are CNY per seat. Changing a plan does not alter existing orders. Payment setup and available slots are required before selling.'
            )}
          </SheetDescription>
        </SheetHeader>
        <div className='min-h-0 flex-1 space-y-6 overflow-y-auto p-6'>
          {query.isPending && <LoadingState inline />}
          {query.isError && <ErrorState onRetry={() => query.refetch()} />}
          {query.data && (
            <>
              {!query.data.payment_ready && (
                <Alert>
                  <AlertDescription>
                    {t('Hosting payments are not available yet.')}
                  </AlertDescription>
                </Alert>
              )}
              <div className='space-y-3'>
                {query.data.plans.map((plan) => (
                  <PlanEditor
                    key={`${identity}:${plan.id}:${plan.price_cents}:${plan.enabled}`}
                    plan={plan}
                  />
                ))}
              </div>
              {props.open && (
                <HostingOrderList
                  identity={identity}
                  admin
                  paymentReady={query.data.payment_ready}
                />
              )}
            </>
          )}
        </div>
      </SheetContent>
    </Sheet>
  )
}

/** 每次保存重新取得仅适用于当前套餐的一次性证明，防止普通会话或旧证明上架收费套餐。 */
function PlanEditor(props: { plan: HostingPlan }) {
  const { t } = useTranslation()
  const cache = useQueryClient()
  const verification = useSecureVerification()
  const [price, setPrice] = useState((props.plan.price_cents / 100).toFixed(2))
  const [enabled, setEnabled] = useState(props.plan.enabled)
  const validPrice =
    /^\d{1,6}(\.\d{1,2})?$/.test(price) &&
    Number(price) > 0 &&
    Number(price) <= 100000
  const save = useMutation({
    mutationFn: async () => {
      if (!validPrice) return
      const proof = await verification.requestVerification({
        scope: 'gpt.hosting.manage',
        context: { action: 'store-plan', binding_id: props.plan.id },
      })
      if (!proof) return
      // 文本人民币金额转为分后提交；后端仍独立校验范围与固定套餐期限。
      await postHosting(
        'admin/store/plans',
        {
          ...props.plan,
          price_cents: Math.round(Number(price) * 100),
          enabled,
        },
        proof.proof_token
      )
    },
    retry: false,
    gcTime: 0,
    onError: handleServerError,
    onSuccess: async () => {
      await cache.invalidateQueries({ queryKey: ['hosting-store-admin'] })
      await cache.invalidateQueries({ queryKey: ['hosting-store'] })
    },
  })
  return (
    <section className='space-y-3 rounded-xl border p-4'>
      <h3 className='font-medium'>
        {t('{{days}} days per seat', { days: props.plan.days })}
      </h3>
      <div className='flex flex-wrap items-end gap-4'>
        <Field className='min-w-40 flex-1'>
          <FieldLabel htmlFor={`price-${props.plan.id}`}>
            {t('Price (CNY)')}
          </FieldLabel>
          <Input
            id={`price-${props.plan.id}`}
            inputMode='decimal'
            value={price}
            aria-invalid={!validPrice}
            disabled={save.isPending}
            onChange={(e) => setPrice(e.target.value)}
          />
        </Field>
        <Field className='w-auto'>
          <FieldLabel htmlFor={`enabled-${props.plan.id}`}>
            {t('On sale')}
          </FieldLabel>
          <Switch
            id={`enabled-${props.plan.id}`}
            checked={enabled}
            onCheckedChange={setEnabled}
            disabled={save.isPending}
          />
        </Field>
        <Button
          variant='outline'
          disabled={save.isPending || !validPrice}
          onClick={() => save.mutate(undefined)}
        >
          {t('Save')}
        </Button>
      </div>
      <SecureVerificationDialog {...verification.dialogProps} />
    </section>
  )
}
