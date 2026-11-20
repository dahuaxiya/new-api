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
import { History, Trash2 } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { Button } from '@/components/ui/button'
import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
  SheetTrigger,
} from '@/components/ui/sheet'
import { formatTimestampToDate } from '@/lib/format'

import {
  listCloud,
  readCloud,
  deleteCloud,
  writeCloud,
  type HistoryRow,
} from './studio-cloud'
import {
  readHistory,
  removeLocalHistory,
  type Conversation,
} from './studio-session'

type Row = HistoryRow & { local?: Conversation }

/** 列表只取摘要。打开某条记录时才下载正文和图片，旧版聊天由用户选择是否同步。 */
export function StudioHistory(props: {
  storageKey: string | null
  disabled: boolean
  onSelect: (item: Conversation) => void
  onDelete: (item: HistoryRow) => void
}) {
  const { t } = useTranslation()
  const [open, setOpen] = useState(false)
  const [rows, setRows] = useState<Row[]>([])
  const [busy, setBusy] = useState(false)
  const [failed, setFailed] = useState(false)
  const [deleting, setDeleting] = useState<Row>()
  const epoch = useRef(0)
  const key = props.storageKey
  useEffect(
    () => () => {
      epoch.current++
    },
    []
  )

  /** 关抽屉或换账号时丢弃迟到的请求，防止把旧账号的详情恢复到当前工作台。 */
  async function perform(action: (ticket: number) => Promise<void>) {
    const ticket = ++epoch.current
    setBusy(true)
    setFailed(false)
    try {
      await action(ticket)
    } catch {
      if (ticket === epoch.current) setFailed(true)
    } finally {
      if (ticket === epoch.current) setBusy(false)
    }
  }

  /** 即使某一侧读取失败，另一侧已有的记录仍然可打开。 */
  async function load(ticket: number) {
    if (!key) return
    const [cloud, local] = await Promise.allSettled([
      listCloud(key),
      Promise.resolve().then(() => readHistory(key)),
    ])
    if (ticket !== epoch.current) return
    const remote = cloud.status === 'fulfilled' ? cloud.value : []
    const ids = new Set(remote.map((row) => row.id))
    const remaining =
      local.status === 'fulfilled'
        ? local.value.filter((row) => !ids.has(row.id))
        : []
    setRows(
      [
        ...remote,
        ...remaining.map((item) => ({
          id: item.id,
          mode: item.mode,
          product: item.product,
          updatedAt: item.updatedAt,
          title: item.entries[0].prompt.slice(0, 100),
          local: item,
        })),
      ].sort((a, b) => b.updatedAt - a.updatedAt)
    )
    setFailed(local.status === 'rejected' || cloud.status === 'rejected')
  }

  return (
    <Sheet
      open={open}
      onOpenChange={(next) => {
        setOpen(next)
        if (next) {
          setRows([])
          void perform(load)
        } else {
          epoch.current++
          setDeleting(undefined)
        }
      }}
    >
      <SheetTrigger
        render={
          <Button
            variant='ghost'
            size='sm'
            className='max-md:size-11'
            title={t('Studio history')}
            disabled={props.disabled || !key}
          />
        }
      >
        <History aria-hidden='true' />
        <span className='max-md:sr-only'>{t('Studio history')}</span>
      </SheetTrigger>
      <SheetContent className='w-full sm:max-w-sm'>
        <SheetHeader>
          <SheetTitle>{t('Studio history')}</SheetTitle>
        </SheetHeader>
        <div className='overflow-y-auto px-3 pb-4'>
          {failed && (
            <div role='alert'>
              {t('Could not access history.')}{' '}
              <Button
                variant='ghost'
                disabled={busy}
                onClick={() => void perform(load)}
              >
                {t('Retry')}
              </Button>
            </div>
          )}
          {busy && <p role='status'>{t('Loading...')}</p>}
          {!busy && !failed && !rows.length && (
            <p className='text-muted-foreground px-2'>{t('No history yet.')}</p>
          )}
          {rows.some((row) => row.local) && (
            <Button
              variant='outline'
              disabled={busy || failed}
              onClick={() =>
                void perform(async (ticket) => {
                  if (!key) return
                  for (const row of rows) {
                    if (ticket !== epoch.current) return
                    if (row.local) {
                      await writeCloud(key, row.local)
                      await removeLocalHistory(key, row.id)
                    }
                  }
                  await load(ticket)
                })
              }
            >
              {t('Sync local chats')}
            </Button>
          )}
          {rows.map((row) => (
            <div key={row.id} className='flex items-center'>
              <Button
                variant='ghost'
                disabled={busy}
                className='h-auto min-w-0 flex-1 flex-col items-start gap-1 px-2 py-3 text-start'
                onClick={() =>
                  void perform(async (ticket) => {
                    const item = row.local ?? (await readCloud(row.id))
                    if (ticket !== epoch.current) return
                    props.onSelect(item)
                    setOpen(false)
                  })
                }
              >
                <span className='w-full truncate'>{row.title}</span>
                <span className='text-muted-foreground max-w-full truncate text-xs'>
                  {t(row.mode === 'image' ? 'Image generation' : 'Chat')} ·{' '}
                  {row.product} ·{' '}
                  {formatTimestampToDate(row.updatedAt, 'milliseconds')}
                </span>
              </Button>
              <Button
                variant='ghost'
                size='icon'
                disabled={busy}
                aria-label={`${t('Delete')} ${row.title}`}
                onClick={() => setDeleting(row)}
              >
                <Trash2 />
              </Button>
            </div>
          ))}
        </div>
        <ConfirmDialog
          open={!!deleting}
          onOpenChange={(next) => {
            if (!next) setDeleting(undefined)
          }}
          title={t('Delete history entry?')}
          desc={deleting?.title ?? ''}
          destructive
          confirmText={t('Delete')}
          isLoading={busy}
          handleConfirm={() =>
            void perform(async (ticket) => {
              if (!key || !deleting) return
              const row = deleting
              setDeleting(undefined)
              // Only remove the local copy after the server has acknowledged deletion.
              await deleteCloud(key, row.id)
              await removeLocalHistory(key, row.id)
              if (ticket !== epoch.current) return
              props.onDelete(row)
              await load(ticket)
            })
          }
        />
      </SheetContent>
    </Sheet>
  )
}
