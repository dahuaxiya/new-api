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
import { useId } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { z } from 'zod'

import { Button } from '@/components/ui/button'
import {
  Field,
  FieldError,
  FieldGroup,
  FieldLabel,
} from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { api } from '@/lib/api'
import { handleServerError } from '@/lib/handle-server-error'
import {
  getServerErrorMessage,
  requireServerSuccess,
} from '@/lib/server-error-message'

const supplyGroupSchema = z.object({
  name: z.string().trim().min(1).max(120),
  key: z
    .string()
    .trim()
    .max(4096)
    .refine((value) => !/\s/u.test(value)),
  models: z.string().max(16000),
})

/** Create a credential without asking for the shared address or copying a site's existing Key. */
export function RetailSupplyGroupForm(props: {
  siteID: number
  models: string
  onCreated: () => void
  onCancel: () => void
}) {
  const { t } = useTranslation()
  const id = useId()
  const form = useForm<z.infer<typeof supplyGroupSchema>>({
    resolver: zodResolver(supplyGroupSchema),
    defaultValues: { name: '', key: '', models: props.models },
  })
  const mutation = useMutation({
    // Read directly from the mounted form: mutation-cache variables must not retain supplier Keys.
    mutationFn: async () => {
      const values = supplyGroupSchema.parse(form.getValues())
      try {
        const response = await api.post('/api/retail/admin/supplies/groups', {
          site_id: props.siteID,
          ...values,
        })
        requireServerSuccess(response.data)
      } catch (error) {
        // Axios errors retain request.config.data, including the supplier Key. Keep only
        // the safe server message in the mutation cache, without attaching the original cause.
        throw new Error(
          getServerErrorMessage(error, t('Unable to save supply group'))
        )
      }
    },
    gcTime: 0,
    retry: false,
    onSuccess: props.onCreated,
    onError: (error) => handleServerError(error),
    onSettled: () => form.setValue('key', ''),
  })
  return (
    <form
      className='rounded-lg border p-4'
      onSubmit={form.handleSubmit(() => mutation.mutate())}
    >
      <FieldGroup>
        <Field>
          <FieldLabel htmlFor={`${id}-name`}>
            {t('Supply group name')}
          </FieldLabel>
          <Input id={`${id}-name`} maxLength={120} {...form.register('name')} />
          {form.formState.errors.name && (
            <FieldError>
              {t('Enter a group name up to 120 characters.')}
            </FieldError>
          )}
        </Field>
        <Field>
          <FieldLabel htmlFor={`${id}-key`}>{t('Group API Key')}</FieldLabel>
          <Input
            id={`${id}-key`}
            type='password'
            autoComplete='new-password'
            spellCheck={false}
            maxLength={4096}
            {...form.register('key')}
          />
          <p className='text-muted-foreground text-xs'>
            {t(
              'Leave the Key empty to create a disabled draft. Each group needs its own upstream credential.'
            )}
          </p>
          {form.formState.errors.key && (
            <FieldError>
              {t('Enter one API Key without spaces or line breaks.')}
            </FieldError>
          )}
        </Field>
        <Field>
          <FieldLabel htmlFor={`${id}-models`}>{t('Group models')}</FieldLabel>
          <Textarea
            id={`${id}-models`}
            rows={3}
            maxLength={16000}
            {...form.register('models')}
          />
          <p className='text-muted-foreground text-xs'>
            {t(
              'Separate models with commas or new lines. Confirm the models allowed by this upstream group.'
            )}
          </p>
          {form.formState.errors.models && (
            <FieldError>{t('The model list is too long.')}</FieldError>
          )}
        </Field>
        <div className='flex justify-end gap-2'>
          <Button
            type='button'
            variant='ghost'
            disabled={mutation.isPending}
            onClick={props.onCancel}
          >
            {t('Cancel')}
          </Button>
          <Button type='submit' disabled={mutation.isPending}>
            {mutation.isPending ? t('Saving...') : t('Create supply group')}
          </Button>
        </div>
      </FieldGroup>
    </form>
  )
}
