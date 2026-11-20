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
import { api } from '@/lib/api'
import { requireServerSuccess } from '@/lib/server-error-message'
import { useAuthStore } from '@/stores/auth-store'

import { conversation, type Conversation } from './studio-session'

export type HistoryRow = Pick<
  Conversation,
  'id' | 'product' | 'mode' | 'updatedAt'
> & { title: string }
const endpoint = '/api/retail/studio/history'
const queues = new Map<string, Promise<void>>()

/** Wait for accepted writes from this tab before listing. Other devices read the server directly. */
export async function listCloud(key: string): Promise<HistoryRow[]> {
  await queues.get(key)
  const result = requireServerSuccess(
    (await api.get(endpoint, { skipErrorHandler: true })).data
  )
  if (!Array.isArray(result.data)) throw new Error('Invalid history list')
  return result.data
}

/** Download just the selected transcript or image, without invoking the model. */
export async function readCloud(id: string): Promise<Conversation> {
  return conversation.parse(
    (
      await api.get(`${endpoint}/${encodeURIComponent(id)}`, {
        skipErrorHandler: true,
      })
    ).data
  )
}

/** Serialize a tab's changes to keep a slow first upload from overwriting its completed turn.
 * Chunk boundaries are byte offsets so large images fit through the normal API gateway.
 */
export function writeCloud(
  key: string,
  item: Conversation,
  parent?: string
): Promise<void> {
  const owner = key.slice('studio.v1:'.length)
  const task = (queues.get(key) ?? Promise.resolve()).then(async () => {
    const bytes = new TextEncoder().encode(
      JSON.stringify({ ...item, mode: item.mode ?? 'text' })
    )
    const upload = crypto.randomUUID(),
      chunk = 2 * 1024 * 1024
    for (let offset = 0; offset < bytes.length; offset += chunk) {
      if (String(useAuthStore.getState().auth.user?.id) !== owner) {
        throw new Error('Account changed')
      }
      const part = bytes.subarray(offset, offset + chunk)
      const result = await api.put(
        `${endpoint}/${item.id}`,
        part.buffer.slice(part.byteOffset, part.byteOffset + part.byteLength),
        {
          params: {
            upload,
            offset,
            last: offset + part.length === bytes.length ? '1' : '0',
            parent,
            owner,
          },
          headers: { 'Content-Type': 'application/octet-stream' },
          skipErrorHandler: true,
        }
      )
      requireServerSuccess(result.data)
    }
  })
  const drained = task.catch(() => {})
  queues.set(key, drained)
  void drained.then(() => {
    if (queues.get(key) === drained) queues.delete(key)
  })
  return task
}

/** Remove a private server record only after earlier writes have finished. */
export async function deleteCloud(key: string, id: string) {
  await queues.get(key)
  requireServerSuccess(
    (
      await api.delete(`${endpoint}/${id}`, {
        params: { owner: key.slice('studio.v1:'.length) },
      })
    ).data
  )
}
