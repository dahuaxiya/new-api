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
import { useEffect, useRef, useState } from 'react'
import { useForm, useWatch } from 'react-hook-form'
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
import { useIsMobile } from '@/hooks/use-mobile'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

import { getStudio, openStudio, studioFormSchema, type StudioForm } from './api'
import { StudioHistory } from './studio-history'
import { StudioReference } from './studio-reference'
import { StudioResults } from './studio-results'
import {
  migrateHistory,
  readActive,
  readLegacy,
  readSelection,
  saveHistory,
  writeSession,
  type Conversation,
} from './studio-session'
import { useStudioRun } from './use-studio-run'

/** Remount all private state on account/session changes; never share transcripts between logins. */
export function ModelStudio() {
  const identity = useAuthStore(
    (s) => `${s.auth.user?.id}:${s.auth.session?.sid}`
  )
  return <StudioWorkspace key={identity} identity={identity} />
}

/** Wallet workspace sends directly using server-enforced budgets and the selected call group. */
function StudioWorkspace(props: { identity: string }) {
  const { t } = useTranslation()
  const isRoot = useAuthStore((s) => s.auth.user?.role === ROLE.SUPER_ADMIN)
  const userId = useAuthStore((s) => s.auth.user?.id)
  const storageKey = userId ? `studio.v1:${userId}` : null
  const [saved] = useState(() => readSelection(storageKey))
  const [mode, setMode] = useState<'text' | 'image'>(saved?.mode ?? 'text')
  const mobile = useIsMobile()
  const [optionsOpen, setOptionsOpen] = useState(false)
  const [reference, setReference] = useState<string>()
  const [referenceBlocked, setReferenceBlocked] = useState(false)
  const [policyOpen, setPolicyOpen] = useState(false)
  const [openedId, setOpenedId] = useState(0)
  const [chosenProductId, setChosenProductId] = useState<string | null>(
    saved?.product ?? null
  )
  const [initial] = useState(() =>
    saved?.mode === 'text' ? readActive(storageKey, saved.product) : undefined
  )
  const [initialTurns] = useState(
    () =>
      initial?.entries ??
      (saved?.mode === 'text' ? readLegacy(storageKey, saved.product) : [])
  )
  const run = useStudioRun(initialTurns)
  const activeId = useRef(initial?.id)
  const pending = useRef<Conversation | null>(null)
  const form = useForm<StudioForm>({
    resolver: zodResolver(studioFormSchema),
    defaultValues: {
      prompt: '',
      model: saved?.model ?? '',
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
  const selectedProductId =
    chosenProductId ??
    policy?.product_id ??
    query.data?.catalog.products[0]?.id ??
    ''
  const product = query.data?.catalog.products.find(
    (item) => item.id === selectedProductId
  )
  const models =
    product?.models.filter(
      (item) => (item.kind === 'image') === (mode === 'image')
    ) ?? []
  const model = useWatch({ control: form.control, name: 'model' })
  const selected = models.find((item) => item.id === model)
  const [storageFailed, setStorageFailed] = useState(false)
  useEffect(() => {
    if (
      selectedProductId &&
      writeSession(storageKey, { product: selectedProductId, mode, model }) ===
        false
    ) {
      queueMicrotask(() => setStorageFailed(true))
    }
  }, [storageKey, selectedProductId, mode, model])
  useEffect(() => {
    if (!storageKey) return
    void migrateHistory(storageKey).catch(() => setStorageFailed(true))
  }, [storageKey])

  useEffect(() => {
    // 流式输出期间只存一次中断占位。收到最终状态才替换它，浏览历史不会续期。
    const started = pending.current
    if (!storageKey || !started || run.busy || !run.entries.length) return
    pending.current = null
    const settled = { ...started, entries: run.entries, updatedAt: Date.now() }
    const tabSaved = writeSession(
      `${storageKey}:chat:${started.product}`,
      settled
    )
    void saveHistory(storageKey, settled).then(
      () => setStorageFailed(tabSaved === false),
      () => setStorageFailed(true)
    )
  }, [storageKey, run.busy, run.entries])
  const available = Boolean(query.data?.catalog.enabled && selected)
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
        'No call groups are available yet. Contact the site administrator.'
      )
    } else if (!hasCatalogModels) {
      blockedMessage = t(
        'No model route is currently available. Contact the site administrator.'
      )
    } else if (!product) {
      blockedMessage = t('Select a call group and model to start.')
    } else if (!models.length) {
      blockedMessage = t(
        'No model route is currently available. Contact the site administrator.'
      )
    }
  }

  /** Saving policy triggers a fresh catalog read; a closed dialog alone never enables spending. */
  const changePolicyOpen = (open: boolean) => {
    setPolicyOpen(open)
    if (!open) void query.refetch()
  }
  /** 只恢复用户选中的文本记录。图像工作区和新对话都以空上下文开始。 */
  const restore = (item?: Conversation) => {
    setReference(undefined)
    setReferenceBlocked(false)
    activeId.current = item?.id
    run.reset(item?.entries ?? [])
    form.setValue('prompt', '')
  }
  /** Group changes clear local context; every request sends its own immutable selection. */
  const changeProduct = (id: string) => {
    if (run.busy) return
    setChosenProductId(id)
    form.setValue('model', '')
    // Conversation context is local to the chosen group; do not forward old prompts to a different supplier.
    restore(mode === 'text' ? readActive(storageKey, id) : undefined)
  }
  /** Keep generated images and conversations separate; mode switches start a fresh local workspace. */
  const changeMode = (value: unknown) => {
    if (run.busy || (value !== 'text' && value !== 'image')) return
    setMode(value)
    restore(
      value === 'text' ? readActive(storageKey, selectedProductId) : undefined
    )
    form.setValue('model', '')
  }
  /** Validate both the form and current catalog before sending; the server independently rechecks all limits. */
  const submit = async (input: StudioForm) => {
    if (!available || !selected || run.busy || referenceBlocked) return
    if (mode === 'text' && input.maxTokens > selected.max_output) {
      form.setError('maxTokens', {
        message: t('Output limit exceeds this model limit'),
      })
      return
    }
    await run.send(
      input,
      mode,
      selectedProductId,
      (turn) => {
        form.setValue('prompt', '')
        // Navigation cancels the transport. Save the submitted prompt once without replaying a paid request.
        if (storageKey && mode === 'text') {
          const interrupted = {
            ...turn,
            error: 'Stopped. Check usage before trying again.',
          }
          const parent =
            activeId.current ?? readActive(storageKey, selectedProductId)?.id
          const snapshot = {
            id: crypto.randomUUID(),
            product: selectedProductId,
            updatedAt: Date.now(),
            entries: [...run.entries, interrupted],
          }
          pending.current = snapshot
          activeId.current = snapshot.id
          const tabSaved = writeSession(
            `${storageKey}:chat:${selectedProductId}`,
            snapshot
          )
          if (!tabSaved) setStorageFailed(true)
          void saveHistory(storageKey, snapshot, parent).catch(() =>
            setStorageFailed(true)
          )
        }
      },
      () => {
        if (!form.getValues('prompt')) form.setValue('prompt', input.prompt)
      },
      mode === 'image' && selected.reference_image ? reference : undefined
    )
    void query.refetch()
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
    <section className='flex min-h-0 min-w-0 flex-1 flex-col max-md:overflow-y-auto'>
      <header className='flex shrink-0 flex-wrap items-center justify-between gap-2 border-b px-3 py-1.5 md:gap-4 md:px-8 md:py-5'>
        <h1 className='text-xl font-semibold tracking-tight'>
          {t('Model studio')}
        </h1>
        <div className='flex items-center gap-2'>
          <StudioHistory
            storageKey={storageKey}
            disabled={run.busy}
            onSelect={(item) => {
              setMode('text')
              setChosenProductId(item.product)
              restore(item)
              form.setValue('model', item.entries.at(-1)?.model ?? '')
              if (
                writeSession(
                  storageKey && `${storageKey}:chat:${item.product}`,
                  item
                ) === false
              ) {
                setStorageFailed(true)
              }
            }}
          />
          <Button
            variant='ghost'
            size='sm'
            title={t('Spending protection')}
            className='max-md:size-11'
            disabled={run.busy || setup.isPending || !product}
            onClick={() => {
              setup.mutate(selectedProductId)
            }}
          >
            <SlidersHorizontal aria-hidden='true' />
            <span className='max-md:sr-only'>{t('Spending protection')}</span>
          </Button>
          <Button
            variant='outline'
            size='sm'
            title={t('New conversation')}
            className='max-md:size-11'
            disabled={run.busy || run.entries.length === 0}
            onClick={() => {
              restore()
              if (
                mode === 'text' &&
                writeSession(
                  storageKey && `${storageKey}:chat:${selectedProductId}`,
                  null
                ) === false
              ) {
                setStorageFailed(true)
              }
            }}
          >
            <Plus aria-hidden='true' />
            <span className='max-md:sr-only'>{t('New conversation')}</span>
          </Button>
        </div>
      </header>
      <div className='grid shrink-0 grid-cols-[minmax(0,2fr)_minmax(0,3fr)] items-center gap-2 border-b px-3 py-2 md:flex md:flex-wrap md:gap-4 md:px-8 md:py-3'>
        <Tabs
          className='max-md:col-span-2'
          value={mode}
          onValueChange={changeMode}
        >
          <TabsList className='max-md:w-full' aria-label={t('Mode')}>
            <TabsTrigger
              className='max-md:min-h-9'
              value='text'
              disabled={run.busy}
            >
              <MessageSquare aria-hidden='true' />
              {t('Chat')}
            </TabsTrigger>
            <TabsTrigger
              className='max-md:min-h-9'
              value='image'
              disabled={run.busy}
            >
              <ImageIcon aria-hidden='true' />
              {t('Image generation')}
            </TabsTrigger>
          </TabsList>
        </Tabs>
        <div className='min-w-0 md:max-w-56 md:min-w-40'>
          <NativeSelect
            className='max-md:w-full max-md:[&_select]:h-10'
            aria-label={t('Call group')}
            value={selectedProductId}
            disabled={run.busy || !catalog?.products.length}
            onChange={(event) => changeProduct(event.target.value)}
          >
            <NativeSelectOption value=''>
              {t('Select a call group')}
            </NativeSelectOption>
            {catalog?.products.map((item) => (
              <NativeSelectOption key={item.id} value={item.id}>
                {item.name}
              </NativeSelectOption>
            ))}
          </NativeSelect>
        </div>
        <div className='min-w-0 md:max-w-xs md:min-w-48 md:flex-1'>
          <NativeSelect
            className='max-md:w-full max-md:[&_select]:h-10'
            aria-label={t('Model')}
            disabled={run.busy || !models.length}
            {...form.register('model', {
              onChange: () => {
                setReference(undefined)
                setReferenceBlocked(false)
              },
            })}
            value={selected?.id ?? ''}
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
          <p className='text-muted-foreground col-span-2 text-xs tabular-nums'>
            {t('Input')} ¥{selected.tiers[0].prices.input} ·{' '}
            {reference && selected.reference_image && (
              <>
                {t('Image input')} ¥
                {Number(
                  (Number(selected.tiers[0].prices.input) * 1.6).toFixed(9)
                )}{' '}
                ·{' '}
              </>
            )}
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
        onSubmit={(event) => void form.handleSubmit(submit)(event)}
        className='mx-auto w-full max-w-3xl shrink-0 space-y-1.5 px-3 py-2 md:space-y-3 md:px-8 md:pb-5'
      >
        <div className='bg-background space-y-1 rounded-2xl border p-2 shadow-sm md:space-y-3 md:p-3'>
          <Label className='sr-only' htmlFor='studio-prompt'>
            {t('Prompt')}
          </Label>
          {mode === 'image' && selected?.reference_image && (
            <StudioReference
              key={`${selectedProductId}:${model}`}
              value={reference}
              onChange={setReference}
              onBlocked={setReferenceBlocked}
              disabled={run.busy}
            />
          )}
          <Textarea
            id='studio-prompt'
            onKeyDown={(event) => {
              // IME confirmation (including Safari's 229 event) must not send paid requests.
              if (
                event.key !== 'Enter' ||
                // 手机键盘的回车保留换行，付费发送由明确的发送按钮触发。
                mobile ||
                event.shiftKey ||
                event.ctrlKey ||
                event.metaKey ||
                event.altKey ||
                event.nativeEvent.isComposing ||
                event.keyCode === 229
              ) {
                return
              }
              event.preventDefault()
              if (!event.repeat && !run.busy) void form.handleSubmit(submit)()
            }}
            placeholder={t(
              mode === 'image'
                ? 'Describe the image you want'
                : 'Write a message'
            )}
            aria-invalid={Boolean(form.formState.errors.prompt)}
            {...form.register('prompt')}
            disabled={run.busy}
            className='max-h-[18dvh] min-h-11 border-0 shadow-none focus-visible:ring-0 md:max-h-96 md:min-h-24'
          />
          {form.formState.errors.prompt && (
            <p role='alert' className='text-destructive text-sm'>
              {t('Enter a prompt of up to 32,000 characters.')}
            </p>
          )}
          <div className='flex flex-wrap items-center justify-between gap-2 md:items-end md:gap-3'>
            <Button
              type='button'
              variant='ghost'
              size='sm'
              className='min-h-10 md:hidden'
              aria-expanded={optionsOpen || !!form.formState.errors.maxTokens}
              aria-controls='studio-generation-options'
              onClick={() => setOptionsOpen(!optionsOpen)}
            >
              <SlidersHorizontal aria-hidden='true' />
              {t('Advanced Settings')}
            </Button>
            <div
              id='studio-generation-options'
              className={
                optionsOpen || form.formState.errors.maxTokens
                  ? 'order-last w-full md:order-none md:w-auto'
                  : 'hidden md:block'
              }
            >
              {mode === 'image' ? (
                <div className='flex gap-2'>
                  <NativeSelect
                    aria-label={t('Image size')}
                    disabled={run.busy}
                    {...form.register('size')}
                  >
                    <NativeSelectOption value='1024x1024'>
                      1:1
                    </NativeSelectOption>
                    <NativeSelectOption value='1024x1536'>
                      2:3
                    </NativeSelectOption>
                    <NativeSelectOption value='1536x1024'>
                      3:2
                    </NativeSelectOption>
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
            </div>
            {run.busy ? (
              <Button
                type='button'
                className='max-md:min-h-10'
                title={t('Stopping may still incur charges.')}
                onClick={run.stop}
              >
                <Square aria-hidden='true' />
                {t('Stop')}
              </Button>
            ) : (
              <Button
                type='submit'
                className='max-md:min-h-10'
                disabled={!available || referenceBlocked}
              >
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
        {storageFailed && (
          <p
            role='status'
            className='text-muted-foreground text-center text-xs'
          >
            {t('Chat history could not be saved in this tab.')}
          </p>
        )}
      </form>
      {tokenId > 0 && (
        <ApiKeyPricePolicy
          studio
          open={policyOpen}
          tokenId={tokenId}
          initialProductId={selectedProductId}
          title={t('Spending protection')}
          onOpenChange={changePolicyOpen}
          onSaved={changeProduct}
        />
      )}
    </section>
  )
}
