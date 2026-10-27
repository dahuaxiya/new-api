/** 官方 OIDC 身份验证：只接受服务器通过 PKCE 换票获得的 ID token。 */
import crypto from 'node:crypto'
import { BOOTSTRAP_URL } from '../oauth/oauth-contract.mjs'
import { CODEX_OAUTH_CLIENT_ID, makeProxyFetch } from '../protocol/codex-models.mjs'

/** 固定诊断码，禁止将 JWT、授权码和远端响应写入错误。 */
export function hostedError(code = 'hosting_conflict') {
  return Object.assign(new Error(code), { code })
}

/** 从固定官方站点获取受限 JSON；不跟随重定向，也不接受 JWT 指定的任意 jku。 */
async function officialJSON(url, fetchImpl) {
  const u = new URL(url)
  if (u.origin !== 'https://auth.openai.com' || u.username || u.password || u.hash) throw hostedError('identity_unavailable')
  const res = await fetchImpl(u.toString(), { redirect: 'error', size: 262144, signal: AbortSignal.timeout(8000) })
  if (res.status !== 200) throw hostedError('identity_unavailable')
  return res.json()
}

/** 验证官方签名、颁发方、受众和期限；主体与账号用于归属校验，套餐仅作展示，不决定准入。 */
export async function verifyHostedIdentity(tokens, proxyUrl, fetchImpl = makeProxyFetch(proxyUrl)) {
  const jwt = String(tokens?.id_token || '')
  if (jwt.length > 32768) throw hostedError('identity_invalid')
  const parts = jwt.split('.')
  if (parts.length !== 3) throw hostedError('identity_invalid')
  let header, claims
  try { header = JSON.parse(Buffer.from(parts[0], 'base64url')); claims = JSON.parse(Buffer.from(parts[1], 'base64url')) }
  catch { throw hostedError('identity_invalid') }
  if (header.alg !== 'RS256' || typeof header.kid !== 'string' || header.kid.length > 256) throw hostedError('identity_invalid')
  const discovery = await officialJSON('https://auth.openai.com/.well-known/openid-configuration', fetchImpl)
  if (!['https://auth.openai.com', 'https://auth.openai.com/'].includes(discovery.issuer) || claims.iss !== discovery.issuer) throw hostedError('identity_invalid')
  const jwks = await officialJSON(discovery.jwks_uri, fetchImpl)
  const keys = Array.isArray(jwks.keys) ? jwks.keys.filter(k => k.kid === header.kid && k.kty === 'RSA' && (!k.use || k.use === 'sig') && (!k.alg || k.alg === 'RS256')) : []
  if (keys.length !== 1 || !crypto.verify('RSA-SHA256', Buffer.from(parts.slice(0, 2).join('.')), crypto.createPublicKey({ key: keys[0], format: 'jwk' }), Buffer.from(parts[2], 'base64url'))) throw hostedError('identity_invalid')
  const now = Math.floor(Date.now() / 1000)
  const audiences = Array.isArray(claims.aud) ? claims.aud : [claims.aud]
  if (!audiences.includes(CODEX_OAUTH_CLIENT_ID) || (audiences.length > 1 && claims.azp !== CODEX_OAUTH_CLIENT_ID) || (claims.azp && claims.azp !== CODEX_OAUTH_CLIENT_ID) || !Number.isFinite(claims.exp) || claims.exp <= now || !Number.isFinite(claims.iat) || claims.iat > now + 60 || (claims.nbf && claims.nbf > now + 60)) throw hostedError('identity_invalid')
  const auth = claims['https://api.openai.com/auth'] || {}
  const account = auth.chatgpt_account_id
  if (typeof claims.sub !== 'string' || !claims.sub || claims.sub.length > 256 || typeof account !== 'string' || !account || account.length > 256) throw hostedError('identity_invalid')
  // 套餐标识会随官方产品变化；不能用固定白名单阻止已验证身份的账号绑定。
  // 缺失或非字符串只记为 unknown，长度遵循门户 plan 字段的 varchar(32) 存储约束。
  const plan = typeof auth.chatgpt_plan_type === 'string'
    ? auth.chatgpt_plan_type.trim().slice(0, 32) || 'unknown'
    : 'unknown'
  const hash = value => crypto.createHash('sha256').update(value).digest('hex')
  const email = typeof claims.email === 'string' ? claims.email : ''
  const at = email.indexOf('@')
  const label = at > 0 ? email[0] + '***' + email.slice(at) : 'GPT account'
  return { account_hash: hash('openai-account\0' + account), subject_hash: hash('openai-subject\0' + claims.sub), label: label.slice(0, 160), plan }
}

/** 实际从已配置出口访问官方站点，不能只凭代理字段存在就分配槽位。 */
export async function probeHostedExit(proxyUrl, platform = 'codex', fetchImpl = makeProxyFetch(proxyUrl)) {
  try {
    if (platform === 'claude') {
      // 无凭据请求应收到官方鉴权拒绝；网络超时、重定向或地区拦截不能算出口就绪。
      const response = await fetchImpl(BOOTSTRAP_URL, { redirect: 'error', size: 262144, signal: AbortSignal.timeout(8000) })
      try { if (response.status !== 401) throw hostedError('exit_unavailable') }
      finally { response.body?.destroy?.() }
    } else if (platform === 'codex') {
      await officialJSON('https://auth.openai.com/.well-known/openid-configuration', fetchImpl)
    } else throw hostedError('exit_unavailable')
  }
  catch { throw hostedError('exit_unavailable') }
}
