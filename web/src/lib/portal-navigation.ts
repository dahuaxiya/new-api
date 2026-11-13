import { ROLE } from './roles'

/** 只决定产品页面是否保留，不承担账号鉴权；历史链接统一回用户首页。 */
export function isRetiredPortalPage(
  pathname: string,
  role: number,
  mode?: 'hosting' | 'retail'
): boolean {
  // 旧收藏也要服从当前站点边界；真正的数据隔离由后端与独立部署保证。
  if (
    mode === 'retail' &&
    /^\/(?:account\/gpt|users\/(?:hosting|onboarding))(?:\/|$)/.test(pathname)
  ) {
    return true
  }
  if (/^\/(?:playground|chat|chat2link)(?:\/|$)/.test(pathname)) return true
  if (/^\/usage-logs\/(?:drawing|task)(?:\/|$)/.test(pathname)) return true
  return (
    role < ROLE.ADMIN &&
    /^\/dashboard\/(?:models|flow|users)(?:\/|$)/.test(pathname)
  )
}
