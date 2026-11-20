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

/** A group owns its text history, so changing supplier never sends another group's conversation. */
export function readTurns(key: string | null, product: string) {
  return read(key && `${key}:chat:${product}`, turns) ?? []
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
