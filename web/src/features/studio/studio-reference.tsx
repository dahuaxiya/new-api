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

/** Read one local reference for the current image request. Changing model or leaving the form cancels an unfinished read. */
export function StudioReference(props: {
  value?: string
  onChange: (value?: string) => void
  onBlocked: (busy: boolean) => void
  disabled: boolean
}) {
  const { t } = useTranslation()
  const fileInput = useRef<HTMLInputElement>(null)
  const reader = useRef<FileReader | null>(null)
  const [invalid, setInvalid] = useState(false)
  useEffect(
    () => () => {
      reader.current?.abort()
    },
    []
  )
  /** Normalize the browser MIME label from the file signature; mobile pickers may omit it. */
  const choose = (file?: File) => {
    if (!file) return
    reader.current?.abort()
    setInvalid(false)
    props.onChange(undefined)
    if (!file.size || file.size > 5 * 1024 * 1024) {
      props.onBlocked(true)
      setInvalid(true)
      return
    }
    const next = new FileReader()
    reader.current = next
    props.onBlocked(true)
    next.addEventListener(
      'load',
      () => {
        const data = String(next.result).split(',')[1] ?? ''
        let mime = ''
        if (data.startsWith('iVBORw0KGgo')) mime = 'image/png'
        else if (data.startsWith('/9j/')) mime = 'image/jpeg'
        else if (
          data.startsWith('UklGR') &&
          atob(data.slice(0, 16)).slice(8, 12) === 'WEBP'
        ) {
          mime = 'image/webp'
        }
        if (mime) props.onChange(`data:${mime};base64,${data}`)
        else setInvalid(true)
        props.onBlocked(!mime)
      },
      { once: true }
    )
    next.addEventListener(
      'error',
      () => {
        setInvalid(true)
        props.onBlocked(true)
      },
      { once: true }
    )
    next.readAsDataURL(file)
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
          choose(event.target.files?.[0])
          event.target.value = ''
        }}
      />
      {props.value && (
        <img
          className='size-12 rounded-md border object-cover'
          src={props.value}
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
      {(props.value || invalid) && (
        <Button
          variant='ghost'
          size='icon'
          type='button'
          disabled={props.disabled}
          aria-label={t('Remove reference image')}
          onClick={() => {
            props.onChange(undefined)
            setInvalid(false)
            props.onBlocked(false)
          }}
        >
          <X aria-hidden='true' />
        </Button>
      )}
      {invalid && (
        <p role='alert' className='text-destructive text-sm'>
          {t('Choose a PNG, JPEG or WebP image up to 5 MiB.')}
        </p>
      )}
    </div>
  )
}
