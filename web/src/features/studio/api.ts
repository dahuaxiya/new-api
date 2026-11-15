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

import { api } from '@/lib/api'
import { requireServerSuccess } from '@/lib/server-error-message'

export const studioFormSchema = z.object({
  prompt: z.string().trim().min(1).max(32000),
  model: z.string().min(1),
  maxTokens: z.number().int().min(1).max(32768),
  size: z.enum(['1024x1024', '1024x1536', '1536x1024']),
  quality: z.enum(['low', 'medium', 'high']),
})
export type StudioForm = z.infer<typeof studioFormSchema>

/** Read the current owner's setup state; no key or conversation is returned. */
export async function getStudio(
  signal: AbortSignal
): Promise<{ token_id: number }> {
  return requireServerSuccess(
    (await api.get('/api/retail/studio', { signal })).data
  ).data
}

/** Explicit setup allocates one accounting identity but does not authorize spending. */
export async function openStudio(): Promise<{ token_id: number }> {
  return requireServerSuccess(
    (await api.post('/api/retail/studio/session', {})).data
  ).data
}

/** Only embed PNG payloads. Upstream URLs are not fetched or turned into download targets. */
export function imageDataURL(response: unknown): string {
  const parsed = z
    .object({
      data: z.union([
        z.array(z.object({ b64_json: z.string().optional() })),
        z.object({ b64_json: z.string().optional() }),
      ]),
    })
    .safeParse(response)
  if (!parsed.success) {
    throw new Error(
      'The image response is incomplete. Check usage before trying again.'
    )
  }
  const items = Array.isArray(parsed.data.data)
    ? parsed.data.data
    : [parsed.data.data]
  const payload = items.find((item) => item.b64_json)?.b64_json
  if (
    !payload ||
    payload.length > 64 * 1024 * 1024 ||
    !payload.startsWith('iVBORw0KGgo') ||
    !/^[A-Za-z0-9+/]+={0,2}$/.test(payload)
  ) {
    throw new Error(
      'The image response is incomplete. Check usage before trying again.'
    )
  }
  return `data:image/png;base64,${payload}`
}

/** Submit exactly one PNG generation. No automatic replay is safe after an uncertain paid request. */
export async function generateStudioImage(
  input: StudioForm,
  signal: AbortSignal
): Promise<string> {
  const response = await api.post(
    '/api/retail/studio/images/generations',
    {
      model: input.model,
      prompt: input.prompt,
      size: input.size,
      quality: input.quality,
      n: 1,
      output_format: 'png',
      stream: false,
    },
    { signal, timeout: 300000, singleUseAuthorization: true }
  )
  return imageDataURL(response.data)
}
