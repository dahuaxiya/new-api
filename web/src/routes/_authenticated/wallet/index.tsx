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

import { Wallet } from '@/features/wallet'

const walletSearchSchema = z.object({
  show_history: z.boolean().optional(),
  tab: z.enum(['balance', 'hosting', 'referral']).optional(),
  // 旧账号迁入的名额是 legacy-<用户 ID>，新名额才是 UUID；按数据库长度接收不透明 ID，归属由服务端核验。
  seat_id: z.string().min(1).max(36).optional(),
})

export const Route = createFileRoute('/_authenticated/wallet/')({
  component: RouteComponent,
  validateSearch: walletSearchSchema,
})

/** 只接受固定账单分区和名额标识；URL 参数不携带价格、付款状态或授权凭据。 */
function RouteComponent() {
  const { show_history, tab, seat_id } = Route.useSearch()
  return (
    <Wallet
      initialShowHistory={show_history}
      initialTab={tab}
      hostingSeatId={seat_id}
    />
  )
}
