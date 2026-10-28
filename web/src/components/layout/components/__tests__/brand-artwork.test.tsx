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

import { BrandArtwork } from '../brand-artwork'

afterEach(cleanup)

/** 内置品牌只提供一个可访问名称，避免浅深色图形重复朗读。 */
it('内置品牌显示组合标志并只提供一个可访问名称', () => {
  render(<BrandArtwork name='luobocode' logo={DEFAULT_LOGO} />)
  const brand = screen.getByRole('img', { name: 'luobocode' })
  expect(screen.getAllByRole('img')).toHaveLength(1)
  expect(brand.querySelector('img')?.getAttribute('src')).toBe(
    '/brand/luobocode-lockup-v11.png'
  )
})

/** 折叠场景使用方形图形，不把横向字标压进图标容器。 */
it('紧凑模式使用独立猫脸并保留品牌名称', () => {
  render(<BrandArtwork name='luobocode' logo={DEFAULT_LOGO} compact />)
  const brand = screen.getByRole('img', { name: 'luobocode' })
  expect(brand.querySelector('img')?.getAttribute('src')).toBe(
    '/brand/luobocode-mark-v11.png'
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
