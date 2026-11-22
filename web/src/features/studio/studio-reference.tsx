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
import { X } from 'lucide-react'
import {
  useEffect,
  useImperativeHandle,
  useRef,
  useState,
  type Ref,
} from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'

import { ImageNotes } from './studio-annotations'
import { useStudioImage } from './studio-image'
import type { ImagePin } from './studio-quote'

const sourceBytes = 50 * 1024 * 1024

/** Read the raster signature so Android and clipboard MIME labels cannot override the file format. */
async function referenceType(file: File) {
  if (!file.size || file.size > sourceBytes) return undefined
  const header = new Uint8Array(await file.slice(0, 12).arrayBuffer())
  const signature = String.fromCharCode(...header)
  if (signature.startsWith('\x89PNG\r\n\x1a\n')) return 'image/png'
  if (signature.startsWith('\xff\xd8\xff')) return 'image/jpeg'
  if (signature.startsWith('RIFF') && signature.slice(8) === 'WEBP') {
    return 'image/webp'
  }
  return undefined
}

/** Read one local reference for the current image request. Changing model or leaving the form cancels an unfinished read. */
export function StudioReference({
  ref,
  fileInput,
  ...props
}: {
  fileInput: Ref<HTMLInputElement>
  value?: File | string
  onChange: (value?: File | string) => void
  onBlocked: (busy: boolean) => void
  pins: ImagePin[]
  onPins: (pins: ImagePin[]) => void
  note: string
  onNote: (note: string) => void
  disabled: boolean
  ref?: Ref<(source: File | string) => void>
}) {
  const { t } = useTranslation()
  const { url } = useStudioImage(props.value)
  const selection = useRef(0)
  const [invalid, setInvalid] = useState(false)
  const [processing, setProcessing] = useState(false)
  useEffect(
    () => () => {
      selection.current += 1
    },
    []
  )

  /** Give the latest clipboard, upload or history selection precedence over a pending file check. */
  const choose = async (source?: File | string) => {
    if (!source) return
    const current = ++selection.current
    setInvalid(false)
    if (typeof source === 'string') {
      // The history picker already validated the stored media reference.
      props.onChange(source)
      props.onBlocked(false)
      setProcessing(false)
      return
    }
    props.onChange(undefined)
    props.onBlocked(true)
    setProcessing(true)
    try {
      const type = await referenceType(source)
      if (!type) {
        throw new Error('Unsupported reference image')
      }
      if (selection.current !== current) return
      // Some pickers label downloaded WebP as PNG. Fix only the MIME metadata and keep the file bytes.
      props.onChange(
        source.type === type
          ? source
          : new File([source], source.name, {
              type,
              lastModified: source.lastModified,
            })
      )
      props.onBlocked(false)
    } catch {
      if (selection.current === current) setInvalid(true)
    } finally {
      if (selection.current === current) setProcessing(false)
    }
  }
  useImperativeHandle(ref, () => (source) => void choose(source))
  return (
    <>
      <input
        ref={fileInput}
        className='hidden'
        type='file'
        aria-label={t('Reference image')}
        accept='image/*'
        disabled={props.disabled}
        onChange={(event) => {
          void choose(event.target.files?.[0])
          event.target.value = ''
        }}
      />
      {(props.value || invalid || processing) && (
        <div className='flex flex-wrap items-center gap-1'>
          {url && (
            <ImageNotes
              url={url}
              pins={props.pins}
              onPins={props.onPins}
              note={props.note}
              onNote={props.onNote}
              disabled={props.disabled}
            />
          )}
          <Button
            variant='ghost'
            size='icon'
            type='button'
            disabled={props.disabled}
            aria-label={t('Remove reference image')}
            onClick={() => {
              selection.current += 1
              props.onChange(undefined)
              setInvalid(false)
              setProcessing(false)
              props.onBlocked(false)
            }}
          >
            <X aria-hidden='true' />
          </Button>
          {processing && (
            <p role='status' className='text-muted-foreground text-xs'>
              {t('Preparing reference image…')}
            </p>
          )}
          {invalid && (
            <p role='alert' className='text-destructive text-sm'>
              {t('Choose a readable PNG, JPEG or WebP up to 50 MiB.')}
            </p>
          )}
        </div>
      )}
    </>
  )
}
