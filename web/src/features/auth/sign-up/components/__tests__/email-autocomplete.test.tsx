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
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { describe, expect, it } from 'vitest'

import { EmailAutocomplete } from '../email-autocomplete'

function Fixture() {
  const [value, setValue] = useState('')
  return (
    <>
      <EmailAutocomplete value={value} onValueChange={setValue} />
      <output>{value}</output>
    </>
  )
}

describe('email autocomplete', () => {
  it('shows domain choices during typing and commits the selected address', async () => {
    render(<Fixture />)
    const user = userEvent.setup()
    const input = screen.getByRole('combobox', { name: 'Email' })

    await user.type(input, 'alice@gm')
    expect(
      await screen.findByRole('option', { name: 'alice@gmail.com' })
    ).toBeVisible()
    await user.keyboard('{ArrowDown}{Enter}')

    await waitFor(() => expect(input).toHaveValue('alice@gmail.com'))
    expect(
      screen.getByText('alice@gmail.com', { selector: 'output' })
    ).toBeVisible()
  })

  it('explains a close spelling as a correction instead of a silent rewrite', async () => {
    render(<Fixture />)
    const user = userEvent.setup()
    const input = screen.getByRole('combobox', { name: 'Email' })

    await user.type(input, 'alice@gmial.com')
    const correction = await screen.findByRole('option')
    expect(correction).toBeVisible()
    expect(within(correction).getByText('alice@gmail.com')).toBeVisible()
    expect(within(correction).getByText('Suggested correction')).toBeVisible()
  })
})
