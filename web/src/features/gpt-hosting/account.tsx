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
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { EmptyState } from '@/components/empty-state'
import { ErrorState } from '@/components/error-state'
import { SectionPageLayout } from '@/components/layout/components/section-page-layout'
import { LoadingState } from '@/components/loading-state'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { Field, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import {
  SecureVerificationDialog,
  useSecureVerification,
} from '@/features/auth/secure-verification'
import { HOSTING_PERMANENT_END } from '@/features/gpt-hosting/api'
import { GptQuotaCard } from '@/features/gpt-quota/GptQuotaCard'
import { isOfficialAuthorizationURL } from '@/features/onboarding/lib/schema'
import { toIntlLocale } from '@/i18n/languages'
import { handleServerError } from '@/lib/handle-server-error'
import { useAuthStore } from '@/stores/auth-store'

import {
  getHosting,
  postHosting,
  type HostingLogin,
  type HostingSelf,
} from './api'

/** 会话变化时卸载授权表单，避免上一个用户的回调、验证结果和缓存被复用。 */
export function HostingAccount(): React.JSX.Element {
  const user = useAuthStore((s) => s.auth.user)
  const session = useAuthStore((s) => s.auth.session?.sid)
  return (
    <AccountSession
      key={`${user?.id}:${session}`}
      identity={`${user?.id}:${session}`}
    />
  )
}

/** 用户只管理自己的账号；实例与槽位选择始终由后端完成。 */
function AccountSession(props: { identity: string }): React.JSX.Element {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const client = useQueryClient()
  const [seatId, setSeatId] = useState('')
  const [platform, setPlatform] = useState<'codex' | 'claude'>('codex')
  const queryKey = ['gpt-hosting', 'self', props.identity, seatId]
  const query = useQuery({
    queryKey,
    queryFn: ({ signal }) => getHosting<HostingSelf>('self', signal, seatId),
    refetchInterval: 15_000,
  })
  const verification = useSecureVerification()
  const [login, setLogin] = useState<HostingLogin>()
  const [callbackValue, setCallbackValue] = useState('')
  const callback = useRef('')
  const [confirmUnbind, setConfirmUnbind] = useState(false)
  const data = query.data
  const action = useMutation({
    mutationFn: async (
      kind: 'login' | 'finish' | 'retry' | 'pause' | 'unbind'
    ) => {
      if (!data?.grant) return
      let proof: string | undefined
      if (kind === 'login' || kind === 'retry' || kind === 'unbind') {
        if (!data?.binding_id) return
        const result = await verification.requestVerification({
          scope: 'gpt.hosting.manage',
          context: {
            action: kind === 'unbind' ? 'unbind' : 'login',
            binding_id: data.binding_id,
          },
        })
        if (!result) return
        proof = result.proof_token
      }
      if (kind === 'login') {
        const next = await postHosting<HostingLogin>(
          'login',
          {
            platform:
              data.binding?.state === 'unbound'
                ? platform
                : data.binding?.platform || 'codex',
          },
          proof,
          data.grant?.seat_id
        )
        if (!isHostedAuthorizationURL(next.auth_url, next.platform)) {
          throw new Error(
            t('The authorization URL is not an approved provider address.')
          )
        }
        setLogin(next)
        callback.current = ''
        setCallbackValue('')
      } else if (kind === 'finish') {
        if (!login) return
        // 授权码不进入 mutation variables/cache，提交结束无论成败均清掉输入。
        try {
          await postHosting(
            'finish',
            {
              session_id: login.session_id,
              callback_url: callback.current,
            },
            undefined,
            data?.grant?.seat_id
          )
        } finally {
          callback.current = ''
          setCallbackValue('')
        }
        setLogin(undefined)
      } else if (kind === 'pause') {
        await postHosting(
          'pause',
          { paused: !data?.binding?.paused },
          undefined,
          data?.grant?.seat_id
        )
      } else {
        await postHosting(kind, {}, proof, data?.grant?.seat_id)
        setLogin(undefined)
        callback.current = ''
        setCallbackValue('')
      }
      setConfirmUnbind(false)
    },
    retry: false,
    gcTime: 0,
    onError: (error) => handleServerError(error),
    onSettled: async () => {
      await client.invalidateQueries({ queryKey })
    },
  })
  const state = data?.binding?.state ?? 'unbound'
  const canLogin = !!data?.valid && state !== 'cleaning'
  let status = t('Not connected')
  if (state === 'active') {
    status = data?.binding?.paused ? t('Paused') : t('Connected')
  }
  if (state === 'reserved' || state === 'authorizing') {
    status = t('Awaiting authorization')
  }
  if (state === 'prepared') status = t('Finishing connection')
  if (state === 'cleaning') status = t('Cleaning credentials')
  return (
    <SectionPageLayout>
      <SectionPageLayout.Title>{t('Hosted accounts')}</SectionPageLayout.Title>
      <SectionPageLayout.Actions>
        <Button
          variant='outline'
          onClick={() => query.refetch()}
          disabled={query.isFetching}
        >
          {t('Refresh')}
        </Button>
      </SectionPageLayout.Actions>
      <SectionPageLayout.Content>
        <div className='mx-auto max-w-4xl space-y-5'>
          {!!data?.seats?.length && (
            <Field>
              <FieldLabel htmlFor='hosting-seat-select'>
                {t('Hosting seat')}
              </FieldLabel>
              <NativeSelect
                id='hosting-seat-select'
                value={data.grant?.seat_id || ''}
                disabled={action.isPending}
                onChange={(event) => {
                  // 切换名额立即销毁授权输入，旧回调不能提交到新选择的账号。
                  setSeatId(event.target.value)
                  setLogin(undefined)
                  callback.current = ''
                  setCallbackValue('')
                  setConfirmUnbind(false)
                }}
              >
                {data.seats.map((seat, index) => (
                  <NativeSelectOption
                    key={seat.grant?.seat_id}
                    value={seat.grant?.seat_id}
                  >
                    {t('Seat {{number}}', { number: index + 1 })} ·{' '}
                    {seat.binding?.account_label || t('Not connected')} ·{' '}
                    {seat.binding?.platform === 'claude' ? 'Claude' : 'GPT'}
                  </NativeSelectOption>
                ))}
              </NativeSelect>
            </Field>
          )}
          {data?.grant && state === 'unbound' && (
            <Field>
              <FieldLabel htmlFor='hosting-platform'>
                {t('Account platform')}
              </FieldLabel>
              <NativeSelect
                id='hosting-platform'
                value={platform}
                disabled={action.isPending}
                onChange={(event) =>
                  setPlatform(
                    event.target.value === 'claude' ? 'claude' : 'codex'
                  )
                }
              >
                <NativeSelectOption value='codex'>GPT</NativeSelectOption>
                <NativeSelectOption value='claude'>Claude</NativeSelectOption>
              </NativeSelect>
            </Field>
          )}

          {query.isPending && <LoadingState />}
          {query.isError && (
            <ErrorState
              description={t('Unable to load hosting status.')}
              onRetry={() => query.refetch()}
            />
          )}
          {data && !data.grant && (
            <EmptyState
              title={t('No hosting entitlement yet')}
              description={t(
                'Ask the administrator for a hosting seat. Each seat has its own term and can host one GPT or Claude account.'
              )}
            />
          )}
          {data?.grant && (
            <>
              <Card>
                <CardHeader>
                  <div className='flex flex-wrap items-center justify-between gap-3'>
                    <CardTitle>{t('Hosting seat')}</CardTitle>
                    <Badge variant={data.valid ? 'default' : 'secondary'}>
                      {data.valid ? t('Active') : t('Inactive')}
                    </Badge>
                  </div>
                  <CardDescription>
                    {t(
                      'Hosting does not include an official subscription or additional official quota.'
                    )}
                  </CardDescription>
                </CardHeader>
                <CardContent className='space-y-3 text-sm'>
                  <p>
                    {t('Service period')}:{' '}
                    {new Date(data.grant.starts_at * 1000).toLocaleString(
                      locale
                    )}{' '}
                    —{' '}
                    {data.grant.ends_at === HOSTING_PERMANENT_END
                      ? t('Permanent hosting')
                      : new Date(data.grant.ends_at * 1000).toLocaleString(
                          locale
                        )}
                  </p>
                  <p className='text-muted-foreground'>
                    {t('Times are shown in your device time zone: {{zone}}.', {
                      zone: Intl.DateTimeFormat().resolvedOptions().timeZone,
                    })}
                  </p>
                  <p>
                    {data.grant.ends_at === HOSTING_PERMANENT_END
                      ? t(
                          'This hosting entitlement has no automatic expiry. The administrator can disable it or change its term, and you may unbind your account.'
                        )
                      : t(
                          'After expiry, access stops immediately. Credentials are retained for {{days}} days for renewal, then cleaned automatically. You may unbind earlier.',
                          { days: data.grant.retention_days }
                        )}
                  </p>
                  {!data.valid && (
                    <Alert>
                      <AlertDescription>
                        {t(
                          'Your hosting entitlement is not active. Contact the administrator to renew it.'
                        )}
                      </AlertDescription>
                    </Alert>
                  )}
                </CardContent>
              </Card>
              <Card>
                <CardHeader>
                  <div className='flex flex-wrap items-center justify-between gap-3'>
                    <CardTitle>
                      {data.binding?.account_label ||
                        t('Connect your own account')}
                    </CardTitle>
                    <Badge variant='outline'>{status}</Badge>
                  </div>
                  <CardDescription>
                    {t(
                      'Each seat hosts one account. Reauthorization must use the same account; unbind first to change accounts.'
                    )}
                  </CardDescription>
                </CardHeader>
                <CardContent className='space-y-4'>
                  {data.binding?.plan && (
                    <p className='text-sm'>
                      {t('Official subscription')}: {data.binding.plan}
                    </p>
                  )}
                  <p className='text-muted-foreground text-sm'>
                    {t(
                      'Connecting or unbinding requires security verification. Manage your verification methods in Security.'
                    )}{' '}
                    <Link className='underline' to='/security'>
                      {t('Security')}
                    </Link>
                  </p>
                  <div className='flex flex-wrap gap-2'>
                    <Button
                      disabled={!canLogin || action.isPending}
                      onClick={() => action.mutate('login')}
                    >
                      {state === 'unbound'
                        ? t('Connect account')
                        : t('Sign in again')}
                    </Button>
                    {state === 'active' && (
                      <Button
                        variant='outline'
                        disabled={
                          action.isPending ||
                          (!data.valid && data.binding?.paused)
                        }
                        onClick={() => action.mutate('pause')}
                      >
                        {data.binding?.paused
                          ? t('Resume access')
                          : t('Pause access')}
                      </Button>
                    )}
                    {(state === 'prepared' || state === 'authorizing') && (
                      <Button
                        variant='outline'
                        disabled={!data.valid || action.isPending}
                        onClick={() => action.mutate('retry')}
                      >
                        {t('Recover connection')}
                      </Button>
                    )}
                    {state !== 'unbound' && (
                      <Button
                        variant='outline'
                        disabled={action.isPending || state === 'cleaning'}
                        onClick={() => setConfirmUnbind(true)}
                      >
                        {t('Unbind account')}
                      </Button>
                    )}
                  </div>
                  {action.isError && (
                    <Alert variant='destructive'>
                      <AlertDescription>
                        {action.error.message}
                      </AlertDescription>
                    </Alert>
                  )}
                  {state === 'cleaning' && (
                    <p role='status' className='text-sm'>
                      {t(
                        'Credential cleanup is still in progress. The slot remains reserved.'
                      )}
                    </p>
                  )}
                  {login && (
                    <div className='space-y-4 rounded-lg border p-4'>
                      <p className='text-sm'>
                        {t(
                          'Sign in on the official provider page. Never enter your account password in this form.'
                        )}
                      </p>
                      <Button
                        nativeButton={false}
                        role='link'
                        render={
                          <a
                            href={login.auth_url}
                            target='_blank'
                            rel='noopener noreferrer'
                          />
                        }
                      >
                        {t('Open official authorization')}
                      </Button>
                      <p className='text-muted-foreground text-sm'>
                        {login.platform === 'claude'
                          ? t(
                              'Paste the complete authorization code from Claude, including the part after #. Do not share it.'
                            )
                          : t(
                              'If the browser opens a localhost address that cannot load, copy its full address and paste it below. Do not share that address.'
                            )}
                      </p>
                      <Field>
                        <FieldLabel htmlFor='hosting-callback'>
                          {login.platform === 'claude'
                            ? t('Authorization code including #state')
                            : t('Authorization callback URL')}
                        </FieldLabel>
                        <Input
                          id='hosting-callback'
                          autoComplete='off'
                          spellCheck={false}
                          value={callbackValue}
                          onChange={(e) => {
                            setCallbackValue(e.target.value)
                            callback.current = e.target.value
                          }}
                        />
                      </Field>
                      <Button
                        disabled={action.isPending || !callbackValue.trim()}
                        onClick={() => action.mutate('finish')}
                      >
                        {t('Complete account sign-in')}
                      </Button>
                    </div>
                  )}
                  <p className='text-muted-foreground text-sm'>
                    {t(
                      'Choose this account for a New API key on the API Keys page. Each key uses only its selected account.'
                    )}{' '}
                    <Link to='/keys' className='underline'>
                      {t('API Keys')}
                    </Link>{' '}
                    ·{' '}
                    <Link to='/docs' className='underline'>
                      {t('User guide')}
                    </Link>
                  </p>
                </CardContent>
              </Card>
              {state === 'active' && data.binding?.platform !== 'claude' && (
                <GptQuotaCard seatId={data.grant.seat_id} />
              )}
            </>
          )}
        </div>
        <ConfirmDialog
          open={confirmUnbind}
          onOpenChange={setConfirmUnbind}
          title={t('Unbind account')}
          desc={t(
            'This stops access for this seat and removes its hosted credentials. Its term does not change. Official authorization is not revoked.'
          )}
          confirmText={t('Unbind account')}
          destructive
          isLoading={action.isPending}
          handleConfirm={() => action.mutate('unbind')}
        />
        <SecureVerificationDialog {...verification.dialogProps} />
      </SectionPageLayout.Content>
    </SectionPageLayout>
  )
}

/** 仅允许两个协议的官方授权页面，禁止远端响应引导用户访问任意地址。 */
function isHostedAuthorizationURL(value: string, platform?: string): boolean {
  if (platform !== 'claude') return isOfficialAuthorizationURL(value)
  try {
    const url = new URL(value)
    return (
      url.origin === 'https://claude.com' &&
      url.pathname === '/cai/oauth/authorize' &&
      !url.username &&
      !url.password &&
      !url.hash
    )
  } catch {
    return false
  }
}
