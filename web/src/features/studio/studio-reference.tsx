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
import { ImagePlus, X } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'

import { useStudioImage } from './studio-image'

const sourceBytes = 50 * 1024 * 1024

/** Mobile file pickers can omit the MIME label, so inspect the original bytes. */
async function isSupportedReference(file: File): Promise<boolean> {
  if (!file.size || file.size > sourceBytes) return false
  const header = new Uint8Array(await file.slice(0, 12).arrayBuffer())
  const signature = String.fromCharCode(...header)
  return (
    signature.startsWith('\x89PNG\r\n\x1a\n') ||
    signature.startsWith('\xff\xd8\xff') ||
    (signature.startsWith('RIFF') && signature.slice(8) === 'WEBP')
  )
}

/** Read one local reference for the current image request. Changing model or leaving the form cancels an unfinished read. */
export function StudioReference(props: {
  value?: File | string
  onChange: (value?: File | string) => void
  onBlocked: (busy: boolean) => void
  disabled: boolean
}) {
  const { t } = useTranslation()
  const { url } = useStudioImage(props.value)
  const fileInput = useRef<HTMLInputElement>(null)
  const selection = useRef(0)
  const [invalid, setInvalid] = useState(false)
  const [processing, setProcessing] = useState(false)
  useEffect(
    () => () => {
      selection.current += 1
    },
    []
  )

  /** Ignore late decoding results when the user picks a different image or leaves the form. */
  const choose = async (file?: File) => {
    if (!file) return
    const current = ++selection.current
    setInvalid(false)
    props.onChange(undefined)
    props.onBlocked(true)
    setProcessing(true)
    try {
      if (!(await isSupportedReference(file))) {
        throw new Error('Unsupported reference image')
      }
      if (selection.current !== current) return
      props.onChange(file)
      props.onBlocked(false)
    } catch {
      if (selection.current === current) setInvalid(true)
    } finally {
      if (selection.current === current) setProcessing(false)
    }
  }
  return (
    <div className='flex flex-wrap items-center gap-2'>
      <input
        ref={fileInput}
        className='hidden'
        type='file'
        aria-label={t('Reference image')}
        accept='image/png,image/jpeg,image/webp'
        disabled={props.disabled}
        onChange={(event) => {
          void choose(event.target.files?.[0])
          event.target.value = ''
        }}
      />
      {url && (
        <img
          className='size-12 rounded-md border object-cover'
          src={url}
          alt={t('Reference image')}
        />
      )}
      <Button
        variant='ghost'
        size='sm'
        className='min-h-10'
        type='button'
        disabled={props.disabled}
        onClick={() => fileInput.current?.click()}
      >
        <ImagePlus aria-hidden='true' />
        {t('Reference image')}
      </Button>
      {(props.value || invalid || processing) && (
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
      )}
      {processing && (
        <p role='status' className='text-muted-foreground text-xs'>
          {t('Preparing reference image…')}
        </p>
      )}
      {!invalid && !processing && (
        <p className='text-muted-foreground text-xs'>
          {t(
            'Reference images are uploaded at original quality, up to 50 MiB.'
          )}
        </p>
      )}
      {invalid && (
        <p role='alert' className='text-destructive text-sm'>
          {t('Choose a readable PNG, JPEG or WebP up to 50 MiB.')}
        </p>
      )}
    </div>
  )
}
