/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { z } from 'zod'

const selection = z.object({
  mode: z.enum(['text', 'image']),
  product: z.string(),
  model: z.string(),
})
const turns = z.array(
  z.object({
    id: z.number().int(),
    prompt: z.string(),
    model: z.string(),
    content: z.string(),
    reasoning: z.string(),
    complete: z.boolean(),
    error: z.string().optional(),
    image: z.string().optional(),
    reference: z.string().optional(),
  })
)

/** Session storage keeps tabs independent. Invalid or unavailable storage starts a fresh workspace. */
function read<T>(key: string | null, schema: z.ZodType<T>): T | undefined {
  if (!key) return
  try {
    const raw = sessionStorage.getItem(key)
    if (raw) return schema.parse(JSON.parse(raw))
  } catch {
    // The user can keep chatting when storage is blocked or older data is unreadable.
  }
}

/** Recover only the model choices for this signed-in account. */
export function readSelection(key: string | null) {
  return read(key, selection)
}

export const conversation = z.object({
  running: z.boolean().optional(),
  mode: z.enum(['text', 'image']).optional(),
  id: z.string(),
  product: z.string(),
  updatedAt: z.number(),
  entries: turns.nonempty(),
})

export type Conversation = z.infer<typeof conversation>
const archive = z.array(conversation)
const retention = 30 * 24 * 60 * 60 * 1000
const maxBytes = 5 * 1024 * 1024

/** 旧版标签把每组记录直接存成数组，升级时保留仍在标签中的对话。 */
export function readLegacy(key: string | null, product: string) {
  return read(key && `${key}:chat:${product}`, turns) ?? []
}

/** 当前标签按分组记住打开的对话。旧版数组由首次挂载时的迁移处理。 */
export function readActive(key: string | null, product: string) {
  const current = read(key && `${key}:chat:${product}`, conversation)
  if (
    current?.product === product &&
    current.updatedAt > Date.now() - retention
  ) {
    return current
  }
}

/** 读取当前账号的文字历史。解析失败时保留原件，由界面报告存储错误。 */
export function readHistory(key: string): Conversation[] {
  const raw = localStorage.getItem(`${key}:history`)
  if (!raw) return []
  return archive
    .parse(JSON.parse(raw))
    .filter((item) => item.updatedAt > Date.now() - retention)
}

/** 每次发送产生一个新版本。两页从同一版本续聊时，各自保留独立分支。 */
export async function saveHistory(
  key: string,
  item: Conversation,
  parentId?: string
) {
  const archiveKey = `${key}:history`
  const update = () => {
    const previous = readHistory(key).filter(
      (row) => row.id !== item.id && row.id !== parentId
    )
    const rows = [item, ...previous]
      .sort((a, b) => b.updatedAt - a.updatedAt)
      .slice(0, 20)
    // 先计算保留范围，再原子替换。单段超限或浏览器拒写时，旧记录仍在。
    const encoded: string[] = []
    let bytes = 2 // JSON 数组的方括号
    let included = false
    for (const row of rows) {
      const json = JSON.stringify(row)
      bytes += new Blob([json]).size + (encoded.length ? 1 : 0)
      if (bytes > maxBytes) break
      encoded.push(json)
      if (row.id === item.id) included = true
    }
    if (!included) throw new Error('Conversation exceeds local history quota')
    localStorage.setItem(archiveKey, `[${encoded.join(',')}]`)
  }
  // 同源标签页共用 localStorage；支持 Web Locks 的浏览器按账号顺序提交读改写。
  if (navigator.locks) await navigator.locks.request(archiveKey, update)
  else update()
}

/** Empty conversations remove their saved history. Storage failures leave the live page usable. */
export function writeSession(key: string | null, value: unknown) {
  if (!key) return
  try {
    if (value === null) sessionStorage.removeItem(key)
    else sessionStorage.setItem(key, JSON.stringify(value))
    return true
  } catch {
    return false
  }
}

/** 升级当前标签中所有旧分组的文字会话；归档失败时恢复旧格式供下次重试。 */
export async function migrateHistory(key: string) {
  const prefix = `${key}:chat:`
  for (const tabKey of Object.keys(sessionStorage).filter((name) =>
    name.startsWith(prefix)
  )) {
    const entries = read(tabKey, turns)
    if (!entries?.length) continue
    const snapshot = {
      id: crypto.randomUUID(),
      updatedAt: Date.now(),
      product: tabKey.slice(prefix.length),
      entries,
    }
    // 同步标记使首次渲染和 Strict Mode 重入都使用同一个会话 ID。
    if (!writeSession(tabKey, snapshot)) {
      throw new Error('Session storage unavailable')
    }
    try {
      await saveHistory(key, snapshot)
    } catch (error) {
      if (readActive(key, snapshot.product)?.id === snapshot.id) {
        writeSession(tabKey, entries)
      }
      throw error
    }
  }
}

/** 清理已同步或已删除的旧版归档。与旧版写入使用同一把锁，避免漏掉其他标签页的聊天。 */
export async function removeLocalHistory(key: string, id: string) {
  const archiveKey = `${key}:history`
  const remove = () => {
    const rows = readHistory(key)
    if (rows.some((row) => row.id === id)) {
      localStorage.setItem(
        archiveKey,
        JSON.stringify(rows.filter((row) => row.id !== id))
      )
    }
  }
  if (navigator.locks) await navigator.locks.request(archiveKey, remove)
  else remove()
}

/** A reload reattaches by UUID. Prefer the full local intent until the server acknowledges it. */
export function readRunning(key: string | null) {
  if (!key) return
  const pointer = read(`${key}:running`, conversation)
  if (!pointer || pointer.updatedAt < Date.now() - retention) return
  const intent = read(`${key}:intent`, z.object({ record: conversation }))
  return intent?.record.id === pointer.id ? intent.record : pointer
}
