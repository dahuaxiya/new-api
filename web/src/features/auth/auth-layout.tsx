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
import { useEffect, useRef } from 'react'
import { useTranslation } from 'react-i18next'

import { BrandArtwork } from '@/components/layout/components/brand-artwork'
import { Footer } from '@/components/layout/components/footer'
import { Skeleton } from '@/components/ui/skeleton'
import { useSystemConfig } from '@/hooks/use-system-config'
import { isLuobocodeBrand } from '@/lib/luobocode-brand'
import { cn } from '@/lib/utils'

import { isIssuerFrame, retailOrigin } from './lib/embedded-login'

type AuthLayoutProps = {
  retailEntrance?: boolean
  retailHandoff?: boolean
  children: React.ReactNode
}

/**
 * 两站登录页复用品牌插画与表单容器，窄屏优先展示原有登录操作。
 * 仅调整布局，不改变登录、找回及跨站身份流程。
 * @param children 登录或找回账号的表单内容。
 * @param retailEntrance 是否使用中转站首页同一套透明小猫入口布局。
 * @param retailHandoff 当前页面是否承接中转站首页的跨站转场；只控制入场动效。
 * @return 正文可自然增高、页脚不覆盖表单操作的登录页面。
 */
export function AuthLayout({
  children,
  retailEntrance = false,
  retailHandoff = false,
}: AuthLayoutProps) {
  const { systemName, logo, loading } = useSystemConfig()
  const { t } = useTranslation()
  const branded = isLuobocodeBrand(systemName)

  const artworkRef = useRef<HTMLImageElement>(null)
  useEffect(() => {
    if (!document.querySelector('#retail-arrival-frame') || loading) return
    let active = true
    // React 已提交并不表示图片已经可画。解码完成才揭开表单，失败也要允许登录。
    const reveal = () => {
      if (active) window.dispatchEvent(new Event('retail-arrival-ready'))
    }
    const img = artworkRef.current
    if (img?.decode) void img.decode().then(reveal, reveal)
    else reveal()
    return () => {
      active = false
    }
  }, [branded, loading, retailEntrance])

  const panel = useRef<HTMLDivElement>(null)
  const embedded = isIssuerFrame()
  useEffect(() => {
    const element = panel.current
    if (!embedded || !element) return
    // 量实际内容而不是 viewport；避免 iframe 随父页反复增高。
    const measure = () =>
      window.parent.postMessage(
        {
          type: 'luobo.form.measure',
          height: element.getBoundingClientRect().height,
        },
        retailOrigin
      )
    const observer = new ResizeObserver(measure)
    observer.observe(element)
    measure()
    return () => observer.disconnect()
  }, [embedded])
  if (embedded) {
    return (
      <div ref={panel} className='luobo-auth-form luobo-embedded-form'>
        {children}
      </div>
    )
  }
  return (
    <div className='relative grid min-h-svh max-w-none grid-rows-[1fr_auto]'>
      <Link
        to='/'
        className='absolute top-4 left-4 z-10 flex items-center gap-2 transition-opacity hover:opacity-80 sm:top-8 sm:left-8'
      >
        {loading ? (
          <Skeleton className='h-8 w-40' />
        ) : (
          <h1>
            <BrandArtwork logo={logo} name={systemName} />
          </h1>
        )}
      </Link>
      {branded && retailEntrance && (
        <Link
          to='/'
          className='text-muted-foreground absolute top-4 right-5 z-10 inline-flex min-h-11 items-center gap-2 text-sm sm:top-6 sm:right-10'
        >
          <span aria-hidden='true'>←</span>
          {t('Back to Home')}
        </Link>
      )}
      <div
        className={cn(
          'container flex items-center pt-20 pb-8 sm:pt-24',
          branded && 'max-w-6xl lg:grid lg:grid-cols-2 lg:gap-20',
          branded && retailEntrance && 'luobo-entry-layout',
          branded && retailHandoff && 'luobo-entry-arrival'
        )}
      >
        {branded && (
          <aside
            className='luobo-auth-art hidden overflow-hidden rounded-3xl border lg:block'
            aria-hidden='true'
          >
            <div className='px-9 pt-10'>
              <p className='font-serif text-4xl leading-tight tracking-tight'>
                {t('API access.')}
                <br />
                <span className='luobo-accent'>{t('Account hosting.')}</span>
              </p>
            </div>
            <img
              ref={artworkRef}
              src={
                retailEntrance
                  ? '/brand/gateway-sculpture.webp'
                  : '/brand/luobocode-connection-768.webp'
              }
              width={768}
              height={512}
              alt=''
              className={cn(
                'w-full',
                retailEntrance && 'luobo-gateway-sculpture'
              )}
            />
            <p className='px-9 pb-8 text-sm leading-6'>
              {t('Use one sign-in for API access and account hosting.')}
            </p>
          </aside>
        )}
        <div className='luobo-auth-form mx-auto flex w-full max-w-[480px] flex-col justify-center space-y-2 px-4 py-8 sm:p-8'>
          {children}
        </div>
      </div>
      <Footer compact />
    </div>
  )
}
