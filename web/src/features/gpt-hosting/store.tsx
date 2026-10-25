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
import { useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Dialog } from '@/components/dialog'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { Field, FieldLabel } from '@/components/ui/field'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { RadioGroup, RadioGroupItem } from '@/components/ui/radio-group'
import { submitPaymentForm } from '@/features/wallet/lib/payment'
import { handleServerError } from '@/lib/handle-server-error'

import {
  getHostingStore,
  postHosting,
  HOSTING_PERMANENT_END,
  type HostingCheckout,
  type HostingSelf,
  type HostingStore as StoreData,
} from './api'
import { HostingOrderList } from './store-orders'

/** 金额明确按人民币显示，不能继承站内用量的美元或自定义币种。 */
function hostingPrice(cents: number): string {
  return `¥${(cents / 100).toFixed(2).replace(/\.00$/, '')}`
}

/** 购买和续费放在托管账号页；弹窗每次打开重新生成请求标识，网络重试复用同一标识。 */
export function HostingStore(props: {
  identity: string
  seat?: HostingSelf
  platform: 'codex' | 'claude'
}) {
  const { t } = useTranslation()
  const [purchase, setPurchase] = useState<'new' | 'renew'>()
  const query = useQuery({
    queryKey: ['hosting-store', props.identity],
    queryFn: ({ signal }) => getHostingStore<StoreData>('store', 1, signal),
  })
  const canRenew =
    props.seat?.grant?.enabled &&
    props.seat.grant.ends_at !== HOSTING_PERMANENT_END &&
    !props.seat.binding?.paused &&
    !['cleaning', 'takeover'].includes(props.seat.binding?.state || '')
  return (
    <Card>
      <CardHeader>
        <div className='flex flex-wrap items-center justify-between gap-3'>
          <CardTitle>{t('Hosting plans')}</CardTitle>
          <div className='flex flex-wrap gap-2'>
            {canRenew && (
              <Button variant='outline' onClick={() => setPurchase('renew')}>
                {t('Renew this seat')}
              </Button>
            )}
            <Button onClick={() => setPurchase('new')}>
              {t('Buy a hosting seat')}
            </Button>
          </div>
        </div>
        <CardDescription>
          {t(
            'Each purchase covers one hosting seat. Your GPT or Claude subscription is purchased separately.'
          )}
        </CardDescription>
      </CardHeader>
      <CardContent className='space-y-4'>
        {query.isPending && <LoadingState inline />}
        {query.isError && <ErrorState onRetry={() => query.refetch()} />}
        {query.data && (
          <>
            <div className='grid gap-3 sm:grid-cols-3'>
              {query.data.plans.map((plan) => (
                <div key={plan.id} className='rounded-xl border p-4'>
                  <p className='text-muted-foreground text-sm'>
                    {t('{{days}} days per seat', { days: plan.days })}
                  </p>
                  <p className='mt-2 text-2xl font-semibold tabular-nums'>
                    {hostingPrice(plan.price_cents)}
                  </p>
                  {!plan.enabled && (
                    <p className='text-muted-foreground mt-2 text-xs'>
                      {t('Not on sale')}
                    </p>
                  )}
                </div>
              ))}
            </div>
            {!query.data.payment_ready && (
              <Alert>
                <AlertDescription>
                  {t('Hosting payments are not available yet.')}
                </AlertDescription>
              </Alert>
            )}
          </>
        )}
        <HostingOrderList
          identity={props.identity}
          paymentReady={query.data?.payment_ready === true}
        />
        {purchase && (
          <HostingPurchase
            key={`${purchase}:${props.seat?.grant?.seat_id}`}
            identity={props.identity}
            store={query.data}
            seat={purchase === 'renew' ? props.seat : undefined}
            platform={props.platform}
            onClose={() => setPurchase(undefined)}
          />
        )}
      </CardContent>
    </Card>
  )
}

