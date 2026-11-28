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
import { beforeEach, describe, expect, it } from 'vitest'

import {
  readActive,
  readSelection,
  writeActive,
  writeSelection,
} from '../studio-session'

beforeEach(() => {
  localStorage.clear()
  sessionStorage.clear()
})

describe('video workspace persistence', () => {
  it('restores separate chat, image and video group selections', () => {
    const key = 'studio.v1:7'
    for (const mode of ['text', 'image', 'video'] as const) {
      writeSelection(key, {
        mode,
        product: `group-${mode}`,
        model: `model-${mode}`,
      })
    }
    for (const mode of ['text', 'image', 'video'] as const) {
      expect(readSelection(key, mode)).toEqual({
        mode,
        product: `group-${mode}`,
        model: `model-${mode}`,
      })
    }
  })

  it('keeps video history when an image in the same group is opened', () => {
    const key = 'studio.v1:7'
    for (const mode of ['video', 'image'] as const) {
      writeActive(key, {
        id: mode,
        mode,
        product: 'ultra',
        updatedAt: Date.now(),
        entries: [
          {
            id: 1,
            prompt: mode,
            model: mode,
            content: '',
            reasoning: '',
            complete: true,
            ...(mode === 'video' ? { video: '/private/video' } : {}),
          },
        ],
      })
    }
    expect(readActive(key, 'ultra', 'video')?.entries[0].video).toBe(
      '/private/video'
    )
    expect(readActive(key, 'ultra', 'image')?.id).toBe('image')
  })
})
