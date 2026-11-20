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
import { Download, ImageIcon, MessageSquare } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import {
  Conversation,
  ConversationContent,
  ConversationScrollButton,
} from '@/components/ai-elements/conversation'
import { Response } from '@/components/ai-elements/response'
import { CopyButton } from '@/components/copy-button'
import { EmptyState } from '@/components/empty-state'
import { Button } from '@/components/ui/button'

import type { StudioEntry } from './use-studio-run'

/** Reuse conversation scroll behavior so streaming never pulls a reader away from older messages. */
export function StudioResults(props: {
  entries: StudioEntry[]
  busy: boolean
  mode: 'text' | 'image'
}) {
  const { t } = useTranslation()
  return (
    <Conversation
      className='min-w-0 max-md:min-h-32'
      initial='instant'
      resize='instant'
      aria-label={t('Results')}
    >
      <ConversationContent className='mx-auto w-full max-w-3xl min-w-0 space-y-5 px-3 py-3 md:space-y-10 md:px-8 md:py-8'>
        {props.entries.length === 0 && (
          <EmptyState
            className='max-md:min-h-0 max-md:gap-2 max-md:p-3'
            icon={props.mode === 'image' ? ImageIcon : MessageSquare}
            title={t(
              props.mode === 'image'
                ? 'Describe the image you want'
                : 'What would you like to work on?'
            )}
          />
        )}
        {props.entries.map((item, index) => (
          <article
            key={item.id}
            className='min-w-0 space-y-3 [overflow-wrap:anywhere] md:space-y-5'
          >
            <p className='bg-muted/60 ms-auto w-fit max-w-[90%] rounded-2xl px-4 py-3 whitespace-pre-wrap'>
              {item.prompt}
            </p>
            <div className='space-y-3'>
              <p className='text-muted-foreground text-xs font-medium'>
                {item.model}
              </p>
              {item.reasoning && (
                <details className='text-muted-foreground text-sm'>
                  <summary>{t('Reasoning')}</summary>
                  <p className='mt-3 whitespace-pre-wrap'>{item.reasoning}</p>
                </details>
              )}
              {item.content && (
                <Response
                  className='max-w-full min-w-0 overflow-x-auto'
                  final={item.complete || Boolean(item.error)}
                >
                  {item.content}
                </Response>
              )}
              {item.image && (
                <div className='space-y-3'>
                  <img
                    src={item.image}
                    alt={item.prompt}
                    className='max-h-[65vh] max-w-full rounded-xl object-contain'
                  />
                  <Button
                    variant='outline'
                    size='sm'
                    render={
                      <a
                        href={item.image}
                        download={`luobocode-${item.id}.${item.image.slice('data:image/'.length, item.image.indexOf(';'))}`}
                      />
                    }
                  >
                    <Download aria-hidden='true' />
                    {t('Download')}
                  </Button>
                </div>
              )}
              {props.busy &&
                index === props.entries.length - 1 &&
                !item.content &&
                !item.image && (
                  <p role='status' className='text-muted-foreground text-sm'>
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
      <ConversationScrollButton />
    </Conversation>
  )
}
