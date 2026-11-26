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
import { render, screen, within } from '@testing-library/react'
import { expect, test } from 'vitest'

import { ChannelGroups } from '../channel-groups'

test('renders routing identifiers separately from product groups', () => {
  render(
    <ChannelGroups
      groups='retail,gpt-normal,gpt-plus,gpt-ultrafast,default'
      sensitiveVisible
      maskedLabel='••••'
      variant='card'
    />
  )

  const products = screen.getByRole('group', { name: 'Groups' })
  expect(within(products).getByText('gpt-normal')).toBeInTheDocument()
  expect(within(products).getByText('gpt-plus')).toBeInTheDocument()
  expect(within(products).getByText('gpt-ultrafast')).toBeInTheDocument()
  expect(within(products).queryByText('retail')).not.toBeInTheDocument()
  expect(within(products).queryByText('default')).not.toBeInTheDocument()

  const routing = screen.getByRole('group', { name: 'Internal routing' })
  expect(within(routing).getByText('default · retail')).toBeInTheDocument()
})

test('masks routing identifiers when sensitive values are hidden', () => {
  render(
    <ChannelGroups
      groups='retail'
      sensitiveVisible={false}
      maskedLabel='••••'
      variant='table'
    />
  )

  const routing = screen.getByRole('group', { name: 'Internal routing' })
  expect(within(routing).getByText('••••')).toBeInTheDocument()
  expect(within(routing).queryByText('retail')).not.toBeInTheDocument()
})