/** 下单只发送产品标识；金额、时长、归属及库存由后端验证，浏览器返回不能视为付款成功。 */
function HostingPurchase(props: {
  identity: string
  store?: StoreData
  seat?: HostingSelf
  platform: 'codex' | 'claude'
  onClose: () => void
}) {
  const { t } = useTranslation()
  const cache = useQueryClient()
  const [plan, setPlan] = useState(
    props.store?.plans.find((p) => p.enabled)?.id || 'monthly'
  )
  const [platform, setPlatform] = useState(
    props.seat?.binding?.platform || props.platform
  )
  const [method, setMethod] = useState(props.store?.methods[0] || 'wxpay')
  const request = useRef({ signature: '', id: '' })
  const selected = props.store?.plans.find((p) => p.id === plan)
  const checkout = useMutation({
    mutationFn: async () => {
      const signature = [
        plan,
        platform,
        method,
        props.seat?.grant?.seat_id,
      ].join(':')
      if (signature !== request.current.signature) {
        request.current = { signature, id: crypto.randomUUID() }
      }
      const result = await postHosting<HostingCheckout>('payment', {
        request_id: request.current.id,
        plan_id: plan,
        platform,
        method,
        seat_id: props.seat?.grant?.seat_id || '',
      })
      // 支付表单只能去服务端配置的 HTTPS 网关，防止误配置降级或脚本 URL。
      const destination = new URL(result.url)
      if (
        destination.protocol !== 'https:' ||
        destination.username ||
        destination.password
      ) {
        throw new Error(t('Invalid payment address'))
      }
      submitPaymentForm(result.url, result.params)
      props.onClose()
    },
    retry: false,
    gcTime: 0,
    onError: handleServerError,
    onSettled: () =>
      cache.invalidateQueries({ queryKey: ['hosting-orders', props.identity] }),
  })
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !checkout.isPending) props.onClose()
      }}
      title={t(props.seat ? 'Renew this seat' : 'Buy a hosting seat')}
      description={t(
        'Each purchase covers one hosting seat. Your GPT or Claude subscription is purchased separately.'
      )}
      footer={
        <Button
          disabled={
            checkout.isPending ||
            !props.store?.payment_ready ||
            !selected?.enabled ||
            !props.store.methods.includes(method)
          }
          onClick={() => checkout.mutate(undefined)}
        >
          {t('Continue to payment')} ·{' '}
          {hostingPrice(selected?.price_cents || 0)}
        </Button>
      }
    >
      <div className='space-y-5'>
        <RadioGroup
          value={plan}
          onValueChange={setPlan}
          aria-label={t('Hosting plans')}
          className='grid gap-3 sm:grid-cols-3'
          disabled={checkout.isPending}
        >
          {props.store?.plans.map((p) => (
            <FieldLabel
              key={p.id}
              htmlFor={`purchase-${p.id}`}
              className='flex cursor-pointer flex-col items-start gap-3 rounded-xl border p-4'
            >
              <RadioGroupItem
                id={`purchase-${p.id}`}
                value={p.id}
                disabled={!p.enabled}
              />
              <span>{t('{{days}} days per seat', { days: p.days })}</span>
              <span className='text-xl font-semibold'>
                {hostingPrice(p.price_cents)}
              </span>
            </FieldLabel>
          ))}
        </RadioGroup>
        <Field>
          <FieldLabel htmlFor='purchase-platform'>
            {t('Account platform')}
          </FieldLabel>
          <NativeSelect
            id='purchase-platform'
            value={platform}
            disabled={
              checkout.isPending ||
              (!!props.seat?.binding && props.seat.binding.state !== 'unbound')
            }
            onChange={(e) => setPlatform(e.target.value as 'codex' | 'claude')}
          >
            <NativeSelectOption value='codex'>GPT</NativeSelectOption>
            <NativeSelectOption value='claude'>Claude</NativeSelectOption>
          </NativeSelect>
        </Field>
        <Field>
          <FieldLabel htmlFor='purchase-method'>
            {t('Payment method')}
          </FieldLabel>
          <NativeSelect
            id='purchase-method'
            value={method}
            disabled={checkout.isPending || !props.store?.payment_ready}
            onChange={(e) => setMethod(e.target.value as 'wxpay' | 'alipay')}
          >
            {(props.store?.methods.length
              ? props.store.methods
              : ['wxpay', 'alipay']
            ).map((m) => (
              <NativeSelectOption key={m} value={m}>
                {t(m === 'wxpay' ? 'WeChat Pay' : 'Alipay')}
              </NativeSelectOption>
            ))}
          </NativeSelect>
        </Field>
        <p className='text-muted-foreground text-sm'>
          {t(
            'Renewal extends an active seat from its expiry date. An expired seat starts again when payment is received. Permanent seats do not need renewal.'
          )}
        </p>
        <p className='text-muted-foreground text-sm'>
          {t(
            'Slots are checked before checkout and reserved for 15 minutes. Returning from payment does not confirm receipt; check your order status.'
          )}
        </p>
        {!props.store?.payment_ready && (
          <Alert>
            <AlertDescription>
              {t('Hosting payments are not available yet.')}
            </AlertDescription>
          </Alert>
        )}
      </div>
    </Dialog>
  )
}
