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
import { useEffect, useRef, useState, type RefObject } from 'react'
import { createPortal } from 'react-dom'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'

import type { ImagePin } from './studio-quote'

/**
 * 根据浏览器选区放置唯一的浮动入口；只接受本工作台同一段消息里的选区。
 * @param props scope 为会话内容容器，onQuote 只在用户确认引用后写入草稿。
 * @return 挂在页面顶层的小按钮，不占消息布局空间。
 */
export function SelectionQuote(props: {
  scope: RefObject<HTMLDivElement | null>
  onQuote: (text: string) => void
}) {
  const { t } = useTranslation()
  const button = useRef<HTMLButtonElement>(null)
  const [anchor, setAnchor] = useState<{
    text: string
    x: number
    y: number
    above: boolean
  } | null>(null)
  const { scope } = props
  useEffect(() => {
    const container = scope.current
    if (!container) return
    /** 从选区起始行取坐标；滚动时重新计算，正文移出对话视口后收起按钮。 */
    function refresh() {
      const selection = window.getSelection()
      const text = selection?.toString().trim()
      const block = selection?.anchorNode?.parentElement?.closest(
        '[data-studio-quotable]'
      )
      if (
        !selection?.rangeCount ||
        !text ||
        !block ||
        !container?.contains(block) ||
        !block.contains(selection.focusNode)
      ) {
        setAnchor(null)
        return
      }
      const range = selection.getRangeAt(0)
      const rect = [...range.getClientRects()].find(
        (line) => line.width > 0 && line.height > 0
      )
      const bounds = container.getBoundingClientRect()
      if (!rect || rect.bottom <= bounds.top || rect.top >= bounds.bottom) {
        setAnchor(null)
        return
      }
      const above = rect.top >= Math.max(0, bounds.top) + 34
      setAnchor({
        text,
        above,
        x: Math.max(
          40,
          Math.min(window.innerWidth - 40, rect.left + rect.width / 2)
        ),
        y: above ? rect.top - 4 : rect.bottom + 4,
      })
    }
    /** Esc 关闭菜单；点击按钮保留选区，直到 click 把摘录写入草稿。 */
    function dismiss(event: KeyboardEvent | PointerEvent) {
      if (
        event instanceof KeyboardEvent
          ? event.key === 'Escape'
          : !button.current?.contains(event.target as Node)
      ) {
        setAnchor(null)
      }
    }
    document.addEventListener('selectionchange', refresh)
    document.addEventListener('scroll', refresh, true)
    window.addEventListener('resize', refresh)
    document.addEventListener('keydown', dismiss)
    document.addEventListener('pointerdown', dismiss)
    return () => {
      document.removeEventListener('selectionchange', refresh)
      document.removeEventListener('scroll', refresh, true)
      window.removeEventListener('resize', refresh)
      document.removeEventListener('keydown', dismiss)
      document.removeEventListener('pointerdown', dismiss)
    }
  }, [scope])
  if (!anchor) return null
  return createPortal(
    <Button
      ref={button}
      type='button'
      variant='outline'
      size='xs'
      className='bg-popover fixed z-50 rounded-full px-2.5 shadow-md'
      style={{
        left: anchor.x,
        top: anchor.y,
        transform: anchor.above ? 'translate(-50%, -100%)' : 'translateX(-50%)',
      }}
      onMouseDown={(event) => event.preventDefault()}
      onClick={() => {
        props.onQuote(anchor.text)
        window.getSelection()?.removeAllRanges()
        setAnchor(null)
      }}
    >
      {t('Quote')}
    </Button>,
    document.body
  )
}

