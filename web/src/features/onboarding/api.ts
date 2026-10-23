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
import {
  getServerErrorMessage,
  requireServerSuccess,
} from '@/lib/server-error-message'

import type {
  OnboardingOptions,
  OnboardingRequest,
  OnboardingResult,
  CreateSlotRequest,
  OnboardingSlot,
  SlotLoginSession,
} from './types'

/**
 * 读取管理员可用的实例和槽位；响应不包含上游凭据。
 * @return 已配置实例及其安全资源摘要。
 */
export async function getOnboardingOptions(): Promise<OnboardingOptions> {
  const response = await api.get('/api/vm2api/onboarding/options')
  return requireServerSuccess(response.data).data
}

/**
 * 提交一次开户事务。重试必须复用同一 request_id，防止网络超时产生重复用户。
 * @param request 已确认的开户资料，密码只在当前页面内存与 HTTPS 请求中流转。
 * @return 服务端持久化的开户状态，不含上游 Key。
 */
export async function openAccount(
  request: OnboardingRequest
): Promise<OnboardingResult> {
  try {
    const response = await api.post('/api/vm2api/onboarding', request)
    return requireServerSuccess(response.data).data
  } catch (error) {
    // Axios 错误包含携带密码的 request config，只传播安全消息，避免进入查询缓存。
    throw new Error(getServerErrorMessage(error))
  }
}

/**
 * 页面刷新或请求超时后，只用操作编号恢复结果，不在 URL 或浏览器存储中保存密码。
 * @param requestId 服务端开户操作的 UUID。
 * @return 不包含密码和上游凭据的操作状态。
 */
export async function getOnboardingResult(
  requestId: string
): Promise<OnboardingResult> {
  const response = await api.get(
    `/api/vm2api/onboarding/${encodeURIComponent(requestId)}`
  )
  return requireServerSuccess(response.data).data
}

/**
 * 恢复已持久化的开户事务，后端复用密码哈希，浏览器无需再次保存密码。
 * @param requestId 原开户操作编号，不能为重试另建编号。
 * @return 恢复后的非敏感开户状态。
 */
export async function retryOnboarding(
  requestId: string
): Promise<OnboardingResult> {
  const response = await api.post(
    `/api/vm2api/onboarding/${encodeURIComponent(requestId)}/retry`
  )
  return requireServerSuccess(response.data).data
}

/**
 * 幂等创建槽位。出口仅可引用管理员配置中的标识，不接受浏览器直接提交代理凭据。
 * @param request 原操作编号及目标实例、槽位名称、出口标识。
 * @return 新槽位的安全视图。
 */
export async function createOnboardingSlot(
  request: CreateSlotRequest
): Promise<OnboardingSlot> {
  const response = await api.post('/api/vm2api/onboarding/slots', request)
  return requireServerSuccess(response.data).data.slot
}

/**
 * 创建官方账号授权会话；只有操作者在官方页面完成登录后才能继续。
 * @param instanceId 已配置实例标识。
 * @param slotId 未被其他用户占用的槽位标识。
 * @return 官方授权地址及短期会话编号，调用方不得持久化。
 */
export async function startSlotLogin(
  instanceId: string,
  slotId: string
): Promise<SlotLoginSession> {
  const response = await api.post('/api/vm2api/onboarding/oauth/start', {
    instance_id: instanceId,
    slot_id: slotId,
  })
  return requireServerSuccess(response.data).data
}

/**
 * 将官方回调交给后端核验 state 并兑换授权；仅传播安全错误，不保留含授权码的请求配置。
 * @param instanceId 当前实例标识。
 * @param slotId 当前槽位标识。
 * @param sessionId 本次短期授权会话。
 * @param callbackUrl 操作者粘贴的完整回调地址。
 * @return 更新后的槽位状态，不返回账号令牌。
 */
export async function finishSlotLogin(
  instanceId: string,
  slotId: string,
  sessionId: string,
  callbackUrl: string
): Promise<OnboardingSlot> {
  try {
    const response = await api.post('/api/vm2api/onboarding/oauth/finish', {
      instance_id: instanceId,
      slot_id: slotId,
      session_id: sessionId,
      callback_url: callbackUrl,
    })
    return requireServerSuccess(response.data).data.slot
  } catch (error) {
    throw new Error(getServerErrorMessage(error))
  }
}
