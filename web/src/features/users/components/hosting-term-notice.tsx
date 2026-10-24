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
import { useQuery } from '@tanstack/react-query'
import { ArrowRight } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { requireServerSuccess } from '@/lib/server-error-message'
import { useAuthStore } from '@/stores/auth-store'

import { searchUsers } from '../api'
import { useUsers } from './users-provider'

/** 显示服务端统计的未设期限用户数；不能把当前页数量当成全站数量。 */
export function HostingTermNotice(props: { onShowUndated: () => void }) {
  const { t } = useTranslation()
  const identity = useAuthStore(
    (s) => `${s.auth.user?.id}:${s.auth.session?.sid}`
  )
  const { refreshTrigger } = useUsers()
  const query = useQuery({
    queryKey: ['users', 'undated-total', identity, refreshTrigger],
    queryFn: async () => {
      const result = requireServerSuccess(
        await searchUsers({ hosting: 'undated', p: 1, page_size: 1 })
      )
      return result.data?.total ?? 0
    },
    staleTime: 30_000,
  })
  // 请求失败时不声称全部用户已设置期限，错误由共享 Query 层提示。
  if (query.isError || !query.data) return null
  return (
    <aside className='users-term-notice' aria-label={t('Hosting term not set')}>
      <div className='space-y-1'>
        <p className='font-semibold'>
          {t('{{count}} users have no hosting term', { count: query.data })}
        </p>
        <p>
          {t(
            'Existing accounts keep working until you set their hosting term.'
          )}
        </p>
      </div>
      <Button variant='outline' onClick={props.onShowUndated}>
        {t('View and set terms')}
        <ArrowRight aria-hidden='true' />
      </Button>
    </aside>
  )
}
