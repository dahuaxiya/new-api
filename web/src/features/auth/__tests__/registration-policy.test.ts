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
