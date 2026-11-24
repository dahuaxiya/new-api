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
import { createFileRoute, redirect } from '@tanstack/react-router'

import { ModelAvailability } from '@/features/model-availability'
import { statusQueryOptions } from '@/lib/status-query'

export const Route = createFileRoute('/model-availability/')({
  /** 托管站共用前端产物，只有中转站提供公开的模型状态。 */
  beforeLoad: async ({ context }) => {
    const status = await context.queryClient.ensureQueryData(statusQueryOptions)
    if (status?.site_mode !== 'retail') throw redirect({ to: '/' })
  },
  component: ModelAvailability,
})
