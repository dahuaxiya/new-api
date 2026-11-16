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
import { zodResolver } from '@hookform/resolvers/zod'
import { useMutation, useQuery } from '@tanstack/react-query'
import {
  ArrowUp,
  ImageIcon,
  MessageSquare,
  Plus,
  SlidersHorizontal,
  Square,
} from 'lucide-react'
import { useState } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { Textarea } from '@/components/ui/textarea'
import {
  getRetailCatalog,
  getRetailKeyPolicy,
} from '@/features/channels/retail-pricing-api'
import { ApiKeyPricePolicy } from '@/features/keys/components/api-key-price-policy'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

import { getStudio, openStudio, studioFormSchema, type StudioForm } from './api'
import { StudioResults } from './studio-results'
import { useStudioRun } from './use-studio-run'

/** Remount all private state on account/session changes; never share transcripts between logins. */
export function ModelStudio() {
  const identity = useAuthStore(
    (s) => `${s.auth.user?.id}:${s.auth.session?.sid}`
  )
  return <StudioWorkspace key={identity} identity={identity} />
}

/** Wallet workspace with a separate consent step and server-authorized model catalog. */
function StudioWorkspace(props: { identity: string }) {
  const { t } = useTranslation()
  const isRoot = useAuthStore((s) => s.auth.user?.role === ROLE.SUPER_ADMIN)
  const [mode, setMode] = useState<'text' | 'image'>('text')
  const [policyOpen, setPolicyOpen] = useState(false)
  const [openedId, setOpenedId] = useState(0)
  const run = useStudioRun()
  const form = useForm<StudioForm>({
    resolver: zodResolver(studioFormSchema),
    defaultValues: {
      prompt: '',
      model: '',
      maxTokens: 1024,
      size: '1024x1024',
      quality: 'medium',
    },
  })
  const query = useQuery({
    queryKey: ['studio', props.identity],
    retry: false,
    queryFn: async ({ signal }) => {
      const [catalog, studio] = await Promise.all([
        getRetailCatalog(signal),
        getStudio(signal),
      ])
      const saved = studio.token_id
        ? await getRetailKeyPolicy(studio.token_id, signal)
        : null
      return { catalog, studio, saved }
    },
  })
  const setup = useMutation({
    mutationFn: openStudio,
    retry: false,
    onSuccess: (data) => {
      setOpenedId(data.token_id)
      setPolicyOpen(true)
      void query.refetch()
    },
  })
  const tokenId = openedId || query.data?.studio.token_id || 0
  const policy = query.data?.saved?.policy
  const product = query.data?.catalog.products.find(
    (item) => item.id === policy?.product_id
  )
  const models =
    product?.models.filter(
      (item) =>
        (item.kind === 'image') === (mode === 'image') && policy?.caps[item.id]
    ) ?? []
  const selected = models.find((item) => item.id === form.watch('model'))
  const available = Boolean(
    query.data?.catalog.enabled && policy?.accept_floating && selected
  )
  const catalog = query.data?.catalog
  const hasCatalogModels = catalog?.products.some((item) => item.models.length)
  // Explain the first blocking prerequisite. A saved binding alone does not
  // grant model access, and creating a budget cannot open a paused price book.
  let blockedMessage = ''
  if (catalog) {
    if (!catalog.enabled) {
      blockedMessage = t('Model requests are paused by the site administrator.')
    } else if (!catalog.products.length) {
      blockedMessage = t(
        'No model plan is assigned to your account group. Contact the site administrator.'
      )
    } else if (!hasCatalogModels) {
      blockedMessage = t(
        'No model route is currently available. Contact the site administrator.'
      )
    } else if (!policy?.accept_floating || !product) {
      blockedMessage = t(
        'Set your budget and price limits before the first request.'
      )
    } else if (!models.length) {
      blockedMessage = t(
        'No models are authorized for this mode. Check your plan and price limits.'
      )
    }
  }

  /** Saving policy triggers a fresh catalog read; a closed dialog alone never enables spending. */
  const changePolicyOpen = (open: boolean) => {
    setPolicyOpen(open)
    if (!open) void query.refetch()
  }
  /** Keep generated images and conversations separate; mode switches start a fresh local workspace. */
  const changeMode = (value: unknown) => {
    if (run.busy || (value !== 'text' && value !== 'image')) return
    setMode(value)
    run.clear()
    form.setValue('model', '')
  }
  /** Validate both the form and current catalog before sending; the server independently rechecks all limits. */
  const submit = async (input: StudioForm) => {
    if (!available || !selected || run.busy) return
    if (mode === 'text' && input.maxTokens > selected.max_output) {
      form.setError('maxTokens', {
        message: t('Output limit exceeds this model limit'),
      })
      return
    }
    await run.send(input, mode)
  }

  if (query.isPending) return <LoadingState message={t('Loading...')} />
  if (query.isError) {
    return (
      <ErrorState
        title={t('Unable to load retail pricing')}
        onRetry={() => void query.refetch()}
      />
    )
  }
  return (
    <section className='flex min-h-0 flex-1 flex-col'>
      <header className='flex flex-wrap items-center justify-between gap-4 border-b px-5 py-5 sm:px-8'>
        <div>
          <h1 className='text-xl font-semibold tracking-tight'>
            {t('Model studio')}
          </h1>
          <p className='text-muted-foreground mt-1 text-sm'>
            {t('Chat and create, in one place.')}
          </p>
        </div>
        <div className='flex items-center gap-2'>
          <Button
            variant='ghost'
            size='sm'
            disabled={run.busy || setup.isPending}
            onClick={() => {
              if (tokenId) setPolicyOpen(true)
              else setup.mutate()
            }}
          >
            <SlidersHorizontal aria-hidden='true' />
            {t('Budget and price limits')}
          </Button>
          <Button
            variant='outline'
            size='sm'
            disabled={run.busy || run.entries.length === 0}
            onClick={run.clear}
          >
            <Plus aria-hidden='true' />
            {t('New conversation')}
          </Button>
        </div>
      </header>
      <div className='flex flex-wrap items-center gap-4 border-b px-5 py-3 sm:px-8'>
        <Tabs value={mode} onValueChange={changeMode}>
          <TabsList aria-label={t('Mode')}>
            <TabsTrigger value='text' disabled={run.busy}>
              <MessageSquare aria-hidden='true' />
              {t('Chat')}
            </TabsTrigger>
            <TabsTrigger value='image' disabled={run.busy}>
              <ImageIcon aria-hidden='true' />
              {t('Image generation')}
            </TabsTrigger>
          </TabsList>
        </Tabs>
        <div className='min-w-48 flex-1 sm:max-w-xs'>
          <NativeSelect
            aria-label={t('Model')}
            disabled={run.busy || !models.length}
            {...form.register('model')}
          >
            <NativeSelectOption value=''>
              {t('Select a model')}
            </NativeSelectOption>
            {models.map((item) => (
              <NativeSelectOption key={item.id} value={item.id}>
                {item.id}
              </NativeSelectOption>
            ))}
          </NativeSelect>
        </div>
        {selected && (
          <p className='text-muted-foreground text-xs tabular-nums'>
            {t('Input')} ¥{selected.tiers[0].prices.input} ·{' '}
            {t(mode === 'image' ? 'Image output' : 'Output')} ¥
            {selected.tiers[0].prices.output} / 1M Tokens
          </p>
        )}
      </div>
      {blockedMessage && (
        <div
          role='status'
          className='bg-muted/40 space-y-2 px-5 py-3 text-sm sm:px-8'
        >
          <p>{blockedMessage}</p>
          {isRoot && (!catalog?.enabled || !hasCatalogModels) && (
            <>
              <p className='text-muted-foreground'>
                {t(
                  'In Channels → Retail pricing, configure official model prices and product routes, then enable new requests. Following a group multiplier alone does not publish models.'
                )}
              </p>
              <Button
                variant='outline'
                size='sm'
                role='link'
                render={<a href='/channels' />}
              >
                {t('Configure model pricing')}
              </Button>
            </>
          )}
        </div>
      )}
      <StudioResults entries={run.entries} busy={run.busy} mode={mode} />
      <form
        onSubmit={form.handleSubmit(submit)}
        className='mx-auto w-full max-w-3xl space-y-3 px-5 pb-5 sm:px-8'
      >
        <div className='bg-background space-y-3 rounded-2xl border p-3 shadow-sm'>
          <Label className='sr-only' htmlFor='studio-prompt'>
            {t('Prompt')}
          </Label>
          <Textarea
            id='studio-prompt'
            placeholder={t(
              mode === 'image'
                ? 'Describe the image you want'
                : 'Write a message'
            )}
            aria-invalid={Boolean(form.formState.errors.prompt)}
            {...form.register('prompt')}
            disabled={run.busy}
            className='min-h-24 border-0 shadow-none focus-visible:ring-0'
          />
          {form.formState.errors.prompt && (
            <p role='alert' className='text-destructive text-sm'>
              {t('Enter a prompt of up to 32,000 characters.')}
            </p>
          )}
          <div className='flex flex-wrap items-end justify-between gap-3'>
            {mode === 'image' ? (
              <div className='flex gap-2'>
                <NativeSelect
                  aria-label={t('Image size')}
                  disabled={run.busy}
                  {...form.register('size')}
                >
                  <NativeSelectOption value='1024x1024'>1:1</NativeSelectOption>
                  <NativeSelectOption value='1024x1536'>2:3</NativeSelectOption>
                  <NativeSelectOption value='1536x1024'>3:2</NativeSelectOption>
                </NativeSelect>
                <NativeSelect
                  aria-label={t('Quality')}
                  disabled={run.busy}
                  {...form.register('quality')}
                >
                  <NativeSelectOption value='low'>
                    {t('Low')}
                  </NativeSelectOption>
                  <NativeSelectOption value='medium'>
                    {t('Medium')}
                  </NativeSelectOption>
                  <NativeSelectOption value='high'>
                    {t('High')}
                  </NativeSelectOption>
                </NativeSelect>
              </div>
            ) : (
              <div className='flex items-center gap-2'>
                <Label
                  htmlFor='studio-output'
                  className='text-muted-foreground text-xs'
                >
                  {t('Max output tokens')}
                </Label>
                <Input
                  id='studio-output'
                  className='w-24'
                  type='number'
                  min={1}
                  max={selected?.max_output ?? 32768}
                  disabled={run.busy}
                  aria-invalid={Boolean(form.formState.errors.maxTokens)}
                  {...form.register('maxTokens', { valueAsNumber: true })}
                />
              </div>
            )}
            {run.busy ? (
              <Button type='button' onClick={run.stop}>
                <Square aria-hidden='true' />
                {t('Stop')}
              </Button>
            ) : (
              <Button type='submit' disabled={!available}>
                <ArrowUp aria-hidden='true' />
                {t(mode === 'image' ? 'Generate image' : 'Send')}
              </Button>
            )}
          </div>
          {form.formState.errors.maxTokens && (
            <p role='alert' className='text-destructive text-sm'>
              {t('Output limit exceeds this model limit')}
            </p>
          )}
        </div>
        <p className='text-muted-foreground text-center text-xs'>
          {t(
            'Content stays on this page only. Refreshing clears it; stopping may still incur charges.'
          )}
        </p>
      </form>
      {tokenId > 0 && (
        <ApiKeyPricePolicy
          open={policyOpen}
          tokenId={tokenId}
          title={t('Budget and price limits')}
          onOpenChange={changePolicyOpen}
        />
      )}
    </section>
  )
}
