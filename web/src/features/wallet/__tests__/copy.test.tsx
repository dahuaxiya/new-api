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
import { act, render, screen } from '@testing-library/react'
import { createInstance } from 'i18next'
import { I18nextProvider, initReactI18next } from 'react-i18next'
import { beforeEach, expect, it, vi } from 'vitest'

import { BillingHistoryDialog } from '@/features/wallet/components/dialogs/billing-history-dialog'
import en from '@/i18n/locales/en.json'
import zh from '@/i18n/locales/zh.json'
import { api } from '@/lib/api'

import { RechargeFormCard } from '../components/recharge-form-card'

const i18n = createInstance()

beforeEach(async () => {
  await i18n.use(initReactI18next).init({
    lng: 'zh',
    fallbackLng: false,
    nsSeparator: false,
    resources: { en, zh },
    interpolation: { escapeValue: false },
  })
})

it('translates billing history statuses and updates them when the language changes', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({
    data: {
      success: true,
      data: {
        total: 3,
        items: ['success', 'pending', 'expired'].map((status, id) => ({
          id,
          status,
          amount: 10,
          money: 10,
          trade_no: `order-${id}`,
          payment_method: 'stripe',
          create_time: 1,
        })),
      },
    },
  })
  render(
    <I18nextProvider i18n={i18n}>
      <BillingHistoryDialog open onOpenChange={vi.fn()} />
    </I18nextProvider>
  )
  await screen.findByText('order-0')
  for (const label of ['成功', '待确认', '已过期']) {
    expect(screen.getByText(label)).toBeVisible()
  }
  await act(() => i18n.changeLanguage('en'))
  for (const label of ['Success', 'Pending', 'Expired']) {
    expect(screen.getByText(label)).toBeVisible()
  }
})

/** 折扣只改变实付金额。切换界面语言后，到账和预计余额仍使用同一人民币金额。 */
it('keeps server quotes separate from wallet credits across languages', async () => {
  const method = { type: 'alipay_official', name: 'Alipay' }
  render(
    <I18nextProvider i18n={i18n}>
      <RechargeFormCard
        topupInfo={{
          enable_online_topup: false,
          enable_alipay_official_topup: true,
          enable_stripe_topup: false,
          pay_methods: [method],
          min_topup: 1,
          stripe_min_topup: 1,
          amount_options: [5, 10, 20, 50],
          discount: { 20: 0.8 },
        }}
        presetAmounts={[{ value: 20, discount: 0.8 }]}
        selectedPreset={20}
        topupAmount={20}
        balance={28.4}
        method={method}
        onChooseMethod={vi.fn()}
        paymentAmount={16}
        calculating={false}
        paymentLoading={null}
        redemptionCode=''
        redeeming={false}
        onSelectPreset={vi.fn()}
        onTopupAmountChange={vi.fn()}
        onPaymentMethodSelect={vi.fn()}
        onRedemptionCodeChange={vi.fn()}
        onRedeem={vi.fn()}
      />
    </I18nextProvider>
  )
  expect(screen.getByText('本次到账').nextElementSibling).toHaveTextContent(
    '20.00'
  )
  expect(screen.getByText('充值后余额').nextElementSibling).toHaveTextContent(
    '48.40'
  )
  expect(screen.getByText('应付金额').nextElementSibling).toHaveTextContent(
    '16.00'
  )
  expect(screen.getByRole('button', { name: '支付宝付款' })).toBeEnabled()
  await act(() => i18n.changeLanguage('en'))
  expect(
    screen.getByText('Balance after top-up').nextElementSibling
  ).toHaveTextContent('48.40')
})
