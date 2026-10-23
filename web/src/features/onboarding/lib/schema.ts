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

import { parseQuotaFromDollars } from '@/lib/format'
import { accountPasswordSchema } from '@/lib/password-policy'

export const onboardingSchema = z.object({
  username: z
    .string()
    .trim()
    .min(1, 'Username is required')
    .max(20, 'Username must not exceed 20 characters')
    .regex(
      /^[A-Za-z0-9_-]+$/,
      'Use letters, numbers, underscores or hyphens for the username.'
    ),
  display_name: z
    .string()
    .trim()
    .refine(
      (value) => [...value].length <= 20,
      'Display name must not exceed 20 characters'
    ),
  password: accountPasswordSchema.refine(
    (value) => [...value].length >= 15,
    'Use at least 15 characters for the initial password.'
  ),
  quota_dollars: z
    .number()
    .finite()
    .min(0, 'Initial quota must be zero or greater')
    .refine((value) => {
      const quota = parseQuotaFromDollars(value)
      return Number.isSafeInteger(quota) && quota <= 2_000_000_000
    }, 'Initial quota exceeds the supported limit.'),
  instance_id: z.string().min(1, 'Select an instance'),
  slot_id: z.string().min(1, 'Select an account slot'),
})

export type OnboardingFormValues = z.infer<typeof onboardingSchema>

export const ONBOARDING_DEFAULTS: OnboardingFormValues = {
  username: '',
  display_name: '',
  password: '',
  quota_dollars: 0,
  instance_id: '',
  slot_id: '',
}

export const createSlotSchema = z.object({
  name: z
    .string()
    .trim()
    .min(1, 'Slot name is required')
    .refine(
      (value) => [...value].length <= 60,
      'Slot name must not exceed 60 characters'
    ),
  exit_id: z.string().min(1, 'Select an outbound connection'),
})
export type CreateSlotFormValues = z.infer<typeof createSlotSchema>

/**
 * 仅允许打开官方授权站点，拒绝后端异常数据中的脚本或其他来源链接。
 * @param value 后端返回的完整授权 URL。
 * @return 是否可以安全提供为官方授权链接。
 */
export function isOfficialAuthorizationURL(value: string): boolean {
  try {
    let url = new URL(value)
    if (url.protocol !== 'https:' || url.username || url.password || url.hash) {
      return false
    }
    if (url.host === 'chatgpt.com' && url.pathname === '/codex/desktop-auth') {
      const nested = url.searchParams.getAll('authorize_url')
      if (nested.length !== 1) return false
      url = new URL(nested[0])
    }
    return (
      url.protocol === 'https:' &&
      url.host === 'auth.openai.com' &&
      url.pathname === '/oauth/authorize' &&
      !url.username &&
      !url.password &&
      !url.hash
    )
  } catch {
    return false
  }
}
