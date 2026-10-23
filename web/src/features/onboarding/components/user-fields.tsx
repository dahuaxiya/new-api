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

import { FieldGroup, FieldLegend, FieldSet } from '@/components/ui/field'
import {
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import { getCurrencyDisplay, getCurrencyLabel } from '@/lib/currency'
import { formatQuota, parseQuotaFromDollars } from '@/lib/format'

import type { OnboardingFormValues } from '../lib/schema'

/**
 * 收集普通用户资料；额度单位和换算沿用用户管理，密码不参与摘要或持久化。
 * @param props 当前表单以及事务开始后的禁用状态。
 * @return 用户资料字段组。
 */
export function OnboardingUserFields(props: {
  form: UseFormReturn<OnboardingFormValues>
  disabled: boolean
}): React.JSX.Element {
  const { t } = useTranslation()
  const currency = getCurrencyDisplay()
  const quota = props.form.watch('quota_dollars')
  return (
    <FieldSet disabled={props.disabled}>
      <FieldLegend>{t('User details')}</FieldLegend>
      <FieldGroup className='grid gap-4 sm:grid-cols-2'>
        <FormField
          control={props.form.control}
          name='username'
          render={({ field }) => (
            <FormItem>
              <FormLabel>{t('Username')}</FormLabel>
              <FormControl>
                <Input {...field} autoComplete='off' />
              </FormControl>
              <FormMessage />
            </FormItem>
          )}
        />
        <FormField
          control={props.form.control}
          name='display_name'
          render={({ field }) => (
            <FormItem>
              <FormLabel>{t('Display Name')}</FormLabel>
              <FormControl>
                <Input {...field} autoComplete='off' />
              </FormControl>
              <FormMessage />
            </FormItem>
          )}
        />
        <FormField
          control={props.form.control}
          name='password'
          render={({ field }) => (
            <FormItem>
              <FormLabel>{t('Initial password')}</FormLabel>
              <FormControl>
                <Input {...field} type='password' autoComplete='new-password' />
              </FormControl>
              <FormDescription>
                {t('Use at least 15 characters for the initial password.')}
              </FormDescription>
              <FormMessage />
            </FormItem>
          )}
        />
        <FormField
          control={props.form.control}
          name='quota_dollars'
          render={({ field }) => (
            <FormItem>
              <FormLabel>
                {t('Initial quota')} ({getCurrencyLabel()})
              </FormLabel>
              <FormControl>
                <Input
                  {...field}
                  type='number'
                  min={0}
                  step={currency.meta.kind === 'tokens' ? 1 : 0.000001}
                  onChange={(event) =>
                    field.onChange(
                      event.target.value === '' ? 0 : Number(event.target.value)
                    )
                  }
                />
              </FormControl>
              <FormMessage />
              <p className='text-muted-foreground text-xs'>
                {formatQuota(parseQuotaFromDollars(quota || 0))}
              </p>
            </FormItem>
          )}
        />
      </FieldGroup>
    </FieldSet>
  )
}
