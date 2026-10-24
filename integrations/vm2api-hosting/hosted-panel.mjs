/** New API 托管协议：预置空槽、准备授权、唯一身份确认后激活；所有接口只在面板鉴权之后调用。 */
import crypto from 'node:crypto'
import fs from 'node:fs'
import path from 'node:path'
import { HOSTING_PERMANENT_END, validHostedTerm, hostedDb, hostedRecord, withHostedWrite } from './hosted-store.mjs'
import { hostedError, verifyHostedIdentity, probeHostedExit } from './hosted-identity.mjs'
import { encryptionEnabled, encryptString, decryptString } from '../db/secure.mjs'
import { getVm, listVms, patchVmRecord, setVmSchedulable } from '../vm/vm-registry.mjs'
import { readCodexAccounts, writeCodexAccounts } from '../vm/codex-slot.mjs'
import { generateCodexAuthUrl, exchangeCodexAuthCode } from '../oauth/codex-oauth.mjs'
import { makeProxyFetch } from '../protocol/codex-models.mjs'
import { VmPoolsRepo } from '../db/repos/vm-pools-repo.mjs'
import { createVmPool } from './vm-pools.mjs'
import { plainApiKey } from './api-keys.mjs'
import { stopSlot, startSlotReady } from '../vm/slot-runtime.mjs'
import { stopCodexKernel, finishCodexRecycle } from '../transport/codex-kernel-supervisor.mjs'
import { isCodexVm } from '../vm/vm-kind.mjs'
const busy = new Set()
const sessions = new Map()
const ID = /^[a-zA-Z0-9_-]{1,80}$/
const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/

/** 返回受限状态，凭据和授权会话绝不混进列表。 */
function view(row) {
  if (!row) return null
  const { vm_id, binding_id, generation, state, account_hash, subject_hash, key_id, pool_id, starts_at, ends_at, label, plan, identity_source } = row
  return { vm_id, binding_id, generation, state, account_hash, subject_hash, key_id, pool_id, starts_at, ends_at, label, plan, identity_source }
}

/** 仅清理与当前槽位关联的短期 OAuth 会话，避免旧页面回调覆盖新绑定。 */
function cancelSessions(id) {
  for (const [key, value] of sessions) if (value.vmId === id || value.expiresAt <= Date.now()) sessions.delete(key)
}

/** 确认槽位没有任何授权或账号池；不把读文件失败误当成空账号。 */
function assertEmpty(root, vm) {
  const dir = path.join(root, 'vms', vm.id)
  const credential = path.join(dir, 'codex-credentials.json')
  if (fs.existsSync(credential)) {
    const data = JSON.parse(fs.readFileSync(credential, 'utf8'))
    if (!Array.isArray(data.accounts) || data.accounts.length !== 0) throw hostedError('slot_not_empty')
  }
  if (vm.codex?.has_access || vm.codex?.has_refresh || vm.codex?.email || vm.codex?.chatgpt_account_id || readCodexAccounts(root, vm.id).length) throw hostedError('slot_not_empty')
  if (new VmPoolsRepo(hostedDb()).list().some(p => p.vm_ids.includes(vm.id))) throw hostedError('slot_not_empty')
}

/** 排空内核与容器后再清理，保留预置槽位、网络和出口；失败时调用方保留 cleaning 状态。 */
async function clearCredentials(ctx, vm) {
  const child = stopCodexKernel(vm.id)
  if (child && !(await finishCodexRecycle(child, null)).exited) throw hostedError('cleanup_pending')
  const stopped = await stopSlot(vm)
  if (!stopped?.ok) throw hostedError('cleanup_pending')
  fs.mkdirSync(path.join(ctx.cfg.paths.project, 'vms', vm.id), { recursive:true, mode:0o700 })
  withHostedWrite(vm.id, () => writeCodexAccounts(ctx.cfg.paths.project, vm.id, []))
  // Codex 内核配置含 access token，停止进程后清除整个运行目录，避免下一次启动复用。
  const run = path.join(ctx.cfg.paths.project, 'vms', vm.id, 'run')
  if (fs.existsSync(run)) for (const entry of fs.readdirSync(run)) {
    if (entry !== 'codex-installation-id') fs.rmSync(path.join(run, entry), { recursive: true, force: true })
  }
  patchVmRecord(ctx.cfg.paths.project, vm.id, row => { row.codex = {}; row.email = null })
  if (readCodexAccounts(ctx.cfg.paths.project, vm.id).length) throw hostedError('cleanup_pending')
}

