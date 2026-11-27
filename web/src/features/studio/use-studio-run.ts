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
import { useEffect, useRef, useState, type RefObject } from 'react'

import { api } from '@/lib/api'
import { getServerErrorMessage } from '@/lib/server-error-message'

import type { StudioForm } from './api'
import { isStudioMedia, readStudioMedia } from './studio-image'
import {
  type Conversation,
  ownsLegacyPointer,
  writeSession,
} from './studio-session'

export type StudioEntry = Conversation['entries'][number]
type Intent = Pick<StudioForm, 'maxTokens' | 'size' | 'quality'> & {
  record: Conversation
  parent?: string
  outputFormat?: StudioForm['outputFormat']
  outputCompression?: number
}
type Cursor = { content: number; reasoning: number }
const emptyCursor = (): Cursor => ({ content: 0, reasoning: 0 })

type Run = {
  record: Conversation
  parent?: string
  controller: AbortController
  stopping: boolean
  reconnecting: boolean
  reference?: File | string
}

/** 每次付费请求独立观察；只把所选任务的变化写到屏幕，离开页面不取消服务端生成。 */
export function useStudioRun(
  initialTurns: StudioEntry[],
  key: string | null,
  initial: Conversation | undefined,
  pendingAtMount: Conversation[],
  selected: RefObject<string | undefined>,
  onFinish: (item: Conversation, savedId?: string) => void
) {
  const [entries, setEntries] = useState(initialTurns)
  const [sentReference, setSentReference] = useState<File | string>()
  const [busy, setBusy] = useState(Boolean(initial?.running))
  const [reconnecting, setReconnecting] = useState(false)
  const jobs = useRef(new Map<string, Run>())
  const [pending, setPending] = useState<Run[]>([])
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
    if (jobs.current.has(record.id) || !record.entries.length) return
    const controller = new AbortController()
    const { signal } = controller
    const job: Run = {
      record,
      parent: intent?.parent,
      controller,
      reference,
      stopping: false,
      reconnecting: false,
    }
    jobs.current.set(record.id, job)
    setPending([...jobs.current.values()])
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
    if (selected.current === record.id) {
      setBusy(true)
      setReconnecting(false)
      setEntries(record.entries)
      setSentReference(reference)
    }
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
          if (key) {
            writeSession(`${key}:intent:${record.id}`, null)
            if (ownsLegacyPointer(key, record.id)) {
              writeSession(`${key}:intent`, null)
            }
          }
        }
        if (job.stopping) {
          await api.delete(endpoint, {
            params: { owner },
            signal,
            skipErrorHandler: true,
          })
          job.stopping = false
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
        job.record = { ...record, entries: [...previous, last] }
        job.reconnecting = false
        if (selected.current === record.id) {
          setEntries(job.record.entries)
          setReconnecting(false)
        }
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
        job.reconnecting = true
        if (selected.current === record.id) setReconnecting(true)
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
    jobs.current.delete(record.id)
    setPending([...jobs.current.values()])
    // 用户可能已经切到另一会话。旧任务只更新自己的记录，不能清掉当前草稿或停止按钮。
    if (selected.current === record.id) {
      setEntries(result.entries)
      if (last.reference) setSentReference(undefined)
      setBusy(false)
      setReconnecting(false)
    }
    if (key) {
      writeSession(`${key}:intent:${record.id}`, null)
      writeSession(`${key}:running:${record.id}`, null)
      if (ownsLegacyPointer(key, record.id)) {
        writeSession(`${key}:intent`, null)
        writeSession(`${key}:running`, null)
      }
    }
    // A rejected upload has no server snapshot. Retrying must use the last accepted parent.
    finish.current(result, accepted ? record.id : intent?.parent)
  }
  const resume = useRef(follow)
  useEffect(() => {
    resume.current = follow
  })
  useEffect(() => {
    const records = [...pendingAtMount]
    if (initial?.running && !records.some((item) => item.id === initial.id)) {
      records.push(initial)
    }
    const observers = jobs.current
    for (const record of records) {
      let intent: Intent | undefined
      try {
        const raw =
          sessionStorage.getItem(`${key}:intent:${record.id}`) ??
          (key && ownsLegacyPointer(key, record.id)
            ? sessionStorage.getItem(`${key}:intent`)
            : null)
        const stored = JSON.parse(raw ?? 'null')
        if (stored?.record?.id === record.id) intent = stored
      } catch {
        /* 缺少发送意图时只读服务端，不重新提交附件或新建 UUID。 */
      }
      void resume.current(record, intent)
    }
    return () => {
      for (const job of observers.values()) job.controller.abort()
      observers.clear()
    }
  }, [initial, pendingAtMount, key])
  /** Persist a small recovery pointer before sending, plus the original intent where device storage permits it. */
  function send(
    record: Conversation,
    input: StudioForm,
    parent?: string,
    reference?: File | string
  ) {
    // 校验与登记同步执行，快速双击即使发生在 React 重绘前也不会创建第二个付费请求。
    if (selected.current !== parent || (parent && jobs.current.has(parent))) {
      return false
    }
    selected.current = record.id
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
      writeSession(`${key}:running:${record.id}`, {
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
        `${key}:intent:${record.id}`,
        reference instanceof File ||
          isStudioMedia(reference) ||
          (record.mode === 'text' && reference)
          ? null
          : intent
      )
    }
    void follow(record, intent, reference)
    return true
  }
  /** 优先使用本标签最新进度；提交回执到达前，历史列表可能仍然指向上一轮。 */
  function lookup(id?: string | null) {
    if (!id) return undefined
    return (
      jobs.current.get(id) ??
      [...jobs.current.values()].find((job) => job.parent === id)
    )?.record
  }
  /** 切换只更换所观察的记录；打开历史不会提交生成请求。 */
  function reset(item?: Conversation) {
    selected.current = item?.id
    const job = item && jobs.current.get(item.id)
    setEntries(job?.record.entries ?? item?.entries ?? [])
    setSentReference(job?.reference)
    setBusy(Boolean(job || item?.running))
    setReconnecting(job?.reconnecting ?? false)
    if (item?.running && !job) void follow(item)
  }
  /** 只给点击时正在看的任务登记停止意图；即使稍后切换会话也不会取消别的任务。 */
  function stop() {
    const job = selected.current && jobs.current.get(selected.current)
    if (job) job.stopping = true
  }
  return {
    entries,
    sentReference,
    busy,
    reconnecting,
    pending,
    lookup,
    send,
    reset,
    stop,
  }
}
