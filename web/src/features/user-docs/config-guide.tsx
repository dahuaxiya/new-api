import { useTranslation } from 'react-i18next'

import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'

import { codexConfig } from './config'
import { Example } from './example'

/**
 * 按空文件与已有配置分流，让用户只阅读当前需要执行的一组步骤。
 * @param props 经过校验的本站 API 地址；示例始终只包含密钥占位符。
 * @return 首次完整配置或已有文件的两段局部修改教程。
 */
export function ConfigGuide(props: { baseUrl: string }) {
  const { t } = useTranslation()
  const config = codexConfig(props.baseUrl)
  // 从同一个模板按固定表头拆出复制片段，避免维护两套示例导致字段或接入地址漂移。
  const providerStart = config.indexOf('[model_providers.my_api]')
  const defaults = config.slice(0, providerStart).trimEnd()
  const provider = config.slice(providerStart)

  return (
    <div className='min-w-0 space-y-5'>
      <div className='space-y-2'>
        <p>{t('docs.config.prepare')}</p>
        <a href='/keys'>{t('Go to API Keys')}</a>
      </div>
      <Tabs defaultValue='new' className='min-w-0 gap-4'>
        <TabsList
          aria-label={t('docs.config.mode')}
          className='w-full sm:w-fit'
        >
          <TabsTrigger value='new' className='px-4'>
            {t('docs.config.newTab')}
          </TabsTrigger>
          <TabsTrigger value='existing' className='px-4'>
            {t('docs.config.existingTab')}
          </TabsTrigger>
        </TabsList>
        <TabsContent value='new' className='min-w-0 space-y-4'>
          <p>{t('docs.config.newFile')}</p>
          <Example title='config.toml' value={config} />
        </TabsContent>
        <TabsContent value='existing' className='min-w-0 space-y-6'>
          <p>{t('docs.file.backup')}</p>
          <section className='min-w-0 space-y-3'>
            <h4 className='text-foreground font-medium'>
              {t('docs.config.defaultsTitle')}
            </h4>
            <p>{t('docs.config.defaultsHint')}</p>
            <Example title={t('docs.config.defaultsLabel')} value={defaults} />
          </section>
          <section className='min-w-0 space-y-3'>
            <h4 className='text-foreground font-medium'>
              {t('docs.config.providerTitle')}
            </h4>
            <p>{t('docs.config.providerHint')}</p>
            <Example title={t('docs.config.providerLabel')} value={provider} />
          </section>
          <p className='border-l-2 pl-3'>{t('docs.config.keepOthers')}</p>
        </TabsContent>
      </Tabs>
      <div className='border-t pt-4 text-xs leading-5'>
        <p>{t('docs.config.privateKey')}</p>
      </div>
    </div>
  )
}
