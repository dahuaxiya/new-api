import { useQuery } from '@tanstack/react-query'
import { Copy, Eye, EyeOff, KeyRound, X } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { StaticDataTable } from '@/components/data-table'
import { EmptyState } from '@/components/empty-state'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { StatusBadge } from '@/components/status-badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { useMediaQuery } from '@/hooks'
import { useCopyToClipboard } from '@/hooks/use-copy-to-clipboard'
import { formatNumber, formatTimestampToDate } from '@/lib/format'
import { handleServerError } from '@/lib/handle-server-error'
import { useAuthStore } from '@/stores/auth-store'

import {
  getUserTokens,
  manageUserToken,
  type AdminToken,
  type AdminTokenAction,
} from '../token-api'
import type { User } from '../types'
import { useUsers } from './users-provider'

const actionLabels = {
  view: 'View',
  copy: 'Copy',
  enable: 'Enable',
  disable: 'Disable',
}
const statusLabels: Record<number, string> = {
  1: 'Enabled',
  2: 'Disabled',
  3: 'Expired',
  4: 'Exhausted',
}

/** 行内详情只保留筛选状态；切换查询或用户时卸载凭据视图，避免旧明文留在新列表。 */
export function UserTokensPanel({
  user,
  onClose,
}: {
  user: User
  onClose: () => void
}) {
  const { t } = useTranslation()
  const [keyword, setKeyword] = useState('')
  const [status, setStatus] = useState('0')
  const [page, setPage] = useState(1)
  const identity = useAuthStore(
    (s) => `${s.auth.user?.id}:${s.auth.session?.sid}`
  )
  return (
    <section
      id={`user-keys-${user.id}`}
      aria-label={t('API keys for {{user}}', { user: user.username })}
      className='user-keys-panel'
    >
      <div className='flex flex-wrap items-center justify-between gap-3'>
        <div className='flex items-center gap-2'>
          <KeyRound className='size-4' aria-hidden='true' />
          <h3 className='text-sm font-semibold'>
            {t('API keys for {{user}}', { user: user.username })}
          </h3>
        </div>
        <Button
          size='icon-sm'
          variant='ghost'
          aria-label={t('Collapse API keys')}
          onClick={onClose}
        >
          <X aria-hidden='true' />
        </Button>
      </div>
      <div className='flex flex-wrap gap-2'>
        <Input
          className='h-8 w-full sm:w-64'
          value={keyword}
          maxLength={100}
          aria-label={t('Search API key names')}
          placeholder={t('Search API key names')}
          onChange={(event) => {
            setKeyword(event.target.value)
            setPage(1)
          }}
        />
        <Select
          value={status}
          onValueChange={(value) => {
            setStatus(value ?? '0')
            setPage(1)
          }}
        >
          <SelectTrigger className='h-8 w-36' aria-label={t('API key status')}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value='0'>{t('All statuses')}</SelectItem>
            {Object.entries(statusLabels).map(([value, label]) => (
              <SelectItem key={value} value={value}>
                {t(label)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>
      <UserTokenResults
        key={`${identity}:${user.id}:${page}:${keyword}:${status}`}
        user={user}
        page={page}
        keyword={keyword}
        status={status}
        onPage={setPage}
        identity={identity}
      />
    </section>
  )
}

/** 敏感响应只保存在本次展开的组件内；关闭、换页、后台切换及超时都会隐藏明文。 */
function UserTokenResults({
  user,
  page,
  keyword,
  status,
  onPage,
  identity,
}: {
  user: User
  page: number
  keyword: string
  status: string
  onPage: (page: number) => void
  identity: string
}) {
  const { t } = useTranslation()
  const isMobile = useMediaQuery('(max-width: 640px)')
  const { requestVerification, verificationActive } = useUsers()
  const { copyToClipboard } = useCopyToClipboard()
  const [revealed, setRevealed] = useState<{ id: number; key: string } | null>(
    null
  )
  const [busy, setBusy] = useState(false)
  const [confirm, setConfirm] = useState<AdminToken | null>(null)
  const lifecycle = useRef<AbortController | null>(null)
  useEffect(() => {
    const controller = new AbortController()
    lifecycle.current = controller
    return () => {
      controller.abort()
    }
  }, [])
  useEffect(() => {
    if (!revealed) return
    const timer = window.setTimeout(() => setRevealed(null), 30_000)
    const hide = () => {
      if (document.hidden) setRevealed(null)
    }
    document.addEventListener('visibilitychange', hide)
    return () => {
      window.clearTimeout(timer)
      document.removeEventListener('visibilitychange', hide)
    }
  }, [revealed])
  const query = useQuery({
    queryKey: ['admin-user-tokens', identity, user.id, page, keyword, status],
    queryFn: ({ signal }) =>
      getUserTokens(user.id, page, keyword, status, signal),
    retry: false,
    meta: { errorToast: false },
  })

  /** 证明只能用于本次操作；异步验证期间若详情被关闭，不再请求或写入剪贴板。 */
  async function perform(token: AdminToken, action: AdminTokenAction) {
    if (busy) return
    const signal = lifecycle.current?.signal
    setBusy(true)
    setRevealed(null)
    try {
      const proof = await requestVerification({
        scope: 'admin.user.token',
        context: { user_id: user.id, token_id: token.id, action },
        title: t('Verify administrator identity'),
        description: t('Confirm {{action}} for {{user}} / {{key}}.', {
          action: t(actionLabels[action]),
          user: user.username,
          key: token.name,
        }),
      })
      if (!proof || signal?.aborted) return
      const fullKey = await manageUserToken(
        user.id,
        token.id,
        action,
        proof.proof_token,
        signal
      )
      if (signal?.aborted) return
      if (action === 'copy') await copyToClipboard(fullKey)
      else if (action === 'view') {
        if (!document.hidden) setRevealed({ id: token.id, key: fullKey })
      } else {
        toast.success(t('API key status updated'))
        setConfirm(null)
        await query.refetch()
      }
    } catch (error) {
      if (!signal?.aborted) handleServerError(error)
    } finally {
      if (!signal?.aborted) setBusy(false)
    }
  }

  /** 按名额匹配绑定账号；历史无名额信息时不猜测账号归属。 */
  function accountLabel(token: AdminToken) {
    const seat = user.hosting?.seats?.find(
      (seat) => seat.grant?.seat_id === token.hosting_seat_id
    )
    if (seat?.binding) return seat.binding.account_label || t('Hosted account')
    if (
      token.hosting_seat_id === user.hosting?.grant?.seat_id &&
      user.hosting.binding
    ) {
      return user.hosting.binding.account_label
    }
    return token.hosting_seat_id
      ? t('Account binding unavailable')
      : t('No hosted account binding')
  }

  const columns = [
    {
      id: 'name',
      header: t('Name / account'),
      cell: (token: AdminToken) => (
        <div className='min-w-0 space-y-1'>
          <p className='font-medium break-all'>{token.name}</p>
          <p className='text-muted-foreground text-xs break-all'>
            {accountLabel(token)}
          </p>
        </div>
      ),
    },
    {
      id: 'key',
      header: t('API Key'),
      cell: (token: AdminToken) => (
        <div className='flex min-w-0 items-center gap-1'>
          <code className='min-w-0 flex-1 text-xs break-all'>
            {revealed?.id === token.id ? revealed.key : `sk-${token.key}`}
          </code>
          <Button
            size='icon-sm'
            variant='ghost'
            disabled={busy}
            aria-label={t(
              revealed?.id === token.id ? 'Hide API key' : 'View full API key'
            )}
            onClick={() =>
              revealed?.id === token.id
                ? setRevealed(null)
                : void perform(token, 'view')
            }
          >
            {revealed?.id === token.id ? (
              <EyeOff aria-hidden='true' />
            ) : (
              <Eye aria-hidden='true' />
            )}
          </Button>
          <Button
            size='icon-sm'
            variant='ghost'
            disabled={busy}
            aria-label={t('Copy API key')}
            onClick={() => void perform(token, 'copy')}
          >
            <Copy aria-hidden='true' />
          </Button>
        </div>
      ),
    },
    {
      id: 'status',
      header: t('Status'),
      cell: (token: AdminToken) => (
        <StatusBadge
          variant={token.status === 1 ? 'success' : 'neutral'}
          label={t(statusLabels[token.status] || 'Unknown')}
          copyable={false}
        />
      ),
    },
    {
      id: 'time',
      header: t('Last Used / Expires'),
      cell: (token: AdminToken) => (
        <div className='text-muted-foreground space-y-1 text-xs'>
          <p>
            {token.accessed_time > 0
              ? formatTimestampToDate(token.accessed_time)
              : t('Never used')}
          </p>
          <p>
            {t('Expires')}:{' '}
            {token.expired_time === -1
              ? t('Never')
              : formatTimestampToDate(token.expired_time)}
          </p>
        </div>
      ),
    },
    {
      id: 'action',
      header: t('Actions'),
      cell: (token: AdminToken) => (
        <Button
          variant='outline'
          size='sm'
          disabled={busy}
          onClick={() => setConfirm(token)}
        >
          {t(token.status === 1 ? 'Disable' : 'Enable')}
        </Button>
      ),
    },
  ]
  if (query.isPending) return <LoadingState className='min-h-28' size='sm' />
  if (query.isError) {
    return (
      <ErrorState
        className='min-h-28'
        title={t('Failed to load API keys')}
        onRetry={() => void query.refetch()}
      />
    )
  }
  let content
  if (query.data.items.length === 0) {
    content = (
      <EmptyState
        className='min-h-28'
        title={t('No API keys found')}
        description={t('This user has no matching API keys.')}
      />
    )
  } else if (isMobile) {
    content = (
      <div className='space-y-3'>
        {query.data.items.map((token) => (
          <article
            className='bg-background space-y-3 rounded-lg border p-3'
            key={token.id}
          >
            <div>{columns[0].cell(token)}</div>
            {columns[1].cell(token)}
            <div className='flex items-center justify-between'>
              {columns[2].cell(token)}
              {columns[4].cell(token)}
            </div>
            {columns[3].cell(token)}
          </article>
        ))}
      </div>
    )
  } else {
    content = (
      <StaticDataTable
        className='bg-background'
        columns={columns}
        data={query.data.items}
        getRowKey={(token) => token.id}
      />
    )
  }
  return (
    <>
      {content}
      <div className='text-muted-foreground flex flex-wrap items-center justify-between gap-2 text-xs'>
        <span>
          {t('Total')}: {formatNumber(query.data.total)} ·{' '}
          {t('Sensitive actions are verified and audited.')}
        </span>
        <div className='flex items-center gap-2'>
          <Button
            size='sm'
            variant='ghost'
            disabled={page <= 1 || busy}
            onClick={() => onPage(page - 1)}
          >
            {t('Previous')}
          </Button>
          <span>{page}</span>
          <Button
            size='sm'
            variant='ghost'
            disabled={page * 10 >= query.data.total || busy}
            onClick={() => onPage(page + 1)}
          >
            {t('Next')}
          </Button>
        </div>
      </div>
      <ConfirmDialog
        open={!!confirm && !verificationActive}
        onOpenChange={(open) => {
          if (!open && !busy) setConfirm(null)
        }}
        title={t(
          confirm?.status === 1 ? 'Disable API key?' : 'Enable API key?'
        )}
        desc={t('Change the API key {{key}} belonging to {{user}}?', {
          key: confirm?.name,
          user: user.username,
        })}
        confirmText={t(confirm?.status === 1 ? 'Disable' : 'Enable')}
        destructive={confirm?.status === 1}
        isLoading={busy}
        handleConfirm={() => {
          if (confirm) {
            void perform(confirm, confirm.status === 1 ? 'disable' : 'enable')
          }
        }}
      />
    </>
  )
}