/** 缩略图和编辑器使用同一组相对坐标，改变显示尺寸时标记仍落在原位置。 */
export function MarkedImage({
  url,
  pins,
  onPoint,
  large,
}: {
  url: string
  pins: ImagePin[]
  onPoint?: (x: number, y: number) => void
  large?: boolean
}) {
  const { t } = useTranslation()
  return (
    <span className='relative inline-block max-w-full align-middle leading-none'>
      <img
        src={url}
        alt={t('Reference image')}
        className={
          large
            ? 'block h-auto max-h-[50dvh] w-auto max-w-full rounded'
            : 'block h-auto max-h-16 w-auto max-w-24 rounded'
        }
      />
      {onPoint && (
        <button
          type='button'
          className='absolute inset-0 cursor-crosshair'
          aria-label={t('Mark a point on the image')}
          onClick={(e) => {
            const box = e.currentTarget.getBoundingClientRect()
            // Keyboard activation adds a centered marker; the number inputs allow precise repositioning.
            if (!e.detail || !box.width || !box.height) {
              onPoint(50, 50)
              return
            }
            onPoint(
              Math.round((100 * (e.clientX - box.left)) / box.width),
              Math.round((100 * (e.clientY - box.top)) / box.height)
            )
          }}
        />
      )}
      {pins.map((pin, i) => (
        <span
          key={pin.id}
          aria-hidden='true'
          className='pointer-events-none absolute grid size-5 -translate-x-1/2 -translate-y-1/2 place-items-center rounded-full border border-white bg-black text-xs font-bold text-white'
          style={{ left: `${pin.x}%`, top: `${pin.y}%` }}
        >
          {i + 1}
        </span>
      ))}
    </span>
  )
}

/** 批注只属于当前草稿的附件；编辑本地标记不会改动历史媒体的原件。 */
export function ImageNotes(props: {
  url: string
  pins: ImagePin[]
  onPins: (pins: ImagePin[]) => void
  note: string
  onNote: (note: string) => void
  disabled: boolean
}) {
  const { t } = useTranslation()
  const [open, setOpen] = useState(false)
  return (
    <>
      <button
        type='button'
        disabled={props.disabled}
        aria-label={t('Annotate image')}
        onClick={() => setOpen(true)}
      >
        <MarkedImage url={props.url} pins={props.pins} />
      </button>
      <div className='max-w-64 text-xs whitespace-pre-wrap'>
        {props.note}
        {props.pins.map((p, i) => (
          <div key={p.id}>
            {i + 1}. {p.note}
          </div>
        ))}
      </div>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent className='sm:max-w-2xl'>
          <DialogTitle>{t('Annotate image')}</DialogTitle>
          <div className='text-sm'>
            {t('Click a point on the image to add a note.')}
          </div>
          <div className='text-center'>
            <MarkedImage
              large
              url={props.url}
              pins={props.pins}
              onPoint={(x, y) =>
                props.onPins([
                  ...props.pins,
                  { id: crypto.randomUUID(), x, y, note: '' },
                ])
              }
            />
          </div>
          <Input
            aria-label={t('Image note')}
            placeholder={t('Image note')}
            value={props.note}
            onChange={(e) => props.onNote(e.target.value)}
          />
          {props.pins.map((p, i) => (
            <div key={p.id} className='flex flex-wrap items-center gap-2'>
              <span>{i + 1}.</span>
              {(['x', 'y'] as const).map((axis) => (
                <label key={axis} className='flex items-center gap-1 text-xs'>
                  {axis.toUpperCase()}%
                  <Input
                    type='number'
                    className='w-16'
                    min={0}
                    max={100}
                    value={p[axis]}
                    onChange={(e) =>
                      props.onPins(
                        props.pins.map((v) =>
                          v.id === p.id
                            ? {
                                ...v,
                                [axis]: Math.max(
                                  0,
                                  Math.min(100, Number(e.target.value))
                                ),
                              }
                            : v
                        )
                      )
                    }
                  />
                </label>
              ))}
              <Input
                className='min-w-28 flex-1'
                value={p.note}
                aria-label={`${t('Annotation')} ${i + 1}`}
                placeholder={t('Annotation')}
                onChange={(e) =>
                  props.onPins(
                    props.pins.map((v) =>
                      v.id === p.id ? { ...v, note: e.target.value } : v
                    )
                  )
                }
              />
              <Button
                type='button'
                variant='ghost'
                onClick={() =>
                  props.onPins(props.pins.filter((v) => v.id !== p.id))
                }
              >
                {t('Remove')}
              </Button>
            </div>
          ))}
          <Button type='button' onClick={() => setOpen(false)}>
            {t('Done')}
          </Button>
        </DialogContent>
      </Dialog>
    </>
  )
}