/** 远端策略写入带代数防护，拒绝旧工作器迟到的激活；先暂停再做跨系统写入。 */
function claim(row, body) {
  // 代数只在同一绑定中递增。清理后的库存可分配给新用户，其代数从自己的记录开始。
  if (!row || !UUID.test(body.binding_id || '') || !Number.isSafeInteger(body.generation) || body.generation < 0 || (row.binding_id && (row.binding_id !== body.binding_id || body.generation < row.generation))) throw hostedError()
  hostedDb().prepare('UPDATE hosted_slots SET generation = ? WHERE vm_id = ?').run(body.generation, row.vm_id)
}

/** 提取已有账号的稳定标识，仅用于保留绑定和防重复；这些字段不是新的官方登录证明。 */
function existingAccountReference(account, vm) {
  let claims = {}
  // 期限管理由 root 授权，不依赖短期 ID token 的有效期或官方网络；新 OAuth 仍必须调用 verifyHostedIdentity。
  for (const token of [account.id_token, account.access_token]) {
    if (typeof token !== 'string' || token.length > 32768) continue
    try {
      const candidate = JSON.parse(Buffer.from(token.split('.')[1], 'base64url'))
      if (candidate?.sub && candidate['https://api.openai.com/auth']?.chatgpt_account_id) { claims = candidate; break }
    } catch { /* 无法读取时不能猜测邮箱对应的身份，下面统一拒绝不完整绑定。 */ }
  }
  const auth = claims['https://api.openai.com/auth'] || {}
  const accountID = auth.chatgpt_account_id, subject = claims.sub
  if (typeof accountID !== 'string' || !accountID || accountID.length > 256 || typeof subject !== 'string' || !subject || subject.length > 256) throw hostedError('hosting_conflict')
  // 管理记录和令牌中已存在的账号必须一致，防止刚换号时沿用上一位用户的期限。
  if ([account.chatgpt_account_id, vm.codex?.chatgpt_account_id].some(id => id && id !== accountID)) throw hostedError('account_changed')
  const email = typeof claims.email === 'string' ? claims.email : ''
  const at = email.indexOf('@')
  const hash = value => crypto.createHash('sha256').update(value).digest('hex')
  return { account_hash:hash('openai-account\0'+accountID),subject_hash:hash('openai-subject\0'+subject),label:(at>0?email[0]+'***'+email.slice(at):'GPT account').slice(0,160),plan:['free','plus','pro'].includes(auth.chatgpt_plan_type)?auth.chatgpt_plan_type:'unknown',identity_source:'legacy_binding' }
}

/** 同步核对旧账号与单槽位 Key 集合；保留旧直连入口，但中央始终只连接指定的一把 Key。 */
function inspectLegacy(ctx, vm, body) {
  if (hostedRecord(vm.id)) throw hostedError('hosting_conflict')
  const accounts = readCodexAccounts(ctx.cfg.paths.project, vm.id)
  if (accounts.length !== 1) throw hostedError('hosting_conflict')
  const identity = existingAccountReference(accounts[0], vm)
  const keys = ctx.apiKeyStore.repo.list()
  const key = keys.find(k => plainApiKey(k) === body.key && body.key)
  const pools = new VmPoolsRepo(hostedDb()).list()
  const scope = k => {
    if (k.vm_pool_id) return pools.find(p => p.id === k.vm_pool_id)?.vm_ids || []
    if (k.group_type === 'all') return []
    try { return Array.isArray(k.allowed_vms) ? k.allowed_vms : JSON.parse(k.allowed_vms || '[]') } catch { throw hostedError('hosting_conflict') }
  }
  // 老版池 Key 保存为 all，但池成员仍是唯一调度范围；只有无池的 all 才是全局授权。
  if (!key || key.status !== 'active' || !(key.group_type === 'openai' || (key.vm_pool_id && key.group_type === 'all')) || scope(key).length !== 1 || scope(key)[0] !== vm.id) throw hostedError('hosting_conflict')
  // 仅兼容保存期限时已存在、且只指向当前槽位的 Key；共享多槽 Key 不能被静默收窄。
  // 停用的旧 Key 也登记墓碑，但保留其停用状态；后续改池、重新启用不能越过本次归属。
  const retained = keys.filter(k => scope(k).includes(vm.id))
  for (const candidate of retained) {
    if (scope(candidate).length !== 1 || !(candidate.group_type === 'openai' || (candidate.vm_pool_id && candidate.group_type === 'all')) || hostedDb().prepare('SELECT 1 FROM hosted_keys WHERE key_id = ?').get(candidate.id)) throw hostedError('hosting_conflict')
  }
  const pool = key.vm_pool_id && pools.find(p => p.id === key.vm_pool_id)
  if (pool && !pool.enabled) throw hostedError('hosting_conflict')
  return { vm_id:vm.id, state:'checked', ...identity, key_id:key.id, pool_id:pool?.id || '', retained_key_ids:retained.map(k => k.id) }
}

