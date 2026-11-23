import { Link } from '@tanstack/react-router'
import {
  ArrowDown,
  ArrowRight,
  ArrowUpRight,
  Braces,
  Fingerprint,
  ReceiptText,
} from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { SiteSwitcher } from '@/features/auth/components/unified-account'
import { useStatus } from '@/hooks/use-status'

import { ApiExample } from './api-example'
import { RetailPricing } from './retail-pricing'

/**
 * 中转站默认首页，连接品牌介绍、实时目录和接入示例；不把目录可见误报为上游可用。
 * @param props 登录状态只决定入口目的地，业务权限继续由目标页面检查。
 * @return 支持键盘导航、窄屏和深色主题的公开首页。
 */
export function RetailHero(props: { isAuthenticated: boolean }) {
  const { t } = useTranslation()
  const { status } = useStatus()
  return (
    <main className='luobo-home mx-auto w-full max-w-[1240px] px-5 sm:px-10'>
      <section className='luobo-hero grid items-center gap-10 pt-28 pb-14 lg:grid-cols-[1fr_1.08fr] lg:gap-12 lg:pt-40 lg:pb-20'>
        <div className='luobo-reveal min-w-0'>
          <h1 className='luobo-display text-[clamp(2.8rem,5.5vw,4.7rem)] leading-[1.16] tracking-[-0.055em]'>
            {t('One API address.')}
            <br />
            <span className='luobo-accent'>{t('Multiple models.')}</span>
          </h1>
          <p className='text-muted-foreground mt-7 max-w-[27rem] text-[15px] leading-7'>
            {t(
              'OpenAI-compatible API access. Choose a model, create a key, and track usage and cost for each request.'
            )}
          </p>
          <div className='mt-9 flex flex-wrap items-center gap-4'>
            <Button
              size='lg'
              className='luobo-primary h-12 rounded-full px-6'
              render={
                <Link
                  to={props.isAuthenticated ? '/dashboard' : '/sign-in'}
                  viewTransition={
                    !props.isAuthenticated &&
                    !window.matchMedia('(prefers-reduced-motion: reduce)')
                      .matches
                  }
                />
              }
            >
              {props.isAuthenticated
                ? t('Enter user center')
                : t('Get started')}
              <ArrowUpRight aria-hidden='true' />
            </Button>
            <Button
              variant='ghost'
              size='lg'
              className='h-12 rounded-full px-4'
              render={<Link to='/pricing' />}
            >
              {t('View prices')}
              <ArrowRight aria-hidden='true' />
            </Button>
          </div>
          <a
            href='#integration'
            className='text-muted-foreground mt-7 inline-flex min-h-11 items-center gap-2 rounded-sm text-xs underline-offset-4 hover:underline focus-visible:outline-2 focus-visible:outline-offset-4'
          >
            <ArrowDown className='size-3.5' aria-hidden='true' />
            {t('See how to connect')}
          </a>
        </div>
        <figure className='luobo-hero-sculpture'>
          <img
            src='/brand/gateway-sculpture.webp'
            width={1536}
            height={1024}
            fetchPriority='high'
            alt={t(
              'A ceramic cat and carrot beside an orange arch connecting several modules.'
            )}
            className='luobo-gateway-sculpture aspect-[3/2] w-full'
          />
        </figure>
      </section>

      <div className='luobo-principles grid border-y md:grid-cols-3'>
        {[
          {
            icon: Braces,
            title: t('OpenAI-compatible'),
            detail: t('Connect with an OpenAI-compatible API.'),
          },
          {
            icon: ReceiptText,
            title: t('Usage records'),
            detail: t('Review requests, tokens and charges together.'),
          },
          {
            icon: Fingerprint,
            title: t('API keys'),
            detail: t('Create and manage keys for your applications.'),
          },
        ].map((item) => (
          <div
            key={item.title}
            className='flex items-start gap-4 py-7 md:px-6 first:md:pl-0 last:md:pr-0'
          >
            <item.icon
              className='luobo-accent mt-0.5 size-5 shrink-0'
              strokeWidth={1.5}
              aria-hidden='true'
            />
            <div>
              <h2 className='text-sm font-medium'>{item.title}</h2>
              <p className='text-muted-foreground mt-2 text-xs leading-5'>
                {item.detail}
              </p>
            </div>
          </div>
        ))}
      </div>

      <RetailPricing />
      <ApiExample />

      {status?.site_sso && (
        <section className='luobo-hosting-callout my-16 flex flex-col justify-between gap-6 rounded-2xl border p-7 sm:flex-row sm:items-center sm:p-9'>
          <div className='max-w-xl'>
            <h2 className='font-serif text-2xl tracking-tight text-balance'>
              {t('Account hosting')}
            </h2>
            <p className='text-muted-foreground mt-3 text-sm leading-6'>
              {t(
                'One account for hosting and API access. Balances, orders and API keys stay separate.'
              )}
            </p>
          </div>
          <div className='shrink-0'>
            <SiteSwitcher />
          </div>
        </section>
      )}
    </main>
  )
}
