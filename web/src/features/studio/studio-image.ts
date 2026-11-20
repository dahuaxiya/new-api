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
import { useEffect, useState } from 'react'

import { api } from '@/lib/api'

const mediaPath =
  /^\/api\/retail\/studio\/media\/[a-f0-9]{8}(?:-[a-f0-9]{4}){3}-[a-f0-9]{12}\/(?:reference|result)$/

/** Only request same-origin media links with the server's fixed UUID and role shape. */
export function isStudioMedia(value?: File | string): value is string {
  return typeof value === 'string' && mediaPath.test(value)
}

/** The dashboard bearer token stays in the API client and is never put in an image URL. */
export async function readStudioMedia(
  path: string,
  signal: AbortSignal
): Promise<Blob> {
  if (!isStudioMedia(path)) throw new Error('Invalid studio image path')
  const { data } = await api.get<Blob>(path, {
    responseType: 'blob',
    signal,
    disableDuplicate: true,
  })
  return data
}

/** Use revocable object URLs for original files and authenticated media while retaining old data URLs. */
export function useStudioImage(source?: File | string) {
  const [loaded, setLoaded] = useState<{
    source: File | string
    url: string
    format: string
  }>()
  useEffect(() => {
    if (!source || (typeof source === 'string' && !isStudioMedia(source))) {
      return
    }
    const abort = new AbortController()
    let url: string | undefined
    const media =
      typeof source === 'string'
        ? readStudioMedia(source, abort.signal)
        : Promise.resolve(source)
    void media
      .then((file) => {
        if (abort.signal.aborted) return
        const format = file.type.split('/')[1] || 'png'
        url = URL.createObjectURL(file)
        setLoaded({ source, url, format })
      })
      .catch(() => {
        // The API client reports failed authenticated downloads; selection remains usable.
      })
    return () => {
      abort.abort()
      if (url) URL.revokeObjectURL(url)
    }
  }, [source])
  if (
    typeof source === 'string' &&
    /^data:image\/(png|jpeg|webp);base64,/.test(source)
  ) {
    return {
      url: source,
      format: source.slice('data:image/'.length, source.indexOf(';')),
    }
  }
  return loaded && loaded.source === source
    ? loaded
    : { url: undefined, format: undefined }
}
