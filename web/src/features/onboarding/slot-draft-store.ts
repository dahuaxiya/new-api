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
import { z } from 'zod'
import { create } from 'zustand'

import type { CreateSlotRequest } from './types'

const draftSchema = z.object({
  request_id: z.uuid(),
  instance_id: z.string().regex(/^[A-Za-z0-9_-]{1,80}$/),
  name: z
    .string()
    .min(1)
    .refine((value) => [...value].length <= 60),
  exit_id: z.string().regex(/^[A-Za-z0-9_-]{1,80}$/),
})

interface SlotDraftStore {
  readDraft: (
    actorId: number,
    instanceId: string
  ) => CreateSlotRequest | undefined
  saveDraft: (actorId: number, draft: CreateSlotRequest) => void
  removeDraft: (actorId: number, instanceId: string) => void
}

/**
 * 按操作者和实例隔离草稿，避免同一浏览器切账号后恢复其他管理员的创建请求。
 * @param actorId 当前已登录管理员 ID。
 * @param instanceId 已配置实例的固定标识。
 * @return 仅用于非敏感建槽草稿的会话存储键。
 */
function draftKey(actorId: number, instanceId: string): string {
  if (
    !Number.isInteger(actorId) ||
    actorId <= 0 ||
    !/^[A-Za-z0-9_-]{1,80}$/.test(instanceId)
  ) {
    throw new Error(
      'The slot draft cannot be saved or restored. Do not create another slot until this is resolved.'
    )
  }
  return `new-api:onboarding-slot:${actorId}:${instanceId}`
}

// 仅持久化建槽所需的非敏感元数据，所有浏览器存储访问集中在此 store。
// 密码、OAuth 会话、授权链接和回调不在 schema 中，保存时会被剔除。
export const useSlotDraftStore = create<SlotDraftStore>(() => ({
  /**
   * 读取并验证未完成草稿；格式损坏时抛错，禁止静默换编号再次创建。
   * @param actorId 当前操作者 ID。
   * @param instanceId 当前实例标识。
   * @return 原创建请求；未保存过草稿时为空。
   */
  readDraft(actorId, instanceId) {
    const raw = sessionStorage.getItem(draftKey(actorId, instanceId))
    if (!raw) return undefined
    const draft = draftSchema.parse(JSON.parse(raw))
    if (draft.instance_id !== instanceId) throw new Error('Invalid slot draft')
    return draft
  },
  /**
   * 先写草稿再发网络请求；写入失败时调用方必须停止创建，以保留安全重试能力。
   * @param actorId 当前操作者 ID。
   * @param draft 校验后的原请求元数据。
   */
  saveDraft(actorId, draft) {
    const safeDraft = draftSchema.parse(draft)
    sessionStorage.setItem(
      draftKey(actorId, draft.instance_id),
      JSON.stringify(safeDraft)
    )
  },
  /**
   * 仅在后端确认槽位创建成功后清除草稿。
   * @param actorId 发起本次创建的操作者 ID。
   * @param instanceId 本次创建所在实例。
   */
  removeDraft(actorId, instanceId) {
    sessionStorage.removeItem(draftKey(actorId, instanceId))
  },
}))
