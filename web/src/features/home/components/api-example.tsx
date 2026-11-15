import { Link } from '@tanstack/react-router'
import { ArrowUpRight } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { CopyButton } from '@/components/copy-button'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'

/**
 * 提供可复制的标准客户端示例；模型和 Key 明确使用环境变量，避免示例绑定未经核验的模型。
 * @return 复用可访问 Tabs 和 CopyButton 的接入面板，不在页面执行任何 API 调用。
 */
export function ApiExample() {
  const { t } = useTranslation()
  // 使用当前公开站点的 origin，不插入可配置的字符串到可执行代码；JSON 序列化保护引号边界。
  const endpoint = `${window.location.origin}/v1`
  const examples = {
    Python: [
      'import os',
      'from openai import OpenAI',
      '',
      'client = OpenAI(',
      `    base_url=${JSON.stringify(endpoint)},`,
      '    api_key=os.environ["LUOBO_API_KEY"],',
      ')',
      '',
      'response = client.chat.completions.create(',
      '    model=os.environ["LUOBO_MODEL"],',
      '    messages=[{"role": "user", "content": "Hello!"}],',
      ')',
      'print(response.choices[0].message.content)',
    ].join('\n'),
    cURL: [
      `curl ${JSON.stringify(`${endpoint}/chat/completions`)} \\`,
      '  -H "Authorization: Bearer $LUOBO_API_KEY" \\',
      '  -H "Content-Type: application/json" \\',
      '  --data "$(jq -n --arg model "$LUOBO_MODEL" \\',
      "    '{model: $model, messages: [",
      '      {role: "user", content: "Hello!"}',
      '    ]}\')"',
    ].join('\n'),
  }
  return (
    <section
      id='integration'
      className='luobo-integration grid scroll-mt-24 gap-9 border-t py-14 lg:grid-cols-[0.8fr_1.2fr] lg:gap-16 lg:py-20'
      aria-labelledby='integration-title'
    >
      <div>
        <h2
          id='integration-title'
          className='font-serif text-3xl leading-tight tracking-tight text-balance sm:text-4xl'
        >
          {t('Integration steps')}
        </h2>
        <ol className='mt-8 space-y-6'>
          {[
            [
              t('Create your account'),
              t('Use one sign-in for API access and account hosting.'),
            ],
            [
              t('Choose a model and create a key'),
              t(
                'Copy a model name from the catalog and create an API key. Check your balance before calling.'
              ),
            ],
            [
              t('Run the example'),
              t(
                'Set LUOBO_API_KEY and LUOBO_MODEL, then use the example. Python requires openai; cURL requires jq.'
              ),
            ],
          ].map(([title, detail], index) => (
            <li key={title} className='flex gap-4'>
              <span
                className='luobo-step mt-0.5 flex size-6 shrink-0 items-center justify-center rounded-full font-mono text-[10px]'
                aria-hidden='true'
              >
                {index + 1}
              </span>
              <div>
                <h3 className='text-sm font-medium'>{title}</h3>
                <p className='text-muted-foreground mt-2 text-xs leading-6'>
                  {detail}
                </p>
              </div>
            </li>
          ))}
        </ol>
        <Link to='/docs' className='luobo-text-link mt-8'>
          {t('User guide')}
          <ArrowUpRight className='size-4' aria-hidden='true' />
        </Link>
      </div>
      <div className='luobo-code min-w-0 overflow-hidden rounded-2xl'>
        <Tabs defaultValue='Python' className='gap-0'>
          <div className='flex items-center justify-between gap-3 border-b border-white/10 px-4 py-3'>
            <TabsList aria-label={t('Code examples')} variant='line'>
              {Object.keys(examples).map((language) => (
                <TabsTrigger
                  key={language}
                  value={language}
                  className='min-h-11 px-3'
                >
                  {language}
                </TabsTrigger>
              ))}
            </TabsList>
            <span className='hidden font-mono text-[10px] text-stone-400 sm:inline'>
              POST /chat/completions
            </span>
          </div>
          {Object.entries(examples).map(([language, code]) => (
            <TabsContent
              key={language}
              value={language}
              className='relative m-0'
            >
              <CopyButton
                value={code}
                aria-label={t('Copy code')}
                className='absolute top-3 right-3 size-11 text-stone-400 hover:bg-white/10 hover:text-white'
              />
              <pre className='min-h-[330px] overflow-x-auto px-5 py-6 pr-14 font-mono text-xs leading-[1.9] sm:px-6'>
                <code>{code}</code>
              </pre>
            </TabsContent>
          ))}
        </Tabs>
        <div className='flex items-center justify-between gap-2 border-t border-white/10 px-5 py-3'>
          <span className='min-w-0 truncate font-mono text-xs text-stone-400'>
            {endpoint}
          </span>
          <CopyButton
            value={endpoint}
            size='sm'
            aria-label={t('Copy API endpoint')}
            className='size-11 text-stone-400 hover:bg-white/10 hover:text-white'
          />
        </div>
      </div>
    </section>
  )
}
