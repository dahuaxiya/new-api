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
import { useMemo, type FocusEventHandler } from 'react'
import { useTranslation } from 'react-i18next'

import { ComboboxInput } from '@/components/ui/combobox-input'

import { getEmailSuggestions } from '../lib/email-suggestions'

type EmailAutocompleteProps = {
  id?: string
  value: string
  onValueChange: (value: string) => void
  onBlur?: FocusEventHandler<HTMLInputElement>
  disabled?: boolean
  invalid?: boolean
  autoFocus?: boolean
  'aria-describedby'?: string
}

/**
 * 邮箱输入在本地即时生成常见域名建议，不发送任何按键内容。菜单复用项目
 * ComboboxInput 的键盘、滚动定位与可访问性能力，只补充邮箱业务选项。
 * @param props 当前值、变更回调和表单可访问性属性。
 * @return 支持补全、拼写纠错和自由输入的邮箱输入框。
 */
export function EmailAutocomplete(props: EmailAutocompleteProps) {
  const { t } = useTranslation()
  const suggestions = useMemo(
    () => getEmailSuggestions(props.value),
    [props.value]
  )
  const options = useMemo(
    () =>
      suggestions.map((suggestion) => ({
        value: suggestion.address,
        label: suggestion.address,
        description:
          suggestion.kind === 'correction' ? t('Suggested correction') : '',
      })),
    [suggestions, t]
  )

  return (
    <ComboboxInput
      id={props.id}
      value={props.value}
      onValueChange={props.onValueChange}
      onBlur={props.onBlur}
      options={options}
      allowCustomValue
      filterOptions={false}
      showEmptyCustomValue={false}
      openOnFocus={false}
      disabled={props.disabled}
      type='email'
      placeholder='name@example.com'
      autoComplete='email'
      autoCapitalize='none'
      inputMode='email'
      spellCheck={false}
      autoFocus={props.autoFocus}
      aria-label={t('Email')}
      aria-invalid={props.invalid ? true : undefined}
      aria-describedby={props['aria-describedby']}
      className='bg-background h-12 rounded-xl px-4 pr-10 text-base shadow-none'
      popupClassName='rounded-xl border-border/80 shadow-xl shadow-black/5'
    />
  )
}
