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
import { useNavigate } from '@tanstack/react-router'
import type { MouseEvent } from 'react'

import { startRetailLoginTransition } from './retail-login-transition'

let pending = false

/** 首页的几个登录入口共享一次转场和登录事务，其他页面沿用普通链接。 */
export function useHomeLogin() {
  const navigate = useNavigate()

  /** 只接管当前页内的普通点击，组合键和新标签页使用链接本身。 */
  return async (event: MouseEvent<HTMLAnchorElement>) => {
    const source = document.querySelector(
      '.luobo-hero-sculpture .luobo-gateway-sculpture'
    )
    if (
      !source ||
      window.location.pathname !== '/' ||
      event.defaultPrevented ||
      event.button !== 0 ||
      event.metaKey ||
      event.ctrlKey ||
      event.shiftKey ||
      event.altKey
    ) {
      return
    }
    event.preventDefault()
    if (pending) return
    pending = true
    const reduced = window.matchMedia(
      '(prefers-reduced-motion: reduce)'
    ).matches
    const transition = startRetailLoginTransition(reduced ? null : source)
    try {
      // 页面导航与插画并行，身份服务只在登录页内启动。路由完成也不能截断动画。
      await Promise.all([navigate({ to: '/sign-in' }), transition.finished])
    } finally {
      pending = false
      transition.cleanup()
    }
  }
}
