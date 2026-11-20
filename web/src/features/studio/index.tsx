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
import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
  SheetTrigger,
} from '@/components/ui/sheet'
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
import { recoverCloud } from './studio-cloud'
import { StudioHistory } from './studio-history'
import { StudioReference } from './studio-reference'
import { StudioResults } from './studio-results'
import {
  migrateHistory,
  readActive,
  readCurrent,
  readRunning,
  readLegacy,
  readSelection,
  writeActive,
  writeCurrent,
  writeSelection,
  type Conversation,
  type StudioSelection,
} from './studio-session'
import { useStudioRun } from './use-studio-run'

const defaultOutputLimit = 10_240

/** Remount all private state on account/session changes; never share transcripts between logins. */

export function ModelStudio() {
  const identity = useAuthStore(
    (s) => `${s.auth.user?.id}:${s.auth.session?.sid}`
  )
  return <StudioSession key={identity} identity={identity} />
}

/** 恢复前只读现有会话，避免用户在旧上下文尚未加载时向模型发送新消息。 */
function StudioSession(props: { identity: string }) {
  const { t } = useTranslation()
  const userId = useAuthStore((s) => s.auth.user?.id)
  const storageKey = userId ? `studio.v1:${userId}` : null
  const [start] = useState(() => {
    const saved = readSelection(storageKey)
    const current = saved && readCurrent(storageKey, saved)
    const initial =
      readRunning(storageKey) ??
      (saved && readActive(storageKey, saved.product, saved.mode))
    const legacy =
      saved?.mode === 'text' && current === undefined
        ? readLegacy(storageKey, saved.product)
        : []
    return { saved, current, initial, legacy }
  })
  const needsRecovery =
    Boolean(storageKey) &&
    !start.initial &&
    !start.legacy.length &&
    start.current?.id !== null
  const recovery = useQuery({
    queryKey: ['studio-current', props.identity, start.saved, start.current],
    queryFn: async ({ signal }) =>
      storageKey
        ? ((await recoverCloud(
            storageKey,
            start.saved,
            start.current?.id ?? undefined,
            signal
          )) ?? null)
        : null,
    enabled: needsRecovery,
    retry: false,
    gcTime: 0,
    refetchOnWindowFocus: false,
    refetchOnReconnect: false,
  })
  if (needsRecovery && recovery.isPending) {
    return <LoadingState message={t('Loading...')} />
  }
  if (needsRecovery && recovery.isError) {
    return (
      <ErrorState
        title={t('Could not load chat history.')}
        onRetry={() => void recovery.refetch()}
      />
    )
  }
  return (
    <StudioWorkspace
      identity={props.identity}
      storageKey={storageKey}
      saved={start.saved}
      initial={
        start.initial ??
        (needsRecovery ? (recovery.data ?? undefined) : undefined)
      }
      legacy={start.legacy}
    />
  )
}

