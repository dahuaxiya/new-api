import { useNavigate } from '@tanstack/react-router'
import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Skeleton } from '@/components/ui/skeleton'

import { issuerFrameName, issuerOrigin } from '../../lib/embedded-login'

/** 父页面保留首页画布，凭据只进入身份站。回调只接受本次事务和当前子窗口。 */
export function EmbeddedLogin({
  url,
  registration = false,
}: {
  url: string
  registration?: boolean
}) {
  const ref = useRef<HTMLIFrameElement>(null)
  const [height, setHeight] = useState(390)
  const [ready, setReady] = useState(false)
  const [fallback, setFallback] = useState(false)
  const navigate = useNavigate()
  const { t } = useTranslation()
  const title = registration
    ? t('Create an account')
    : t('Sign in to the relay')
  useEffect(() => {
    let consumed = false
    const state = new URL(url).searchParams.get('state')
    const timer = setTimeout(() => setFallback(true), 12000)
    /** 登录回调在顶层兑换一次，子窗口不会写入或传递中转站会话。 */
    function receive(event: MessageEvent) {
      if (
        event.origin !== issuerOrigin ||
        event.source !== ref.current?.contentWindow
      ) {
        return
      }
      const data = event.data
      // Only these two local routes can be selected by the active issuer window.
      if (
        data?.type ===
        (registration ? 'luobo.form.sign-in' : 'luobo.form.sign-up')
      ) {
        void navigate({ to: registration ? '/sign-in' : '/sign-up' })
        return
      }
      if (data?.type === 'luobo.form.measure' && Number.isFinite(data.height)) {
        setReady(true)
        clearTimeout(timer)
        setFallback(false)
        setHeight(
          Math.max(
            registration ? 395 : 230,
            Math.min(1400, Math.ceil(data.height) + 8)
          )
        )
      }
      if (data?.type === 'luobo.form.error') setFallback(true)
      if (
        registration ||
        data?.type !== 'luobo.form.complete' ||
        consumed ||
        !state ||
        data.state !== state ||
        typeof data.code !== 'string' ||
        !data.code ||
        data.code.length > 1024
      ) {
        return
      }
      consumed = true
      void navigate({
        href: `/oauth/site-sso?${new URLSearchParams({ code: data.code, state })}`,
        replace: true,
      })
    }
    window.addEventListener('message', receive)
    return () => {
      clearTimeout(timer)
      window.removeEventListener('message', receive)
    }
  }, [url, navigate, registration])
  return (
    <div className='relative'>
      {!ready && (
        <div className='luobo-embedded-form pointer-events-none absolute inset-x-0 top-0'>
          <h2 className='font-semibold tracking-tight'>{title}</h2>
          <span className='sr-only' role='status'>
            {t('Loading...')}
          </span>
          {/* Match the issuer's field geometry; placeholders never receive focus or credentials. */}
          <div aria-hidden='true' className='mt-8 grid gap-4'>
            {(registration ? ['Email'] : ['Username or Email', 'Password']).map(
              (label) => (
                <div key={label} className='grid gap-2'>
                  <span className='text-muted-foreground text-sm leading-none font-medium'>
                    {t(label)}
                  </span>
                  <Skeleton className='bg-muted/50 border-border/60 h-12 rounded-[9px] border motion-reduce:animate-none' />
                </div>
              )
            )}
            <Skeleton className='bg-primary/15 mt-2 h-12 rounded-[9px] motion-reduce:animate-none' />
          </div>
        </div>
      )}
      <iframe
        // 身份站为固定的另一来源；脚本和它自己的 Cookie 都是验证会话所必需的。
        // eslint-disable-next-line react/iframe-missing-sandbox
        sandbox='allow-scripts allow-same-origin allow-forms allow-top-navigation-by-user-activation allow-popups'
        title={title}
        ref={ref}
        name={issuerFrameName}
        src={url}
        style={{ height, visibility: ready ? 'visible' : 'hidden' }}
        className='block w-full border-0'
      />
      {fallback && registration && (
        <button
          type='button'
          className='text-primary underline'
          onClick={() => window.location.reload()}
        >
          {t('Retry')}
        </button>
      )}
      {fallback && !registration && (
        <a className='text-primary underline' href={url}>
          {t('Sign in')}
        </a>
      )}
    </div>
  )
}
