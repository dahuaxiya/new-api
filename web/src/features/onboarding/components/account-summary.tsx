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
import { Link } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'

import { CopyButton } from '@/components/copy-button'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { formatQuota } from '@/lib/format'

import type { OnboardingResult } from '../types'

/**
 * 展示开户的非敏感结果；用户自行登录创建 Key，管理员不会在这里看到上游凭据。
 * @param props 可公开给管理员核对的账号归属与统一入口。
 * @return 确认摘要或开户成功信息。
 */
export function AccountSummary(props: {
  username: string
  instance: string
  slot: string
  quota?: number
  apiBaseUrl: string
  result?: OnboardingResult
}): React.JSX.Element {
  const { t } = useTranslation()
  return (
    <div className='space-y-4'>
      {props.result?.status === 'completed' && (
        <Alert>
          <AlertTitle>{t('Account opened')}</AlertTitle>
          <AlertDescription>
            {t(
              'The user can sign in and create an API key in the user center.'
            )}
          </AlertDescription>
        </Alert>
      )}
      <dl className='grid min-w-0 grid-cols-[auto_1fr] gap-x-5 gap-y-3 text-sm'>
        <dt className='text-muted-foreground'>{t('Username')}</dt>
        <dd className='min-w-0 break-all'>{props.username}</dd>
        <dt className='text-muted-foreground'>{t('Instance')}</dt>
        <dd className='min-w-0 break-all'>{props.instance}</dd>
        <dt className='text-muted-foreground'>{t('Account slot')}</dt>
        <dd className='min-w-0 break-all'>{props.slot}</dd>
        {props.quota !== undefined && (
          <>
            <dt className='text-muted-foreground'>{t('Initial quota')}</dt>
            <dd>{formatQuota(props.quota)}</dd>
          </>
        )}
        <dt className='text-muted-foreground'>{t('API Base URL')}</dt>
        <dd className='flex min-w-0 items-center gap-2'>
          <code className='min-w-0 break-all'>{props.apiBaseUrl}</code>
          <CopyButton
            value={props.apiBaseUrl}
            aria-label={t('Copy API base URL')}
          />
        </dd>
      </dl>
      {props.result?.status === 'completed' && (
        <Button variant='outline' render={<Link to='/users' />}>
          {t('Back to users')}
        </Button>
      )}
    </div>
  )
}