/** Wallet workspace sends directly using server-enforced budgets and the selected call group. */
function StudioWorkspace(props: {
  identity: string
  storageKey: string | null
  saved?: StudioSelection
  initial?: Conversation
  legacy: Conversation['entries']
}) {
  const { t } = useTranslation()
  const isRoot = useAuthStore((s) => s.auth.user?.role === ROLE.SUPER_ADMIN)
  const { storageKey, saved, initial } = props
  const [mode, setMode] = useState<'text' | 'image'>(
    initial?.mode ?? saved?.mode ?? 'text'
  )
  const mobile = useIsMobile()
  const [optionsOpen, setOptionsOpen] = useState(false)
  const [reference, setReference] = useState<File | string>()
  const selectReference = useRef<(source: File | string) => void>(null)
  const [referenceBlocked, setReferenceBlocked] = useState(false)
  const [policyOpen, setPolicyOpen] = useState(false)
  const [openedId, setOpenedId] = useState(0)
  const [chosenProductId, setChosenProductId] = useState<string | null>(
    initial?.product ?? saved?.product ?? null
  )
  const activeId = useRef(initial?.id)

  const form = useForm<StudioForm>({
    resolver: zodResolver(studioFormSchema),
    defaultValues: {
      prompt: '',
      model:
        (!initial?.running && saved?.model) ||
        initial?.entries.at(-1)?.model ||
        '',
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
  /** 缓存当前标签的正文，跨标签重开用账号和工作区内的会话 ID。 */
  const remember = (item: Conversation) => {
    const mode = item.mode ?? 'text'
    if (
      writeCurrent(storageKey, { mode, product: item.product }, item.id) ===
      false
    ) {
      setStorageFailed(true)
    }
    writeActive(storageKey, item)
  }
  const run = useStudioRun(
    initial?.entries ?? props.legacy,
    storageKey,
    initial,
    (item) => {
      remember(item)
      const last = item.entries.at(-1)
      if (last?.error && !form.getValues('prompt')) {
        form.setValue('prompt', last.prompt)
      }
      void query.refetch()
    }
  )
  useEffect(() => {
    if (
      selectedProductId &&
      writeSelection(storageKey, {
        product: selectedProductId,
        mode,
        model,
      }) === false
    ) {
      queueMicrotask(() => setStorageFailed(true))
    }
  }, [storageKey, selectedProductId, mode, model])
  useEffect(() => {
    if (!storageKey) return
    void migrateHistory(storageKey).catch(() => setStorageFailed(true))
  }, [storageKey])
  useEffect(() => {
    if (!initial || initial.running) return
    writeActive(storageKey, initial)
    writeCurrent(
      storageKey,
      { mode: initial.mode ?? 'text', product: initial.product },
      initial.id
    )
  }, [storageKey, initial])

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
  /** 恢复原会话或打开空白编辑区，清除前一会话未发送的附件。 */
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
    // Conversation context is local to the chosen group; do not forward old prompts to a different supplier.
    const item = readActive(storageKey, id, mode)
    restore(item)
    form.setValue('model', item?.entries.at(-1)?.model ?? '')
  }
  /** 聊天与生图分别保留当前打开的记录。 */
  const changeMode = (value: unknown) => {
    if (run.busy || (value !== 'text' && value !== 'image')) return
    setMode(value)
    const item = readActive(storageKey, selectedProductId, value)
    restore(item)
    form.setValue('model', item?.entries.at(-1)?.model ?? '')
  }
  /** Validate both the form and current catalog before sending; the server independently rechecks all limits. */
  const submit = async (input: StudioForm) => {
    if (!available || !selected || run.busy || referenceBlocked) return
    if (mode === 'text' && input.maxTokens > selected.max_output) {
      setOptionsOpen(true)
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
    const sentReference =
      mode === 'text' || selected.reference_image ? reference : undefined
    const entry = {
      id: (run.entries.at(-1)?.id ?? 0) + 1,
      prompt: input.prompt,
      model: input.model,
      content: '',
      reasoning: '',
      complete: false,
      reference:
        typeof sentReference === 'string' && sentReference.startsWith('data:')
          ? sentReference
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
    setReference(undefined)
    if (mobile) setOptionsOpen(false)
    activeId.current = record.id
    // 标签页在服务器接收后被回收时，仍可用同一个 ID 恢复生成。
    writeCurrent(storageKey, record, record.id)
    run.send(record, input, parent, sentReference)
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
  const settingsButton = (
    <Button
      type='button'
      variant='ghost'
      size='sm'
      className='text-muted-foreground min-h-10 px-2.5 md:min-h-9'
      aria-expanded={
        optionsOpen || (!mobile && !!form.formState.errors.maxTokens)
      }
      aria-controls='studio-generation-options'
      onClick={mobile ? undefined : () => setOptionsOpen(!optionsOpen)}
    >
      <SlidersHorizontal aria-hidden='true' />
      {t('Advanced Settings')}
    </Button>
  )
  // 手机端将参数放到浮层中，展开时也能给对话结果保留原有高度。
  const generationOptions =
    mode === 'image' ? (
      <div className='flex flex-wrap items-center gap-2'>
        <NativeSelect
          aria-label={t('Image size')}
          disabled={run.busy}
          {...form.register('size')}
        >
          {selected?.reference_image && (
            <NativeSelectOption value='auto'>{t('Auto')}</NativeSelectOption>
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
            <NativeSelectOption value='auto'>{t('Auto')}</NativeSelectOption>
          )}
          <NativeSelectOption value='low'>{t('Low')}</NativeSelectOption>
          <NativeSelectOption value='medium'>{t('Medium')}</NativeSelectOption>
          <NativeSelectOption value='high'>{t('High')}</NativeSelectOption>
        </NativeSelect>
        {selected?.reference_image && (
          <>
            <NativeSelect
              aria-label={t('Output format')}
              disabled={run.busy}
              {...form.register('outputFormat')}
            >
              <NativeSelectOption value='png'>PNG</NativeSelectOption>
              <NativeSelectOption value='jpeg'>JPEG</NativeSelectOption>
              <NativeSelectOption value='webp'>WebP</NativeSelectOption>
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
    )
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
              const scope = { mode: item.mode ?? 'text', product: item.product }
              if (
                readCurrent(storageKey, scope)?.id === item.id ||
                activeId.current === item.id
              ) {
                writeCurrent(storageKey, scope, null)
              }
              if (activeId.current === item.id) restore()
            }}
            onSelect={(item) => {
              setMode(item.mode ?? 'text')
              setChosenProductId(item.product)
              restore(item)
              form.setValue('model', item.entries.at(-1)?.model ?? '')
              remember(item)
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
                writeCurrent(
                  storageKey,
                  { mode, product: selectedProductId },
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
      <StudioResults
        entries={run.entries}
        sentReference={run.sentReference}
        busy={run.busy}
        mode={mode}
      />
      <form
        onSubmit={(event) => void form.handleSubmit(submit)(event)}
        className='mx-auto w-full max-w-[52rem] shrink-0 px-2.5 pt-1 pb-2 md:px-6 md:pb-4'
      >
        <div className='bg-background focus-within:border-ring/55 space-y-1 rounded-2xl border p-1.5 shadow-sm transition-colors md:space-y-1.5 md:p-2'>
          <Label className='sr-only' htmlFor='studio-prompt'>
            {t('Prompt')}
          </Label>
          <div className='flex flex-wrap items-center gap-x-1'>
            <StudioHistory
              storageKey={storageKey}
              disabled={run.busy}
              quote={{
                entries: run.entries,
                canUseImages: mode === 'image' && extendedImage,
                onPick: (item) => {
                  if (item.type === 'image') {
                    selectReference.current?.(item.value)
                    return
                  }
                  // Keep the user's draft intact and show exactly what will be sent as context.
                  const quoted = item.value.trim().replaceAll(/^/gm, '> ')
                  const draft = form.getValues('prompt')
                  form.setValue('prompt', `${quoted}\n\n${draft}`, {
                    shouldValidate: true,
                  })
                },
              }}
            />
            {selected && (mode === 'text' || selected.reference_image) && (
              <StudioReference
                ref={selectReference}
                key={`${selectedProductId}:${model}`}
                value={reference}
                onChange={setReference}
                onBlocked={setReferenceBlocked}
                disabled={run.busy}
              />
            )}
          </div>
          <Textarea
            id='studio-prompt'
            onPaste={(event) => {
              if (!selectReference.current || run.busy) return
              const image = [...event.clipboardData.files].find(
                (file) =>
                  file.type.startsWith('image/') ||
                  !file.type ||
                  file.type === 'application/octet-stream'
              )
              if (!image) return
              // Copy Image can include its source URL as text. Consume only a real image file.
              event.preventDefault()
              selectReference.current(image)
            }}
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
            className='max-h-[18dvh] min-h-11 resize-none border-0 bg-transparent px-2.5 py-2.5 shadow-none focus-visible:ring-0 md:max-h-[32dvh] md:min-h-14 dark:bg-transparent'
          />
          {form.formState.errors.prompt && (
            <p role='alert' className='text-destructive text-sm'>
              {t('Enter a prompt of up to 32,000 characters.')}
            </p>
          )}
          <div className='flex flex-wrap items-center justify-between gap-1.5'>
            {mobile ? (
              <Sheet open={optionsOpen} onOpenChange={setOptionsOpen}>
                <SheetTrigger render={settingsButton} />
                <SheetContent
                  id='studio-generation-options'
                  side='bottom'
                  className='max-h-[75dvh] gap-2 overflow-y-auto rounded-t-2xl pb-[max(1rem,env(safe-area-inset-bottom))]'
                >
                  <SheetHeader>
                    <SheetTitle>{t('Advanced Settings')}</SheetTitle>
                  </SheetHeader>
                  <div className='px-4'>{generationOptions}</div>
                </SheetContent>
              </Sheet>
            ) : (
              <>
                {settingsButton}
                <div
                  id='studio-generation-options'
                  className={
                    optionsOpen || form.formState.errors.maxTokens
                      ? 'order-last w-full border-t px-2.5 pt-2 pb-1'
                      : 'hidden'
                  }
                >
                  {generationOptions}
                </div>
              </>
            )}
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
