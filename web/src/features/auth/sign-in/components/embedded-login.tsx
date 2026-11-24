import { useNavigate } from '@tanstack/react-router'
import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { issuerFrameName, issuerOrigin } from '../../lib/embedded-login'

/** 父页面保留首页画布，凭据只进入身份站。回调只接受本次事务和当前子窗口。 */
export function EmbeddedLogin({ url }: { url: string }) {
  const ref = useRef<HTMLIFrameElement>(null)
  const [height, setHeight] = useState(390)
  const [ready, setReady] = useState(false)
  const [fallback, setFallback] = useState(false)
  const navigate = useNavigate()
  const { t } = useTranslation()
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
      if (data?.type === 'luobo.form.measure' && Number.isFinite(data.height)) {
        setReady(true)
        clearTimeout(timer)
        setFallback(false)
        setHeight(Math.max(230, Math.min(1400, Math.ceil(data.height) + 8)))
      }
      if (data?.type === 'luobo.form.error') setFallback(true)
      if (
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
  }, [url, navigate])
  return (
    <div className='relative'>
      {!ready && (
        <div role='status' className='absolute inset-x-0 top-0'>
          <h2>{t('Sign in to the relay')}</h2>
          <p className='text-muted-foreground mt-6 text-sm'>
            {t('Loading...')}
          </p>
        </div>
      )}
      <iframe
        // 身份站为固定的另一来源；脚本和它自己的 Cookie 都是验证会话所必需的。
        // eslint-disable-next-line react/iframe-missing-sandbox
        sandbox='allow-scripts allow-same-origin allow-forms allow-top-navigation-by-user-activation allow-popups'
        title={t('Sign in to the relay')}
        ref={ref}
        name={issuerFrameName}
        src={url}
        style={{ height, visibility: ready ? 'visible' : 'hidden' }}
        className='block w-full border-0'
      />
      {fallback && (
        <a className='text-primary underline' href={url}>
          {t('Sign in')}
        </a>
      )}
    </div>
  )
}