/** 处理单槽动作；所有路径由管理员登记、所有绑定由中央数据库生成，客户端不能提供上游地址。 */
async function operate(ctx, vm, action, body, commit) {
  const db = hostedDb(), root = ctx.cfg.paths.project
  let row = hostedRecord(vm.id)
  const px = ctx.requireSlotProxy(vm, {})
  if (action === 'takeover-check') return inspectLegacy(ctx, vm, body)
  if (action === 'takeover') {
    if (!UUID.test(body.binding_id || '') || !Number.isSafeInteger(body.generation) || body.generation < 0) throw hostedError()
    if (!row) {
      // 首次接管原子冻结现有 Key 集合。失败必须整体回滚；重试不能把后来新增的 Key 混进来。
      // 使用 SQLite 保存点，兼容没有 DatabaseSync.isTransaction 的旧 Node，异常时仍可靠回滚。
      db.exec('SAVEPOINT hosted_takeover')
      try {
        const checked = inspectLegacy(ctx, vm, body)
        if (checked.account_hash !== body.account_hash || checked.subject_hash !== body.subject_hash || checked.key_id !== body.key_id) throw hostedError('account_changed')
        // 同步检查到写入之间没有 await；旧账号标识只用于维持归属，不代替官方登录证明。
        db.prepare("INSERT INTO hosted_slots (vm_id,binding_id,generation,state,account_hash,subject_hash,key_id,pool_id,label,plan,identity_source) VALUES (?,?,?,'prepared',?,?,?,?,?,?,?)").run(vm.id,body.binding_id,body.generation,checked.account_hash,checked.subject_hash,checked.key_id,checked.pool_id || null,checked.label,checked.plan,checked.identity_source)
        const insert = db.prepare('INSERT INTO hosted_keys (key_id,vm_id,binding_id) VALUES (?,?,?)')
        for (const id of checked.retained_key_ids) insert.run(id,vm.id,body.binding_id)
        db.exec('RELEASE hosted_takeover')
      } catch (error) {
        db.exec('ROLLBACK TO hosted_takeover; RELEASE hosted_takeover')
        throw error
      }
      row = hostedRecord(vm.id)
    }
    claim(row, body)
    db.prepare('INSERT OR IGNORE INTO hosted_keys (key_id,vm_id,binding_id) VALUES (?,?,?)').run(row.key_id,vm.id,body.binding_id)
    if (row.binding_id !== body.binding_id || row.account_hash !== body.account_hash || row.subject_hash !== body.subject_hash) throw hostedError()
    if (!['prepared','active','paused'].includes(row.state)) throw hostedError()
    // 首次接管需要结束旧内核的在途刷新；重复调用已激活状态时保持原运行进程。
    if (row.state === 'prepared') {
      setVmSchedulable(root,vm.id,false,'hosting_takeover')
      const child = stopCodexKernel(vm.id)
      if (child && !(await finishCodexRecycle(child,null)).exited) throw hostedError('cleanup_pending')
    }
    return view(hostedRecord(vm.id))
  }
  if (action === 'enroll') {
    if (row && row.state !== 'inventory') return view(row)
    if (row) { await clearCredentials(ctx, vm); return view(row) }
    if (!px.ok) throw hostedError('exit_unavailable')
    assertEmpty(root, vm)
    await probeHostedExit(px.proxyUrl)
    // 永久隔离标记先写，随后即使进程崩溃，master/all 也不能调度此槽。
    db.prepare('INSERT INTO hosted_slots (vm_id) VALUES (?)').run(vm.id)
    setVmSchedulable(root, vm.id, false, 'hosting_inventory')
    await clearCredentials(ctx, vm)
    return view(hostedRecord(vm.id))
  }
  if (!row) throw hostedError('inventory_required')
  claim(row, body)
  if (action === 'reserve') {
    if (row.binding_id === body.binding_id) return view(hostedRecord(vm.id))
    if (row.state !== 'inventory' || !px.ok) throw hostedError('slot_unavailable')
    assertEmpty(root, vm)
    await probeHostedExit(px.proxyUrl)
    db.prepare("UPDATE hosted_slots SET binding_id = ?, state = 'reserved', pending = NULL WHERE vm_id = ?").run(body.binding_id, vm.id)
    return view(hostedRecord(vm.id))
  }
  if (action === 'release' && row.state === 'inventory' && !row.binding_id) return view(row)
  if (row.binding_id !== body.binding_id) throw hostedError()
  if (action === 'login') {
    if (!px.ok || !['reserved','prepared','active','paused','reauth'].includes(row.state)) throw hostedError()
    cancelSessions(vm.id)
    // 重新登录先禁止新请求，避免旧进程与新 refresh token 同时工作。
    db.prepare("UPDATE hosted_slots SET state = ?, pending = NULL WHERE vm_id = ?").run(row.account_hash ? 'reauth' : 'reserved', vm.id)
    setVmSchedulable(root, vm.id, false, 'hosting_authorization')
    const child = stopCodexKernel(vm.id)
    if (child && !(await finishCodexRecycle(child, null)).exited) throw hostedError('cleanup_pending')
    const auth = generateCodexAuthUrl({ vmId: vm.id, proxyUrl: px.proxyUrl, installationId: vm.device_id })
    sessions.set(auth.session_id, { vmId: vm.id, bindingId: body.binding_id, state: new URL(new URL(auth.auth_url).searchParams.get('authorize_url')).searchParams.get('state'), expiresAt: auth.expires_at })
    return auth
  }
  if (action === 'prepare') {
    if (!px.ok || !encryptionEnabled()) throw hostedError('hosting_unavailable')
    if (row.state === 'prepared' && row.pending) return view(row)
    const session = sessions.get(body.session_id)
    let callback
    try { callback = new URL(body.callback_url) } catch { throw hostedError('invalid_callback') }
    if (!session || session.vmId !== vm.id || session.bindingId !== body.binding_id || session.expiresAt <= Date.now() || callback.origin !== 'http://localhost:1455' || callback.pathname !== '/auth/callback' || callback.username || callback.password || callback.hash || callback.searchParams.getAll('state').length !== 1 || callback.searchParams.get('state') !== session.state || callback.searchParams.getAll('code').length !== 1 || !callback.searchParams.get('code')) throw hostedError('authorization_expired')
    sessions.delete(body.session_id) // 换票前消费，网络失败也不能重放。
    const fetchImpl = makeProxyFetch(px.proxyUrl)
    const tokens = await exchangeCodexAuthCode({ sessionId: body.session_id, code: body.callback_url, proxyUrl: px.proxyUrl, vmId: vm.id, fetchImpl: (url,init) => fetchImpl(url, { ...init, redirect:'error' }) })
    const identity = await verifyHostedIdentity(tokens, px.proxyUrl)
    if ((row.account_hash && row.account_hash !== identity.account_hash) || (row.subject_hash && row.subject_hash !== identity.subject_hash)) throw hostedError('account_changed')
    // 只保存加密的待激活授权，不写正式凭据。中央唯一约束通过后才允许 activate。
    db.prepare("UPDATE hosted_slots SET state = 'prepared', pending = ?, account_hash = ?, subject_hash = ?, label = ?, plan = ?, identity_source = 'official' WHERE vm_id = ?").run(encryptString(JSON.stringify(tokens)), identity.account_hash, identity.subject_hash, identity.label, identity.plan, vm.id)
    return view(hostedRecord(vm.id))
  }
  if (action === 'activate') {
    if (body.account_hash !== row.account_hash || body.subject_hash !== row.subject_hash || !row.account_hash || !validHostedTerm(body.starts_at, body.ends_at) || (body.ends_at !== HOSTING_PERMANENT_END && body.ends_at <= Math.floor(Date.now()/1000))) throw hostedError()
    if (!['prepared','active','paused'].includes(row.state)) throw hostedError()
    if (row.pending) {
      const account = JSON.parse(decryptString(row.pending))
      await withHostedWrite(vm.id, () => commit({ cfg:ctx.cfg,vmPath:path.join(root,'vms',vm.id+'.json'),existing:vm,account,routingConfig:ctx.routingConfig }))
    }
    let pool = row.pool_id && new VmPoolsRepo(db).get(row.pool_id)
    if (!pool) {
      pool = new VmPoolsRepo(db).list().find(p => p.name === 'hst-'+body.binding_id.replaceAll('-',''))
      if (!pool) pool = createVmPool(new VmPoolsRepo(db), { name:'hst-'+body.binding_id.replaceAll('-',''), vm_ids:[vm.id] }, [vm])
      db.prepare('UPDATE hosted_slots SET pool_id = ? WHERE vm_id = ?').run(pool.id,vm.id)
    }
    if (pool.vm_ids.length !== 1 || pool.vm_ids[0] !== vm.id) throw hostedError()
    let key = row.key_id && ctx.apiKeyStore.repo.getById(row.key_id)
    if (!key) {
      key = ctx.apiKeyStore.list().find(k => k.name === 'hst-'+body.binding_id.replaceAll('-',''))
      if (key) key = ctx.apiKeyStore.repo.getById(key.id)
      if (!key) key = ctx.apiKeyStore.create({ name:'hst-'+body.binding_id.replaceAll('-',''),vm_pool_id:pool.id,group_type:'openai',max_concurrency:3 })
      db.prepare('UPDATE hosted_slots SET key_id = ? WHERE vm_id = ?').run(key.id,vm.id)
    }
    db.prepare('INSERT OR IGNORE INTO hosted_keys (key_id, vm_id, binding_id) VALUES (?, ?, ?)').run(key.id, vm.id, body.binding_id)
    const plain = plainApiKey(key)
    if (!plain) throw hostedError('hosting_unavailable')
    const enabled = body.starts_at <= Math.floor(Date.now()/1000) && body.enabled !== false
    // 从定期改为永久时必须清空主 Key 的独立到期时间；兼容旧 Key 的个人设置不变。
    ctx.apiKeyStore.update(key.id, { vm_pool_id:pool.id,group_type:'openai',status:enabled?'active':'disabled',expires_at:body.ends_at === HOSTING_PERMANENT_END ? null : new Date(body.ends_at*1000).toISOString() })
    const boot = await startSlotReady(getVm(root,vm.id),root,{routing:ctx.routingConfig})
    if (!boot?.ok) throw hostedError('slot_unavailable')
    db.prepare("UPDATE hosted_slots SET state = ?, starts_at = ?, ends_at = ?, pending = NULL WHERE vm_id = ?").run(enabled?'active':'paused',body.starts_at,body.ends_at,vm.id)
    setVmSchedulable(root,vm.id,true,null)
    return { ...view(hostedRecord(vm.id)), key:plain }
  }
  if (action === 'policy') {
    if (!row.key_id || !['active','paused'].includes(row.state) || !validHostedTerm(body.starts_at, body.ends_at)) throw hostedError()
    const active = body.enabled === true && body.starts_at <= Math.floor(Date.now()/1000) && (body.ends_at === HOSTING_PERMANENT_END || body.ends_at > Math.floor(Date.now()/1000))
    db.prepare('UPDATE hosted_slots SET state = ?, starts_at = ?, ends_at = ? WHERE vm_id = ?').run(active?'active':'paused',body.starts_at,body.ends_at,vm.id)
    // 显式 null 清除旧期限；暂停、归属和历史 Key 墓碑仍由请求路径检查。
    ctx.apiKeyStore.update(row.key_id, { status:active?'active':'disabled',expires_at:body.ends_at === HOSTING_PERMANENT_END ? null : new Date(body.ends_at*1000).toISOString() })
    return view(hostedRecord(vm.id))
  }
  if (action === 'release') {
    cancelSessions(vm.id)
    db.prepare("UPDATE hosted_slots SET state = 'cleaning', pending = NULL WHERE vm_id = ?").run(vm.id)
    if (row.key_id) ctx.apiKeyStore.update(row.key_id,{status:'disabled'})
    setVmSchedulable(root,vm.id,false,'hosting_cleanup')
    await clearCredentials(ctx,vm)
    // 保留隔离标记，旧 Key 即使被管理员重新启用也不能访问下一位用户。
    if (row.pool_id) db.prepare('DELETE FROM vm_pool_members WHERE pool_id = ? AND vm_id = ?').run(row.pool_id,vm.id)
    db.prepare("UPDATE hosted_slots SET binding_id = NULL, state = 'inventory', account_hash = NULL, subject_hash = NULL, key_id = NULL, pool_id = NULL, pending = NULL, label = NULL, plan = NULL, identity_source = 'official', starts_at = 0, ends_at = 0 WHERE vm_id = ?").run(vm.id)
    return view(hostedRecord(vm.id))
  }
  throw hostedError()
}

