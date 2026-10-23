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
import { Link } from '@tanstack/react-router'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { z } from 'zod'

import { EmptyState } from '@/components/empty-state'
import { ErrorState } from '@/components/error-state'
import { SectionPageLayout } from '@/components/layout/components/section-page-layout'
import { LoadingState } from '@/components/loading-state'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { Field, FieldError, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Switch } from '@/components/ui/switch'
import {
  SecureVerificationDialog,
  useSecureVerification,
} from '@/features/auth/secure-verification'
import { getUser } from '@/features/users/api'
import { toIntlLocale } from '@/i18n/languages'
import { handleServerError } from '@/lib/handle-server-error'
import { useAuthStore } from '@/stores/auth-store'

import {
  getHosting,
  postHosting,
  type HostingAdmin as AdminData,
  type HostingGrant,
} from './api'

const grantSchema = z
  .object({
    user_id: z.number().int().positive('Enter a valid user ID.'),
    start: z.string().min(1, 'Select a start date.'),
    end: z.string().min(1, 'Select an end date.'),
    retention_days: z.number().int().min(0).max(30),
    enabled: z.boolean(),
    note: z.string().max(255),
  })
  .refine(
    (v) =>
      Number.isFinite(Date.parse(v.start)) &&
      Date.parse(v.end) > Date.parse(v.start),
    {
      path: ['end'],
      message: 'The end date must be later than the start date.',
    }
  )
type GrantForm = z.infer<typeof grantSchema>

/** 编辑旧名额时将 UTC 秒转换为设备本地分钟，防止 datetime-local 隐式改变时区。 */
function localInput(seconds: number): string {
  const d = new Date(seconds * 1000)
  return new Date(d.getTime() - d.getTimezoneOffset() * 60000)
    .toISOString()
    .slice(0, 16)
}

