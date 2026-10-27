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
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useEffect, useRef, useState } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { z } from 'zod'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { Button } from '@/components/ui/button'
import { Field, FieldError, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'
import { HOSTING_PERMANENT_END, postHosting } from '@/features/gpt-hosting/api'
import { WindowReading } from '@/features/gpt-quota/quota-reading'
import { quotaSchema } from '@/features/gpt-quota/schema'
import { toIntlLocale } from '@/i18n/languages'
import { api } from '@/lib/api'
import { handleServerError } from '@/lib/handle-server-error'
import { ROLE } from '@/lib/roles'
import { requireServerSuccess } from '@/lib/server-error-message'
import { useAuthStore } from '@/stores/auth-store'

import {
  getHostingSummaries,
  hostingLocalDate,
  hostingStatusLabels,
  renewedHostingEnd,
} from '../hosting-api'
import type { User } from '../types'
import { HostingConnection } from './hosting-cells'
import { useUsers } from './users-provider'

const grantForm = z
  .object({
    start: z.string().min(1),
    end: z.string(),
    permanent: z.boolean(),
    retention: z.number().int().min(0).max(30),
    enabled: z.boolean(),
    note: z.string().max(255),
  })
  .refine(
    (v) =>
      Number.isFinite(Date.parse(v.start)) &&
      (v.permanent || Date.parse(v.end) > Date.parse(v.start)),
    {
      path: ['end'],
      message: 'The end date must be later than the start date.',
    }
  )
type GrantValues = z.infer<typeof grantForm>

/** 一个用户抽屉内完成额度读取与期限管理；预检、确认和二次验证完成后才写入权益。 */
export function HostingUserPanel(props: { user: User }) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const actor = useAuthStore((s) => s.auth.user)
  const sid = useAuthStore((s) => s.auth.session?.sid)
  const cache = useQueryClient()
  const { triggerRefresh, requestVerification, verificationActive } = useUsers()
  const queryKey = ['hosting-user', actor?.id, sid, props.user.id]
  const query = useQuery({
    queryKey,
    queryFn: async ({ signal }) =>
      (await getHostingSummaries([props.user.id], signal))[0],
    retry: false,
    staleTime: 15_000,
  })
  const [selectedSeat, setSelectedSeat] = useState('')
  const isNewSeat = selectedSeat === '__new'
  const selected = query.data?.seats?.find(
    (seat) => seat.grant?.seat_id === selectedSeat
  )
  // 新增名额不能沿用首名额的账号摘要；选择已有名额时只展示该名额的数据。
  const isAdditional =
    !!selected && selected.grant?.seat_id !== query.data?.grant?.seat_id
  let data = query.data
  if (isNewSeat && query.data) {
    data = {
      ...query.data,
      grant: null,
      binding: null,
      quota: null,
      slot_id: undefined,
      instance_id: undefined,
      instance_name: undefined,
      connection: undefined,
      status: 'unopened',
    }
  } else if (isAdditional && query.data && selected) {
    data = {
      ...query.data,
      grant: selected.grant,
      binding: selected.binding,
      slot_id: selected.slot_id,
      instance_id: selected.instance_id,
      instance_name: selected.instance_name,
      connection: selected.connection,
      quota: undefined,
      quota_unavailable: false,
      status: selected.status || (selected.valid ? 'active' : 'expired'),
    }
  }
  const quotaKey = [
    'hosting-seat-quota',
    actor?.id,
    sid,
    props.user.id,
    selectedSeat,
  ]
  const seatQuota = useQuery({
    queryKey: quotaKey,
    queryFn: async ({ signal }) => {
      const res = await api.get(`/api/gpt-quota/users/${props.user.id}`, {
        signal,
        params: { seat_id: selectedSeat },
      })
      return quotaSchema.parse(requireServerSuccess(res.data).data)
    },
    enabled:
      isAdditional &&
      selected.binding?.platform !== 'claude' &&
      !!selected.binding?.account_label,
    retry: false,
  })
  const grant = isNewSeat ? undefined : data?.grant
  const [review, setReview] = useState<GrantValues | null>(null)
  const [preflight, setPreflight] = useState<string>('')
  const [checking, setChecking] = useState(false)
  const form = useForm<GrantValues>({
    resolver: zodResolver(grantForm),
    defaultValues: {
      start: hostingLocalDate(Date.now() / 1000),
      end: '',
      permanent: false,
      retention: 7,
      enabled: true,
      note: '',
    },
  })
  const loadedGrant = useRef<string>('')
  useEffect(() => {
    const version = JSON.stringify(grant)
    // 后台刷新不能覆盖管理员尚未保存的期限；保存成功后才允许重新装载。
    if (grant && version !== loadedGrant.current && !form.formState.isDirty) {
      loadedGrant.current = version
      form.reset({
        start: hostingLocalDate(grant.starts_at),
        end:
          grant.ends_at === HOSTING_PERMANENT_END
            ? ''
            : hostingLocalDate(grant.ends_at),
        permanent: grant.ends_at === HOSTING_PERMANENT_END,
        retention: grant.retention_days,
        enabled: grant.enabled,
        note: grant.note,
      })
    }
  }, [grant, form, form.formState.isDirty])
  const permanent = form.watch('permanent')
  const isTakeover =
    !isNewSeat && (data?.status === 'undated' || data?.status === 'takeover')
  const canWrite =
    actor?.role === ROLE.SUPER_ADMIN &&
    props.user.role === ROLE.USER &&
    props.user.status === 1
  const save = useMutation({
    mutationFn: async (values: GrantValues) => {
      const proof = await requestVerification({
        scope: 'admin.user.update',
        context: { user_id: props.user.id },
      })
      if (!proof) return false
      const result = await postHosting<{ sync_pending?: boolean }>(
        isTakeover ? 'admin/takeover' : 'admin/grants',
        {
          user_id: props.user.id,
          seat_id: isNewSeat ? undefined : grant?.seat_id,
          new_seat: isNewSeat,
          starts_at: Math.floor(Date.parse(values.start) / 1000),
          ends_at: values.permanent
            ? HOSTING_PERMANENT_END
            : Math.floor(Date.parse(values.end) / 1000),
          retention_days: values.retention,
          enabled: values.enabled,
          note: values.note,
        },
        proof.proof_token
      )
      form.reset(values)
      loadedGrant.current = ''
      return result.sync_pending ? 'pending' : 'saved'
    },
    retry: false,
    onError: (error) => handleServerError(error),
    onSettled: async () => {
      setReview(null)
      await cache.invalidateQueries({ queryKey })
      triggerRefresh()
    },
    onSuccess: (changed) => {
      if (changed) setSelectedSeat('')
      if (changed === 'pending') {
        toast.warning(t('Hosting term saved; instance sync is pending.'))
      } else if (changed) toast.success(t('Hosting entitlement saved'))
    },
  })
  const refresh = useMutation({
    mutationFn: async () => {
      const res = await api.post(
        `/api/gpt-quota/users/${props.user.id}/refresh`,
        {},
        {
          timeout: 90_000,
          params: selectedSeat ? { seat_id: selectedSeat } : undefined,
        }
      )
      return quotaSchema.parse(requireServerSuccess(res.data).data)
    },
    retry: false,
    onError: (error) => handleServerError(error),
    onSuccess: async (quota) => {
      if (isAdditional) {
        cache.setQueryData(quotaKey, quota)
      } else {
        cache.setQueryData(queryKey, (old: typeof data) =>
          old ? { ...old, quota, quota_unavailable: false } : old
        )
      }
      triggerRefresh()
    },
  })
  /** 旧绑定只核对既有用户、槽位和 Key 的对应关系，不要求刷新 GPT 登录；确认和管理员验证之后才写期限。 */
  async function reviewGrant(values: GrantValues): Promise<void> {
    setChecking(true)
    setPreflight('')
    try {
      if (!isNewSeat && data?.status === 'undated') {
        const result = await postHosting<{
          account_label: string
          instance_id: string
          slot_id: string
        }>('admin/takeover/check', { user_id: props.user.id })
        setPreflight(
          `${result.account_label} · ${result.instance_id} / ${result.slot_id}`
        )
      }
      setReview(values)
    } catch (error) {
      handleServerError(error)
    } finally {
      setChecking(false)
    }
  }
  const reading = isAdditional ? seatQuota.data : data?.quota
  const quota = reading && reading.bound === true ? reading : undefined
  return (
    <section className='users-hosting-panel space-y-6 border-b p-6'>
      <div className='users-account-summary space-y-2'>
        <h3 className='font-semibold'>{t('Hosted accounts')}</h3>
        <p className='text-sm'>
          {t(
            hostingStatusLabels[data?.status || 'unavailable'] ||
              'Hosting status unavailable'
          )}
        </p>
        <p className='text-muted-foreground text-xs'>
          {data?.binding?.account_label ||
            (quota?.bound ? quota.account_label : undefined) ||
            '—'}{' '}
          ·{' '}
          {data?.binding?.plan ||
            (quota?.bound ? quota.plan : undefined) ||
            '—'}
        </p>
        {data && <HostingConnection summary={data} detailed />}
        {data?.status === 'undated' && (
          <p className='text-muted-foreground text-sm'>
            {t(
              'Existing service continues until an administrator sets a hosting term.'
            )}
          </p>
        )}
        {(data?.status === 'undated' ||
          data?.binding?.identity_source === 'legacy_binding') && (
          <p className='text-muted-foreground text-sm'>
            {t(
              'Administrators can manage this term without GPT sign-in. Official identity is verified when the user signs in again.'
            )}
          </p>
        )}
        {data?.status === 'takeover' && (
          <p role='alert' className='text-destructive text-sm'>
            {t(
              'Takeover is incomplete. Retry the saved operation; the account remains reserved.'
            )}
          </p>
        )}
        {query.isError && (
          <Button
            type='button'
            variant='outline'
            onClick={() => query.refetch()}
          >
            {t('Retry')}
          </Button>
        )}
      </div>
      <div className='flex flex-wrap items-end gap-3'>
        <Field className='min-w-48 flex-1'>
          <FieldLabel htmlFor='admin-hosting-seat'>
            {t('Hosting seats')} · {query.data?.bound_seats || 0}/
            {query.data?.seat_count || 0}
          </FieldLabel>
          <NativeSelect
            id='admin-hosting-seat'
            value={
              isNewSeat ? '__new' : selectedSeat || data?.grant?.seat_id || ''
            }
            disabled={save.isPending || checking || refresh.isPending}
            onChange={(event) => {
              setSelectedSeat(event.target.value)
              loadedGrant.current = ''
              form.reset({
                start: hostingLocalDate(Date.now() / 1000),
                end: '',
                permanent: false,
                retention: 7,
                enabled: true,
                note: '',
              })
            }}
          >
            {!query.data?.seats?.length && (
              <NativeSelectOption value=''>
                {t('First hosting seat')}
              </NativeSelectOption>
            )}
            {query.data?.seats?.map((seat, index) => (
              <NativeSelectOption
                key={seat.grant?.seat_id}
                value={seat.grant?.seat_id}
              >
                {t('Seat {{number}}', { number: index + 1 })} ·{' '}
                {seat.binding?.account_label || t('Not connected')}
              </NativeSelectOption>
            ))}
            {isNewSeat && (
              <NativeSelectOption value='__new'>
                {t('New hosting seat')}
              </NativeSelectOption>
            )}
          </NativeSelect>
        </Field>
        {canWrite && !!query.data?.seats?.length && (
          <Button
            type='button'
            variant='outline'
            disabled={save.isPending || isNewSeat || refresh.isPending}
            onClick={() => {
              setSelectedSeat('__new')
              loadedGrant.current = ''
              form.reset({
                start: hostingLocalDate(Date.now() / 1000),
                end: '',
                permanent: false,
                retention: 7,
                enabled: true,
                note: '',
              })
            }}
          >
            {t('Add hosting seat')}
          </Button>
        )}
      </div>
      {!isNewSeat && data?.binding?.platform !== 'claude' && (
        <div className='space-y-3'>
          <div className='flex flex-wrap items-center justify-between gap-2'>
            <h3 className='text-sm font-semibold'>
              {t('GPT subscription quota')}
            </h3>
            {data?.slot_id && (
              <Button
                type='button'
                size='sm'
                variant='outline'
                disabled={refresh.isPending}
                onClick={() => refresh.mutate()}
              >
                {refresh.isPending
                  ? t('Querying official quota...')
                  : t('Refresh official quota')}
              </Button>
            )}
          </div>
          <p className='text-muted-foreground text-xs'>
            {t(
              'Official quota is read-only. Hosting renewals and site balance changes do not reset it.'
            )}
          </p>
          {quota?.bound && (
            <>
              <div className='grid gap-3 sm:grid-cols-2'>
                {quota.windows.map((window) => (
                  <WindowReading
                    key={window.id}
                    window={window}
                    now={Date.now()}
                  />
                ))}
              </div>
              <p className='text-muted-foreground text-xs'>
                {t('Last successful reading')}:{' '}
                {quota.observed_at
                  ? new Date(quota.observed_at).toLocaleString(locale)
                  : t('Unknown')}
                {quota.stale ? ` · ${t('Saved reading may be outdated')}` : ''}
              </p>
            </>
          )}
          {(data?.quota_unavailable || seatQuota.isError) && (
            <p className='text-muted-foreground text-sm'>
              {t('GPT quota could not be loaded')}
            </p>
          )}
        </div>
      )}
      {canWrite &&
        data &&
        !['conflict', 'unavailable', 'restricted'].includes(data.status) && (
          <form
            className='users-term-form space-y-4'
            onSubmit={form.handleSubmit(reviewGrant)}
          >
            <div>
              <h3 className='text-sm font-semibold'>
                {isNewSeat ? t('New hosting seat') : t('Hosting entitlement')}
              </h3>
              <p className='text-muted-foreground text-xs'>
                {t(
                  'Each seat hosts one GPT or Claude account and has its own term. Disabling a seat blocks only its account.'
                )}
              </p>
            </div>
            <div className='space-y-2'>
              <div className='flex items-center justify-between'>
                <FieldLabel htmlFor='hosting-permanent'>
                  {t('Permanent hosting')}
                </FieldLabel>
                <Switch
                  id='hosting-permanent'
                  checked={permanent}
                  disabled={data.status === 'takeover'}
                  onCheckedChange={(value) =>
                    form.setValue('permanent', value, {
                      shouldDirty: true,
                      shouldValidate: true,
                    })
                  }
                />
              </div>
              {permanent && (
                <p className='text-muted-foreground text-xs'>
                  {t(
                    'No automatic expiry. Turn off permanent hosting to set an end date or extend the term.'
                  )}
                </p>
              )}
            </div>
            <div className='grid gap-3 sm:grid-cols-2'>
              <Field>
                <FieldLabel htmlFor='hosting-start'>
                  {t('Starts at')}
                </FieldLabel>
                <Input
                  id='hosting-start'
                  type='datetime-local'
                  {...form.register('start')}
                  disabled={data.status === 'takeover'}
                />
              </Field>
              <Field>
                <FieldLabel htmlFor='hosting-end'>{t('Ends at')}</FieldLabel>
                <Input
                  id='hosting-end'
                  type='datetime-local'
                  {...form.register('end')}
                  disabled={data.status === 'takeover' || permanent}
                />
                {form.formState.errors.end && (
                  <FieldError>
                    {t(
                      form.formState.errors.end.message || 'Invalid parameters'
                    )}
                  </FieldError>
                )}
              </Field>
            </div>
            <div className='flex flex-wrap gap-2'>
              {[30, 90].map((days) => (
                <Button
                  type='button'
                  key={days}
                  size='sm'
                  variant='outline'
                  disabled={data.status === 'takeover' || permanent}
                  onClick={() =>
                    form.setValue(
                      'end',
                      renewedHostingEnd(
                        grant && grant.ends_at !== HOSTING_PERMANENT_END
                          ? hostingLocalDate(grant.ends_at)
                          : '',
                        days
                      ),
                      { shouldDirty: true }
                    )
                  }
                >
                  {t('Extend by {{days}} days', { days })}
                </Button>
              ))}
            </div>
            <Field>
              <FieldLabel htmlFor='hosting-retention'>
                {t('Credential retention days after expiry')}
              </FieldLabel>
              <Input
                id='hosting-retention'
                type='number'
                min={0}
                max={30}
                {...form.register('retention', { valueAsNumber: true })}
                disabled={data.status === 'takeover' || permanent}
              />
              {form.formState.errors.retention && (
                <FieldError>{t('Enter 0 to 30 days')}</FieldError>
              )}
            </Field>
            <Field>
              <FieldLabel htmlFor='hosting-note'>
                {t('Reason or note')}
              </FieldLabel>
              <Textarea
                id='hosting-note'
                maxLength={255}
                {...form.register('note')}
                disabled={data.status === 'takeover'}
              />
            </Field>
            <div className='flex items-center justify-between'>
              <FieldLabel htmlFor='hosting-enabled'>
                {t('Hosting enabled')}
              </FieldLabel>
              <Switch
                id='hosting-enabled'
                checked={form.watch('enabled')}
                onCheckedChange={(value) =>
                  form.setValue('enabled', value, { shouldDirty: true })
                }
                disabled={data.status === 'takeover'}
              />
            </div>
            <Button
              type='submit'
              disabled={save.isPending || checking || refresh.isPending}
            >
              {checking
                ? t('Checking existing binding...')
                : t('Review and save hosting term')}
            </Button>
          </form>
        )}
      <ConfirmDialog
        open={!!review && !verificationActive}
        onOpenChange={(value) => !value && !save.isPending && setReview(null)}
        title={t('Confirm hosting entitlement')}
        desc={
          <div className='space-y-2'>
            <p>{preflight}</p>
            <p>
              {props.user.username} · {review?.start.replace('T', ' ')} →{' '}
              {review?.permanent
                ? t('Permanent hosting')
                : review?.end.replace('T', ' ')}
            </p>
            <p>
              {review?.enabled ? t('Hosting enabled') : t('Hosting disabled')} ·{' '}
              {review?.permanent
                ? t('No automatic expiry')
                : `${review?.retention} ${t('days')}`}
            </p>
            <p>
              {isTakeover
                ? t(
                    'GPT sign-in is not required. Existing direct keys and credentials are retained; all retained keys share this hosting term. New API keeps using its existing upstream key.'
                  )
                : t('Only the hosting entitlement will change.')}
            </p>
          </div>
        }
        handleConfirm={() => review && save.mutate(review)}
        isLoading={save.isPending}
      />
    </section>
  )
}
