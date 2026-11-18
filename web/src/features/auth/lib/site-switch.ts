import { api } from '@/lib/api'

import { createOAuthAuthorization } from '../api'
import { rememberOAuthLoginRedirect } from './oauth-callback-mode'

const intentKey = 'luobo.site-switch.v1'
const switchHash = /^#switch=([a-f0-9-]{36})$/

/** 在托管站记录本标签页的点击与当前账号。禁用会话存储时沿用普通导航。 */
export function hostingSwitchTarget(client: string, userId: number): string {
  const target = new URL('/sign-in', client)
  try {
    const nonce = crypto.randomUUID()
    sessionStorage.setItem(
      intentKey,
      JSON.stringify({ nonce, userId, until: Date.now() + 60_000 })
    )
    target.hash = `switch=${nonce}`
    return target.href
  } catch {
    return new URL('/dashboard', client).href
  }
}

/** 临时标记只经 URL 片段往返，不发送给 Web 服务器，也不能代替 OAuth 凭据。 */
export function switchNonce(): string | undefined {
  return switchHash.exec(window.location.hash)?.[1]
}

/** 中转站在自己的域名创建带浏览器 Cookie 的登录事务，回跳目标固定为控制台。 */
export async function startSiteSwitch(
  issuer: string,
  nonce: string
): Promise<string> {
  const { authorizationUrl, state } = await createOAuthAuthorization(
    'site-sso',
    'login'
  )
  const target = new URL(authorizationUrl ?? '', window.location.origin)
  if (
    target.origin !== issuer ||
    target.pathname !== '/sso' ||
    target.searchParams.get('state') !== state ||
    target.searchParams.get('verify') !== 'login'
  )
    throw new Error('Verification flow expired')
  rememberOAuthLoginRedirect(state, '/dashboard')
  target.hash = `switch=${nonce}`
  return target.href
}

/** 只消费同一标签页、同一身份最近一次点击；直接打开授权链接仍展示账号选择页。 */
export function consumeSiteSwitch(nonce: string, userId: number): boolean {
  try {
    const intent = JSON.parse(sessionStorage.getItem(intentKey) ?? 'null')
    if (
      !intent ||
      intent.nonce !== nonce ||
      intent.userId !== userId ||
      intent.until <= Date.now()
    )
      return false
    sessionStorage.removeItem(intentKey)
    return true
  } catch {
    return false
  }
}

/** 身份源使用原授权接口签发短期授权码，回调限于配置的中转站和本次 state。 */
export async function approveSiteSwitch(
  search: {
    state: string
    challenge: string
    client_id: string
    verify: 'login'
  },
  client: string
): Promise<string> {
  const { data } = await api.post('/api/site-sso/authorize', search)
  const target = new URL(data.data?.redirect_url ?? '', window.location.origin)
  if (
    !data.success ||
    target.origin !== client ||
    target.pathname !== '/oauth/site-sso' ||
    target.searchParams.get('state') !== search.state ||
    !target.searchParams.get('code')
  )
    throw new Error('Verification flow expired')
  return target.href
}
