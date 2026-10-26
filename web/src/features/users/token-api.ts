import { z } from 'zod'

import { api } from '@/lib/api'
import { requireServerSuccess } from '@/lib/server-error-message'

const adminTokenSchema = z.object({
  id: z.number(),
  name: z.string(),
  key: z.string(),
  status: z.number(),
  hosting_seat_id: z.string(),
  created_time: z.number(),
  accessed_time: z.number(),
  expired_time: z.number(),
})
export type AdminToken = z.infer<typeof adminTokenSchema>
export type AdminTokenAction = 'view' | 'copy' | 'enable' | 'disable'

/** 当前页的计数批量读取；缺失键代表无权访问，不能用零代替请求失败。 */
export async function getUserTokenCounts(ids: number[]) {
  if (!ids.length) return {}
  // 用户表允许大分页，拆成后端接受的有界批次，仍避免逐用户请求。
  const batches = []
  for (let i = 0; i < ids.length; i += 100) {
    batches.push(
      api.get('/api/user/token_counts', {
        params: { ids: ids.slice(i, i + 100).join(',') },
      })
    )
  }
  const results = await Promise.all(batches)
  return Object.assign(
    {},
    ...results.map((res) =>
      z
        .record(z.string(), z.number())
        .parse(requireServerSuccess(res.data).data)
    )
  ) as Record<string, number>
}

/** 仅掩码元数据进入查询缓存，明文接口不能调用此函数或放进 Query。 */
export async function getUserTokens(
  userId: number,
  page: number,
  keyword: string,
  status: string,
  signal?: AbortSignal
) {
  const res = await api.get(`/api/user/${userId}/tokens`, {
    params: { p: page, page_size: 10, keyword, status },
    signal,
  })
  return z
    .object({ items: z.array(adminTokenSchema), total: z.number() })
    .parse(requireServerSuccess(res.data).data)
}

/** 每个动作使用新的绑定证明；禁止登录刷新拦截器自动重放单次授权。 */
export async function manageUserToken(
  userId: number,
  tokenId: number,
  action: AdminTokenAction,
  proof: string,
  signal?: AbortSignal
) {
  const reveal = action === 'view' || action === 'copy'
  const res = await api.request({
    url: `/api/user/${userId}/tokens/${tokenId}/${reveal ? 'key' : 'status'}`,
    method: reveal ? 'POST' : 'PATCH',
    data: { action },
    headers: { 'X-Security-Proof': proof },
    singleUseAuthorization: true,
    signal,
  })
  const data = requireServerSuccess(res.data).data
  return reveal ? z.object({ key: z.string() }).parse(data).key : ''
}
