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
import { zodResolver } from '@hookform/resolvers/zod'
import { useMutation } from '@tanstack/react-query'
import { useEffect, useRef, useState } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import {
  Field,
  FieldDescription,
  FieldError,
  FieldGroup,
  FieldLabel,
} from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { handleServerError } from '@/lib/handle-server-error'
import { useAuthStore } from '@/stores/auth-store'

import { createOnboardingSlot } from '../api'
import { createSlotSchema, type CreateSlotFormValues } from '../lib/schema'
import { useSlotDraftStore } from '../slot-draft-store'
import type {
  CreateSlotRequest,
  OnboardingInstance,
  OnboardingSlot,
} from '../types'
import { SlotLogin } from './slot-login'

/**
 * 创建槽位与登录分开确认；实例切换时由父组件 key 重建，防止复用另一实例的授权会话。
 * @param props 当前实例、槽位与更新回调。
 * @return 创建槽位和官方登录步骤。
 */
export function SlotSetup(props: {
  instance: OnboardingInstance
  slot?: OnboardingSlot
  disabled: boolean
  onSlotUpdated: (slot: OnboardingSlot) => void
}): React.JSX.Element {
  const { t } = useTranslation()
  const actorId = useAuthStore((state) => state.auth.user?.id ?? 0)
  const [restoring, setRestoring] = useState(true)
  const [storageFailure, setStorageFailure] = useState(false)
  const [creating, setCreating] = useState(false)
  const [confirmOpen, setConfirmOpen] = useState(false)
  const [attempted, setAttempted] = useState(false)
  const request = useRef<CreateSlotRequest | undefined>(undefined)
  const form = useForm<CreateSlotFormValues>({
    resolver: zodResolver(createSlotSchema),
    defaultValues: { name: '', exit_id: '' },
  })
  useEffect(() => {
    try {
      const saved = useSlotDraftStore
        .getState()
        .readDraft(actorId, props.instance.id)
      if (saved) {
        request.current = saved
        form.reset({ name: saved.name, exit_id: saved.exit_id })
        setCreating(true)
        setAttempted(true)
      }
    } catch {
      // 草稿可能对应已创建但响应丢失的远端容器；读取失败不能重新生成编号。
      setStorageFailure(true)
    } finally {
      setRestoring(false)
    }
  }, [actorId, props.instance.id, form])
  const exits = props.instance.exits ?? []
  const mutation = useMutation({
    mutationFn: async () => {
      if (!request.current) throw new Error(t('Invalid onboarding request'))
      return createOnboardingSlot(request.current)
    },
    retry: false,
    onSuccess: (slot) => {
      props.onSlotUpdated(slot)
      try {
        useSlotDraftStore.getState().removeDraft(actorId, props.instance.id)
      } catch {
        setStorageFailure(true)
        return
      }
      setCreating(false)
      setAttempted(false)
      form.reset()
      request.current = undefined
    },
    onError: (error) => handleServerError(error),
  })

  /**
   * 第一次确认前固定创建参数；超时重试不会生成新槽位编号。
   * @param values 校验后的槽位名称及已配置出口标识。
   */
  function reviewSlot(values: CreateSlotFormValues): void {
    if (!attempted) {
      request.current = {
        ...values,
        instance_id: props.instance.id,
        request_id: crypto.randomUUID(),
      }
    }
    setConfirmOpen(true)
  }

  /** 草稿持久化是创建前置条件，确保刷新后的重试仍能寻址同一个容器。 */
  function confirmSlot(): void {
    if (!request.current) return
    try {
      useSlotDraftStore.getState().saveDraft(actorId, request.current)
    } catch {
      setStorageFailure(true)
      setConfirmOpen(false)
      return
    }
    setConfirmOpen(false)
    setAttempted(true)
    mutation.mutate()
  }

  return (
    <div className='space-y-4'>
      {storageFailure && (
        <Alert variant='destructive'>
          <AlertDescription>
            {t(
              'The slot draft cannot be saved or restored. Do not create another slot until this is resolved.'
            )}
          </AlertDescription>
        </Alert>
      )}
      {!creating && !props.disabled && !restoring && !storageFailure && (
        <Button
          type='button'
          variant='outline'
          onClick={() => setCreating(true)}
        >
          {t('Create new slot')}
        </Button>
      )}
      {creating && (
        <div className='space-y-4 rounded-lg border p-4'>
          <p className='text-sm font-medium'>{t('Create new slot')}</p>
          <FieldGroup className='sm:grid sm:grid-cols-2'>
            <Field data-invalid={!!form.formState.errors.name}>
              <FieldLabel htmlFor='onboarding-slot-name'>
                {t('Slot name')}
              </FieldLabel>
              <Input
                id='onboarding-slot-name'
                {...form.register('name')}
                disabled={attempted || props.disabled}
                aria-invalid={!!form.formState.errors.name}
              />
              {form.formState.errors.name && (
                <FieldError>
                  {t(form.formState.errors.name.message || '')}
                </FieldError>
              )}
            </Field>
            <Field data-invalid={!!form.formState.errors.exit_id}>
              <FieldLabel htmlFor='onboarding-slot-exit'>
                {t('Outbound connection')}
              </FieldLabel>
              <NativeSelect
                id='onboarding-slot-exit'
                {...form.register('exit_id')}
                disabled={attempted || props.disabled}
                aria-invalid={!!form.formState.errors.exit_id}
                className='w-full'
              >
                <NativeSelectOption value=''>
                  {t('Select an outbound connection')}
                </NativeSelectOption>
                {exits.map((exit) => (
                  <NativeSelectOption
                    key={exit.id}
                    value={exit.id}
                    disabled={!exit.available}
                  >
                    {exit.name}
                    {exit.reason ? ` — ${t(exit.reason)}` : ''}
                  </NativeSelectOption>
                ))}
              </NativeSelect>
              {form.formState.errors.exit_id && (
                <FieldError>
                  {t(form.formState.errors.exit_id.message || '')}
                </FieldError>
              )}
            </Field>
          </FieldGroup>
          {!attempted && !exits.some((exit) => exit.available) && (
            <Alert>
              <AlertDescription>
                {t(
                  'Configure an outbound connection on this instance before creating a slot.'
                )}
              </AlertDescription>
            </Alert>
          )}
          <FieldDescription>
            {t(
              'Creating a slot does not sign in to GPT. Complete the official authorization below before opening the user account.'
            )}
          </FieldDescription>
          <div className='flex flex-wrap gap-2'>
            <Button
              type='button'
              disabled={
                mutation.isPending ||
                props.disabled ||
                storageFailure ||
                restoring ||
                (!attempted && !exits.some((exit) => exit.available))
              }
              onClick={form.handleSubmit(reviewSlot)}
            >
              {mutation.isPending ? t('Processing...') : t('Create slot')}
            </Button>
            {!attempted && (
              <Button
                type='button'
                variant='ghost'
                onClick={() => setCreating(false)}
              >
                {t('Cancel')}
              </Button>
            )}
          </div>
          {mutation.isError && (
            <Alert variant='destructive'>
              <AlertTitle>{t('Slot creation did not complete')}</AlertTitle>
              <AlertDescription>
                {t(
                  'Retry with the same details to avoid creating a duplicate slot.'
                )}
              </AlertDescription>
            </Alert>
          )}
        </div>
      )}
      {props.slot && !props.slot.assigned && props.slot.logged_in === false && (
        <SlotLogin
          key={props.slot.id}
          instanceId={props.instance.id}
          slot={props.slot}
          disabled={props.disabled || mutation.isPending}
          onSlotUpdated={props.onSlotUpdated}
        />
      )}
      {props.slot?.logged_in && (
        <Alert>
          <AlertDescription>
            {props.slot.available
              ? t(
                  'GPT account is signed in. Continue with the user details below.'
                )
              : t(
                  'GPT is signed in, but the slot is not ready yet. Refresh the account slots to check again.'
                )}
          </AlertDescription>
        </Alert>
      )}
      <ConfirmDialog
        open={confirmOpen}
        onOpenChange={setConfirmOpen}
        title={t('Confirm slot creation')}
        desc={t(
          'A new slot will be created on {{instance}} using the selected outbound connection.',
          { instance: props.instance.name }
        )}
        confirmText={t('Create slot')}
        handleConfirm={() => {
          confirmSlot()
        }}
      />
    </div>
  )
}
