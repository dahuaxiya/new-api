import { afterEach, expect, it } from 'vitest'

import { hasSessionHint } from '../session-hint'

afterEach(() => {
  document.cookie = 'new_api_has_session=; Max-Age=0; Path=/'
  document.cookie = 'new_api_retail_has_session=; Max-Age=0; Path=/'
})

/** 中转站刷新页面时能发现自己的登录提示，无需借用托管站 Cookie。 */
it('recognizes a retail session without a hosting session cookie', () => {
  document.cookie = 'new_api_retail_has_session=1; Path=/'
  expect(hasSessionHint()).toBe(true)
})

/** 托管站沿用已有名称，两个提示都没有时公共首页不请求续期。 */
it('keeps the hosting hint compatible and skips refresh for anonymous visitors', () => {
  expect(hasSessionHint()).toBe(false)
  document.cookie = 'new_api_has_session=1; Path=/'
  expect(hasSessionHint()).toBe(true)
})
