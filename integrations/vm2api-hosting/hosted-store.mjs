/** 托管隔离规则独立存储，避免被普通槽位元数据更新覆盖。 */
import { getDb, isDbOpen } from '../db/database.mjs'
const initialized = new WeakSet()

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

/** 未托管账号沿用原权限；托管槽位仅接受登记的唯一 Key，master/all 也不能绕过。 */
export function hostedAllows(scope, vmId, now = Math.floor(Date.now() / 1000)) {
  const row = hostedRecord(vmId)
  // 历史托管 Key 的墓碑永久保留；即使被通用面板改池或重新启用，也不能访问新用户或传统槽位。
  const key = isDbOpen() && scope?.hosted_key_id ? hostedDb().prepare('SELECT * FROM hosted_keys WHERE key_id = ?').get(scope.hosted_key_id) : null
  if (key && (!row || key.vm_id !== vmId || key.binding_id !== row.binding_id)) return false
  if (!row) return true
  return row.state === 'active' && !!row.key_id && row.key_id === scope?.hosted_key_id &&
    now >= row.starts_at && now < row.ends_at
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
