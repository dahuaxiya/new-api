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

import { useStreamRequest } from '@/features/playground/hooks/use-stream-request'
import { handleServerError } from '@/lib/handle-server-error'
import { getServerErrorMessage } from '@/lib/server-error-message'

import { generateStudioImage, type StudioForm } from './api'

export type StudioEntry = {
  id: number
  prompt: string
  model: string
  content: string
  reasoning: string
  image?: string
  complete: boolean
  error?: string
}

/** Keep transcripts in memory and cancel transport on navigation/account changes.
 * A synchronous latch prevents two submissions before React renders the disabled button.
 */
export function useStudioRun(initialTurns: StudioEntry[]) {
  const [entries, setEntries] = useState<StudioEntry[]>(initialTurns)
  const [busy, setBusy] = useState(false)
  const locked = useRef(false)
  const imageAbort = useRef<AbortController | null>(null)
  const sequence = useRef(initialTurns.at(-1)?.id ?? 0)
  const stream = useStreamRequest('/api/retail/studio/chat/completions')
  useEffect(
    () => () => {
      imageAbort.current?.abort()
    },
    []
  )

  /** Finish only the active generation; a stopped stream cannot append to the next conversation. */
  const finish = (id: number, patch: Partial<StudioEntry> = {}) => {
    if (id !== sequence.current) return
    setEntries((current) =>
      current.map((item) => (item.id === id ? { ...item, ...patch } : item))
    )
    locked.current = false
    setBusy(false)
  }

  /** Submit a snapshot of the input; failed or interrupted turns never enter future model context. */
  const send = async (
    input: StudioForm,
    mode: 'text' | 'image',
    productId: string,
    onAccepted: (turn: StudioEntry) => void,
    onFailure: () => void,
    reference?: string
  ): Promise<void> => {
    if (locked.current) return
    locked.current = true
    setBusy(true)
    const id = ++sequence.current
    const turn = {
      id,
      prompt: input.prompt,
      model: input.model,
      content: '',
      reasoning: '',
      complete: false,
    }
    onAccepted(turn)
    setEntries((current) => [...current, turn])
    if (mode === 'image') {
      const controller = new AbortController()
      imageAbort.current = controller
      try {
        const image = await generateStudioImage(
          input,
          controller.signal,
          productId,
          reference
        )
        finish(id, { image, complete: true })
      } catch (error) {
        if (!controller.signal.aborted) {
          onFailure()
          handleServerError(error)
          finish(id, { error: getServerErrorMessage(error) })
        }
      }
      return
    }
    const messages = entries
      .filter((item) => item.complete && !item.image && !item.error)
      .flatMap((item) => [
        { role: 'user' as const, content: item.prompt },
        { role: 'assistant' as const, content: item.content },
      ])
    try {
      await stream.sendStreamRequest(
        {
          model: input.model,
          messages: [...messages, { role: 'user', content: input.prompt }],
          max_tokens: input.maxTokens,
          stream: true,
          stream_options: { include_usage: true },
        },
        (kind, chunk) => {
          if (id !== sequence.current) return
          const field = kind === 'reasoning' ? 'reasoning' : 'content'
          setEntries((current) =>
            current.map((item) =>
              item.id === id ? { ...item, [field]: item[field] + chunk } : item
            )
          )
        },
        () => finish(id, { complete: true }),
        (message) => {
          onFailure()
          finish(id, { error: message })
        },
        { 'X-Retail-Product': productId }
      )
    } catch (error) {
      onFailure()
      finish(id, { error: getServerErrorMessage(error) })
    }
  }

  /** Stop receiving, not billing: upstream work already dispatched may still incur a charge. */
  const stop = () => {
    stream.stopStream()
    imageAbort.current?.abort()
    finish(sequence.current, {
      error: 'Stopped. Check usage before trying again.',
    })
    sequence.current += 1
  }

  /** Discard only the local transcript, after any active transport has been stopped. */
  const reset = (next: StudioEntry[]) => {
    if (locked.current) return
    sequence.current = Math.max(sequence.current, next.at(-1)?.id ?? 0)
    setEntries(next)
  }
  return { entries, busy, send, stop, reset }
}
