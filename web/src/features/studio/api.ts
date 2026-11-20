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
export async function getStudio(signal: AbortSignal): Promise<{
  token_id: number
  defaults: { max_request_cny: string; daily_budget_cny: string }
}> {
  return requireServerSuccess(
    (await api.get('/api/retail/studio', { signal })).data
  ).data
}

/** Opening protection initializes bounded defaults only when the owner has no saved policy. */
export async function openStudio(
  productId: string
): Promise<{ token_id: number }> {
  return requireServerSuccess(
    (await api.post('/api/retail/studio/session', { product_id: productId }))
      .data
  ).data
}

const incompleteImage =
  'The image response is incomplete. Check usage before trying again.'
const maxImageBytes = 48 * 1024 * 1024
// The supplier returns completed images from this bucket. Never send the session
// headers to a URL supplied in a model response or follow it to another origin.
const imageOrigin = 'https://pub-13fd1d3607a441b4a9d232b3a223f5f6.r2.dev'
const imageItem = z.object({
  b64_json: z.string().nullish(),
  url: z.string().nullish(),
})

/** Normalize inline raster images by signature so a supplier MIME label cannot make markup executable. */
function inlineImage(value: string): string | undefined {
  if (value.length > 64 * 1024 * 1024 + 32) return
  const payload = value.replace(/^data:image\/(?:png|jpeg|webp);base64,/i, '')
  if (
    !payload ||
    payload.length > 64 * 1024 * 1024 ||
    !/^[A-Za-z0-9+/]+={0,2}$/.test(payload)
  ) {
    return
  }
  let mime: string
  if (payload.startsWith('iVBORw0KGgo')) mime = 'png'
  else if (payload.startsWith('/9j/')) mime = 'jpeg'
  else if (
    payload.startsWith('UklGR') &&
    atob(payload.slice(0, 16)).slice(8, 12) === 'WEBP'
  ) {
    mime = 'webp'
  } else return
  return `data:image/${mime};base64,${payload}`
}

/** Read an already generated asset once, bounding streamed bytes even when Content-Length is absent. */
async function downloadStudioImage(
  url: URL,
  signal: AbortSignal
): Promise<string> {
  const response = await fetch(url.href, {
    credentials: 'omit',
    redirect: 'error',
    referrerPolicy: 'no-referrer',
    signal,
  })
  const reader = response.body?.getReader()
  if (
    !response.ok ||
    !reader ||
    Number(response.headers.get('Content-Length')) > maxImageBytes
  ) {
    await reader?.cancel()
    throw new Error(incompleteImage)
  }
  const chunks: ArrayBuffer[] = []
  let length = 0
  try {
    for (;;) {
      const { done, value } = await reader.read()
      if (done) break
      length += value.byteLength
      if (length > maxImageBytes) {
        await reader.cancel()
        throw new Error(incompleteImage)
      }
      chunks.push(new Uint8Array(value).buffer)
    }
  } finally {
    reader.releaseLock()
  }
  // Both preview and download use local bytes, even after the supplier link expires.
  const encoded = await new Promise<string>((resolve, reject) => {
    const file = new FileReader()
    file.addEventListener(
      'load',
      () => resolve(String(file.result).split(',')[1]),
      { once: true }
    )
    file.addEventListener('error', () => reject(new Error(incompleteImage)), {
      once: true,
    })
    file.readAsDataURL(new Blob(chunks))
  })
  const image = inlineImage(encoded)
  if (!image) throw new Error(incompleteImage)
  return image
}

/** Accept both Images API data shapes. Prefer inline bytes to avoid an unnecessary network read. */
export async function imageDataURL(
  response: unknown,
  signal: AbortSignal
): Promise<string> {
  const parsed = z
    .object({ data: z.union([z.array(imageItem), imageItem]) })
    .safeParse(response)
  if (!parsed.success) throw new Error(incompleteImage)
  const items = Array.isArray(parsed.data.data)
    ? parsed.data.data
    : [parsed.data.data]
  for (const item of items) {
    const image =
      inlineImage(item.b64_json ?? '') ||
      (item.url?.startsWith('data:') && inlineImage(item.url))
    if (image) return image
  }
  for (const { url: raw } of items) {
    if (!raw || !URL.canParse(raw)) continue
    const url = new URL(raw)
    if (
      url.origin === imageOrigin &&
      !url.username &&
      !url.password &&
      url.pathname.startsWith('/images/')
    ) {
      return downloadStudioImage(url, signal)
    }
  }
  throw new Error(incompleteImage)
}

/** Submit exactly one PNG generation. No automatic replay is safe after an uncertain paid request. */
export async function generateStudioImage(
  input: StudioForm,
  signal: AbortSignal,
  productId: string,
  reference?: string
): Promise<string> {
  const response = await api.post(
    reference
      ? '/api/retail/studio/images/edits'
      : '/api/retail/studio/images/generations',
    {
      model: input.model,
      prompt: input.prompt,
      size: input.size,
      quality: input.quality,
      n: 1,
      ...(reference ? { images: [{ image_url: reference }] } : {}),
      output_format: 'png',
      stream: false,
    },
    {
      signal,
      timeout: 300000,
      singleUseAuthorization: true,
      headers: { 'X-Retail-Product': productId },
    }
  )
  return imageDataURL(response.data, signal)
}
