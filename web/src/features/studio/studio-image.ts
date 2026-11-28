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
  /^\/api\/retail\/studio\/media\/[a-f0-9]{8}(?:-[a-f0-9]{4}){3}-[a-f0-9]{12}\/(?:(?:reference|result)(?:-(?:0|[1-9]\d*))?|chat-(?:0|[1-9]\d*)|video)$/

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
  const [attempt, setAttempt] = useState(0)
  const [failure, setFailure] = useState<{
    source: File | string
    attempt: number
  }>()
  /** 仅重试读取原有私有文件，调用方不会因此重新创建付费任务。 */
  function retry(): void {
    setAttempt((value) => value + 1)
  }
  const failed = failure?.source === source && failure?.attempt === attempt
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
        if (!abort.signal.aborted) setFailure({ source, attempt })
      })
    return () => {
      abort.abort()
      if (url) URL.revokeObjectURL(url)
    }
  }, [source, attempt])
  if (
    typeof source === 'string' &&
    /^data:image\/(png|jpeg|webp);base64,/.test(source)
  ) {
    return {
      url: source,
      format: source.slice('data:image/'.length, source.indexOf(';')),
      failed: false,
      retry,
    }
  }
  return loaded && loaded.source === source
    ? { ...loaded, failed: false, retry }
    : { url: undefined, format: undefined, failed, retry }
}
