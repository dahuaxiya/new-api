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
  FileText,
  X,
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
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
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
import { annotatedPrompt, type ImagePin, type TextQuote } from './studio-quote'
import { StudioReference } from './studio-reference'
import { StudioResults } from './studio-results'
import {
  migrateHistory,
  readActive,
  readCurrent,
  readPendingRuns,
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
    const pending = readPendingRuns(storageKey)
    // 刷新时保持原先所看的会话；后台图片即使还在生成，也不能挤掉另一个会话或空白页。
    const initial =
      pending.find((item) =>
        saved
          ? item.product === saved.product &&
            (item.mode ?? 'text') === saved.mode &&
            (current === undefined || current.id === item.id)
          : true
      ) ??
      (saved && readActive(storageKey, saved.product, saved.mode))
    const legacy =
      saved?.mode === 'text' && current === undefined
        ? readLegacy(storageKey, saved.product)
        : []
    return { saved, current, initial, legacy, pending }
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
      pending={start.pending}
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
  pending: Conversation[]
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
  const [quotes, setQuotes] = useState<TextQuote[]>([])
  const [pins, setPins] = useState<ImagePin[]>([])
  const [imageNote, setImageNote] = useState('')
  /** 替换或删除附件时，旧图上的坐标和文字不能落到新图上。 */
  function changeReference(source?: File | string) {
    setReference(source)
    setPins([])
    setImageNote('')
  }
  /** 每条摘录使用独立 ID，以便分别编辑或移除批注。 */
  function quoteText(text: string) {
    setQuotes((previous) => [
      ...previous,
      { id: crypto.randomUUID(), text, note: '' },
    ])
  }
  const selectReference = useRef<(source: File | string) => void>(null)
  const fileInput = useRef<HTMLInputElement>(null)
  const documentInput = useRef<HTMLInputElement>(null)
  const [documents, setDocuments] = useState<
    { id: string; name: string; text: string }[]
  >([])
  const [readingDocuments, setReadingDocuments] = useState(false)
  const [documentError, setDocumentError] = useState(false)
  const documentRead = useRef(0)

  /** 只在当前草稿接收本次选中的文件。切换会话或清空草稿后，丢弃尚未完成的读取。 */
  function clearDocuments() {
    documentRead.current++
    setReadingDocuments(false)
    setDocumentError(false)
    setDocuments([])
  }

  /**
   * 按选择顺序读取一批 UTF-8 文本，整批成功后才加入草稿，避免发送不完整的附件。
   * @param files 浏览器文件选择器返回的文件；取消选择时保留当前草稿。
   */
  async function attachDocuments(files: File[]) {
    if (!files.length) return
    const ticket = ++documentRead.current
    setReadingDocuments(true)
    setDocumentError(false)
    try {
      // 提示词最多 32,000 个字符。先限制读取字节数，再对解码后的整条消息做表单校验。
      const bytes = files.reduce((size, file) => size + file.size, 0)
      if (bytes > 128_000 || documents.length + files.length > 32) {
        throw new Error('attachment-limit')
      }
      const batch = await Promise.all(
        files.map(async (file) => {
          if (
            !file.size ||
            /[.](?:pdf|docx?|xlsx?|pptx?|zip)$/i.test(file.name)
          ) {
            throw new Error('unsupported-format')
          }
          const text = new TextDecoder('utf-8', { fatal: true }).decode(
            await file.arrayBuffer()
          )
          // 有些二进制文件也能解成 UTF-8；禁止控制字符进入聊天和生图提示词。
          if (
            !text.trim() ||
            [...text].some((c) => c < ' ' && !['\t', '\n', '\r'].includes(c))
          ) {
            throw new Error('non-text')
          }
          return { id: crypto.randomUUID(), name: file.name, text }
        })
      )
      if (ticket !== documentRead.current) return
      if (
        [...documents, ...batch].reduce(
          (n, doc) => n + doc.text.length + doc.name.length + 2,
          0
        ) > 32_000
      ) {
        throw new Error('attachment-limit')
      }
      setDocuments((previous) => [...previous, ...batch])
    } catch {
      if (ticket === documentRead.current) setDocumentError(true)
    } finally {
      if (ticket === documentRead.current) setReadingDocuments(false)
    }
  }
  const [referenceBlocked, setReferenceBlocked] = useState(false)
  const [policyOpen, setPolicyOpen] = useState(false)
  const [openedId, setOpenedId] = useState(0)
  const [chosenProducts, setChosenProducts] = useState(() => {
    const products = {
      text: readSelection(storageKey, 'text')?.product,
      image: readSelection(storageKey, 'image')?.product,
    }
    if (initial) products[initial.mode ?? 'text'] = initial.product
    return products
  })
  const activeId = useRef(initial?.id)

  const form = useForm<StudioForm>({
    // Validate the exact outbound prompt, including note-only turns, without inserting citations into the editor.
    resolver: (values, context, options) =>
      zodResolver(studioFormSchema)(
        {
          ...values,
          prompt: annotatedPrompt(
            [
              values.prompt,
              ...documents.map((doc) => [doc.name, doc.text].join('\n')),
            ]
              .filter(Boolean)
              .join('\n\n'),
            quotes,
            reference ? pins : [],
            reference ? imageNote : ''
          ),
        },
        context,
        options
      ),
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
    chosenProducts[mode] ??
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
  const outputFormat = useWatch({
    control: form.control,
    name: 'outputFormat',
  })
  const model = useWatch({ control: form.control, name: 'model' })
  const selected = models.find((item) => item.id === model)
  const extendedImage = selected?.reference_image === true
  const canAttachImage = Boolean(selected && (mode === 'text' || extendedImage))
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
  const [draftEpoch, setDraftEpoch] = useState(0)
  const drafts = useRef(new Map<string, string>())
  const viewRevision = useRef(0)
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
    props.pending,
    activeId,
    (item, savedId) => {
      const visible = activeId.current === item.id
      const isCurrent =
        readCurrent(storageKey, {
          mode: item.mode ?? 'text',
          product: item.product,
        })?.id === item.id
      if (visible) activeId.current = savedId
      if (savedId === item.id && (visible || isCurrent)) {
        remember(item)
      } else if (isCurrent) {
        // The optimistic ID was never accepted. Preserve the previous snapshot for refresh and retry.
        writeCurrent(
          storageKey,
          { mode: item.mode ?? 'text', product: item.product },
          savedId ?? null
        )
      }
      const last = item.entries.at(-1)
      if (visible && last?.error && !form.getValues('prompt')) {
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
  /** 文本草稿只在本次工作台中按会话保存；切换分组时不把原提示或附件带给另一个模型。 */
  const restore = (
    item?: Conversation,
    nextMode = mode,
    nextProduct = selectedProductId
  ) => {
    const outgoing = activeId.current ?? `${mode}:${selectedProductId}`
    const incoming = item?.id ?? `${nextMode}:${nextProduct}`
    const text = form.getValues('prompt')
    if (text) drafts.current.set(outgoing, text)
    else drafts.current.delete(outgoing)
    viewRevision.current++
    // 未完成的图片校验只能回写原草稿，即使新旧会话使用相同分组和模型。
    setDraftEpoch((epoch) => epoch + 1)
    setQuotes([])
    clearDocuments()
    changeReference(undefined)
    setReferenceBlocked(false)
    run.reset(item)
    form.setValue('prompt', drafts.current.get(incoming) ?? '')
    // 校验结果属于刚离开的草稿，不能把空输入提示带到另一会话或模式。
    form.clearErrors('prompt')
  }
  /** Group changes clear local context; every request sends its own immutable selection. */
  const changeProduct = (id: string) => {
    if (id === selectedProductId) return
    setChosenProducts((previous) => ({ ...previous, [mode]: id }))
    // Conversation context is local to the chosen group; do not forward old prompts to a different supplier.
    const item =
      run.lookup(readCurrent(storageKey, { product: id, mode })?.id) ??
      readActive(storageKey, id, mode)
    restore(item, mode, id)
    form.setValue('model', item?.entries.at(-1)?.model ?? '')
  }
  /** 切换到该模式自己的分组和模型，首次进入时使用默认分组。 */
  const changeMode = (value: unknown) => {
    if (value === mode || (value !== 'text' && value !== 'image')) return
    const saved = readSelection(storageKey, value)
    const productId =
      chosenProducts[value] ??
      policy?.product_id ??
      query.data?.catalog.products[0]?.id ??
      ''
    // 固定两边当前的默认选择，后续修改任一模式的支出保护也不会连带切换另一边。
    setChosenProducts((previous) => ({
      ...previous,
      [mode]: selectedProductId,
      [value]: productId,
    }))
    setMode(value)
    const item =
      run.lookup(
        readCurrent(storageKey, { product: productId, mode: value })?.id
      ) ?? readActive(storageKey, productId, value)
    restore(item, value, productId)
    form.setValue(
      'model',
      item?.entries.at(-1)?.model ??
        (saved?.product === productId ? saved.model : '')
    )
  }
  /** Validate both the form and current catalog before sending; the server independently rechecks all limits. */
  const submit = async (input: StudioForm) => {
    if (
      !available ||
      !selected ||
      run.busy ||
      referenceBlocked ||
      readingDocuments ||
      documentError
    ) {
      return
    }
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
    const parent = activeId.current
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
        mode === 'image' &&
        typeof sentReference === 'string' &&
        sentReference.startsWith('data:')
          ? sentReference
          : undefined,
    }
    const record = {
      id: crypto.randomUUID(),
      product: selectedProductId,
      mode,
      updatedAt: Date.now(),
      running: true,
      entries: [...run.entries, entry],
    }
    if (!run.send(record, input, parent, sentReference)) return
    drafts.current.delete(parent ?? `${mode}:${selectedProductId}`)
    viewRevision.current++
    form.setValue('prompt', '')
    setQuotes([])
    clearDocuments()
    changeReference(undefined)
    if (mobile) setOptionsOpen(false)
    // 标签页在服务器接收后被回收时，仍可用同一个 ID 恢复生成。
    writeCurrent(storageKey, record, record.id)
  }

  /** 表单校验是异步的；期间若切换会话或另一份提交已被接收，丢弃旧事件。 */
  function sendDraft() {
    const revision = viewRevision.current
    if (run.busy) return
    void form.handleSubmit((values) => {
      if (viewRevision.current === revision) return submit(values)
    })()
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
      className='text-muted-foreground size-8'
      title={t('Advanced Settings')}
      aria-label={t('Advanced Settings')}
      aria-expanded={
        optionsOpen || (!mobile && !!form.formState.errors.maxTokens)
      }
      aria-controls='studio-generation-options'
      onClick={mobile ? undefined : () => setOptionsOpen(!optionsOpen)}
    >
      <SlidersHorizontal aria-hidden='true' />
    </Button>
  )
  // 手机端将参数放到浮层中，展开时也能给对话结果保留原有高度。
  const generationOptions =
    mode === 'image' ? (
      <div className='flex flex-wrap items-center gap-2'>
        <NativeSelect aria-label={t('Image size')} {...form.register('size')}>
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
            {...form.register('customSize')}
          />
        )}

        <NativeSelect aria-label={t('Quality')} {...form.register('quality')}>
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
            pending={run.pending}
            onDelete={(item) => {
              const scope = {
                mode: item.mode ?? 'text',
                product: item.product,
              }
              if (
                readCurrent(storageKey, scope)?.id === item.id ||
                activeId.current === item.id
              ) {
                writeCurrent(storageKey, scope, null)
              }
              if (activeId.current === item.id) restore()
            }}
            onSelect={(snapshot) => {
              const item = run.lookup(snapshot.id) ?? snapshot
              if (activeId.current === item.id) return
              setMode(item.mode ?? 'text')
              setChosenProducts((previous) => ({
                ...previous,
                [item.mode ?? 'text']: item.product,
              }))
              restore(item, item.mode ?? 'text', item.product)
              form.setValue('model', item.entries.at(-1)?.model ?? '')
              remember(item)
            }}
          />
          <Button
            variant='ghost'
            size='sm'
            title={t('Spending protection')}
            className='max-md:size-11 max-md:rounded-xl'
            disabled={setup.isPending || !product}
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
            disabled={run.entries.length === 0}
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
      <div className='bg-background shrink-0 border-b px-3 pt-1 pb-2 md:px-6 md:pb-3'>
        <Tabs value={mode} onValueChange={changeMode}>
          <TabsList className='h-9 max-md:w-full' aria-label={t('Mode')}>
            <TabsTrigger className='max-md:min-h-9' value='text'>
              <MessageSquare aria-hidden='true' />
              {t('Chat')}
            </TabsTrigger>
            <TabsTrigger className='max-md:min-h-9' value='image'>
              <ImageIcon aria-hidden='true' />
              {t('Image generation')}
            </TabsTrigger>
          </TabsList>
        </Tabs>
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
        onQuoteText={quoteText}
        onQuoteImage={
          canAttachImage
            ? (source) => selectReference.current?.(source)
            : undefined
        }
      />
      <form
        onSubmit={(event) => {
          event.preventDefault()
          sendDraft()
        }}
        className='mx-auto w-full max-w-[52rem] shrink-0 px-2.5 pt-1 pb-2 md:px-6 md:pb-4'
      >
        <div className='bg-background focus-within:border-ring/55 flex flex-col gap-1 rounded-2xl border p-1.5 shadow-sm transition-colors md:gap-1.5 md:p-2'>
          <Label className='sr-only' htmlFor='studio-prompt'>
            {t('Prompt')}
          </Label>
          <input
            ref={documentInput}
            className='hidden'
            type='file'
            multiple
            accept='text/*,.txt,.md,.csv,.tsv,.json,.jsonl,.yaml,.yml,.toml,.xml,.log,.js,.jsx,.ts,.tsx,.py,.go,.java,.rs,.sql,.sh,.html,.css,.ini,.conf,.diff,.patch'
            aria-label={t('Upload file')}
            disabled={readingDocuments}
            onChange={(event) => {
              const files = [...(event.currentTarget.files ?? [])]
              event.currentTarget.value = ''
              void attachDocuments(files)
            }}
          />
          {documents.length > 0 && (
            <div className='flex max-h-28 flex-wrap gap-1 overflow-auto'>
              {documents.map((doc) => (
                <div
                  key={doc.id}
                  className='bg-muted flex max-w-full items-center gap-1 rounded-lg ps-2 text-sm'
                >
                  <FileText className='size-4 shrink-0' aria-hidden='true' />
                  <span className='truncate'>{doc.name}</span>
                  <Button
                    type='button'
                    variant='ghost'
                    size='icon-sm'
                    aria-label={[t('Remove'), doc.name].join(' ')}
                    onClick={() =>
                      setDocuments((current) =>
                        current.filter((item) => item.id !== doc.id)
                      )
                    }
                  >
                    <X aria-hidden='true' />
                  </Button>
                </div>
              ))}
            </div>
          )}
          {readingDocuments && (
            <p role='status' className='text-muted-foreground text-xs'>
              {t('Loading...')}
            </p>
          )}
          {documentError && (
            <div
              role='alert'
              className='text-destructive flex items-center gap-1 text-xs'
            >
              {t(
                'Choose UTF-8 text files, up to 32 files and 32,000 characters in total.'
              )}
              <Button
                type='button'
                size='icon-sm'
                variant='ghost'
                aria-label={t('Close')}
                onClick={() => setDocumentError(false)}
              >
                <X aria-hidden='true' />
              </Button>
            </div>
          )}
          {canAttachImage && (
            <StudioReference
              ref={selectReference}
              fileInput={fileInput}
              key={`${draftEpoch}:${selectedProductId}:${model}`}
              value={reference}
              onChange={changeReference}
              pins={pins}
              onPins={setPins}
              note={imageNote}
              onNote={setImageNote}
              disabled={false}
              onBlocked={setReferenceBlocked}
            />
          )}
          {quotes.length > 0 && (
            <div className='max-h-36 space-y-1 overflow-auto'>
              {quotes.map((q) => (
                <div key={q.id} className='bg-muted/50 rounded-lg p-2'>
                  <blockquote className='line-clamp-3 border-s-2 ps-2 text-sm whitespace-pre-wrap'>
                    {q.text}
                  </blockquote>
                  <div className='flex gap-1'>
                    <Input
                      aria-label={t('Annotation')}
                      placeholder={t('Annotation')}
                      value={q.note}
                      onChange={(event) =>
                        setQuotes(
                          quotes.map((item) =>
                            item.id === q.id
                              ? { ...item, note: event.target.value }
                              : item
                          )
                        )
                      }
                    />
                    <Button
                      type='button'
                      variant='ghost'
                      onClick={() =>
                        setQuotes(quotes.filter((item) => item.id !== q.id))
                      }
                    >
                      {t('Remove')}
                    </Button>
                  </div>
                </div>
              ))}
            </div>
          )}
          <Textarea
            id='studio-prompt'
            onPaste={(event) => {
              if (!selectReference.current) return
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
                run.busy ||
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
              if (!event.repeat) sendDraft()
            }}
            placeholder={t(
              mode === 'image'
                ? 'Describe the image you want'
                : 'Write a message'
            )}
            aria-invalid={Boolean(form.formState.errors.prompt)}
            {...form.register('prompt')}
            className='max-h-[18dvh] min-h-11 resize-none border-0 bg-transparent px-2.5 py-2.5 shadow-none focus-visible:ring-0 md:max-h-[32dvh] md:min-h-14 dark:bg-transparent'
          />
          {form.formState.errors.prompt && (
            <p role='alert' className='text-destructive text-sm'>
              {t('Enter a prompt of up to 32,000 characters.')}
            </p>
          )}
          <div className='flex min-w-0 flex-wrap items-center gap-0.5 sm:gap-1'>
            <div className='me-auto flex min-w-0 flex-1 items-center gap-0.5 sm:gap-1'>
              <DropdownMenu>
                <DropdownMenuTrigger
                  render={
                    <Button
                      type='button'
                      variant='ghost'
                      size='icon'
                      className='text-muted-foreground rounded-full'
                      aria-label={t('Add')}
                      title={t('Add')}
                    />
                  }
                >
                  <Plus aria-hidden='true' />
                </DropdownMenuTrigger>
                <DropdownMenuContent side='top' align='start' className='w-max'>
                  <DropdownMenuItem
                    disabled={!canAttachImage}
                    onClick={() => fileInput.current?.click()}
                  >
                    <ImageIcon aria-hidden='true' />
                    {t('Upload image')}
                  </DropdownMenuItem>
                  <DropdownMenuItem
                    disabled={readingDocuments}
                    onClick={() => documentInput.current?.click()}
                  >
                    <FileText aria-hidden='true' />
                    {t('Upload file')}
                  </DropdownMenuItem>
                </DropdownMenuContent>
              </DropdownMenu>
              <div className='max-w-36 min-w-0 shrink'>
                <NativeSelect
                  className='text-muted-foreground hover:bg-muted w-full rounded-lg [&_select]:h-9 [&_select]:cursor-pointer [&_select]:truncate [&_select]:border-0 [&_select]:ps-1.5 [&_select]:pe-6 [&_select]:text-xs md:[&_select]:text-sm [&_svg]:end-1 [&_svg]:size-3.5'
                  aria-label={t('Call group')}
                  value={selectedProductId}
                  disabled={!catalog?.products.length}
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
              <div className='max-w-64 min-w-0 shrink'>
                <NativeSelect
                  className='text-muted-foreground hover:bg-muted w-full rounded-lg [&_select]:h-9 [&_select]:cursor-pointer [&_select]:truncate [&_select]:border-0 [&_select]:ps-1.5 [&_select]:pe-6 [&_select]:text-xs md:[&_select]:text-sm [&_svg]:end-1 [&_svg]:size-3.5'
                  aria-label={t('Model')}
                  disabled={!models.length}
                  {...form.register('model', {
                    onChange: () => {
                      changeReference(undefined)
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
                      type='button'
                      className='text-muted-foreground size-8'
                      title={t('Pricing')}
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
                              (
                                Number(selected.tiers[0].prices.input) * 1.6
                              ).toFixed(9)
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
                className='size-9 rounded-full'
                title={t('Stopping may still incur charges.')}
                onClick={run.stop}
              >
                <Square aria-hidden='true' />
                <span className='sr-only'>{t('Stop')}</span>
              </Button>
            ) : (
              <Button
                type='submit'
                className='size-9 rounded-full'
                disabled={
                  !available ||
                  referenceBlocked ||
                  readingDocuments ||
                  documentError
                }
              >
                <ArrowUp aria-hidden='true' />
                <span className='sr-only'>
                  {t(mode === 'image' ? 'Generate image' : 'Send')}
                </span>
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
