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

import { quotaSchema } from '@/features/gpt-quota/schema'
import { api } from '@/lib/api'
import { requireServerSuccess } from '@/lib/server-error-message'

export const hostingSummarySchema = z.object({
  user_id: z.number(),
  status: z.string(),
  source: z.string().optional(),
  instance_id: z.string().optional(),
  instance_name: z.string().optional(),
  slot_id: z.string().optional(),
  grant: z
    .object({
      user_id: z.number(),
      starts_at: z.number(),
      ends_at: z.number(),
      enabled: z.boolean(),
      retention_days: z.number(),
      note: z.string(),
    })
    .nullable()
    .optional(),
  binding: z
    .object({
      state: z.string(),
      account_label: z.string(),
      plan: z.string(),
      paused: z.boolean(),
    })
    .nullable()
    .optional(),
  quota: quotaSchema.nullable().optional(),
  quota_unavailable: z.boolean().optional(),
})
export type HostingSummary = z.infer<typeof hostingSummarySchema>

/** 仅查询当前分页的服务端摘要；不会触发官方额度探测。 */
export async function getHostingSummaries(
  ids: number[],
  signal?: AbortSignal
): Promise<HostingSummary[]> {
  if (!ids.length) return []
  const res = await api.get('/api/gpt-hosting/users', {
    params: { ids: ids.join(',') },
    signal,
    timeout: 12_000,
  })
  return z
    .array(hostingSummarySchema)
    .parse(requireServerSuccess(res.data).data)
}

/** 时间输入使用设备时区，传给后端前转换为 Unix 秒。 */
export function hostingLocalDate(seconds: number): string {
  const d = new Date(seconds * 1000)
  return new Date(d.getTime() - d.getTimezoneOffset() * 60000)
    .toISOString()
    .slice(0, 16)
}

/** 续期只预填表单；到期后从现在计算，尚未到期从原到期时间计算。 */
export function renewedHostingEnd(
  end: string,
  days: number,
  now = Date.now()
): string {
  return hostingLocalDate(
    (Math.max(now, Date.parse(end) || 0) + days * 86400000) / 1000
  )
}

export const hostingStatusLabels: Record<string, string> = {
  unopened: 'Hosting not enabled',
  undated: 'Hosting term not set',
  active: 'Hosting active',
  expiring: 'Hosting expires soon',
  expired: 'Hosting expired',
  scheduled: 'Hosting scheduled',
  awaiting_login: 'Awaiting GPT sign-in',
  disabled: 'Hosting disabled',
  takeover: 'Takeover incomplete',
  conflict: 'Account binding needs review',
  unavailable: 'Hosting status unavailable',
  restricted: 'Not applicable',
}
