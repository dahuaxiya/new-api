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
  CircleDollarSign,
  LoaderCircle,
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
import {
  Popover,
  PopoverContent,
  PopoverTitle,
  PopoverTrigger,
} from '@/components/ui/popover'
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

import {
  getStudio,
  openStudio,
  studioFormSchema,
  validImageDimensions,
  type StudioForm,
} from './api'
import { StudioHistory } from './studio-history'
import { StudioReference } from './studio-reference'
import { StudioResults } from './studio-results'
import {
  migrateHistory,
  readActive,
  readRunning,
  readLegacy,
  readSelection,
  writeSession,
  type Conversation,
} from './studio-session'
import { useStudioRun } from './use-studio-run'

const defaultOutputLimit = 10_240

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
  const [resuming] = useState(() => readRunning(storageKey))
  const [saved] = useState(() => readSelection(storageKey))
  const [mode, setMode] = useState<'text' | 'image'>(
    resuming?.mode ?? saved?.mode ?? 'text'
  )
  const mobile = useIsMobile()
  const [optionsOpen, setOptionsOpen] = useState(false)
  const [reference, setReference] = useState<File | string>()
  const [referenceBlocked, setReferenceBlocked] = useState(false)
  const [policyOpen, setPolicyOpen] = useState(false)
  const [openedId, setOpenedId] = useState(0)
  const [chosenProductId, setChosenProductId] = useState<string | null>(
    resuming?.product ?? saved?.product ?? null
  )
  const [initial] = useState(
    () =>
      resuming ??
      (saved?.mode === 'text'
        ? readActive(storageKey, saved.product)
        : undefined)
  )
  const [initialTurns] = useState(
    () =>
      initial?.entries ??
      (saved?.mode === 'text' ? readLegacy(storageKey, saved.product) : [])
  )
  const activeId = useRef(initial?.id)

  const form = useForm<StudioForm>({
    resolver: zodResolver(studioFormSchema),
    defaultValues: {
      prompt: '',
      model: resuming?.entries.at(-1)?.model ?? saved?.model ?? '',
      maxTokens: defaultOutputLimit,
      size: '1024x1024',
      quality: 'medium',
      customSize: '',
      outputFormat: 'png',
      outputCompression: '',
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
  const imageSize = useWatch({ control: form.control, name: 'size' })
  const outputFormat = useWatch({ control: form.control, name: 'outputFormat' })
  const model = useWatch({ control: form.control, name: 'model' })
  const selected = models.find((item) => item.id === model)
  const extendedImage = selected?.reference_image === true
  const selectedModelId = selected?.id
  useEffect(() => {
    if (mode !== 'image' || !selectedModelId || extendedImage) return
    // Synchronize the form after its current options become unavailable.
    if (
      !['1024x1024', '1024x1536', '1536x1024'].includes(form.getValues('size'))
    ) {
      form.setValue('size', '1024x1024')
    }
    if (form.getValues('quality') === 'auto') {
      form.setValue('quality', 'medium')
    }
    form.setValue('outputFormat', 'png')
    form.clearErrors(['size', 'outputCompression'])
  }, [mode, selectedModelId, extendedImage, form])
  const [storageFailed, setStorageFailed] = useState(false)
  const maxOutput = selected?.max_output
  const preferredLimit = useRef(defaultOutputLimit)
  useEffect(() => {
    if (mode === 'text' && maxOutput) {
      form.setValue('maxTokens', Math.min(preferredLimit.current, maxOutput))
    }
  }, [mode, model, maxOutput, form])
  const run = useStudioRun(initialTurns, storageKey, initial, (item) => {
    if (
      item.mode !== 'image' &&
      writeSession(storageKey && `${storageKey}:chat:${item.product}`, item) ===
        false
    ) {
      setStorageFailed(true)
    }
    const last = item.entries.at(-1)
    if (last?.error && !form.getValues('prompt')) {
      form.setValue('prompt', last.prompt)
    }
    if (last?.reference) setReference(last.reference)
    void query.refetch()
  })
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
    run.reset(item?.entries ?? [], item)
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
    if (mode === 'image') {
      // A model change can hide advanced settings that the new model does not support.
      if (!selected.reference_image) {
        input = {
          ...input,
          size: ['1024x1024', '1024x1536', '1536x1024'].includes(input.size)
            ? input.size
            : '1024x1024',
          quality: input.quality === 'auto' ? 'medium' : input.quality,
          outputFormat: 'png',
        }
      }
      const size =
        input.size === 'custom'
          ? input.customSize.trim().toLowerCase()
          : input.size
      if (!validImageDimensions(size)) {
        form.setError('size', {
          message: t('Enter a supported resolution, for example 1920x1088.'),
        })
        setOptionsOpen(true)
        return
      }
      const compression = Number(input.outputCompression)
      if (
        input.outputFormat !== 'png' &&
        input.outputCompression !== '' &&
        (!Number.isInteger(compression) || compression < 0 || compression > 100)
      ) {
        form.setError('outputCompression', {
          message: t('Enter an integer from 0 to 100.'),
        })
        setOptionsOpen(true)
        return
      }
      input = { ...input, size }
    }
    const parent = mode === 'text' ? activeId.current : undefined
    const entry = {
      id: (run.entries.at(-1)?.id ?? 0) + 1,
      prompt: input.prompt,
      model: input.model,
      content: '',
      reasoning: '',
      complete: false,
      reference:
        mode === 'image' &&
        selected.reference_image &&
        typeof reference === 'string' &&
        reference.startsWith('data:')
          ? reference
          : undefined,
    }
    const record = {
      id: crypto.randomUUID(),
      product: selectedProductId,
      mode,
      updatedAt: Date.now(),
      running: true,
      entries: mode === 'image' ? [entry] : [...run.entries, entry],
    }
    form.setValue('prompt', '')
    activeId.current = record.id
    run.send(
      record,
      input,
      parent,
      mode === 'image' && selected.reference_image ? reference : undefined
    )
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
    <section className='bg-muted/25 flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden'>
      <header className='bg-background flex shrink-0 items-center justify-between gap-1 px-3 pt-1.5 pb-0.5 md:px-6 md:pt-3'>
        <h1 className='truncate text-base font-semibold tracking-tight md:text-lg'>
          {t('Model studio')}
        </h1>
        <div className='flex shrink-0 items-center gap-0.5 md:gap-1.5'>
          <StudioHistory
            storageKey={storageKey}
            disabled={run.busy}
            onDelete={(item) => {
              if (readActive(storageKey, item.product)?.id === item.id) {
                writeSession(
                  storageKey && `${storageKey}:chat:${item.product}`,
                  null
                )
              }
              if (activeId.current === item.id) restore()
            }}
            onSelect={(item) => {
              setMode(item.mode ?? 'text')
              setChosenProductId(item.product)
              restore(item)
              if (item.mode === 'image') setReference(item.entries[0].reference)
              form.setValue('model', item.entries.at(-1)?.model ?? '')
              if (
                item.mode !== 'image' &&
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
            className='max-md:size-11 max-md:rounded-xl'
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
            className='max-md:size-11 max-md:rounded-xl'
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
      <div className='bg-background grid shrink-0 grid-cols-[minmax(0,2fr)_minmax(0,3fr)_auto] items-center gap-2 border-b px-3 pt-1 pb-2 md:flex md:flex-wrap md:gap-2.5 md:px-6 md:pb-3'>
        <Tabs
          className='max-md:col-span-3 md:me-1.5'
          value={mode}
          onValueChange={changeMode}
        >
          <TabsList className='h-9 max-md:w-full' aria-label={t('Mode')}>
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
            className='w-full [&_select]:h-10 [&_select]:truncate md:[&_select]:h-9'
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
            className='w-full [&_select]:h-10 [&_select]:truncate md:[&_select]:h-9'
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
        <Popover>
          <PopoverTrigger
            render={
              <Button
                variant='ghost'
                size='icon'
                className='size-10'
                aria-label={t('Pricing')}
                disabled={!selected}
              />
            }
          >
            <CircleDollarSign aria-hidden='true' />
          </PopoverTrigger>
          {selected && (
            <PopoverContent className='w-64 p-3' align='end'>
              <PopoverTitle>{selected.id}</PopoverTitle>
              <dl className='grid grid-cols-[1fr_auto] gap-x-3 gap-y-2 tabular-nums'>
                <dt>{t('Input')}</dt>
                <dd>¥{selected.tiers[0].prices.input}</dd>
                {reference && selected.reference_image && (
                  <>
                    <dt>{t('Image input')}</dt>
                    <dd>
                      ¥
                      {Number(
                        (Number(selected.tiers[0].prices.input) * 1.6).toFixed(
                          9
                        )
                      )}
                    </dd>
                  </>
                )}
                <dt>{t(mode === 'image' ? 'Image output' : 'Output')}</dt>
                <dd>¥{selected.tiers[0].prices.output}</dd>
              </dl>
              <p className='text-muted-foreground text-end text-xs'>
                / 1M Tokens
              </p>
            </PopoverContent>
          )}
        </Popover>
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
        className='mx-auto w-full max-w-[52rem] shrink-0 px-2.5 pt-1 pb-2 md:px-6 md:pb-4'
      >
        <div className='bg-background focus-within:border-ring/55 space-y-1 rounded-2xl border p-1.5 shadow-sm transition-colors md:space-y-1.5 md:p-2'>
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
            className='max-h-[23dvh] min-h-12 resize-none border-0 bg-transparent px-2.5 py-2.5 shadow-none focus-visible:ring-0 md:max-h-[32dvh] md:min-h-14 dark:bg-transparent'
          />
          {form.formState.errors.prompt && (
            <p role='alert' className='text-destructive text-sm'>
              {t('Enter a prompt of up to 32,000 characters.')}
            </p>
          )}
          <div className='flex flex-wrap items-center justify-between gap-1.5'>
            <Button
              type='button'
              variant='ghost'
              size='sm'
              className='text-muted-foreground min-h-11 px-2.5 md:min-h-9'
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
                  ? 'order-last w-full border-t px-2.5 pt-2 pb-1'
                  : 'hidden'
              }
            >
              {mode === 'image' ? (
                <div className='flex flex-wrap items-center gap-2'>
                  <NativeSelect
                    aria-label={t('Image size')}
                    disabled={run.busy}
                    {...form.register('size')}
                  >
                    {selected?.reference_image && (
                      <NativeSelectOption value='auto'>
                        {t('Auto')}
                      </NativeSelectOption>
                    )}
                    <NativeSelectOption value='1024x1024'>
                      1:1 · 1024 × 1024
                    </NativeSelectOption>
                    <NativeSelectOption value='1024x1536'>
                      2:3 · 1024 × 1536
                    </NativeSelectOption>
                    <NativeSelectOption value='1536x1024'>
                      3:2 · 1536 × 1024
                    </NativeSelectOption>
                    {selected?.reference_image && (
                      <>
                        <NativeSelectOption value='1536x1152'>
                          4:3 · 1536 × 1152
                        </NativeSelectOption>
                        <NativeSelectOption value='1152x1536'>
                          3:4 · 1152 × 1536
                        </NativeSelectOption>
                        <NativeSelectOption value='1536x864'>
                          16:9 · 1536 × 864
                        </NativeSelectOption>
                        <NativeSelectOption value='864x1536'>
                          9:16 · 864 × 1536
                        </NativeSelectOption>
                        <NativeSelectOption value='custom'>
                          {t('Custom')}
                        </NativeSelectOption>
                      </>
                    )}
                  </NativeSelect>
                  {extendedImage && imageSize === 'custom' && (
                    <Input
                      aria-label={t('Custom resolution')}
                      placeholder='1920x1088'
                      className='w-32'
                      disabled={run.busy}
                      {...form.register('customSize')}
                    />
                  )}

                  <NativeSelect
                    aria-label={t('Quality')}
                    disabled={run.busy}
                    {...form.register('quality')}
                  >
                    {selected?.reference_image && (
                      <NativeSelectOption value='auto'>
                        {t('Auto')}
                      </NativeSelectOption>
                    )}
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
                  {selected?.reference_image && (
                    <>
                      <NativeSelect
                        aria-label={t('Output format')}
                        disabled={run.busy}
                        {...form.register('outputFormat')}
                      >
                        <NativeSelectOption value='png'>PNG</NativeSelectOption>
                        <NativeSelectOption value='jpeg'>
                          JPEG
                        </NativeSelectOption>
                        <NativeSelectOption value='webp'>
                          WebP
                        </NativeSelectOption>
                      </NativeSelect>
                      {outputFormat !== 'png' && (
                        <Input
                          type='number'
                          min={0}
                          max={100}
                          step={1}
                          placeholder='0–100'
                          className='w-24'
                          disabled={run.busy}
                          aria-label={t('Output compression (%)')}
                          {...form.register('outputCompression')}
                        />
                      )}
                    </>
                  )}
                  {(form.formState.errors.size ||
                    form.formState.errors.outputCompression) && (
                    <p role='alert' className='text-destructive w-full text-sm'>
                      {form.formState.errors.size?.message ||
                        form.formState.errors.outputCompression?.message}
                    </p>
                  )}
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
                    onInput={(event) => {
                      const value = event.currentTarget.valueAsNumber
                      if (value >= 1 && value <= 32768) {
                        preferredLimit.current = value
                      }
                    }}
                  />
                </div>
              )}
            </div>
            {run.busy ? (
              <Button
                type='button'
                className='min-h-11 rounded-xl px-3.5 md:min-h-9'
                title={t('Stopping may still incur charges.')}
                onClick={run.stop}
              >
                <Square aria-hidden='true' />
                {t('Stop')}
              </Button>
            ) : (
              <Button
                type='submit'
                className='min-h-11 rounded-xl px-3.5 md:min-h-9'
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
        {run.reconnecting && (
          <p
            role='status'
            className='text-muted-foreground flex items-center gap-2 px-2.5 pt-2 text-xs'
          >
            <LoaderCircle
              className='size-3.5 motion-safe:animate-spin'
              aria-hidden='true'
            />
            {t('Connection interrupted. Reconnecting…')}
          </p>
        )}
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
