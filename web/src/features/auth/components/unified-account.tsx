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
import { ArrowLeftRight, Loader2, ShieldCheck } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
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
import { useAuthStore } from '@/stores/auth-store'

import { createOAuthAuthorization } from '../api'
import { rememberOAuthLoginRedirect } from '../lib/oauth-callback-mode'
import { hostingSwitchTarget } from '../lib/site-switch'
import { UserAuthForm } from '../sign-in/components/user-auth-form'

/**
 * 统一登录保留 OAuth 的来源校验，等待期间显示状态并防止重复创建事务。
 * @param props 登录后返回的本站路径，由原有回调流程校验和使用。
 * @return 包含失败恢复和浏览器后退恢复的登录入口。
 */
export function UnifiedAccountSignIn(props: { redirectTo?: string }) {
  const { t } = useTranslation()
  const { status } = useStatus()
  const [pending, setPending] = useState(false)
  const requestId = useRef(0)
  const inFlight = useRef(false)
  const issuer = status?.site_sso?.issuer_url

  useEffect(() => {
    /**
     * bfcache 恢复不会重新挂载组件；旧请求完成后也不能再次触发跳转。
     * @param event 浏览器恢复事件，仅处理保留原页面状态的历史导航。
     */
    function restore(event: PageTransitionEvent) {
      if (!event.persisted) return
      requestId.current += 1
      inFlight.current = false
      setPending(false)
    }
    window.addEventListener('pageshow', restore)
    return () => {
      window.removeEventListener('pageshow', restore)
      requestId.current += 1
    }
  }, [])

  /**
   * 只有用户点击后才创建登录事务，服务端绑定浏览器和 PKCE。
   * @return 事务创建结束；页面恢复或卸载后不再处理该次结果。
   */
  async function signIn() {
    if (inFlight.current || !issuer) return
    inFlight.current = true
    const attempt = ++requestId.current
    setPending(true)
    try {
      const result = await createOAuthAuthorization('site-sso', 'login')
      // 后退恢复或卸载已经取消本次页面意图，迟到的响应不能覆盖新一次操作。
      if (attempt !== requestId.current) return
      if (
        !result.authorizationUrl ||
        !issuer ||
        new URL(result.authorizationUrl).origin !== issuer
      ) {
        throw new Error('Verification flow expired')
      }
      rememberOAuthLoginRedirect(result.state, props.redirectTo)
      window.location.assign(result.authorizationUrl)
    } catch (error) {
      if (attempt !== requestId.current) return
      handleServerError(error)
      inFlight.current = false
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
        className='h-11 w-full aria-disabled:opacity-50'
        disabled={pending || !issuer}
        focusableWhenDisabled={pending}
        aria-busy={pending}
        onClick={() => void signIn()}
      >
        {pending ? (
          <Loader2
            className='size-4 animate-spin motion-reduce:animate-none'
            aria-hidden='true'
          />
        ) : (
          <ShieldCheck className='size-4' aria-hidden='true' />
        )}
        {pending ? t('Connecting...') : t('Continue with unified account')}
      </Button>
      <p className='text-muted-foreground text-xs'>
        {t(
          'Enter your account and password on the unified sign-in page. Already signed in? Choose “Sign in with another account” there.'
        )}
      </p>
      <span role='status' className='sr-only'>
        {pending ? t('Connecting...') : ''}
      </span>
      {issuer && (
        <div className='flex justify-between text-sm'>
          <a
            href={`${issuer}/sign-up`}
            className='inline-flex min-h-11 items-center underline underline-offset-4'
          >
            {t('Sign up')}
          </a>
          <a
            href={`${issuer}/forgot-password`}
            className='inline-flex min-h-11 items-center underline underline-offset-4'
          >
            {t('Forgot password')}
          </a>
        </div>
      )}
      <Collapsible>
        <CollapsibleTrigger className='text-muted-foreground min-h-11 text-xs underline underline-offset-4'>
          {t('Operator recovery login')}
        </CollapsibleTrigger>
        <CollapsibleContent className='pt-4'>
          <UserAuthForm redirectTo={props.redirectTo} />
        </CollapsibleContent>
      </Collapsible>
    </div>
  )
}

/**
 * 注册、恢复和安全设置由身份源处理，配置未就绪时不生成账户管理链接。
 * @param props 允许跳转的固定账号页面；本卡片不修改消费站资产。
 * @return 统一账号说明，以及配置就绪后可用的管理入口。
 */
export function UnifiedAccountNotice({
  path = '/security',
}: {
  path?: '/security' | '/sign-up' | '/forgot-password'
}) {
  const { t } = useTranslation()
  const { status } = useStatus()
  const issuer = status?.site_sso?.issuer_url
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
      {issuer && (
        <Button render={<a href={`${issuer}${path}`} />}>
          {t('Manage unified account')}
        </Button>
      )}
    </TitledCard>
  )
}

/** 两站入口由服务端配置提供，公共页和控制台共用；切站不传递会话 Token 或 API Key。 */
export function SiteSwitcher() {
  const { t } = useTranslation()
  const { status } = useStatus()
  const sso = status?.site_sso
  const user = useAuthStore((store) => store.auth.user)
  if (!sso) return null
  return (
    <Button
      variant='outline'
      size='sm'
      render={
        <a
          href={`${sso.consumer ? sso.issuer_url : sso.client_url}/dashboard`}
          onClick={(event) => {
            // 新标签页没有当前页的切站意图，保留链接原生行为。
            if (
              sso.consumer ||
              !user ||
              event.defaultPrevented ||
              event.button !== 0 ||
              event.ctrlKey ||
              event.metaKey ||
              event.shiftKey ||
              event.altKey
            )
              return
            event.preventDefault()
            window.location.assign(hostingSwitchTarget(sso.client_url, user.id))
          }}
        />
      }
    >
      <ArrowLeftRight className='size-4' />
      <span>{t(sso.consumer ? 'Account hosting' : 'API site')}</span>
    </Button>
  )
}
