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
import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, expect, it } from 'vitest'

import { DEFAULT_LOGO } from '@/lib/constants'
import { applyFaviconToDom } from '@/lib/dom-utils'

import { BrandArtwork } from '../brand-artwork'

afterEach(cleanup)

/** 内置品牌只提供一个可访问名称，避免浅深色图形重复朗读。 */
it('内置品牌显示组合标志并只提供一个可访问名称', () => {
  render(<BrandArtwork name='luobocode' logo={DEFAULT_LOGO} />)
  const brand = screen.getByRole('img', { name: 'luobocode' })
  expect(screen.getAllByRole('img')).toHaveLength(1)
  expect(brand.querySelector('img')?.getAttribute('src')).toBe(
    '/brand/luobocode-ceramic-v13.webp'
  )
})

/** 折叠场景使用方形图形，不把横向字标压进图标容器。 */
it('紧凑模式使用独立坐姿猫与萝卜并保留品牌名称', () => {
  render(<BrandArtwork name='luobocode' logo={DEFAULT_LOGO} compact />)
  const brand = screen.getByRole('img', { name: 'luobocode' })
  expect(brand.querySelector('img')?.getAttribute('src')).toBe(
    '/brand/luobocode-ceramic-v13.webp'
  )
})

/** 后台换名或换图均应退出固定字标，避免覆盖管理员定制。 */
it.each([
  ['其他站点', DEFAULT_LOGO],
  ['luobocode', '/custom-logo.svg'],
])('自定义品牌 %s 保留配置的名称和图片', (name, logo) => {
  render(<BrandArtwork name={name} logo={logo} />)
  expect(screen.getByRole('img', { name })).toHaveAttribute('src', logo)
  expect(screen.getByText(name)).toBeVisible()
})

/** 两站的站名后缀仍使用相同品牌资产，避免上线后退回默认占位图。 */
it.each(['luobocode · 中转站', 'luobocode · 托管站'])(
  '站名 %s 复用品牌字标',
  (name) => {
    render(<BrandArtwork name={name} logo={DEFAULT_LOGO} />)
    expect(
      screen.getByRole('img', { name }).querySelector('img')
    ).toHaveAttribute('src', '/brand/luobocode-ceramic-v13.webp')
  }
)

/** 历史内置 Logo 配置升级到当前标签页图标，自定义图片仍由管理员掌握。 */
it('兼容已保存的默认图标且不覆盖自定义图标', () => {
  applyFaviconToDom(DEFAULT_LOGO)
  expect(document.querySelector('link[rel="icon"]')).toHaveAttribute(
    'href',
    '/brand/luobocode-favicon-v13.png'
  )
  applyFaviconToDom('/custom-icon.svg')
  expect(document.querySelectorAll('link[rel="icon"]')).toHaveLength(1)
  expect(document.querySelector('link[rel="icon"]')).toHaveAttribute(
    'href',
    '/custom-icon.svg'
  )
  document.querySelector('link[rel="icon"]')?.remove()
})