/** 历史账号只用于保守拒绝重复绑定，不凭未验证旧 JWT 授权任何新请求；只向门户后端返回哈希。 */
function legacyIdentity(root, vm, managed) {
  const accounts = new Set(), subjects = new Set()
  const hash = (kind, value) => crypto.createHash('sha256').update(kind + '\0' + value).digest('hex')
  if (!managed) {
    const records = readCodexAccounts(root,vm.id)
    for (const account of [vm.codex || {}, ...records]) {
      if (account.chatgpt_account_id) accounts.add(hash('openai-account', String(account.chatgpt_account_id)))
      for (const token of [account.id_token, account.access_token]) {
        try {
          const claims = JSON.parse(Buffer.from(String(token).split('.')[1], 'base64url'))
          const id = claims['https://api.openai.com/auth']?.chatgpt_account_id
          if (id) accounts.add(hash('openai-account', String(id)))
          if (claims.sub) subjects.add(hash('openai-subject', String(claims.sub)))
        } catch { /* 旧文件无 JWT 时仍使用现有明确账号字段，不写入或旋转凭据。 */ }
      }
    }
  }
  return { legacy_account_hashes:[...accounts], legacy_subject_hashes:[...subjects] }
}

/** 面板扩展入口；非本协议返回 false，错误只返回固定代码，禁止泄露上游返回正文。 */
export async function handleHostedPanel(ctx, req, res, url, commit) {
  const p = url.pathname
  if (!p.startsWith('/api/panel/hosting/')) return false
  try {
    if (req.panelRole && req.panelRole !== 'admin') throw hostedError('forbidden')
    if (p === '/api/panel/hosting/slots' && req.method === 'GET') {
      const slots = listVms(ctx.cfg.paths.project).filter(v => isCodexVm(v) && !v.node_id).map(v => {
        const record = hostedRecord(v.id)
        let empty = false
        try { assertEmpty(ctx.cfg.paths.project, getVm(ctx.cfg.paths.project,v.id)); empty = true } catch {}
        const px = ctx.requireSlotProxy(getVm(ctx.cfg.paths.project,v.id),{})
        return { id:v.id,name:v.name,empty,exit_ready:px.ok===true,managed:!!record,hosting:view(record), ...legacyIdentity(ctx.cfg.paths.project,v,record) }
      })
      ctx.json(res,200,{ok:true,data:{version:1,slots}}); return true
    }
    const match = p.match(/^\/api\/panel\/hosting\/slots\/([A-Za-z0-9_-]{1,80})\/(enroll|reserve|login|prepare|activate|policy|release|takeover-check|takeover)$/)
    if (!match || req.method !== 'POST') throw hostedError()
    const id = match[1]
    if (!ID.test(id) || busy.has(id)) throw hostedError('hosting_busy')
    busy.add(id)
    try {
      const vm = getVm(ctx.cfg.paths.project,id)
      if (!vm || !isCodexVm(vm) || vm.node_id) throw hostedError('slot_unavailable')
      const body = await ctx.readBody(req, 20000)
      const result = await operate(ctx,vm,match[2],body,commit)
      ctx.json(res,200,{ok:true,data:result})
    } finally { busy.delete(id) }
  } catch (error) {
    const safe = new Set(['hosting_conflict','hosting_unavailable','hosting_busy','inventory_required','slot_not_empty','slot_unavailable','exit_unavailable','cleanup_pending','invalid_callback','authorization_expired','account_changed','personal_account_required','identity_invalid','identity_unavailable','forbidden'])
    ctx.json(res,409,{ok:false,error:{code:safe.has(error.code)?error.code:'hosting_unavailable',message:'Hosted account operation could not be completed'}})
  }
  return true
}
