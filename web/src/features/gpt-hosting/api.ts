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
import { isAxiosError } from 'axios'
import { t } from 'i18next'

import { api } from '@/lib/api'
import {
  getServerErrorMessage,
  requireServerSuccess,
} from '@/lib/server-error-message'

// 只有服务端约定的 -1 表示永久；0 或缺失值不能被当成永久权益。
export const HOSTING_PERMANENT_END = -1

export type HostingGrant = {
  seat_id?: string
  user_id: number
  starts_at: number
  ends_at: number
  enabled: boolean
  retention_days: number
  note: string
}
export type HostingSelf = {
  seats?: HostingSelf[]
  grant: HostingGrant | null
  valid?: boolean
  binding_id?: string
  binding: {
    seat_id?: string
    platform?: 'codex' | 'claude'
    state: string
    paused: boolean
    account_label: string
    plan: string
  } | null
}
export type HostingAdmin = {
  grants: HostingGrant[]
  instances: Array<{
    id: string
    name: string
    available: boolean
    slots: Array<{
      id: string
      platform?: 'codex' | 'claude'
      name: string
      empty: boolean
      exit_ready: boolean
      managed: boolean
      enabled: boolean
      state: string
    }>
  }>
}
export type HostingLogin = {
  platform?: string
  auth_url: string
  session_id: string
  expires_at: string
}

/** 只读取会话对应的安全视图，调用方不能拼接任意管理接口。 */
export async function getHosting<T extends HostingSelf | HostingAdmin>(
  view: 'self' | 'admin',
  signal?: AbortSignal,
  seatId?: string
): Promise<T> {
  const response = await api.get(`/api/gpt-hosting/${view}`, {
    signal,
    params: seatId ? { seat_id: seatId } : undefined,
  })
  return requireServerSuccess(response.data).data
}

/** 固定错误码映射成可操作提示，不传播含授权码的 Axios 请求配置或 cause。 */
function hostingMessage(error: unknown): string {
  const code = isAxiosError(error) ? error.response?.data?.code : ''
  switch (code) {
    case 'slot_unavailable':
      return t(
        'No prepared slot is available. Please contact the administrator.'
      )
    case 'account_changed':
      return t(
        'Sign in to the same account, or unbind it before changing accounts.'
      )
    case 'hosting_conflict':
      return t(
        'The account or slot is already reserved, or its state has changed. Refresh and try again.'
      )
    case 'hosting_expired':
      return t(
        'Your hosting entitlement is not active. Contact the administrator to renew it.'
      )
    case 'oauth_session_expired':
      return t('Authorization has expired. Start a new sign-in.')
    case 'oauth_session_mismatch':
    case 'invalid_oauth_callback':
      return t(
        'This callback does not match the current authorization. Copy the complete callback URL.'
      )
    case 'personal_account_required':
      return t(
        'Only personal GPT or Claude accounts with supported subscriptions can be hosted.'
      )
    case 'identity_invalid':
    case 'identity_unavailable':
      return t(
        'The account sign-in could not be verified. Start a new sign-in.'
      )
    case 'cleanup_pending':
      return t(
        'Credential cleanup is still in progress. The slot remains reserved.'
      )
    default:
      return getServerErrorMessage(error)
  }
}

/** 敏感提交只在当前调用中保留 proof 和回调；自动重试必须由业务状态恢复接口处理。 */
export async function postHosting<T = HostingSelf>(
  action:
    | 'login'
    | 'finish'
    | 'retry'
    | 'pause'
    | 'unbind'
    | 'admin/grants'
    | 'admin/inventory'
    | 'admin/takeover/check'
    | 'admin/takeover',
  body: unknown,
  proof?: string,
  seatId?: string
): Promise<T> {
  try {
    const response = await api.post(`/api/gpt-hosting/${action}`, body, {
      params: seatId ? { seat_id: seatId } : undefined,
      headers: proof ? { 'X-Security-Proof': proof } : undefined,
      timeout: 90_000,
      singleUseAuthorization: !!proof,
    })
    const result = requireServerSuccess(response.data)
    return { ...result.data, sync_pending: response.data.sync_pending === true }
  } catch (error) {
    throw new Error(hostingMessage(error))
  }
}
