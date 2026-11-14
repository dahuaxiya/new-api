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
import { ArrowLeftRight, ShieldCheck } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'
import { TitledCard } from '@/components/ui/titled-card'
import { useStatus } from '@/hooks/use-status'
import { handleServerError } from '@/lib/handle-server-error'

import { createOAuthAuthorization } from '../api'
import { rememberOAuthLoginRedirect } from '../lib/oauth-callback-mode'
import { UserAuthForm } from '../sign-in/components/user-auth-form'

/** 统一登录复用现有 OAuth 回调；恢复入口仍调用本站原有密码和 MFA 流程。 */
export function UnifiedAccountSignIn({ redirectTo }: { redirectTo?: string }) {
  const { t } = useTranslation()
  const { status } = useStatus()
  const [pending, setPending] = useState(false)
  const issuer = status?.site_sso?.issuer_url

  /** 只有用户点击后才创建登录事务，服务端绑定浏览器和 PKCE。 */
  async function signIn() {
    setPending(true)
    try {
      const result = await createOAuthAuthorization('site-sso', 'login')
      if (
        !result.authorizationUrl ||
        !issuer ||
        new URL(result.authorizationUrl).origin !== issuer
      ) {
        throw new Error('Verification flow expired')
      }
      rememberOAuthLoginRedirect(result.state, redirectTo)
      window.location.assign(result.authorizationUrl)
    } catch (error) {
      handleServerError(error)
      setPending(false)
    }
  }

  return (
    <div className='space-y-5'>
      <p className='text-muted-foreground text-sm'>
        {t(
          'One account for hosting and API access. Balances, orders and API keys stay separate.'
        )}
      </p>
      <Button
        className='w-full'
        disabled={pending}
        onClick={() => void signIn()}
      >
        <ShieldCheck className='size-4' />
        {t('Continue with unified account')}
      </Button>
      <div className='flex justify-between text-sm'>
        <a href={`${issuer}/sign-up`} className='underline underline-offset-4'>
          {t('Sign up')}
        </a>
        <a
          href={`${issuer}/forgot-password`}
          className='underline underline-offset-4'
        >
          {t('Forgot password')}
        </a>
      </div>
      <Collapsible>
        <CollapsibleTrigger className='text-muted-foreground text-xs underline underline-offset-4'>
          {t('Operator recovery login')}
        </CollapsibleTrigger>
        <CollapsibleContent className='pt-4'>
          <UserAuthForm redirectTo={redirectTo} />
        </CollapsibleContent>
      </Collapsible>
    </div>
  )
}

/** 注册、恢复和安全设置都由身份源处理，本卡片不修改消费站资产。 */
export function UnifiedAccountNotice({
  path = '/security',
}: {
  path?: '/security' | '/sign-up' | '/forgot-password'
}) {
  const { t } = useTranslation()
  const { status } = useStatus()
  return (
    <TitledCard
      title={t('Unified account')}
      icon={<ShieldCheck className='size-4' />}
    >
      <p className='text-muted-foreground mb-4 text-sm'>
        {t(
          'Manage your password, email and sign-in methods on the hosting site. This site keeps its own balance, orders and API keys.'
        )}
      </p>
      <Button render={<a href={`${status?.site_sso?.issuer_url}${path}`} />}>
        {t('Manage unified account')}
      </Button>
    </TitledCard>
  )
}

/** 两站入口由服务端配置提供，公共页和控制台共用；切站不传递会话 Token 或 API Key。 */
export function SiteSwitcher() {
  const { t } = useTranslation()
  const { status } = useStatus()
  const sso = status?.site_sso
  if (!sso) return null
  return (
    <Button
      variant='outline'
      size='sm'
      render={
        <a
          href={`${sso.consumer ? sso.issuer_url : sso.client_url}/dashboard`}
        />
      }
    >
      <ArrowLeftRight className='size-4' />
      <span>{t(sso.consumer ? 'Account hosting' : 'API site')}</span>
    </Button>
  )
}
