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
import { describe, expect, it } from 'vitest'

import { getRetailLoginTargetRect } from '../retail-login-transition'

const sourceRect = {
  left: 704,
  top: 252,
  width: 472,
  height: 315,
} as DOMRect

describe('retail login transition', () => {
  it('moves the desktop sculpture toward the login artwork column', () => {
    const target = getRetailLoginTargetRect(sourceRect, 1280, 900)

    expect(target.left).toBeLessThan(sourceRect.left)
    expect(target.left).toBeGreaterThan(48)
    expect(target.top).toBeGreaterThan(250)
    expect(target.width).toBeGreaterThan(460)
    expect(target.width).toBeLessThanOrEqual(560)
  })

  it('aligns the mobile sculpture with the left edge of the email field', () => {
    const mobileSource = {
      left: 20,
      top: 210,
      width: 340,
      height: 227,
    } as DOMRect
    const target = getRetailLoginTargetRect(mobileSource, 390, 844)

    expect(target.width).toBeGreaterThanOrEqual(112)
    expect(target.width).toBeLessThanOrEqual(148)
    expect(target.left).toBe(22)
    expect(target.top).toBeLessThan(mobileSource.top)
  })
})
