import {
  BookOpen,
  Check,
  ChevronRight,
  Code2,
  FileKey,
  Gauge,
  HelpCircle,
  Terminal,
} from 'lucide-react'
import { useEffect, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import { CodeBlockFrame } from '@/components/ai-elements/code-block'
import { CopyButton } from '@/components/copy-button'
import { PublicLayout } from '@/components/layout'
import { Button } from '@/components/ui/button'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { useStatus } from '@/hooks/use-status'
import { cn } from '@/lib/utils'

import { codexConfig, docsApiBase } from './config'

const defaultTopic = {
  id: 'codex-setup',
  label: 'Codex setup',
  icon: Terminal,
  group: 'Client setup',
} as const

const topics = [
  {
    id: 'gpt-hosting',
    label: 'My GPT account',
    icon: Gauge,
    group: 'Get connected',
  },
  {
    id: 'quick-start',
    label: 'Before you start',
    icon: BookOpen,
    group: 'Get connected',
  },
  {
    id: 'api-key',
    label: 'Get your API key',
    icon: FileKey,
    group: 'Get connected',
  },
  defaultTopic,
  {
    id: 'api-setup',
    label: 'Other API clients',
    icon: Code2,
    group: 'Client setup',
  },
  {
    id: 'quota',
    label: 'Quota and usage',
    icon: Gauge,
    group: 'Help and reference',
  },
  {
    id: 'troubleshooting',
    label: 'Troubleshooting',
    icon: HelpCircle,
    group: 'Help and reference',
  },
] as const

/** Read only known document anchors so shared URLs and back navigation agree. */
function currentTopic(): string {
  const id = window.location.hash.slice(1)
  return topics.some((topic) => topic.id === id) ? id : 'codex-setup'
}

/** Compose the existing code frame and copy control for non-editable examples. */
function Example(props: { title: string; value: string }) {
  const { t } = useTranslation()
  return (
    <CodeBlockFrame
      showToolbar
      title={props.title}
      endActions={
        <CopyButton
          value={props.value}
          size='sm'
          aria-label={t('Copy {{file}}', { file: props.title })}
        >
          {t('Copy')}
        </CopyButton>
      }
    >
      <pre className='overflow-x-auto p-4 text-xs leading-6 sm:p-5 sm:text-sm'>
        <code>{props.value}</code>
      </pre>
    </CodeBlockFrame>
  )
}

/** Keep numbered instructions readable on both desktop and narrow screens. */
function Step(props: { number: number; title: string; children: ReactNode }) {
  return (
    <section className='border-border/70 rounded-xl border p-5 sm:p-7'>
      <h3 className='mb-4 flex items-center gap-3 text-lg font-semibold'>
        <span className='bg-primary/10 text-primary flex size-8 shrink-0 items-center justify-center rounded-full text-sm'>
          {props.number}
        </span>
        {props.title}
      </h3>
      <div className='text-muted-foreground [&_a]:text-primary space-y-4 text-sm leading-7 [&_a]:underline [&_code]:break-words'>
        {props.children}
      </div>
    </section>
  )
}

/** Publish a credential-free guide; authentication is required only after following console links.
 * Keep first-time and existing-file instructions together so users preserve unrelated settings.
 */
export function UserDocs() {
  const { t } = useTranslation()
  const { status } = useStatus()
  const [active, setActive] = useState(currentTopic)
  const baseUrl = docsApiBase(status?.server_address, window.location.origin)
  const topic = topics.find((item) => item.id === active) ?? defaultTopic
  const config = codexConfig(baseUrl)

  useEffect(() => {
    // Hash links remain shareable and browser Back restores the previous chapter.
    const update = () => setActive(currentTopic())
    window.addEventListener('hashchange', update)
    return () => window.removeEventListener('hashchange', update)
  }, [])

  return (
    <PublicLayout showMainContainer={false}>
      <main className='bg-muted/20 min-h-screen pt-20'>
        <div className='mx-auto grid max-w-7xl gap-6 px-4 pb-12 lg:grid-cols-[230px_minmax(0,1fr)] lg:gap-10 lg:px-6'>
          <aside className='lg:sticky lg:top-20 lg:self-start'>
            <div className='mb-5 px-3 pt-4'>
              <p className='text-primary mb-2 text-xs font-semibold tracking-widest'>
                NEW API / GUIDE
              </p>
              <h1 className='text-xl font-semibold'>{t('User guide')}</h1>
            </div>
            <nav
              aria-label={t('Documentation chapters')}
              className='bg-card grid gap-4 rounded-xl border p-3 sm:grid-cols-3 lg:grid-cols-1'
            >
              {['Get connected', 'Client setup', 'Help and reference'].map(
                (group) => (
                  <div key={group}>
                    <p className='text-muted-foreground px-3 py-2 text-xs font-medium'>
                      {t(group)}
                    </p>
                    {topics
                      .filter((item) => item.group === group)
                      .map((item) => (
                        <a
                          key={item.id}
                          href={`#${item.id}`}
                          aria-current={active === item.id ? 'page' : undefined}
                          className={cn(
                            'my-1 flex items-center gap-2 rounded-lg px-3 py-2.5 text-sm transition-colors focus-visible:outline-2 focus-visible:outline-offset-2',
                            active === item.id
                              ? 'bg-primary/10 text-primary font-medium'
                              : 'text-muted-foreground hover:bg-muted hover:text-foreground'
                          )}
                        >
                          <item.icon
                            className='size-4 shrink-0'
                            aria-hidden='true'
                          />
                          {t(item.label)}
                        </a>
                      ))}
                  </div>
                )
              )}
            </nav>
            <p className='text-muted-foreground hidden px-3 pt-5 text-xs leading-6 lg:block'>
              {t('One account, one personal key. Start with a single request.')}
            </p>
          </aside>

          <article className='min-w-0 pb-8' aria-labelledby='doc-title'>
            <header className='py-7 sm:py-9'>
              <p className='text-muted-foreground mb-3 flex items-center gap-2 text-xs'>
                {t('User guide')}
                <ChevronRight className='size-3' />
                {t(topic.group)}
              </p>
              <h2
                id='doc-title'
                className='text-3xl font-semibold tracking-tight sm:text-4xl'
              >
                {t(topic.label)}
              </h2>
              <p className='text-muted-foreground mt-3 text-sm leading-7'>
                {t('Connect your tools to this site, step by step.')}
              </p>
            </header>
            <div className='bg-card space-y-6 rounded-2xl border p-5 shadow-xs sm:p-8'>
              {active === 'quick-start' && (
                <>
                  <h3 className='text-xl font-semibold'>
                    {t('Three things to prepare')}
                  </h3>
                  <ol className='text-muted-foreground list-decimal space-y-4 pl-5 text-sm leading-7'>
                    <li>
                      {t(
                        'Sign in with the account provided by your administrator.'
                      )}
                    </li>
                    <li>
                      {t(
                        'Create or copy your personal key on the API Keys page.'
                      )}
                    </li>
                    <li>
                      {t(
                        'Use the API base below and a model enabled for your account.'
                      )}
                    </li>
                  </ol>
                  <Example title='API Base URL' value={baseUrl} />
                  <p className='text-muted-foreground text-sm leading-7'>
                    {t(
                      'This address is for clients. Do not append /responses to a base URL; the client adds the endpoint itself.'
                    )}
                  </p>
                  <Button render={<a href='#codex-setup' />}>
                    {t('Set up Codex')}
                    <ChevronRight className='size-4' />
                  </Button>
                </>
              )}
              {active === 'api-key' && (
                <>
                  <Step number={1} title={t('Open API Keys')}>
                    <p>
                      {t(
                        'Sign in, open API Keys, then create a key or copy an existing personal key. Keep the group assigned to you.'
                      )}
                    </p>
                    <a href='/keys'>{t('Go to API Keys')}</a>
                  </Step>
                  <Step number={2} title={t('Use the key in your client')}>
                    <p>
                      {t(
                        'Replace YOUR_API_KEY in the examples with the full key from this site. Your website password and upstream account password are not API keys.'
                      )}
                    </p>
                    <p>
                      {t(
                        'Do not paste a real key into this documentation page. The examples never read or store your keys.'
                      )}
                    </p>
                  </Step>
                  <Step number={3} title={t('Keep your key private')}>
                    <p>
                      {t(
                        'Do not share your key or commit a configuration containing it. If exposed, revoke it on the API Keys page and create a replacement.'
                      )}
                    </p>
                  </Step>
                </>
              )}
              {active === 'gpt-hosting' && (
                <>
                  <Step number={1} title={t('One account hosting seat')}>
                    <p>
                      {t(
                        'Ask the administrator to grant one hosting seat with a start and end date. You will then be able to connect your own GPT account.'
                      )}
                    </p>
                    <p>
                      {t(
                        'Hosting does not include a GPT subscription or additional official quota.'
                      )}
                    </p>
                  </Step>
                  <Step number={2} title={t('Connect GPT account')}>
                    <a href='/account/gpt'>{t('My GPT account')}</a>
                    <p>
                      {t(
                        'Connecting or unbinding requires security verification. Manage your verification methods in Security.'
                      )}
                    </p>
                    <p>
                      {t(
                        'Sign in on the official OpenAI page. Never enter a GPT password in this form.'
                      )}
                    </p>
                    <p>
                      {t(
                        'If the browser opens a localhost address that cannot load, copy its full address and paste it below. Do not share that address.'
                      )}
                    </p>
                    <p>
                      {t(
                        'One GPT account can be bound to one user. Reauthorization must use the same account.'
                      )}
                    </p>
                  </Step>
                  <Step number={3} title={t('Get your API key')}>
                    <p>
                      {t(
                        'All your New API keys share this account, its official quota and hosting expiry.'
                      )}
                    </p>
                    <a href='/keys'>{t('Go to API Keys')}</a>
                  </Step>
                  <Step number={4} title={t('Pause access')}>
                    <p>
                      {t(
                        'Hosting expiry stops new requests. The account page shows the credential retention period and lets you unbind before automatic cleanup.'
                      )}
                    </p>
                    <p>
                      {t(
                        'This stops access and removes the hosted credentials. Your service expiry does not change. This does not revoke authorization on the official GPT website.'
                      )}
                    </p>
                  </Step>
                </>
              )}
              {active === 'codex-setup' && (
                <>
                  <div className='bg-primary/5 text-foreground rounded-lg px-4 py-3 text-sm leading-7'>
                    {t(
                      'Already installed Codex? Start at step 2. Windows instructions below only require editing a file.'
                    )}
                  </div>
                  <Tabs defaultValue='windows'>
                    <TabsList
                      aria-label={t('Operating system')}
                      className='mb-5 h-11 w-full sm:w-80'
                    >
                      <TabsTrigger value='windows'>Windows</TabsTrigger>
                      <TabsTrigger value='mac'>macOS</TabsTrigger>
                      <TabsTrigger value='linux'>Linux</TabsTrigger>
                    </TabsList>
                    {(['windows', 'mac', 'linux'] as const).map((os) => (
                      <TabsContent key={os} value={os} className='space-y-5'>
                        <Step number={1} title={t('Install or update Codex')}>
                          <p>
                            {t(
                              'Use the official Codex download or installation guide. Desktop and CLI availability depends on your operating system.'
                            )}
                          </p>
                          <a
                            href='https://developers.openai.com/codex/quickstart'
                            target='_blank'
                            rel='noreferrer'
                          >
                            {t('Open official installation guide')}
                          </a>
                        </Step>
                        <Step
                          number={2}
                          title={t('Find your configuration file')}
                        >
                          <p>
                            {t(
                              os === 'windows'
                                ? 'docs.windows.openFolder'
                                : 'Open the .codex folder in your home directory. Create it if it does not exist.'
                            )}
                          </p>
                          <Example
                            title={t('Configuration folder')}
                            value={
                              os === 'windows'
                                ? '%USERPROFILE%\\.codex'
                                : '~/.codex'
                            }
                          />
                          {os === 'windows' && (
                            <p>{t('docs.windows.createFolder')}</p>
                          )}
                          <p>{t('docs.file.openOrCreate')}</p>
                          {os === 'windows' && (
                            <ol className='list-decimal space-y-2 pl-5'>
                              <li>{t('docs.windows.saveAs')}</li>
                              <li>{t('docs.windows.extensions')}</li>
                            </ol>
                          )}
                          <p>{t('docs.file.backup')}</p>
                        </Step>
                        <Step number={3} title={t('Edit config.toml')}>
                          <p>{t('docs.config.prepare')}</p>
                          <a href='/keys'>{t('Go to API Keys')}</a>
                          <Example title='API Base URL' value={baseUrl} />
                          <p>{t('docs.config.baseUrl')}</p>
                          <h4 className='text-foreground font-semibold'>
                            {t('docs.config.newTitle')}
                          </h4>
                          <p>{t('docs.config.newFile')}</p>
                          <Example title='config.toml' value={config} />
                          <h4 className='text-foreground font-semibold'>
                            {t('docs.config.existingTitle')}
                          </h4>
                          <ol className='list-decimal space-y-3 pl-5'>
                            <li>{t('docs.config.topLevel')}</li>
                            <li>{t('docs.config.provider')}</li>
                            <li>{t('docs.config.keepOthers')}</li>
                          </ol>
                          <p className='bg-muted rounded-lg px-4 py-3'>
                            {t('docs.config.privateKey')}
                          </p>
                        </Step>
                        <Step
                          number={4}
                          title={t('Restart and send a test message')}
                        >
                          <ol className='list-decimal space-y-3 pl-5'>
                            <li>{t('docs.test.restart')}</li>
                            <li>{t('docs.test.message')}</li>
                            <li>{t('docs.test.logs')}</li>
                          </ol>
                          <p>{t('docs.test.login')}</p>
                          <p>{t('docs.test.help')}</p>
                          <a href='/usage-logs/common'>
                            {t('View usage logs')}
                          </a>
                        </Step>
                      </TabsContent>
                    ))}
                  </Tabs>
                  <p className='text-muted-foreground text-xs leading-6'>
                    {t('Configuration reference:')}{' '}
                    <a
                      className='underline'
                      href='https://learn.chatgpt.com/docs/config-file/config-reference'
                      target='_blank'
                      rel='noreferrer'
                    >
                      OpenAI Docs
                    </a>
                  </p>
                </>
              )}
              {active === 'api-setup' && (
                <>
                  <h3 className='text-xl font-semibold'>
                    {t('OpenAI-compatible connection')}
                  </h3>
                  <p className='text-muted-foreground text-sm leading-7'>
                    {t(
                      'In a client that supports custom OpenAI-compatible providers, enter the base URL below, your personal API key, and an available model. For Codex, select the Responses protocol.'
                    )}
                  </p>
                  <Example title='Base URL' value={baseUrl} />
                  <Example title='Authorization' value='Bearer YOUR_API_KEY' />
                  <Example
                    title='Responses endpoint'
                    value={`${baseUrl}/responses`}
                  />
                  <p className='text-muted-foreground text-sm leading-7'>
                    {t(
                      'A base URL and a full endpoint are different fields. Enter /v1 only once. Model availability and supported protocols depend on your assigned channel.'
                    )}
                  </p>
                  <Example
                    title='Python · OpenAI SDK'
                    value={`from openai import OpenAI\nimport os\n\nclient = OpenAI(\n    api_key=os.environ["MY_API_KEY"],\n    base_url=${JSON.stringify(baseUrl)},\n)\nresponse = client.responses.create(\n    model="gpt-6.1-sol",\n    input="Hello",\n)\nprint(response.output_text)`}
                  />
                  <p className='text-muted-foreground text-sm leading-7'>
                    {t(
                      'The Python example requires the openai package and a MY_API_KEY environment variable containing your personal key.'
                    )}
                  </p>
                </>
              )}
              {active === 'quota' && (
                <>
                  <h3 className='text-xl font-semibold'>
                    {t('Read your subscription windows')}
                  </h3>
                  <ul className='text-muted-foreground space-y-4 text-sm leading-7'>
                    {[
                      'Your overview and API Keys page show the official 5-hour and 7-day quota for your linked account.',
                      'Remaining percentage and reset time come from the upstream snapshot. Unknown means no reading was returned, not zero usage.',
                      'The server syncs about every 5 minutes. Refreshing the page does not reset quota or force an upstream refresh.',
                      'The reset time uses your local timezone. An expired time waits for a new reading; it does not prove quota has replenished.',
                      'Site balance and subscription quota are separate limits. Your key only uses the channel and account assigned to you.',
                    ].map((text) => (
                      <li key={text} className='flex gap-3'>
                        <Check className='text-primary mt-1.5 size-4 shrink-0' />
                        <span>{t(text)}</span>
                      </li>
                    ))}
                  </ul>
                  <Button render={<a href='/dashboard/overview' />}>
                    {t('Open my overview')}
                  </Button>
                </>
              )}
              {active === 'troubleshooting' && (
                <div className='space-y-6'>
                  {[
                    [
                      '401 · Unauthorized',
                      'Check that the full personal key is present, active, and sent as a Bearer token. Use the key and API base from the same site.',
                    ],
                    [
                      '429 · pool_overloaded',
                      'No eligible slot capacity was available before the queue timed out. Try one request at a time; if it persists, ask the administrator to check slot concurrency, health and quota.',
                    ],
                    [
                      '503 · server_is_overloaded',
                      'The service reports temporary overload. Wait and retry with fewer simultaneous requests. This message alone does not identify which upstream component is overloaded.',
                    ],
                    [
                      'Work browser access requires an authenticated account',
                      'This is an application account requirement. Sign in to the required OpenAI account or use a local Codex chat without that browser feature. Changing the site API key does not supply an OpenAI login.',
                    ],
                    [
                      'Duplicate key / duplicate table',
                      'Do not append a second copy of the same TOML section. Merge fields into the existing provider table and keep model_provider at the top level.',
                    ],
                    [
                      'Model unavailable',
                      'Use a model enabled for your key and channel. A model name in this guide is only an example; contact the administrator if the model list is empty.',
                    ],
                  ].map(([title, body]) => (
                    <section
                      key={title}
                      className='border-border/70 border-b pb-6 last:border-0 last:pb-0'
                    >
                      <h3 className='mb-2 font-mono text-sm font-semibold break-words'>
                        {t(title)}
                      </h3>
                      <p className='text-muted-foreground text-sm leading-7'>
                        {t(body)}
                      </p>
                    </section>
                  ))}
                </div>
              )}
            </div>
            <footer className='text-muted-foreground mt-6 flex flex-wrap items-center justify-between gap-3 text-xs leading-6'>
              <span>
                {t(
                  'Need help? Send the error and request time to your administrator, with the API key removed.'
                )}
              </span>
              <a className='underline' href='/keys'>
                {t('Go to API Keys')}
              </a>
            </footer>
          </article>
        </div>
      </main>
    </PublicLayout>
  )
}
