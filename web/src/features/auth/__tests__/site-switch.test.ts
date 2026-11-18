import { afterEach, expect, it, vi } from 'vitest'

import { consumeSiteSwitch, hostingSwitchTarget } from '../lib/site-switch'

afterEach(() => {
  sessionStorage.clear()
  vi.useRealTimers()
})

/** 只有发起切站的身份能够使用这次点击，返回或复制链接不会再次授权。 */
it('consumes a hosting click once and only for the account that clicked', () => {
  const url = new URL(hostingSwitchTarget('https://retail.example.test', 17))
  const nonce = url.hash.slice('#switch='.length)
  expect(url.pathname).toBe('/sign-in')
  expect(consumeSiteSwitch(nonce, 19)).toBe(false)
  expect(consumeSiteSwitch(nonce, 17)).toBe(true)
  expect(consumeSiteSwitch(nonce, 17)).toBe(false)
})

/** 过期点击须走普通授权，不能与后来的手工登录共用。 */
it('expires an unused click', () => {
  vi.useFakeTimers()
  const target = new URL(hostingSwitchTarget('https://retail.example.test', 17))
  vi.advanceTimersByTime(61_000)
  expect(consumeSiteSwitch(target.hash.slice('#switch='.length), 17)).toBe(
    false
  )
})
