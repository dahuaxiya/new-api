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
import type { UseFormReturn } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import {
  FieldDescription,
  FieldGroup,
  FieldLegend,
  FieldSet,
} from '@/components/ui/field'
import {
  FormControl,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'

import type { OnboardingFormValues } from '../lib/schema'
import type { OnboardingInstance } from '../types'

/**
 * 选择实例时清除旧槽位，避免把另一台机器的槽位编号误提交给新实例。
 * @param props 表单、服务端可用实例和刷新操作。
 * @return 带可用性提示的实例及槽位选择。
 */
export function SlotSelector(props: {
  form: UseFormReturn<OnboardingFormValues>
  instances: OnboardingInstance[]
  disabled: boolean
  refreshing: boolean
  onRefresh: () => void
}): React.JSX.Element {
  const { t } = useTranslation()
  const instance = props.instances.find(
    (item) => item.id === props.form.watch('instance_id')
  )
  return (
    <FieldSet disabled={props.disabled}>
      <FieldLegend>{t('Instance and account slot')}</FieldLegend>
      <FieldGroup className='grid gap-4 sm:grid-cols-2'>
        <FormField
          control={props.form.control}
          name='instance_id'
          render={({ field }) => (
            <FormItem>
              <FormLabel>{t('Instance')}</FormLabel>
              <FormControl>
                <NativeSelect
                  {...field}
                  className='w-full'
                  onChange={(event) => {
                    field.onChange(event.target.value)
                    props.form.setValue('slot_id', '')
                  }}
                >
                  <NativeSelectOption value=''>
                    {t('Select an instance')}
                  </NativeSelectOption>
                  {props.instances.map((item) => (
                    <NativeSelectOption
                      key={item.id}
                      value={item.id}
                      disabled={!item.available}
                    >
                      {item.name}
                      {!item.available ? ` — ${t('Unavailable')}` : ''}
                    </NativeSelectOption>
                  ))}
                </NativeSelect>
              </FormControl>
              <FormMessage />
            </FormItem>
          )}
        />
        <FormField
          control={props.form.control}
          name='slot_id'
          render={({ field }) => (
            <FormItem>
              <FormLabel>{t('Account slot')}</FormLabel>
              <FormControl>
                <NativeSelect
                  {...field}
                  className='w-full'
                  disabled={!instance || props.disabled}
                >
                  <NativeSelectOption value=''>
                    {t('Select an account slot')}
                  </NativeSelectOption>
                  {(instance?.slots ?? []).map((slot) => (
                    <NativeSelectOption
                      key={slot.id}
                      value={slot.id}
                      disabled={
                        slot.assigned ||
                        (!slot.available && slot.logged_in !== false)
                      }
                    >
                      {slot.name || slot.id}
                      {slot.reason ? ` — ${t(slot.reason)}` : ''}
                    </NativeSelectOption>
                  ))}
                </NativeSelect>
              </FormControl>
              <FormMessage />
            </FormItem>
          )}
        />
      </FieldGroup>
      <FieldDescription>
        {t(
          'Existing vm2api keys for the selected slot remain valid. Opening this account does not revoke their access.'
        )}
      </FieldDescription>
      <Button
        type='button'
        variant='outline'
        disabled={props.disabled || props.refreshing}
        onClick={props.onRefresh}
      >
        {t('Refresh account slots')}
      </Button>
      {instance && !instance.slots.some((slot) => slot.available) && (
        <Alert>
          <AlertDescription>
            {t('This instance has no available account slots.')}
          </AlertDescription>
        </Alert>
      )}
      {props.instances
        .filter((item) => !item.available)
        .map((item) => (
          <Alert key={item.id}>
            <AlertDescription>
              {item.name}: {item.error ? t(item.error) : t('Unavailable')}
            </AlertDescription>
          </Alert>
        ))}
    </FieldSet>
  )
}
