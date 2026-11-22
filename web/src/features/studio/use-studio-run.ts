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
import { useEffect, useRef, useState } from 'react'

import { api } from '@/lib/api'
import { getServerErrorMessage } from '@/lib/server-error-message'

import type { StudioForm } from './api'
import { isStudioMedia, readStudioMedia } from './studio-image'
import { type Conversation, writeSession } from './studio-session'

export type StudioEntry = Conversation['entries'][number]
type Intent = Pick<StudioForm, 'maxTokens' | 'size' | 'quality'> & {
  record: Conversation
  parent?: string
  outputFormat?: StudioForm['outputFormat']
  outputCompression?: number
}
type Cursor = { content: number; reasoning: number }
const emptyCursor = (): Cursor => ({ content: 0, reasoning: 0 })

/** Follow one durable generation. Unmount only detaches the observer; explicit Stop cancels the worker. */
export function useStudioRun(
  initialTurns: StudioEntry[],
  key: string | null,
  initial: Conversation | undefined,
  onFinish: (item: Conversation, savedId?: string) => void
) {
  const [entries, setEntries] = useState(initialTurns)
  const [sentReference, setSentReference] = useState<File | string>()
  const [busy, setBusy] = useState(Boolean(initial?.running))
  const [reconnecting, setReconnecting] = useState(false)
  const current = useRef<AbortController | null>(null)
  const active = useRef('')
  const stopRequested = useRef(false)
  const finish = useRef(onFinish)
  useEffect(() => {
    finish.current = onFinish
  }, [onFinish])

  /** One UUID survives lost acknowledgements. GET cursors advance only after consuming a response. */
  async function follow(
    record: Conversation,
    intent?: Intent,
    reference?: File | string
  ) {
    current.current?.abort()
    const controller = new AbortController()
    const { signal } = controller
    current.current = controller
    active.current = record.id
    stopRequested.current = false
    const endpoint = `/api/retail/studio/runs/${record.id}`
    const owner = key?.slice('studio.v1:'.length)
    let accepted = !intent
    let cursor = emptyCursor()
    const tail = record.entries.at(-1)
    if (!tail) return
    let last: StudioEntry = {
      ...tail,
      content: '',
      reasoning: '',
    }
    let previous = record.entries.slice(0, -1)
    setBusy(true)
    setEntries(record.entries)
    // 本地附件只用于当前消息的即时预览，历史记录只保存服务端提供的图片引用。
    setSentReference(reference)
    let backoff = 0
    let request: Intent | FormData | undefined
    while (!signal.aborted) {
      try {
        if (!accepted) {
          if (!request && intent) {
            let file: Blob | string | undefined = isStudioMedia(reference)
              ? await readStudioMedia(reference, signal)
              : reference
            // Old transcripts may embed the raster. Chat accepts only multipart attachments on the new turn.
            if (
              record.mode === 'text' &&
              typeof file === 'string' &&
              /^data:image\/(png|jpeg|webp);base64,/.test(file)
            ) {
              file = await (await fetch(file, { signal })).blob()
            }
            if (file instanceof Blob) {
              const form = new FormData()
              form.append('intent', JSON.stringify(intent))
              form.append('image', file, 'reference')
              request = form
            } else {
              request = intent
            }
          }
          await api.post(endpoint, request, {
            params: { owner },
            signal,
            timeout: request instanceof FormData ? 300000 : 15000,
            skipErrorHandler: true,
          })
          accepted = true
          // Once acknowledged, a tiny pointer suffices even when the image exceeds sessionStorage quota.
          if (signal.aborted) return
          if (key) writeSession(`${key}:intent`, null)
        }
        if (stopRequested.current) {
          await api.delete(endpoint, {
            params: { owner },
            signal,
            skipErrorHandler: true,
          })
          stopRequested.current = false
        }
        const { data } = await api.get(endpoint, {
          params: cursor,
          signal,
          timeout: 20000,
          skipErrorHandler: true,
          disableDuplicate: true,
        })
        if (signal.aborted) return
        if (!data.running) {
          previous = data.record.entries.slice(0, -1)
          last = data.record.entries.at(-1)
          break
        }
        last = {
          ...last,
          content: last.content + data.content,
          reasoning: last.reasoning + data.reasoning,
        }
        cursor = { content: data.nextContent, reasoning: data.nextReasoning }
        setEntries([...previous, last])
        setReconnecting(false)
        backoff = 0
      } catch (error) {
        if (signal.aborted) return
        const status = (error as { response?: { status: number } }).response
          ?.status
        if (
          (status &&
            status < 500 &&
            status !== 408 &&
            (status !== 429 || !accepted)) ||
          status === 507
        ) {
          last = {
            ...last,
            error: getServerErrorMessage(error),
            complete: false,
          }
          break
        }
        setReconnecting(true)
        backoff = Math.min(5000, backoff ? 2 * backoff : 800)
      }
      await new Promise((resolve) =>
        setTimeout(resolve, backoff || (record.mode === 'image' ? 1600 : 650))
      )
    }
    if (signal.aborted) return
    const result = {
      ...record,
      running: false,
      entries: [...previous, last],
      updatedAt: Date.now(),
    }
    setEntries(result.entries)
    // 服务端保存后改用持久引用。上传失败时仍可在当前失败消息里看到原图。
    if (last.reference) setSentReference(undefined)
    setBusy(false)
    setReconnecting(false)
    active.current = ''
    if (key) {
      writeSession(`${key}:intent`, null)
      writeSession(`${key}:running`, null)
    }
    // A rejected upload has no server snapshot. Retrying must use the last accepted parent.
    finish.current(result, accepted ? record.id : intent?.parent)
  }
  const resume = useRef(follow)
  useEffect(() => {
    resume.current = follow
  })
  useEffect(() => {
    if (initial?.running) {
      let intent: Intent | undefined
      try {
        const stored = JSON.parse(
          sessionStorage.getItem(`${key}:intent`) ?? 'null'
        )
        if (stored?.record?.id === initial.id) intent = stored
      } catch {
        /* The server record can still be read after local storage loss. */
      }
      void resume.current(initial, intent)
    }
    return () => current.current?.abort()
  }, [initial, key])
  /** Persist a small recovery pointer before sending, plus the original intent where device storage permits it. */
  function send(
    record: Conversation,
    input: StudioForm,
    parent?: string,
    reference?: File | string
  ) {
    if (active.current) return
    const intent = {
      // Image history and original files are already on the server. Send only the new turn.
      record:
        record.mode === 'image'
          ? { ...record, entries: record.entries.slice(-1) }
          : record,
      parent,
      maxTokens: input.maxTokens,
      size: input.size,
      quality: input.quality,
      ...(record.mode === 'image'
        ? {
            outputFormat: input.outputFormat,
            ...(input.outputFormat !== 'png' && input.outputCompression !== ''
              ? { outputCompression: Number(input.outputCompression) }
              : {}),
          }
        : {}),
    }
    if (key) {
      writeSession(`${key}:running`, {
        ...record,
        entries: record.entries.map((entry) => ({
          ...entry,
          reference: entry.reference?.startsWith('data:')
            ? undefined
            : entry.reference,
          image: entry.image?.startsWith('data:') ? undefined : entry.image,
        })),
      })
      // A file remains in memory for same-page retries. After navigation, only resume a claimed job.
      writeSession(
        `${key}:intent`,
        reference instanceof File ||
          isStudioMedia(reference) ||
          (record.mode === 'text' && reference)
          ? null
          : intent
      )
    }
    void follow(record, intent, reference)
  }
  /** Archive lookup can attach from a different device; never submit when opening history. */
  function reset(next: StudioEntry[], item?: Conversation) {
    if (active.current) return
    setEntries(next)
    setSentReference(undefined)
    if (item?.running) {
      if (key) {
        writeSession(`${key}:running`, {
          ...item,
          entries: item.entries.map((entry) => ({
            ...entry,
            reference: undefined,
            image: undefined,
          })),
        })
      }
      void follow(item)
    }
  }
  /** Acknowledgement can race a stop click: the observer sends cancellation after acceptance. */
  function stop() {
    stopRequested.current = true
  }
  return { entries, sentReference, busy, reconnecting, send, reset, stop }
}
