/** 托管隔离规则独立存储，避免被普通槽位元数据更新覆盖。 */
import { getDb, isDbOpen } from '../db/database.mjs'
const initialized = new WeakSet()
// 与 New API 的 HostingPermanentEnd 保持一致，只有显式 -1 表示永久，缺失值必须拒绝。
export const HOSTING_PERMANENT_END = -1

/** 校验完整期限，永久权益仍须有正数开始时间；不影响 OAuth 或登录会话期限。 */
export function validHostedTerm(startsAt, endsAt) {
  return Number.isSafeInteger(startsAt) && startsAt > 0 && startsAt <= 4102444800 && Number.isSafeInteger(endsAt) &&
    (endsAt === HOSTING_PERMANENT_END || (endsAt > startsAt && endsAt <= 4102444800))
}

/** 建表为可重复的附加迁移；未修改历史账号和任何上游凭据。 */
export function hostedDb() {
  const db = getDb()
  if (!initialized.has(db)) {
    db.exec(`CREATE TABLE IF NOT EXISTS hosted_slots (
      vm_id TEXT PRIMARY KEY, binding_id TEXT, generation INTEGER NOT NULL DEFAULT 0,
      state TEXT NOT NULL DEFAULT 'inventory', account_hash TEXT, subject_hash TEXT,
      key_id TEXT, pool_id TEXT, starts_at INTEGER NOT NULL DEFAULT 0,
      ends_at INTEGER NOT NULL DEFAULT 0, pending TEXT, label TEXT, plan TEXT,
      identity_source TEXT NOT NULL DEFAULT 'official'
    )`)
    db.exec(`CREATE TABLE IF NOT EXISTS hosted_keys (key_id TEXT PRIMARY KEY, vm_id TEXT NOT NULL, binding_id TEXT NOT NULL)`)
    // 已上线版本的表原本没有来源字段；旧的官方验证记录维持原语义，新旧库均可重复启动。
    if (!db.prepare('PRAGMA table_info(hosted_slots)').all().some(column => column.name === 'identity_source')) {
      db.exec("ALTER TABLE hosted_slots ADD COLUMN identity_source TEXT NOT NULL DEFAULT 'official'")
    }
    initialized.add(db)
  }
  return db
}

/** 每次调度读取实时记录；数据库故障向上传播，不能回退到全局账号池。 */
export function hostedRecord(vmId) {
  if (!isDbOpen()) return null
  return hostedDb().prepare('SELECT * FROM hosted_slots WHERE vm_id = ?').get(vmId) || null
}

/** 未托管账号沿用原权限；托管槽位仅接受本绑定登记的 Key，旧直连 Key 共用期限，master/all 不能绕过。 */
export function hostedAllows(scope, vmId, now = Math.floor(Date.now() / 1000)) {
  const row = hostedRecord(vmId)
  // 历史托管 Key 的墓碑永久保留；即使被通用面板改池或重新启用，也不能访问新用户或传统槽位。
  const key = isDbOpen() && scope?.hosted_key_id ? hostedDb().prepare('SELECT * FROM hosted_keys WHERE key_id = ?').get(scope.hosted_key_id) : null
  if (key && (!row || key.vm_id !== vmId || key.binding_id !== row.binding_id)) return false
  if (!row) return true
  // 新开户只登记主 Key；旧绑定在接管事务中冻结兼容集合。不能按当前池成员动态放行新增 Key。
  return row.state === 'active' && !!row.key_id && !!key &&
    validHostedTerm(row.starts_at, row.ends_at) && now >= row.starts_at && (row.ends_at === HOSTING_PERMANENT_END || now < row.ends_at)
}

/** 内核是托管账号唯一的后台 token 刷新者；JS 管理写入必须持有当前绑定的激活/清理上下文。 */
import { AsyncLocalStorage } from 'node:async_hooks'
const credentialWrite = new AsyncLocalStorage()

/** 将跨 await 的管理写入权限限定在一个绑定及其代数，其他异步探测无法继承该权限。 */
export function withHostedWrite(vmId, operation) {
  const row = hostedRecord(vmId)
  return credentialWrite.run({ vmId, bindingId:row?.binding_id, generation:row?.generation }, operation)
}

/** 拒绝普通导入、迟到刷新和清理后回写；非托管槽位完全沿用既有写入流程。 */
export function assertHostedWrite(vmId) {
  const row = hostedRecord(vmId)
  if (!row) return
  const permit = credentialWrite.getStore()
  if (!permit || permit.vmId !== vmId || permit.bindingId !== row.binding_id || permit.generation !== row.generation) {
    throw Object.assign(new Error('hosting_managed_credentials'), { code:'hosting_managed_credentials' })
  }
}
