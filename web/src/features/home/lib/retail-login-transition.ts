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

const transitionDuration = 490

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

  // 与身份页的 .luobo-entry-layout 共用原稿的 720px 断点及 404px 表单宽。
  if (viewportWidth <= 720) {
    const width = Math.max(112, Math.min(148, viewportWidth * 0.36))
    return {
      left: (viewportWidth - Math.min(viewportWidth - 44, 404)) / 2,
      top: 68 + Math.max(14, Math.min(40, viewportHeight * 0.04)),
      width,
      height: width * aspectRatio,
    }
  }

  const shellWidth = Math.min(1040, viewportWidth - 96)
  const gap = Math.max(48, Math.min(120, viewportWidth * 0.08))
  const width = ((shellWidth - gap) * 1.2) / 2.2
  return {
    left: (viewportWidth - shellWidth) / 2,
    top: Math.max(100, (viewportHeight - width * aspectRatio) / 2),
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
    document.documentElement.clientWidth,
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
  positionLoginAura(aura, target)
  overlay.append(wash, aura, art)
  document.body.append(overlay)

  const deltaX = target.left - sourceRect.left
  const deltaY = target.top - sourceRect.top
  const scale = target.width / sourceRect.width
  // 位移和尺寸走原稿的同一条曲线，单独的 translate 只负责九像素的浅弧。
  const artAnimation = art.animate(
    [
      {
        transform: 'translate3d(0, 0, 0) scale(1)',
      },
      {
        transform: `translate3d(${deltaX}px, ${deltaY}px, 0) scale(${scale})`,
      },
    ],
    {
      duration: transitionDuration,
      fill: 'both',
      easing: 'cubic-bezier(.27,.12,.16,1)',
    }
  )
  const arcAnimation = art.animate(
    [
      { translate: '0 0', easing: 'cubic-bezier(.3,0,.5,1)' },
      {
        translate: '0 -9px',
        offset: 0.38,
        easing: 'cubic-bezier(.45,0,.35,1)',
      },
      { translate: '0 0' },
    ],
    { duration: transitionDuration }
  )
  const auraAnimation = aura.animate([{ opacity: 0 }, { opacity: 0.3 }], {
    duration: transitionDuration,
    fill: 'both',
    easing: 'cubic-bezier(0.22, 0.7, 0.2, 1)',
  })
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
    arcAnimation.cancel()
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

/** 光晕固定在登录页插画中心，透明插画经过时只改变光晕亮度。 */
function positionLoginAura(
  aura: HTMLDivElement,
  target: RetailLoginTargetRect
) {
  const diameter = Math.max(260, Math.min(620, target.width * 1.42))
  const targetCenterX = target.left + target.width / 2
  const targetCenterY = target.top + target.height / 2
  Object.assign(aura.style, {
    left: `${targetCenterX - diameter / 2}px`,
    top: `${targetCenterY - diameter / 2}px`,
    width: `${diameter}px`,
    height: `${diameter}px`,
  })
}
