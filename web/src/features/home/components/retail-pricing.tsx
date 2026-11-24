import { Link } from '@tanstack/react-router'
import { ArrowUpRight } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { loadModelDetailsContent } from '@/features/pricing/components/load-model-details'
import { ModelCard } from '@/features/pricing/components/model-card'
import { ModelDetailsDrawer } from '@/features/pricing/components/model-details-drawer'
import { usePricingData } from '@/features/pricing/hooks'

/**
 * 展示后台公开目录，复用模型广场的价格卡片和详情，避免分段计费被压成固定单价。
 * 首页不自行估价，也不把目录可见性等同于上游余额或健康状态。
 * @return 包含加载、失败重试和空目录状态的价格预览。
 */
export function RetailPricing() {
  const { t } = useTranslation()
  const pricing = usePricingData()
  const [selectedName, setSelectedName] = useState<string | null>(null)
  const listed = pricing.models.slice(0, 3)
  const selected = pricing.models.find(
    (model) => model.model_name === selectedName
  )
  return (
    <section className='py-16 sm:py-24' aria-labelledby='model-preview-title'>
      <div className='mb-8 flex flex-wrap items-end justify-between gap-5'>
        <div>
          <h2
            id='model-preview-title'
            className='font-serif text-3xl tracking-tight text-balance sm:text-4xl'
          >
            {t('Models and pricing')}
          </h2>
        </div>
        <div className='flex flex-wrap gap-x-5 gap-y-2'>
          <Link to='/model-availability' className='luobo-text-link'>
            {t('Model availability')}
            <ArrowUpRight className='size-4' aria-hidden='true' />
          </Link>
          <Link to='/pricing' className='luobo-text-link'>
            {t('Full price list')}
            <ArrowUpRight className='size-4' aria-hidden='true' />
          </Link>
        </div>
      </div>
      {/* 错误优先于缓存数据展示，避免失效的价格快照被误读成刚刚加载的报价。 */}
      {pricing.error ? (
        <div
          role='alert'
          className='flex flex-wrap items-center justify-between gap-4 rounded-xl border p-6'
        >
          <p className='text-muted-foreground text-sm'>
            {t('Prices could not be loaded.')}
          </p>
          <Button variant='outline' onClick={() => void pricing.refetch()}>
            {t('Retry')}
          </Button>
        </div>
      ) : (
        <div className='grid gap-4 md:grid-cols-3'>
          {pricing.isLoading &&
            [0, 1, 2].map((slot) => (
              <div
                key={slot}
                className='luobo-catalog-skeleton rounded-2xl border p-6'
                aria-hidden='true'
              >
                <Skeleton className='h-10 w-3/4' />
                <div className='mt-5 grid grid-cols-3 gap-4'>
                  {[0, 1, 2].map((column) => (
                    <Skeleton key={column} className='h-12' />
                  ))}
                </div>
                <Skeleton className='mt-4 ml-auto h-11 w-20' />
              </div>
            ))}
          {!pricing.isLoading && listed.length === 0 && (
            <p className='text-muted-foreground col-span-full rounded-2xl border p-8 text-sm'>
              {t(
                'Browse the full catalog for available models and pricing details.'
              )}
            </p>
          )}
          {listed.map((model) => (
            <div key={model.model_name} className='luobo-catalog-card min-w-0'>
              <ModelCard
                model={model}
                compact
                onClick={setSelectedName}
                onDetailsIntent={() => {
                  void loadModelDetailsContent().catch(() => undefined)
                }}
                priceRate={pricing.priceRate}
                usdExchangeRate={pricing.usdExchangeRate}
                tokenUnit='M'
              />
            </div>
          ))}
        </div>
      )}
      <p className='text-muted-foreground mt-5 text-xs leading-5'>
        {t('Open details for pricing tiers and supported endpoints.')}
      </p>
      {selected && (
        <ModelDetailsDrawer
          open
          onOpenChange={(open) => {
            if (!open) setSelectedName(null)
          }}
          model={selected}
          groupRatio={pricing.groupRatio}
          usableGroup={pricing.usableGroup}
          endpointMap={
            pricing.endpointMap as Record<
              string,
              { path?: string; method?: string }
            >
          }
          autoGroups={pricing.autoGroups}
          priceRate={pricing.priceRate}
          usdExchangeRate={pricing.usdExchangeRate}
          tokenUnit='M'
          showRechargePrice={false}
        />
      )}
    </section>
  )
}
