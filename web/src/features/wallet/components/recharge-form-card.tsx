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
import { Check, ExternalLink, Gift, Loader2, Wallet } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { IconBadge } from '@/components/ui/icon-badge'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from '@/components/ui/tooltip'

import { getMinTopupAmount, getPaymentIcon } from '../lib'
import type {
  PaymentMethod,
  PresetAmount,
  TopupInfo,
  CreemProduct,
  WaffoPayMethod,
} from '../types'
import { CreemProductsSection } from './creem-products-section'

interface RechargeFormCardProps {
  topupInfo: TopupInfo | null
  presetAmounts: PresetAmount[]
  selectedPreset: number | null
  onSelectPreset: (preset: PresetAmount) => void
  topupAmount: number
  onTopupAmountChange: (amount: number) => void
  paymentAmount: number
  calculating: boolean
  onPaymentMethodSelect: (method: PaymentMethod) => void
  paymentLoading: string | null
  redemptionCode: string
  onRedemptionCodeChange: (code: string) => void
  onRedeem: () => void
  redeeming: boolean
  topupLink?: string
  loading?: boolean
  balance: number | null
  method?: PaymentMethod
  onChooseMethod: (method: PaymentMethod) => void
  creemProducts?: CreemProduct[]
  enableCreemTopup?: boolean
  onCreemProductSelect?: (product: CreemProduct) => void
  enableWaffoTopup?: boolean
  waffoPayMethods?: WaffoPayMethod[]
  waffoMinTopup?: number
  onWaffoMethodSelect?: (method: WaffoPayMethod, index: number) => void
  enableWaffoPancakeTopup?: boolean
}

const yuan = new Intl.NumberFormat('zh-CN', {
  style: 'currency',
  currency: 'CNY',
  minimumFractionDigits: 2,
})

/**
 * 零售钱包按后台开放的方式收款。左侧编辑金额，右侧使用服务端报价。
 * @param props 余额为人民币，缺失时显示占位；付款操作沿用原有渠道确认流程。
 * @return 可在窄屏按阅读顺序堆叠的充值表单。
 */
