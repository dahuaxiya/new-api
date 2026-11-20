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
import { History, ImageIcon, MessageSquare, Search, Trash2 } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
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
  const [filter, setFilter] = useState<'all' | 'text' | 'image'>('all')
  const [search, setSearch] = useState('')
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

  // 搜索只匹配已加载的标题和分组，筛选时无需下载正文或图片。
  const term = search.trim().toLowerCase()
  const visibleRows = rows.filter((row) => {
    const mode = row.mode ?? 'text'
    return (
      (filter === 'all' || filter === mode) &&
      (row.title.toLowerCase().includes(term) ||
        row.product.toLowerCase().includes(term))
    )
  })

  return (
    <Sheet
      open={open}
      onOpenChange={(next) => {
        setOpen(next)
        if (next) {
          setRows([])
          setSearch('')
          setFilter('all')
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
            className='max-md:size-11 max-md:rounded-xl'
            title={t('Studio history')}
            disabled={props.disabled || !key}
          />
        }
      >
        <History aria-hidden='true' />
        <span className='max-md:sr-only'>{t('Studio history')}</span>
      </SheetTrigger>
      <SheetContent className='w-full gap-0 sm:max-w-md'>
        <SheetHeader>
          <SheetTitle>{t('Studio history')}</SheetTitle>
        </SheetHeader>
        <div className='space-y-2.5 border-b px-4 pb-3'>
          <div className='relative'>
            <Search
              className='text-muted-foreground pointer-events-none absolute start-3 top-1/2 size-4 -translate-y-1/2'
              aria-hidden='true'
            />
            <Input
              className='h-11 ps-9'
              type='search'
              aria-label={t('Search history')}
              placeholder={t('Search history')}
              value={search}
              onChange={(event) => setSearch(event.target.value)}
            />
          </div>
          <div
            className='flex items-center gap-1.5'
            role='group'
            aria-label={t('Mode')}
          >
            {(
              [
                ['all', t('All')],
                ['text', t('Chat')],
                ['image', t('Image generation')],
              ] as const
            ).map(([value, label]) => (
              <Button
                key={value}
                type='button'
                size='sm'
                className='min-h-10 rounded-full px-3'
                variant={filter === value ? 'secondary' : 'ghost'}
                aria-pressed={filter === value}
                onClick={() => setFilter(value)}
              >
                {label}
              </Button>
            ))}
          </div>
        </div>
        <div className='min-h-0 flex-1 overflow-y-auto px-3 pt-2 pb-[max(1rem,env(safe-area-inset-bottom))]'>
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
          {busy && (
            <p className='text-muted-foreground px-2 py-3' role='status'>
              {t('Loading...')}
            </p>
          )}
          {!busy && !failed && !rows.length && (
            <p className='text-muted-foreground px-2'>{t('No history yet.')}</p>
          )}
          {!busy && rows.length > 0 && !visibleRows.length && (
            <p className='text-muted-foreground px-2 py-3'>
              {t('No results found.')}
            </p>
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
          {visibleRows.map((row) => (
            <div
              key={row.id}
              className='hover:bg-muted/65 focus-within:bg-muted/65 flex items-center gap-1 rounded-xl'
            >
              <Button
                variant='ghost'
                disabled={busy}
                className='h-auto min-w-0 flex-1 justify-start gap-3 px-2 py-3 text-start hover:bg-transparent'
                onClick={() =>
                  void perform(async (ticket) => {
                    const item = row.local ?? (await readCloud(row.id))
                    if (ticket !== epoch.current) return
                    props.onSelect(item)
                    setOpen(false)
                  })
                }
              >
                <span className='bg-muted grid size-9 shrink-0 place-items-center rounded-lg'>
                  {row.mode === 'image' ? (
                    <ImageIcon aria-hidden='true' />
                  ) : (
                    <MessageSquare aria-hidden='true' />
                  )}
                </span>
                <span className='grid min-w-0 flex-1 gap-1'>
                  <span className='truncate'>{row.title}</span>
                  <span className='text-muted-foreground truncate text-xs'>
                    {row.product} ·{' '}
                    {formatTimestampToDate(row.updatedAt, 'milliseconds')}
                  </span>
                </span>
              </Button>
              <Button
                variant='ghost'
                size='icon'
                className='size-11 shrink-0'
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
