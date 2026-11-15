import { act, cleanup, render } from '@testing-library/react'
import { createPortal } from 'react-dom'
import { afterEach, expect, it } from 'vitest'

import { useSystemConfigStore } from '@/stores/system-config-store'

import { LuobocodeTheme } from '../luobocode-theme'

const initialConfig = useSystemConfigStore.getState().config

afterEach(() => {
  cleanup()
  useSystemConfigStore.getState().setConfig(initialConfig)
  document.body.removeAttribute('data-theme-preset')
})

/** 弹窗和路由共用文档主题，退出本站品牌时恢复原有样式且不改用户主题选择。 */
it('品牌主题覆盖 body portal 并响应站名变更', () => {
  useSystemConfigStore
    .getState()
    .setConfig({ systemName: 'luobocode · 中转站' })
  document.body.setAttribute('data-theme-preset', 'rose-garden')
  const { unmount } = render(
    <>
      <LuobocodeTheme />
      {createPortal(<div role='dialog' />, document.body)}
    </>
  )
  expect(
    document.querySelector('[role="dialog"]')?.closest('.luobo-shell')
  ).toBe(document.body)
  expect(document.body).toHaveAttribute('data-theme-preset', 'rose-garden')
  act(() => useSystemConfigStore.getState().setConfig({ systemName: 'Custom' }))
  expect(document.body).not.toHaveClass('luobo-shell')
  act(() =>
    useSystemConfigStore
      .getState()
      .setConfig({ systemName: 'luobocode · 托管站' })
  )
  expect(document.body).toHaveClass('luobo-shell')
  unmount()
  expect(document.body).not.toHaveClass('luobo-shell')
})