export function RechargeFormCard(props: RechargeFormCardProps) {
  const { t } = useTranslation()
  const {
    topupInfo,
    topupAmount,
    paymentLoading,
    waffoPayMethods,
    waffoMinTopup,
    onWaffoMethodSelect,
    enableWaffoTopup,
    enableCreemTopup,
    creemProducts,
    onCreemProductSelect,
    redemptionCode,
    onRedemptionCodeChange,
    redeeming,
    onRedeem,
    topupLink,
  } = props
  const redemptionEnabled = topupInfo?.enable_redemption !== false
  const hasWaffoPaymentMethods = Boolean(waffoPayMethods?.length)
  const minimum = Math.max(
    1,
    getMinTopupAmount(topupInfo) || 1,
    props.method?.min_topup || 0
  )
  const valid = Number.isSafeInteger(topupAmount) && topupAmount >= minimum
  const ready =
    valid &&
    Number.isFinite(props.paymentAmount) &&
    props.paymentAmount > 0 &&
    !props.calculating &&
    !paymentLoading
  const methods = topupInfo?.pay_methods ?? []
  const hasOnline =
    topupInfo &&
    (topupInfo.enable_online_topup ||
      topupInfo.enable_alipay_official_topup ||
      topupInfo.enable_stripe_topup ||
      enableWaffoTopup ||
      props.enableWaffoPancakeTopup)
  const isAlipay =
    props.method?.type === 'alipay_official' || props.method?.type === 'alipay'
  return (
    <>
      <div className='wallet-grid'>
        <section className='wallet-panel'>
          <div className='wallet-balance'>
            <div>
              <h2 className='text-sm font-medium'>{t('Current Balance')}</h2>
              <strong className='text-2xl font-medium tracking-tight tabular-nums sm:text-3xl'>
                {props.balance === null ? '—' : yuan.format(props.balance)}
              </strong>
            </div>
            <Wallet aria-hidden='true' />
          </div>
          <div className='wallet-form'>
            {props.loading ? (
              <p role='status'>{t('Loading...')}</p>
            ) : hasOnline ? (
              <>
                <fieldset>
                  <legend className='text-sm font-medium'>{t('Topup Amount')}</legend>
                  <div className='wallet-presets'>
                    {props.presetAmounts.map((preset) => (
                      <Button
                        key={preset.value}
                        variant='outline'
                        aria-pressed={topupAmount === preset.value}
                        disabled={
                          Boolean(paymentLoading) || preset.value < minimum
                        }
                        onClick={() => props.onSelectPreset(preset)}
                      >
                        ¥ {preset.value}
                      </Button>
                    ))}
                  </div>
                </fieldset>
                <div>
                  <div className='wallet-field-label'>
                    <Label htmlFor='topup-amount'>{t('Custom Amount')}</Label>
                    <span className='text-xs'>
                      {t('Minimum {{amount}}', { amount: minimum })} ·{' '}
                      {t('Enter a whole number')}
                    </span>
                  </div>
                  <div className='wallet-amount'>
                    <span aria-hidden='true'>¥</span>
                    <Input
                      id='topup-amount'
                      type='number'
                      inputMode='numeric'
                      min={minimum}
                      step={1}
                      value={topupAmount || ''}
                      disabled={Boolean(paymentLoading)}
                      aria-invalid={Boolean(topupAmount) && !valid}
                      onChange={(e) =>
                        props.onTopupAmountChange(Number(e.target.value))
                      }
                    />
                  </div>
                </div>
                <fieldset>
                  <legend className='text-sm font-medium'>{t('Payment Method')}</legend>
                  <div className='wallet-methods'>
                    {methods.map((method) => (
                      <Button
                        key={method.type}
                        variant='ghost'
                        aria-pressed={method.type === props.method?.type}
                        disabled={Boolean(paymentLoading)}
                        onClick={() => props.onChooseMethod(method)}
                      >
                        {getPaymentIcon(
                          method.type === 'alipay_official'
                            ? 'alipay'
                            : method.type,
                          'size-7',
                          method.icon,
                          method.name
                        )}
                        <span>{t(method.name)}</span>
                        {method.type === props.method?.type && (
                          <Check
                            aria-hidden='true'
                            className='ms-auto size-4'
                          />
                        )}
                      </Button>
                    ))}
                  </div>
                </fieldset>
                {enableWaffoTopup &&
                  hasWaffoPaymentMethods &&
                  onWaffoMethodSelect && (
                    <div className='space-y-2.5 sm:space-y-3'>
                      <Label className='text-muted-foreground text-xs font-medium tracking-wider uppercase'>
                        {t('Waffo Payment')}
                      </Label>
                      <div className='grid grid-cols-2 gap-1.5 sm:gap-3 lg:grid-cols-3'>
                        {waffoPayMethods?.map((method, index) => {
                          const loadingKey = `waffo-${index}`
                          const methodKey = `${method.payMethodType ?? 'unknown'}-${method.payMethodName ?? method.name}`
                          const waffoMin = waffoMinTopup || 0
                          const belowMin = waffoMin > topupAmount
                          const disabledReason = belowMin
                            ? t('Minimum topup amount: {{amount}}', {
                                amount: waffoMin,
                              })
                            : undefined
                          const disabledLabel = belowMin
                            ? `${t('Minimum:')} ${waffoMin}`
                            : undefined

                          let methodIcon = getPaymentIcon('waffo')
                          if (paymentLoading === loadingKey) {
                            methodIcon = (
                              <Loader2 className='h-4 w-4 animate-spin' />
                            )
                          } else if (method.icon) {
                            methodIcon = (
                              <img
                                src={method.icon}
                                alt={method.name}
                                className='h-4 w-4 object-contain'
                              />
                            )
                          }

                          const button = (
                            <Button
                              key={methodKey}
                              variant='outline'
                              onClick={() => onWaffoMethodSelect(method, index)}
                              disabled={belowMin || !!paymentLoading}
                              title={disabledReason}
                              aria-label={
                                disabledReason
                                  ? `${method.name}. ${disabledReason}`
                                  : method.name
                              }
                              className='min-h-14 min-w-0 justify-start gap-2 rounded-lg px-3 py-2 text-left'
                            >
                              {methodIcon}
                              <span className='flex min-w-0 flex-col items-start gap-0.5'>
                                <span className='max-w-full truncate'>
                                  {method.name}
                                </span>
                                {disabledLabel && (
                                  <span className='text-muted-foreground max-w-full truncate text-[11px] leading-4 font-normal'>
                                    {disabledLabel}
                                  </span>
                                )}
                              </span>
                            </Button>
                          )

                          return belowMin ? (
                            <TooltipProvider key={methodKey}>
                              <Tooltip>
                                <TooltipTrigger render={button} />
                                <TooltipContent>
                                  {disabledReason}
                                </TooltipContent>
                              </Tooltip>
                            </TooltipProvider>
                          ) : (
                            button
                          )
                        })}
                      </div>
                    </div>
                  )}

                {!methods.length && !hasWaffoPaymentMethods && (
                  <p>
                    {t(
                      'No payment methods available. Please contact administrator.'
                    )}
                  </p>
                )}
              </>
            ) : (
              <p>
                {t(
                  'Online topup is not enabled. Please use redemption code or contact administrator.'
                )}
              </p>
            )}
          </div>
        </section>
        <section className='wallet-panel wallet-summary'>
          <h2 className='text-sm font-semibold'>{t('Payment details')}</h2>
          <dl className='text-sm'>
            <div>
              <dt>{t('Amount credited')}</dt>
              <dd className='font-medium tabular-nums'>{valid ? yuan.format(topupAmount) : '—'}</dd>
            </div>
            <div>
              <dt>{t('Balance after top-up')}</dt>
              <dd className='font-medium tabular-nums'>
                {props.balance !== null && valid
                  ? yuan.format(props.balance + topupAmount)
                  : '—'}
              </dd>
            </div>
            <div className='wallet-due'>
              <dt>{t('Amount Due')}</dt>
              <dd className='text-xl font-medium tracking-tight tabular-nums sm:text-2xl' aria-live='polite'>
                {ready ? yuan.format(props.paymentAmount) : '—'}
              </dd>
            </div>
          </dl>
          <Button
            className='wallet-pay'
            disabled={!hasOnline || !props.method || !ready}
            onClick={() =>
              props.method && props.onPaymentMethodSelect(props.method)
            }
          >
            {isAlipay ? t('Pay with Alipay') : t('Confirm Payment')}
          </Button>
        </section>
      </div>
      {/* Creem Products Section */}
      {enableCreemTopup &&
        Array.isArray(creemProducts) &&
        creemProducts.length > 0 &&
        onCreemProductSelect && (
          <div className='space-y-2.5 border-t pt-4 sm:space-y-3 sm:pt-6'>
            <Label className='text-muted-foreground text-xs font-medium tracking-wider uppercase'>
              {t('Creem Payment')}
            </Label>
            <CreemProductsSection
              products={creemProducts}
              onProductSelect={onCreemProductSelect}
            />
          </div>
        )}

      {/* Redemption Code Section */}
      {redemptionEnabled ? (
        <details className='wallet-code text-sm'>
          <summary>{t('Redemption Code')}</summary>
          <div className='space-y-2.5 border-t pt-4 sm:space-y-3 sm:pt-6'>
            <div className='flex items-center gap-2'>
              <IconBadge tone='warning' size='xs'>
                <Gift />
              </IconBadge>
              <Label
                htmlFor='redemption-code'
                className='text-muted-foreground text-xs font-medium tracking-wider uppercase'
              >
                {t('Have a Code?')}
              </Label>
            </div>
            <div className='grid grid-cols-[minmax(0,1fr)_auto] gap-2'>
              <Input
                id='redemption-code'
                value={redemptionCode}
                onChange={(e) => onRedemptionCodeChange(e.target.value)}
                placeholder={t('Enter your redemption code')}
                className='h-9 min-w-0'
              />
              <Button
                onClick={onRedeem}
                disabled={redeeming}
                variant='outline'
                className='h-9 px-4'
              >
                {redeeming && <Loader2 className='mr-2 h-4 w-4 animate-spin' />}
                {t('Redeem')}
              </Button>
            </div>
            {topupLink && (
              <p className='text-muted-foreground text-xs'>
                {t('Need a redemption code?')}{' '}
                <a
                  href={topupLink}
                  target='_blank'
                  rel='noopener noreferrer'
                  className='inline-flex items-center gap-1 underline-offset-4 hover:underline'
                >
                  {t('Get one here')}
                  <ExternalLink className='h-3 w-3' />
                </a>
              </p>
            )}
          </div>
        </details>
      ) : (
        <Alert className='border-t'>
          <AlertDescription>
            {t(
              'Redemption codes are disabled until the administrator confirms compliance terms.'
            )}
          </AlertDescription>
        </Alert>
      )}
    </>
  )
}
