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
import { useEffect, useMemo, useRef } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { Textarea } from '@/components/ui/textarea'
import {
  useChannelModelDiscovery,
  type ChannelModelDiscoveryRequest,
} from '../hooks/use-channel-model-discovery'
import type { RetailBindingChannel } from '../retail-pricing-api'

/** Parse the same separators as the supply editor without changing case-sensitive upstream IDs. */
function modelNames(value: string) {
  return value
    .split(/[,\r\n]/)
    .map((item) => item.trim())
    .filter(Boolean)
}

/** Preview this supply credential and edit only its draft; the parent confirms the eventual save. */
export function RetailSupplyModelPicker(props: {
  channel: RetailBindingChannel
  draftKey: string
  value: string
  label: string
  following: boolean
  disabled: boolean
  onChange: (models: string) => void
}) {
  const { t } = useTranslation()
  const { id, base_url, type, has_key } = props.channel
  const key = props.draftKey.trim()
  // A new group must supply its own key. Never substitute the site parent's saved key.
  const request = useMemo<ChannelModelDiscoveryRequest>(
    () =>
      key || id < 0
        ? { kind: 'preview', data: { base_url, type, key } }
        : { kind: 'saved', channelId: id },
    [base_url, type, key, id]
  )
  const enabled = key ? !!type && !/\s/.test(key) : id > 0 && has_key
  const discovery = useChannelModelDiscovery({ enabled, request })
  const selected = new Set(modelNames(props.value))
  // Keep saved entries selectable even if the upstream omits them. Only a fresh success adds candidates.
  const candidates = [
    ...new Set([
      ...modelNames(props.channel.models),
      ...selected,
      ...(discovery.status === 'success'
        ? discovery.models.filter(
            (name) =>
              typeof name === 'string' &&
              name.length > 0 &&
              name.length <= 200 &&
              name === name.trim() &&
              !/[,\r\n]/.test(name)
          )
        : []),
    ]),
  ].sort()
  const locked = props.disabled || props.following
  const gesture = useRef<{
    checked: boolean
    last: number
    values: Set<string>
  } | null>(null)
  useEffect(() => {
    // Release outside the list or a window switch must not leave the next hover painting.
    const stop = () => {
      gesture.current = null
    }
    window.addEventListener('mouseup', stop)
    window.addEventListener('blur', stop)
    return () => {
      stop()
      window.removeEventListener('mouseup', stop)
      window.removeEventListener('blur', stop)
    }
  }, [request, locked])

  /** Fill skipped rows during fast motion. Retracing applies the same value until mouseup. */
  function paint(index: number) {
    const drag = gesture.current
    if (!drag || locked) return
    for (const name of candidates.slice(
      Math.min(index, drag.last),
      Math.max(index, drag.last) + 1
    )) {
      if (drag.checked) drag.values.add(name)
      else drag.values.delete(name)
    }
    drag.last = index
    props.onChange([...drag.values].join(','))
  }
  return (
    <fieldset className='space-y-2'>
      <legend className='text-sm font-medium'>{props.label}</legend>
      <Button
        type='button'
        size='sm'
        variant='outline'
        disabled={!enabled || props.disabled || discovery.status === 'loading'}
        onClick={() => void discovery.fetch()}
      >
        {discovery.status === 'loading' ? t('Loading...') : t('Fetch Models')}
      </Button>
      {!enabled && (
        <p className='text-muted-foreground text-xs'>
          {t('Enter the supply group Key to fetch models.')}
        </p>
      )}
      {discovery.status === 'error' && (
        <p role='alert'>{t('Failed to fetch models')}</p>
      )}
      {discovery.status === 'success' && !discovery.models.length && (
        <p role='status'>{t('No models found')}</p>
      )}
      {props.following ? (
        <p className='text-muted-foreground text-xs'>
          {t(
            'To select models manually, turn off model following for all bindings using this supply channel.'
          )}
        </p>
      ) : (
        candidates.length > 0 && (
          <p className='text-muted-foreground text-xs'>
            {t('Drag across models to select or deselect them.')}
          </p>
        )
      )}
      {candidates.length > 0 && (
        <div className='max-h-64 overflow-y-auto rounded-md border p-1'>
          {candidates.map((name, index) => (
            <button
              key={name}
              type='button'
              role='checkbox'
              aria-checked={selected.has(name)}
              disabled={locked}
              className='hover:bg-muted focus-visible:bg-muted flex w-full select-none items-center gap-2 rounded px-2 py-1 text-start text-sm disabled:opacity-60'
              onMouseDown={(event) => {
                if (event.button !== 0 || locked) return
                event.preventDefault()
                event.currentTarget.focus({ preventScroll: true })
                gesture.current = {
                  checked: !selected.has(name),
                  last: index,
                  values: new Set(selected),
                }
                paint(index)
              }}
              onMouseEnter={(event) => {
                if (event.buttons === 1) paint(index)
              }}
              onClick={(event) => {
                // A physical mouse click was already applied on press. Keyboard and AT clicks have zero detail.
                if (event.detail !== 0) return
                const next = new Set(selected)
                if (next.has(name)) next.delete(name)
                else next.add(name)
                props.onChange([...next].join(','))
              }}
            >
              <span
                aria-hidden='true'
                className='flex size-4 shrink-0 items-center justify-center rounded-sm border'
              >
                {selected.has(name) ? '✓' : ''}
              </span>
              <span className='break-all'>{name}</span>
            </button>
          ))}
        </div>
      )}
      {!props.following && (
        <details>
          <summary className='cursor-pointer text-xs'>
            {t('One model per line, or separated by commas')}
          </summary>
          <Textarea
            aria-label={t('One model per line, or separated by commas')}
            maxLength={16000}
            disabled={locked}
            value={props.value}
            onChange={(event) => props.onChange(event.target.value)}
          />
        </details>
      )}
    </fieldset>
  )
}
