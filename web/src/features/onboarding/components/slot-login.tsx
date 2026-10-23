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
import { useMutation } from '@tanstack/react-query'
import { useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Field, FieldDescription, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { handleServerError } from '@/lib/handle-server-error'
import { getServerErrorMessage } from '@/lib/server-error-message'

import { finishSlotLogin, startSlotLogin } from '../api'
import { isOfficialAuthorizationURL } from '../lib/schema'
import type { OnboardingSlot, SlotLoginSession } from '../types'

/**
 * 官方登录由操作者在新窗口完成；页面仅传递短期回调，不接收 GPT 密码或长期令牌。
 * @param props 当前实例、未分配槽位及成功后的更新回调。
 * @return 官方授权及回调确认表单。
 */
export function SlotLogin(props: {
  instanceId: string
  slot: OnboardingSlot
  disabled: boolean
  onSlotUpdated: (slot: OnboardingSlot) => void
}): React.JSX.Element {
  const { t } = useTranslation()
  const [session, setSession] = useState<SlotLoginSession>()
  const [callbackUrl, setCallbackUrl] = useState('')
  // 回调可能携带一次性授权码，不作为 mutation variables 写入 React Query 缓存。
  const callback = useRef('')
  const start = useMutation({
    mutationFn: async () => {
      const data = await startSlotLogin(props.instanceId, props.slot.id)
      if (!isOfficialAuthorizationURL(data.auth_url)) {
        throw new Error(
          t('The authorization URL is not an approved OpenAI address.')
        )
      }
      setSession(data)
      finish.reset()
      setCallbackUrl('')
      callback.current = ''
    },
    retry: false,
    gcTime: 0,
    onError: (error) => handleServerError(error),
  })
  const finish = useMutation({
    mutationFn: async () => {
      if (!session) throw new Error(t('Start GPT authorization first.'))
      return finishSlotLogin(
        props.instanceId,
        props.slot.id,
        session.session_id,
        callback.current
      )
    },
    retry: false,
    gcTime: 0,
    onSuccess: (slot) => {
      setSession(undefined)
      setCallbackUrl('')
      callback.current = ''
      props.onSlotUpdated(slot)
    },
    onError: (error) => handleServerError(error),
  })
  const busy = props.disabled || start.isPending || finish.isPending
  return (
    <div className='space-y-4 rounded-lg border p-4'>
      <div>
        <h3 className='text-sm font-medium'>{t('Sign in to GPT')}</h3>
        <p className='text-muted-foreground mt-1 text-sm'>
          {t(
            'Sign in on the official OpenAI page. Never enter a GPT password in this form.'
          )}
        </p>
      </div>
      <Button
        type='button'
        variant='outline'
        disabled={busy}
        onClick={() => start.mutate()}
      >
        {session ? t('Restart authorization') : t('Start GPT authorization')}
      </Button>
      {(start.isError || finish.isError) && (
        <Alert variant='destructive'>
          <AlertDescription>
            {getServerErrorMessage(start.error ?? finish.error)}
          </AlertDescription>
        </Alert>
      )}
      {session && (
        <>
          <Button
            nativeButton={false}
            role='link'
            render={
              <a
                href={session.auth_url}
                target='_blank'
                rel='noopener noreferrer'
              />
            }
          >
            {t('Open official authorization')}
          </Button>
          <Alert>
            <AlertTitle>
              {t('After authorizing, copy the callback URL')}
            </AlertTitle>
            <AlertDescription>
              {t(
                'If the browser opens a localhost address that cannot load, copy its full address and paste it below. Do not share that address.'
              )}
            </AlertDescription>
          </Alert>
          <Field>
            <FieldLabel htmlFor='onboarding-callback'>
              {t('Authorization callback URL')}
            </FieldLabel>
            <Input
              id='onboarding-callback'
              value={callbackUrl}
              onChange={(event) => {
                setCallbackUrl(event.target.value)
                callback.current = event.target.value
              }}
              autoComplete='off'
              spellCheck={false}
              disabled={busy}
            />
            <FieldDescription>
              {t(
                'The server verifies the callback belongs to this slot and authorization session.'
              )}
            </FieldDescription>
          </Field>
          <Button
            type='button'
            disabled={busy || !callbackUrl.trim()}
            onClick={() => finish.mutate()}
          >
            {finish.isPending ? t('Processing...') : t('Complete GPT sign-in')}
          </Button>
        </>
      )}
    </div>
  )
}
