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
import { act, cleanup, renderHook, waitFor } from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'

import { useStudioImage } from '../studio-image'

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

/** 私有媒体下载失败后允许重试，成功后清除错误并释放对象 URL。 */
it('exposes failed video retrieval and retries without submitting another generation', async () => {
  const get = vi
    .spyOn(api, 'get')
    .mockRejectedValueOnce(new Error('offline'))
    .mockResolvedValueOnce({ data: new Blob(['video'], { type: 'video/mp4' }) })
  vi.spyOn(URL, 'createObjectURL').mockReturnValue('blob:retried-video')
  const revoke = vi.spyOn(URL, 'revokeObjectURL')
  const path =
    '/api/retail/studio/media/11111111-1111-4111-8111-111111111111/video'
  const hook = renderHook(() => useStudioImage(path))
  await waitFor(() => expect(hook.result.current.failed).toBe(true))
  act(() => hook.result.current.retry())
  await waitFor(() =>
    expect(hook.result.current.url).toBe('blob:retried-video')
  )
  expect(hook.result.current.failed).toBe(false)
  expect(get).toHaveBeenCalledTimes(2)
  hook.unmount()
  expect(revoke).toHaveBeenCalledWith('blob:retried-video')
})
