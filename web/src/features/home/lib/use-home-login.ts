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

import { startSiteLogin } from '@/features/auth/lib/site-switch'
import { useStatus } from '@/hooks/use-status'

import { startRetailLoginTransition } from './retail-login-transition'

let pending = false

/** 首页的几个登录入口共享一次转场和登录事务，其他页面沿用普通链接。 */
export function useHomeLogin() {
  const { status } = useStatus()
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
    /** 离开时恢复源图，使浏览器从往返缓存还原首页时不会显示遮罩。 */
    const reset = () => {
      pending = false
      transition.cleanup()
      window.removeEventListener('pageshow', restoreHome)
    }
    /** 仅在往返缓存恢复首页时清理，离站前清理会让最后一帧闪回首页。 */
    const restoreHome = (event: PageTransitionEvent) => {
      if (event.persisted) reset()
    }
    window.addEventListener('pageshow', restoreHome)
    try {
      // 身份事务与动画并行。两者都完成后才离开当前文档。
      if (status?.site_sso?.consumer) {
        const [destination] = await Promise.all([
          startSiteLogin(status.site_sso.issuer_url, '/dashboard/overview'),
          transition.finished,
        ])
        window.location.replace(destination)
        return
      }
      await transition.finished
    } catch {
      // 交接失败时由正式登录路由展示重试或限流提示。
      await transition.finished
    }
    try {
      await navigate({ to: '/sign-in' })
    } finally {
      reset()
    }
  }
}
