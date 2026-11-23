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

import { loginFormSchema, registerFormSchema } from '../constants'

describe('public registration password policy', () => {
  it('rejects short registration passwords while keeping existing-account login available', () => {
    const input = {
      username: 'new-user',
      email: 'user@example.com',
      password: 'old-pass',
      confirmPassword: 'old-pass',
    }
    expect(registerFormSchema.safeParse(input).success).toBe(false)
    expect(loginFormSchema.safeParse(input).success).toBe(true)
  })

  it('counts Unicode characters and preserves spaces in a valid passphrase', () => {
    const password = '  明月照松林山川溪水远行人  '
    const result = registerFormSchema.safeParse({
      username: 'new-user',
      password,
      confirmPassword: password,
    })
    expect(result.success).toBe(true)
    if (result.success) expect(result.data.password).toBe(password)
  })

  it('rejects a malformed email without making an empty optional email invalid', () => {
    expect(
      registerFormSchema.safeParse({
        username: 'new-user',
        email: 'not-an-email',
        password: 'private registration passphrase',
        confirmPassword: 'private registration passphrase',
      }).success
    ).toBe(false)
    expect(
      registerFormSchema.safeParse({
        username: 'new-user',
        email: '',
        password: 'private registration passphrase',
        confirmPassword: 'private registration passphrase',
      }).success
    ).toBe(true)
  })

  it('rejects a mismatched confirmation and an overlong password', () => {
    expect(
      registerFormSchema.safeParse({
        username: 'new-user',
        password: 'private registration passphrase',
        confirmPassword: 'another registration passphrase',
      }).success
    ).toBe(false)
    const password = '界'.repeat(129)
    expect(
      registerFormSchema.safeParse({
        username: 'new-user',
        password,
        confirmPassword: password,
      }).success
    ).toBe(false)
  })
})
