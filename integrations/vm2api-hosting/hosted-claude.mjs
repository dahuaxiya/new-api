/** Claude 订阅托管只接受官方 CLI 登录，身份必须由官方 bootstrap 接口确认。 */
import crypto from 'node:crypto'
import fs from 'node:fs'
import path from 'node:path'
import { hostedError } from './hosted-identity.mjs'
import { fetchOauthBootstrapAccount } from '../oauth/oauth-identity.mjs'
import { makeProxyFetch } from '../protocol/codex-models.mjs'
import { stopClaudeAuthLoginSession } from '../oauth/claude-auth-login.mjs'
import { stopOfficialCcResident } from '../oauth/official-cc-bootstrap.mjs'
import { stopSlot } from '../vm/slot-runtime.mjs'
import { patchVmRecord } from '../vm/vm-registry.mjs'

/** 检查正式授权及 CLI 缓存；文件损坏或无法读取必须阻止复用，不能当作空槽。 */
export function assertClaudeEmpty(root, vm) {
  if (vm.claude?.has_access || vm.claude?.has_refresh || vm.claude?.account_uuid || vm.has_token) throw hostedError('slot_not_empty')
  for (const file of ['credentials.json', '.credentials.json']) {
    const target = path.join(root, 'vms', vm.id, 'cli-home', '.claude', file)
    if (fs.existsSync(target)) {
      const data = JSON.parse(fs.readFileSync(target, 'utf8'))
      if (Object.keys(data).length) throw hostedError('slot_not_empty')
    }
  }
}

/** 停止全部槽内执行后清除凭据和授权会话；保留 CLI 安装文件、槽位和网络配置。 */
export async function clearClaudeCredentials(root, vm) {
  await stopClaudeAuthLoginSession(root, vm.id)
  stopOfficialCcResident(vm.id, root)
  const stopped = await stopSlot(vm)
  if (!stopped?.ok) throw hostedError('cleanup_pending')
  const dir = path.join(root, 'vms', vm.id)
  for (const relative of ['cli-home/.claude', 'cli-home/.claude.json', 'run/auth-login']) {
    fs.rmSync(path.join(dir, relative), { recursive: true, force: true })
  }
  patchVmRecord(root, vm.id, record => {
    record.claude = {}; record.email = null; record.account_uuid = null; record.org_uuid = null
    record.worker_credential = null; record.has_token = false; record.refresh_status = null
    record.schedulable = false; record.schedule_disabled_reason = 'hosting_authorization'
    return record
  })
  assertClaudeEmpty(root, { id: vm.id })
}

/** 仅信任固定官方接口的当前身份，不能使用客户端提供的邮箱或 CLI 元数据代替核验。 */
export async function verifyHostedClaudeIdentity(tokens, proxyUrl, fetchImpl = makeProxyFetch(proxyUrl, 15000)) {
  if (!tokens?.access_token || !tokens?.refresh_token || !String(tokens.scope).includes('user:profile') || !String(tokens.scope).includes('user:inference')) throw hostedError('identity_invalid')
  const result = await fetchOauthBootstrapAccount({ accessToken: tokens.access_token, proxyUrl, fetchImpl: (url, init) => fetchImpl(url, { ...init, redirect: 'error', size: 262144, signal: AbortSignal.timeout(15000) }) })
  const identity = result.identity
  const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i
  if (!result.ok || !uuid.test(identity?.account_uuid || '') || !uuid.test(identity?.org_uuid || '')) throw hostedError('identity_unavailable')
  if (!['pro', 'max'].includes(identity.subscription_type)) throw hostedError('personal_account_required')
  if ((tokens.account_uuid && tokens.account_uuid !== identity.account_uuid) || (tokens.org_uuid && tokens.org_uuid !== identity.org_uuid)) throw hostedError('account_changed')
  // 同一 Claude 用户换组织仍是同一账号，不以邮箱、组织或机器编号建立唯一性。
  const hash = value => crypto.createHash('sha256').update(value).digest('hex')
  const email = identity.email || '', at = email.indexOf('@')
  return { tokens: { ...tokens, ...identity }, identity: { account_hash: hash('anthropic-account\0' + identity.account_uuid), subject_hash: hash('anthropic-subject\0' + identity.account_uuid), label: (at > 0 ? email[0] + '***' + email.slice(at) : 'Claude account').slice(0, 160), plan: identity.subscription_type } }
}
