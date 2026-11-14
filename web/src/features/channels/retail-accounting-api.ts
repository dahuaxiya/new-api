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
import { api } from '@/lib/api'
import { requireServerSuccess } from '@/lib/server-error-message'

export type RetailCharge = {
  id: string
  user_id: number
  token_id: number
  reserved: number
  actual: number
  status: string
  review_reason: string
  updated_at: number
}
export type RetailAttempt = {
  id: number
  request_id: string
  channel_id: number
  state: string
  status_code: number
  cost_state: string
}
export type RetailCost = {
  id: number
  reference: string
  kind: string
  channel_id: number
  currency: string
  amount_minor: number
  period: string
}
export type AccountingCursor = {
  charge_before?: number
  charge_id?: string
  attempt_before?: number
  cost_before?: number
}
export type RetailAccountingData = {
  charges: RetailCharge[]
  attempts: RetailAttempt[]
  costs: RetailCost[]
  next: AccountingCursor
}

/** 只读取 root 账务接口；取消信号随会话或查询变化传递，业务失败不能当作空账单。 */
export async function getRetailAccounting(
  params: AccountingCursor & { request_id?: string },
  signal: AbortSignal
): Promise<RetailAccountingData> {
  const response = await api.get<{
    success: boolean
    data: RetailAccountingData
  }>('/api/retail/admin/accounting', { params, signal })
  return requireServerSuccess(response.data).data
}

/** 原样提交账本预留金额供服务端做并发核对；不得本地推断退款成功或修改余额缓存。 */
export async function releaseRetailReservation(
  charge: RetailCharge,
  reason: string
) {
  const response = await api.post(
    `/api/retail/admin/charges/${encodeURIComponent(charge.id)}/refund`,
    { expected_reserved: charge.reserved, reason }
  )
  return requireServerSuccess(response.data)
}
