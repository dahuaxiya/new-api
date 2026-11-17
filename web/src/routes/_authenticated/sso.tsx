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
import { createFileRoute } from '@tanstack/react-router'
import { z } from 'zod'

import { SiteSSOAuthorize } from '@/features/auth/components/site-sso-authorize'

const schema = z.object({
  state: z.string().regex(/^[A-Za-z0-9_-]{43}$/),
  challenge: z.string().regex(/^[A-Za-z0-9_-]{43}$/),
  client_id: z.string().min(1).max(32),
  verify: z.enum(['login', 'verify']),
})

/** Keep route validation ahead of the authorization UI; only validated state/challenge reach the issuer. */
function SiteSSOPage() {
  return <SiteSSOAuthorize search={Route.useSearch()} />
}

export const Route = createFileRoute('/_authenticated/sso')({
  validateSearch: schema,
  component: SiteSSOPage,
})
