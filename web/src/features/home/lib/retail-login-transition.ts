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
export type RetailLoginTargetRect = {
  left: number
  top: number
  width: number
  height: number
}

export type RetailLoginTransitionHandle = {
  finished: Promise<void>
  cleanup: () => void
}

const transitionDuration = 620

/**
 * 计算首页小猫在登录页的落点。桌面端对齐身份页左侧插画列，移动端缩成
 * 表单上方的品牌图；两边都沿用原图比例，避免跨域跳转前出现拉伸。
 * @param source 首页小猫当前在视口中的矩形。
 * @param viewportWidth 当前浏览器视口宽度。
 * @param viewportHeight 当前浏览器视口高度。
 * @return 登录页目标插画在视口中的矩形。
 */
export function getRetailLoginTargetRect(
  source: DOMRect,
  viewportWidth: number,
  viewportHeight: number
): RetailLoginTargetRect {
  const aspectRatio = source.width > 0 ? source.height / source.width : 2 / 3

  if (viewportWidth < 1024) {
    const width = Math.max(112, Math.min(148, viewportWidth * 0.36))
    return {
      left: (viewportWidth - width) / 2,
      top: Math.max(84, Math.min(112, viewportHeight * 0.12)),
      width,
      height: width * aspectRatio,
    }
  }

  const shellWidth = Math.min(1152, viewportWidth - 64)
  const width = Math.min((shellWidth - 80) / 2, viewportWidth * 0.42)
  return {
    left: (viewportWidth - shellWidth) / 2,
    top: Math.max(250, Math.min(viewportHeight * 0.38, viewportHeight * 0.46)),
    width,
    height: width * aspectRatio,
  }
}

/**
 * 在即将发生跨站登录时，把首页透明小猫复制到独立动画层。登录事务可以和
 * 动画并行，动画结束后只留下纸张色遮罩，避免把整块矩形背景横向拖入页面。
 * @param source 首页里带透明背景的小猫图片。
 * @return 动画完成信号和幂等清理函数。
 */
export function startRetailLoginTransition(
  source: Element | null
): RetailLoginTransitionHandle {
  if (
    !source ||
    typeof source.animate !== 'function' ||
    typeof source.getBoundingClientRect !== 'function'
  ) {
    return { finished: Promise.resolve(), cleanup: () => undefined }
  }

  const sourceRect = source.getBoundingClientRect()
  if (sourceRect.width <= 0 || sourceRect.height <= 0) {
    return { finished: Promise.resolve(), cleanup: () => undefined }
  }

  const target = getRetailLoginTargetRect(
    sourceRect,
    window.innerWidth,
    window.innerHeight
  )
  const overlay = document.createElement('div')
  const wash = document.createElement('div')
  const aura = document.createElement('div')
  const art = source.cloneNode(true) as HTMLElement
  overlay.className = 'luobo-login-transition'
  overlay.setAttribute('aria-hidden', 'true')
  wash.className = 'luobo-login-transition-wash'
  aura.className = 'luobo-login-transition-aura'
  art.className = 'luobo-login-transition-art'
  art.removeAttribute('alt')
  art.setAttribute('role', 'presentation')
  Object.assign(art.style, {
    left: `${sourceRect.left}px`,
    top: `${sourceRect.top}px`,
    width: `${sourceRect.width}px`,
    height: `${sourceRect.height}px`,
  })
  // 克隆层接管首帧后立即隐藏原图，否则原图会和移动中的副本同时出现。
  const sourceElement = source as HTMLElement
  const sourceVisibility = sourceElement.style.visibility
  sourceElement.style.visibility = 'hidden'
  positionLoginAura(aura, sourceRect, target)
  overlay.append(wash, aura, art)
  document.body.append(overlay)

  const deltaX = target.left - sourceRect.left
  const deltaY = target.top - sourceRect.top
  const scale = target.width / sourceRect.width
  const lift = Math.max(8, Math.min(14, Math.abs(deltaX) * 0.025))
  const artAnimation = art.animate(
    [
      {
        transform: 'translate3d(0, 0, 0) scale(1) rotate(0deg)',
        opacity: 1,
        offset: 0,
      },
      {
        transform: `translate3d(${deltaX * 0.44}px, ${deltaY * 0.3 - lift}px, 0) scale(${1 + (scale - 1) * 0.18}) rotate(-1deg)`,
        opacity: 1,
        offset: 0.4,
        easing: 'cubic-bezier(0.31, 0.32, 0.25, 1)',
      },
      {
        transform: `translate3d(${deltaX}px, ${deltaY}px, 0) scale(${scale}) rotate(-0.25deg)`,
        opacity: 1,
        offset: 0.82,
        easing: 'cubic-bezier(0.22, 0.68, 0.2, 1)',
      },
      {
        transform: `translate3d(${deltaX}px, ${deltaY}px, 0) scale(${scale * 0.985}) rotate(0deg)`,
        opacity: 0.94,
        offset: 1,
      },
    ],
    {
      duration: transitionDuration,
      fill: 'both',
      easing: 'linear',
    }
  )
  const auraAnimation = aura.animate(
    [
      { opacity: 0, scale: '0.72' },
      { opacity: 0.78, scale: '1', offset: 0.38 },
      { opacity: 0.3, scale: '1.16' },
    ],
    {
      duration: transitionDuration,
      fill: 'both',
      easing: 'cubic-bezier(0.22, 0.7, 0.2, 1)',
    }
  )
  const washAnimation = wash.animate(
    [
      { opacity: 0 },
      { opacity: 0.08, offset: 0.22 },
      { opacity: 0.58, offset: 0.7 },
      { opacity: 0.98 },
    ],
    {
      duration: transitionDuration,
      fill: 'both',
      easing: 'cubic-bezier(0.2, 0.66, 0.24, 1)',
    }
  )
  document.documentElement.dataset.retailLoginTransition = 'active'

  let cleaned = false
  const cleanup = () => {
    if (cleaned) return
    cleaned = true
    artAnimation.cancel()
    auraAnimation.cancel()
    washAnimation.cancel()
    overlay.remove()
    sourceElement.style.visibility = sourceVisibility
    delete document.documentElement.dataset.retailLoginTransition
  }
  return {
    finished: Promise.all([
      artAnimation.finished,
      auraAnimation.finished,
      washAnimation.finished,
    ])
      .then(() => undefined)
      .catch(() => undefined),
    cleanup,
  }
}

/** 光晕沿小猫中心移动，提前在目标位置铺出品牌暖光。 */
function positionLoginAura(
  aura: HTMLDivElement,
  source: DOMRect,
  target: RetailLoginTargetRect
) {
  const diameter = Math.max(260, Math.min(620, target.width * 1.42))
  const sourceCenterX = source.left + source.width / 2
  const sourceCenterY = source.top + source.height / 2
  const targetCenterX = target.left + target.width / 2
  const targetCenterY = target.top + target.height / 2
  Object.assign(aura.style, {
    left: `${sourceCenterX - diameter / 2}px`,
    top: `${sourceCenterY - diameter / 2}px`,
    width: `${diameter}px`,
    height: `${diameter}px`,
    translate: `${targetCenterX - sourceCenterX}px ${targetCenterY - sourceCenterY}px`,
  })
}
