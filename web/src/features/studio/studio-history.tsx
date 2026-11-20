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
import { History } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
  SheetTrigger,
} from '@/components/ui/sheet'
import { formatTimestampToDate } from '@/lib/format'

import { readHistory, type Conversation } from './studio-session'

/** 仅在用户打开抽屉时读取该账号的本地记录，选择会话不会触发模型请求。 */
export function StudioHistory(props: {
  storageKey: string | null
  disabled: boolean
  onSelect: (item: Conversation) => void
}) {
  const { t } = useTranslation()
  const [open, setOpen] = useState(false)
  const [rows, setRows] = useState<Conversation[]>([])
  const [failed, setFailed] = useState(false)
  return (
    <Sheet
      open={open}
      onOpenChange={(next) => {
        if (next && props.storageKey) {
          try {
            setRows(readHistory(props.storageKey))
            setFailed(false)
          } catch {
            setRows([])
            setFailed(true)
          }
        }
        setOpen(next)
      }}
    >
      <SheetTrigger
        render={
          <Button
            variant='ghost'
            size='sm'
            className='max-md:size-11'
            title={t('Chat history')}
            disabled={props.disabled || !props.storageKey}
          />
        }
      >
        <History aria-hidden='true' />
        <span className='max-md:sr-only'>{t('Chat history')}</span>
      </SheetTrigger>
      <SheetContent className='w-full sm:max-w-sm'>
        <SheetHeader>
          <SheetTitle>{t('Chat history')}</SheetTitle>
        </SheetHeader>
        <div className='overflow-y-auto px-3 pb-4'>
          {failed && <p role='alert'>{t('Could not load chat history.')}</p>}
          {!failed && !rows.length && (
            <p className='text-muted-foreground px-2'>
              {t('No saved conversations yet.')}
            </p>
          )}
          {rows.map((row) => (
            <Button
              key={row.id}
              variant='ghost'
              className='h-auto w-full min-w-0 flex-col items-start gap-1 px-2 py-3 text-start'
              onClick={() => {
                props.onSelect(row)
                setOpen(false)
              }}
            >
              <span className='w-full truncate'>
                {row.entries[0].prompt.slice(0, 100)}
              </span>
              <span className='text-muted-foreground max-w-full truncate text-xs'>
                {row.product} ·{' '}
                {formatTimestampToDate(row.updatedAt, 'milliseconds')}
              </span>
            </Button>
          ))}
        </div>
      </SheetContent>
    </Sheet>
  )
}
