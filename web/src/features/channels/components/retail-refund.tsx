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
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { z } from 'zod'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { formatLogQuota } from '@/lib/format'
import { handleServerError } from '@/lib/handle-server-error'

import {
  releaseRetailReservation,
  type RetailCharge,
} from '../retail-accounting-api'

const schema = z.object({ reason: z.string().trim().min(5).max(500) })

type Props = {
  charge: RetailCharge
  onClose: () => void
  onReleased: () => void
}

/** 人工释放只处理超过一天的预留；服务器复核状态、金额和时间，失败时保留原因与对话框。 */
export function RetailRefund(props: Props) {
  const { t } = useTranslation()
  const form = useForm<z.infer<typeof schema>>({
    resolver: zodResolver(schema),
    mode: 'onChange',
    defaultValues: { reason: '' },
  })
  const mutation = useMutation({
    mutationFn: (values: z.infer<typeof schema>) =>
      releaseRetailReservation(props.charge, values.reason),
    onSuccess: () => {
      props.onReleased()
      props.onClose()
    },
    onError: (error) => handleServerError(error),
  })
  return (
    <ConfirmDialog
      open
      onOpenChange={(open) => {
        if (!open && !mutation.isPending) props.onClose()
      }}
      title={t('Release reservation')}
      desc={
        <>
          {t(
            'Confirm the request has stopped and the upstream incurred no charge before returning the reservation.'
          )}
          <p className='mt-2 break-all'>{props.charge.id}</p>
          <p>
            {t('Reserved')}: {formatLogQuota(props.charge.reserved)}
          </p>
        </>
      }
      confirmText={t('Confirm release')}
      disabled={!form.formState.isValid}
      isLoading={mutation.isPending}
      handleConfirm={() =>
        void form.handleSubmit((values) => mutation.mutate(values))()
      }
    >
      <div className='space-y-2'>
        <Label htmlFor='retail-review-reason'>
          {t('Review reason (5–500 characters)')}
        </Label>
        <Textarea
          id='retail-review-reason'
          maxLength={500}
          disabled={mutation.isPending}
          {...form.register('reason')}
        />
      </div>
    </ConfirmDialog>
  )
}
