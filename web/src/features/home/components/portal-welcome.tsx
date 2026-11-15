import { Link } from '@tanstack/react-router'
import { Activity, BookOpen, KeyRound, ArrowRight } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { useStatus } from '@/hooks/use-status'
import { useSystemConfig } from '@/hooks/use-system-config'

import { RetailHero } from './retail-hero'

/**
 * 根据站点模式展示中转首页或托管入口，不展示虚构业务指标。
 * @param props 当前登录状态，只控制主按钮目的地。
 * @return 与站点配置匹配的公开首页。
 */
export function PortalWelcome(props: { isAuthenticated: boolean }) {
  const { t } = useTranslation()
  const { status } = useStatus()
  const retail = status?.site_mode === 'retail'
  const { systemName } = useSystemConfig()
  const features = [
    {
      title: t('API Keys'),
      description: t('Create and manage keys for your applications.'),
      icon: KeyRound,
    },
    {
      title: t('Usage overview'),
      description: t(
        retail
          ? 'Track API requests, token usage and wallet charges.'
          : 'Track requests, token usage and your GPT subscription quota.'
      ),
      icon: Activity,
    },
    {
      title: t('User guide'),
      description: t('Connect Codex with a short, step-by-step guide.'),
      icon: BookOpen,
    },
  ]
  if (retail) {
    return <RetailHero isAuthenticated={props.isAuthenticated} />
  }
  return (
    <main className='mx-auto w-full max-w-[1240px] px-5 pt-32 pb-20 sm:px-10 sm:pt-40'>
      <div className='max-w-2xl'>
        <p className='text-primary mb-5 text-sm font-medium'>{systemName}</p>
        <h1 className='luobo-display text-4xl leading-tight tracking-tight sm:text-6xl'>
          {retail ? t('Pay as you go API') : t('Hosted accounts')}
        </h1>
        <p className='text-muted-foreground mt-5 max-w-xl text-base leading-7'>
          {t(
            'Manage your keys, understand your usage, and connect the tools you use every day.'
          )}
        </p>
        <div className='mt-8 flex flex-wrap gap-3'>
          <Button
            size='lg'
            render={
              <Link to={props.isAuthenticated ? '/dashboard' : '/sign-in'} />
            }
          >
            {t('Enter user center')}
            <ArrowRight />
          </Button>
          <Button variant='outline' size='lg' render={<Link to='/docs' />}>
            {t('User guide')}
          </Button>
        </div>
      </div>
      <div className='mt-16 grid gap-4 md:grid-cols-3'>
        {features.map(({ title, description, icon: Icon }) => (
          <Card key={title} className='shadow-none'>
            <CardContent className='pt-2'>
              <Icon className='text-primary mb-5 size-5' />
              <h2 className='font-medium'>{title}</h2>
              <p className='text-muted-foreground mt-2 text-sm leading-6'>
                {description}
              </p>
            </CardContent>
          </Card>
        ))}
      </div>
    </main>
  )
}
