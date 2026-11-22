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
import { Download, ImageIcon, LoaderCircle, MessageSquare } from 'lucide-react'
import { useRef } from 'react'
import { useTranslation } from 'react-i18next'

import {
  Conversation,
  ConversationContent,
  ConversationScrollButton,
} from '@/components/ai-elements/conversation'
import { Response } from '@/components/ai-elements/response'
import { CopyButton } from '@/components/copy-button'
import { Button } from '@/components/ui/button'

import { SelectionQuote } from './studio-annotations'
import { useStudioImage } from './studio-image'
import type { StudioEntry } from './use-studio-run'

/** Reuse conversation scroll behavior so streaming never pulls a reader away from older messages. */
export function StudioResults(props: {
  entries: StudioEntry[]
  sentReference?: File | string
  busy: boolean
  onQuoteText: (text: string) => void
  onQuoteImage?: (source: File | string) => void
  mode: 'text' | 'image'
}) {
  const { t } = useTranslation()
  const transcript = useRef<HTMLDivElement>(null)
  return (
    <div ref={transcript} className='flex min-h-0 flex-1 flex-col'>
      <Conversation
        className='min-w-0'
        initial='instant'
        resize='instant'
        aria-label={t('Results')}
      >
        <ConversationContent className='mx-auto flex min-h-full w-full max-w-[52rem] min-w-0 flex-col gap-7 px-4 py-4 md:gap-10 md:px-8 md:py-8'>
          {props.entries.length === 0 && (
            <div className='m-auto grid justify-items-center gap-3 px-2 py-4 text-center'>
              <div className='bg-background text-foreground/75 grid size-11 place-items-center rounded-2xl border shadow-xs md:size-14'>
                {props.mode === 'image' ? (
                  <ImageIcon className='size-5' aria-hidden='true' />
                ) : (
                  <MessageSquare className='size-5' aria-hidden='true' />
                )}
              </div>
              <h2 className='text-foreground/90 text-base font-medium tracking-tight md:text-2xl'>
                {props.mode === 'image'
                  ? t('Describe the image you want')
                  : t('What would you like to work on?')}
              </h2>
            </div>
          )}
          {props.entries.map((item, index) => (
            <article
              key={item.id}
              className='min-w-0 space-y-3 wrap-anywhere md:space-y-4'
            >
              <StudioUserMessage
                prompt={item.prompt}
                onQuoteImage={props.busy ? undefined : props.onQuoteImage}
                reference={
                  item.reference ??
                  (index === props.entries.length - 1
                    ? props.sentReference
                    : undefined)
                }
              />
              <div className='bg-background min-w-0 space-y-3 rounded-2xl rounded-es-md border px-3.5 py-3 shadow-xs md:px-5 md:py-4'>
                <p className='text-muted-foreground text-xs font-medium wrap-anywhere'>
                  {item.model}
                </p>
                {item.reasoning && (
                  <details className='bg-muted/65 text-muted-foreground rounded-lg px-3 py-2 text-sm'>
                    <summary className='cursor-pointer'>
                      {t('Reasoning')}
                    </summary>
                    <p className='mt-3 whitespace-pre-wrap'>{item.reasoning}</p>
                  </details>
                )}
                {item.content && (
                  <div data-studio-quotable>
                    <Response
                      className='max-w-full min-w-0 overflow-x-auto text-[0.9375rem] leading-relaxed'
                      final={item.complete || Boolean(item.error)}
                    >
                      {item.content}
                    </Response>
                  </div>
                )}
                {item.image && (
                  <StudioResultImage
                    item={item}
                    onQuote={props.busy ? undefined : props.onQuoteImage}
                  />
                )}
                {props.busy &&
                  index === props.entries.length - 1 &&
                  !item.content &&
                  !item.image && (
                    <p
                      role='status'
                      className='text-muted-foreground flex items-center gap-2 text-sm'
                    >
                      <LoaderCircle
                        className='size-3.5 motion-safe:animate-spin'
                        aria-hidden='true'
                      />
                      {t(
                        props.mode === 'image'
                          ? 'Generating image…'
                          : 'Responding...'
                      )}
                    </p>
                  )}
                {item.error && (
                  <p role='alert' className='text-destructive text-sm'>
                    {t(item.error)}
                  </p>
                )}
                {item.complete && item.content && (
                  <CopyButton
                    value={item.content}
                    aria-label={t('Copy response')}
                    size='sm'
                  />
                )}
              </div>
            </article>
          ))}
        </ConversationContent>
        {!props.busy && (
          <SelectionQuote scope={transcript} onQuote={props.onQuoteText} />
        )}
        <ConversationScrollButton
          className='size-11 rounded-full shadow-sm'
          aria-label={t('Scroll to bottom')}
        />
      </Conversation>
    </div>
  )
}

/** 同一条用户消息显示提示词和参考图，兼容本地预览与已保存的私有图片。 */
function StudioUserMessage(props: {
  onQuoteImage?: (source: File | string) => void
  prompt: string
  reference?: File | string
}) {
  const { t } = useTranslation()
  const { url } = useStudioImage(props.reference)
  const source = props.reference
  const quote = props.onQuoteImage
  return (
    <div className='bg-primary/8 ms-auto w-fit max-w-[92%] space-y-2 rounded-2xl rounded-ee-md px-3.5 py-2.5 text-[0.9375rem] leading-relaxed md:max-w-[80%] md:px-4'>
      {url && (
        <img
          src={url}
          alt={t('Reference image')}
          className='max-h-56 max-w-full rounded-lg object-contain'
          decoding='async'
        />
      )}
      {source && quote && (
        <Button size='sm' variant='ghost' onClick={() => quote(source)}>
          {t('Quote image')}
        </Button>
      )}
      <p data-studio-quotable className='whitespace-pre-wrap'>
        {props.prompt}
      </p>
    </div>
  )
}

/** Render either a private file-backed result or an older inline result with its original format. */
function StudioResultImage(props: {
  item: StudioEntry
  onQuote?: (source: string) => void
}) {
  const { t } = useTranslation()
  const source = props.item.image
  const { url, format } = useStudioImage(source)
  if (!url) return null
  return (
    <div className='space-y-3'>
      <img
        src={url}
        alt={props.item.prompt}
        className='bg-muted/45 max-h-[58dvh] max-w-full rounded-xl object-contain'
        loading='lazy'
        decoding='async'
      />
      {props.onQuote && source && (
        <Button
          variant='outline'
          size='sm'
          onClick={() => props.onQuote?.(source)}
        >
          {t('Quote image')}
        </Button>
      )}
      <Button
        variant='outline'
        size='sm'
        render={
          <a href={url} download={`luobocode-${props.item.id}.${format}`} />
        }
      >
        <Download aria-hidden='true' />
        {t('Download')}
      </Button>
    </div>
  )
}
