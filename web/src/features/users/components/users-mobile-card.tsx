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
import { flexRender, type Row } from '@tanstack/react-table'
import { useTranslation } from 'react-i18next'

import type { User } from '../types'

/**
 * 仅组合移动端字段位置；列渲染、权限和管理菜单仍复用桌面列定义。
 * 通用 CardRowContent 把操作固定在末尾，无法表达预览中顶部的管理入口。
 */
export function UsersMobileCard(props: { row: Row<User> }) {
  const { t } = useTranslation()
  const cells = props.row
    .getVisibleCells()
    .filter((cell) => cell.column.id !== 'select')
  const title = cells.find((cell) => cell.column.id === 'username')
  const actions = cells.find((cell) => cell.column.id === 'actions')
  const status = cells.find((cell) => cell.column.id === 'status')
  return (
    <article
      className='users-mobile-card'
      aria-label={props.row.original.username}
    >
      <div className='flex items-start justify-between gap-2'>
        <div className='min-w-0 flex-1'>
          {title && flexRender(title.column.columnDef.cell, title.getContext())}
        </div>
        <div className='flex shrink-0 flex-col items-end gap-1'>
          {actions &&
            flexRender(actions.column.columnDef.cell, actions.getContext())}
          {status &&
            flexRender(status.column.columnDef.cell, status.getContext())}
        </div>
      </div>
      <dl className='users-mobile-fields'>
        {cells
          .filter(
            (cell) => cell !== title && cell !== actions && cell !== status
          )
          .map((cell) => (
            <div
              key={cell.id}
              className='min-w-0 space-y-2'
              data-field={cell.column.id}
            >
              <dt className='text-muted-foreground text-xs'>
                {typeof cell.column.columnDef.header === 'string'
                  ? cell.column.columnDef.header
                  : t('Status')}
              </dt>
              <dd>
                {flexRender(cell.column.columnDef.cell, cell.getContext())}
              </dd>
            </div>
          ))}
      </dl>
    </article>
  )
}
