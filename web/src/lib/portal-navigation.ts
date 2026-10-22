import { ROLE } from './roles'

/** 只决定产品页面是否保留，不承担账号鉴权；历史链接统一回用户首页。 */
export function isRetiredPortalPage(pathname: string, role: number): boolean {
  if (/^\/(?:playground|chat|chat2link)(?:\/|$)/.test(pathname)) return true
  if (/^\/usage-logs\/(?:drawing|task)(?:\/|$)/.test(pathname)) return true
  return (
    role < ROLE.ADMIN &&
    /^\/dashboard\/(?:models|flow|users)(?:\/|$)/.test(pathname)
  )
}