/** 管理员发放有期限的单名额并登记预置库存；不会自动创建槽位或改动已有出口。 */
export function HostingAdmin(): React.JSX.Element {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const identity = useAuthStore(
    (s) => `${s.auth.user?.id}:${s.auth.session?.sid}`
  )
  const queryKey = ['gpt-hosting', 'admin', identity]
  const client = useQueryClient()
  const query = useQuery({
    queryKey,
    queryFn: ({ signal }) => getHosting<AdminData>('admin', signal),
  })
  const verification = useSecureVerification()
  const form = useForm<GrantForm>({
    resolver: zodResolver(grantSchema),
    defaultValues: {
      user_id: 0,
      start: '',
      end: '',
      enabled: true,
      retention_days: 7,
      note: '',
    },
  })
  const userId = form.watch('user_id')
  const user = useQuery({
    queryKey: ['gpt-hosting-user', identity, userId],
    queryFn: () => getUser(userId),
    enabled: userId > 0,
    retry: false,
  })
  const save = useMutation({
    mutationFn: async (values: GrantForm) => {
      const proof = await verification.requestVerification({
        scope: 'admin.user.update',
        context: { user_id: values.user_id },
      })
      if (!proof) return
      await postHosting(
        'admin/grants',
        {
          user_id: values.user_id,
          starts_at: Math.floor(Date.parse(values.start) / 1000),
          ends_at: Math.floor(Date.parse(values.end) / 1000),
          retention_days: values.retention_days,
          enabled: values.enabled,
          note: values.note,
        },
        proof.proof_token
      )
    },
    retry: false,
    onError: (error) => handleServerError(error),
    onSuccess: async () => {
      await client.invalidateQueries({ queryKey })
    },
  })
  const inventory = useMutation({
    mutationFn: (body: {
      instance_id: string
      slot_id: string
      enabled: boolean
    }) => postHosting('admin/inventory', body),
    onError: (error) => handleServerError(error),
    onSuccess: async () => {
      await client.invalidateQueries({ queryKey })
    },
  })
  /** 编辑和续期复用原名额；账号归属和绑定状态不受表单重置影响。 */
  function editGrant(grant: HostingGrant): void {
    form.reset({
      user_id: grant.user_id,
      start: localInput(grant.starts_at),
      end: localInput(grant.ends_at),
      enabled: grant.enabled,
      retention_days: grant.retention_days,
      note: grant.note,
    })
    form.setFocus('end')
  }
  return (
    <SectionPageLayout>
      <SectionPageLayout.Title>
        {t('GPT hosting management')}
      </SectionPageLayout.Title>
      <SectionPageLayout.Actions>
        <Button
          variant='outline'
          disabled={query.isFetching}
          onClick={() => query.refetch()}
        >
          {t('Refresh')}
        </Button>
      </SectionPageLayout.Actions>
      <SectionPageLayout.Content>
        <div className='mx-auto max-w-5xl space-y-5'>
          <Card>
            <CardHeader>
              <CardTitle>{t('Grant or renew one hosting seat')}</CardTitle>
              <CardDescription>
                {t(
                  'Create the user first, then grant a dated entitlement. Existing account bindings are not migrated automatically.'
                )}{' '}
                <Link to='/users' className='underline'>
                  {t('Users')}
                </Link>
              </CardDescription>
            </CardHeader>
            <CardContent>
              <form
                className='grid gap-4 sm:grid-cols-2'
                onSubmit={form.handleSubmit((values) => save.mutate(values))}
              >
                <Field>
                  <FieldLabel htmlFor='hosting-user'>{t('User ID')}</FieldLabel>
                  <Input
                    id='hosting-user'
                    type='number'
                    min={1}
                    {...form.register('user_id', { valueAsNumber: true })}
                    aria-invalid={!!form.formState.errors.user_id}
                  />
                  <FieldError>
                    {form.formState.errors.user_id?.message &&
                      t(form.formState.errors.user_id.message)}
                  </FieldError>
                  <p className='text-muted-foreground text-sm'>
                    {user.data?.data?.username ?? t('Select an existing user.')}
                  </p>
                </Field>
                <Field>
                  <FieldLabel htmlFor='hosting-note'>{t('Note')}</FieldLabel>
                  <Input
                    id='hosting-note'
                    maxLength={255}
                    {...form.register('note')}
                  />
                </Field>
                <Field>
                  <FieldLabel htmlFor='hosting-start'>
                    {t('Starts at')}
                  </FieldLabel>
                  <Input
                    id='hosting-start'
                    type='datetime-local'
                    {...form.register('start')}
                  />
                  <FieldError>
                    {form.formState.errors.start?.message &&
                      t(form.formState.errors.start.message)}
                  </FieldError>
                </Field>
                <Field>
                  <FieldLabel htmlFor='hosting-end'>{t('Ends at')}</FieldLabel>
                  <Input
                    id='hosting-end'
                    type='datetime-local'
                    {...form.register('end')}
                  />
                  <FieldError>
                    {form.formState.errors.end?.message &&
                      t(form.formState.errors.end.message)}
                  </FieldError>
                </Field>
                <Field>
                  <FieldLabel htmlFor='hosting-retention'>
                    {t('Credential retention after expiry (days)')}
                  </FieldLabel>
                  <Input
                    id='hosting-retention'
                    type='number'
                    min={0}
                    max={30}
                    {...form.register('retention_days', {
                      valueAsNumber: true,
                    })}
                  />
                  <FieldError>
                    {form.formState.errors.retention_days?.message &&
                      t('Enter a value from 0 to 30.')}
                  </FieldError>
                </Field>
                <Field>
                  <FieldLabel htmlFor='hosting-enabled'>
                    {t('Enabled')}
                  </FieldLabel>
                  <Switch
                    id='hosting-enabled'
                    checked={form.watch('enabled')}
                    onCheckedChange={(value) => form.setValue('enabled', value)}
                  />
                </Field>
                <p className='text-muted-foreground text-sm sm:col-span-2'>
                  {t('Times are shown in your device time zone: {{zone}}.', {
                    zone: Intl.DateTimeFormat().resolvedOptions().timeZone,
                  })}{' '}
                  {t(
                    'Disabling a seat blocks access immediately. Scheduled cleanup uses its end date and retention period.'
                  )}
                </p>
                <Button
                  className='justify-self-start'
                  type='submit'
                  disabled={save.isPending || !user.data?.success}
                >
                  {save.isPending ? t('Processing...') : t('Save entitlement')}
                </Button>
              </form>
            </CardContent>
          </Card>
          {query.isPending && <LoadingState />}
          {query.isError && (
            <ErrorState
              description={t('Unable to load hosting status.')}
              onRetry={() => query.refetch()}
            />
          )}
          {query.data && (
            <>
              <Card>
                <CardHeader>
                  <CardTitle>{t('Hosting entitlements')}</CardTitle>
                </CardHeader>
                <CardContent className='space-y-3'>
                  {!query.data.grants.length && (
                    <EmptyState title={t('No hosting entitlement yet')} />
                  )}
                  {query.data.grants.map((grant) => (
                    <div
                      key={grant.user_id}
                      className='flex flex-wrap items-center justify-between gap-3 rounded-lg border p-3 text-sm'
                    >
                      <div>
                        <p className='font-medium'>
                          {t('User ID')} {grant.user_id} ·{' '}
                          {grant.enabled ? t('Enabled') : t('Disabled')}
                        </p>
                        <p className='text-muted-foreground'>
                          {new Date(grant.starts_at * 1000).toLocaleString(
                            locale
                          )}{' '}
                          —{' '}
                          {new Date(grant.ends_at * 1000).toLocaleString(
                            locale
                          )}
                        </p>
                      </div>
                      <Button
                        size='sm'
                        variant='outline'
                        onClick={() => editGrant(grant)}
                      >
                        {t('Edit or renew')}
                      </Button>
                    </div>
                  ))}
                </CardContent>
              </Card>
              <Card>
                <CardHeader>
                  <CardTitle>{t('Prepared slot inventory')}</CardTitle>
                  <CardDescription>
                    {t(
                      'Only enable empty local GPT slots with a working outbound connection. Disabling inventory stops new assignments and keeps current users connected.'
                    )}
                  </CardDescription>
                </CardHeader>
                <CardContent className='space-y-5'>
                  {query.data.instances.map((instance) => (
                    <section key={instance.id} className='space-y-3'>
                      <h3 className='font-medium'>
                        {instance.name}{' '}
                        <Badge variant='outline'>
                          {instance.available
                            ? t('Available')
                            : t('Unavailable')}
                        </Badge>
                      </h3>
                      {instance.available && !instance.slots.length && (
                        <p className='text-muted-foreground text-sm'>
                          {t('No prebuilt slots on this instance.')}
                        </p>
                      )}
                      {instance.slots.map((slot) => (
                        <div
                          key={slot.id}
                          className='flex flex-wrap items-center justify-between gap-3 rounded-lg border p-3 text-sm'
                        >
                          <div>
                            <p className='font-medium'>
                              {slot.name || slot.id}
                            </p>
                            <p className='text-muted-foreground'>
                              {slot.empty
                                ? t('Empty slot')
                                : t('Occupied or not verified empty')}{' '}
                              ·{' '}
                              {slot.exit_ready
                                ? t('Outbound configured')
                                : t('Outbound unavailable')}
                            </p>
                          </div>
                          <Button
                            size='sm'
                            variant='outline'
                            disabled={
                              inventory.isPending ||
                              !instance.available ||
                              (!slot.enabled &&
                                (!slot.empty || !slot.exit_ready))
                            }
                            onClick={() =>
                              inventory.mutate({
                                instance_id: instance.id,
                                slot_id: slot.id,
                                enabled: !slot.enabled,
                              })
                            }
                          >
                            {slot.enabled
                              ? t('Stop new assignments')
                              : t('Allow self-service assignment')}
                          </Button>
                        </div>
                      ))}
                    </section>
                  ))}
                </CardContent>
              </Card>
            </>
          )}
        </div>
        <SecureVerificationDialog {...verification.dialogProps} />
      </SectionPageLayout.Content>
    </SectionPageLayout>
  )
}
