import type { QueryClient } from '@tanstack/react-query'

import type { SystemStatus } from '@/features/auth/types'
import { readCachedStatus, STATUS_QUERY_KEY } from '@/lib/status-query'

/**
 * 选择退出后的公开路由。调用方须在清空查询缓存前调用；未知站点安全落回首页。
 * @param client 当前标签页的查询缓存，无需为退出再请求一次站点配置。
 * @return 中转站回首页；有独立登录表单的站点保留登录入口。
 */
export function signedOutRoute(client: QueryClient): '/' | '/sign-in' {
  const status = client.getQueryData<SystemStatus>(STATUS_QUERY_KEY) ??
    (readCachedStatus() as SystemStatus | null)
  // 中转站的 /sign-in 会立即启动 SSO。退出广播绝不能被其他标签当作登录意图。
  return !status || status.site_mode === 'retail' || status.site_sso?.consumer
    ? '/'
    : '/sign-in'
}
