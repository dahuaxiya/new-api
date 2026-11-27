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
export type StudioSelection = z.infer<typeof selection>
const current = z.object({ id: z.string().nullable() })
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
function read<T>(
  key: string | null,
  schema: z.ZodType<T>,
  persistent = false
): T | undefined {
  if (!key) return
  try {
    const raw = (persistent ? localStorage : sessionStorage).getItem(key)
    if (raw) return schema.parse(JSON.parse(raw))
  } catch {
    // The user can keep chatting when storage is blocked or older data is unreadable.
  }
}

/**
 * 读取账号最近使用的模式，或指定模式自己的分组和模型。
 * 旧版只有共享选择，仅在模式相符时兼容，避免把聊天分组带入生图。
 * @param key 当前账号的存储前缀；未登录时不读取。
 * @param mode 指定聊天或生图；省略时恢复最近使用的模式。
 * @return 对应模式的选择；尚未选择或存储不可用时为空。
 */
export function readSelection(
  key: string | null,
  mode?: StudioSelection['mode']
): StudioSelection | undefined {
  if (mode) {
    const scoped = key && `${key}:selection:${mode}`
    const saved = read(scoped, selection) ?? read(scoped, selection, true)
    if (saved?.mode === mode) return saved
  }
  const saved = read(key, selection) ?? read(key, selection, true)
  if (!mode || saved?.mode === mode) return saved
}

/**
 * 分模式保存选择，同时记录最近打开的模式，供刷新和重开页面恢复。
 * @param key 当前账号的存储前缀；未登录时不写入。
 * @param value 当前模式的分组和模型，不能覆盖另一模式的选择。
 * @return 两份选择均写入可用存储时为真，写入失败时为假。
 */
export function writeSelection(key: string | null, value: StudioSelection) {
  if (!key) return
  const scoped = writePreference(`${key}:selection:${value.mode}`, value)
  const recent = writePreference(key, value)
  return scoped && recent
}

/** null ID 表示用户明确打开空白会话，缺失指针时才可以从服务器历史找回。 */
export function readCurrent(
  key: string | null,
  scope: Pick<StudioSelection, 'mode' | 'product'>
) {
  const name = key && `${key}:current:${scope.mode}:${scope.product}`
  return read(name, current) ?? read(name, current, true)
}

/** 只跨刷新保存指针，完整记录仍从账号自己的服务器历史获取。 */
export function writeCurrent(
  key: string | null,
  scope: Pick<StudioSelection, 'mode' | 'product'>,
  id: string | null
) {
  return writePreference(
    key && `${key}:current:${scope.mode}:${scope.product}`,
    { id }
  )
}

/** 标签页和持久存储分别尝试，任一可用时都能恢复选择。 */
function writePreference(key: string | null, value: unknown) {
  if (!key) return
  const written = writeSession(key, value)
  try {
    localStorage.setItem(key, JSON.stringify(value))
    return true
  } catch {
    return written
  }
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
export function readActive(
  key: string | null,
  product: string,
  mode: StudioSelection['mode'] = 'text'
) {
  const current = read(
    key && `${key}:${mode === 'text' ? 'chat' : 'image'}:${product}`,
    conversation
  )
  const pointer = readCurrent(key, { mode, product })
  if (
    current?.product === product &&
    (current.mode ?? 'text') === mode &&
    (pointer === undefined || pointer.id === current.id) &&
    current.updatedAt > Date.now() - retention
  ) {
    return current
  }
}

/** 在当前标签缓存正文，收回标签后可用持久化 ID 从服务器恢复。 */
export function writeActive(key: string | null, item: Conversation) {
  return writeSession(
    key && `${key}:${item.mode === 'image' ? 'image' : 'chat'}:${item.product}`,
    item
  )
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

/** 同一标签能同时有多个服务端任务。旧版单任务指针保留到该任务结束。 */
export function readPendingRuns(key: string | null): Conversation[] {
  if (!key) return []
  try {
    const legacy = `${key}:running`
    const items = new Map<string, Conversation>()
    for (const name of Object.keys(sessionStorage)) {
      if (name !== legacy && !name.startsWith(`${legacy}:`)) continue
      const record = read(name, conversation)
      if (record?.running && record.updatedAt > Date.now() - retention) {
        items.set(record.id, record)
      }
    }
    return [...items.values()]
  } catch {
    return [] // 存储不可用时仍可从服务端历史恢复已经受理的任务。
  }
}

/** 只有旧版共享指针属于此任务时才允许读取或清除旧版发送意图。 */
export function ownsLegacyPointer(key: string, id: string) {
  return read(`${key}:running`, conversation)?.id === id
}
