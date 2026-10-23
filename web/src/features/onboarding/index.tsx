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
import { Link, useNavigate } from '@tanstack/react-router'
import { useEffect, useRef, useState } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { EmptyState } from '@/components/empty-state'
import { ErrorState } from '@/components/error-state'
import { SectionPageLayout } from '@/components/layout'
import { LoadingState } from '@/components/loading-state'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { Form } from '@/components/ui/form'
import { docsApiBase } from '@/features/user-docs/config'
import { parseQuotaFromDollars } from '@/lib/format'
import { handleServerError } from '@/lib/handle-server-error'
import { ROLE } from '@/lib/roles'
import {
  getServerErrorMessage,
  getServerErrorStatus,
} from '@/lib/server-error-message'
import { useAuthStore } from '@/stores/auth-store'

import {
  getOnboardingOptions,
  getOnboardingResult,
  openAccount,
  retryOnboarding,
} from './api'
import { AccountSummary } from './components/account-summary'
import { SlotSelector } from './components/slot-selector'
import { SlotSetup } from './components/slot-setup'
import { OnboardingUserFields } from './components/user-fields'
import {
  ONBOARDING_DEFAULTS,
  onboardingSchema,
  type OnboardingFormValues,
} from './lib/schema'
import type {
  OnboardingOptions,
  OnboardingRequest,
  OnboardingResult,
  OnboardingSlot,
} from './types'

/**
 * 管理员开户工作流：只把操作编号写入地址，刷新后可恢复状态，密码留在本次表单内存。
 * @param props 地址中的非敏感操作编号，可为空以开始新开户。
 * @return 管理员开户页面。
 */
export function Onboarding(props: { requestId?: string }): React.JSX.Element {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const role = useAuthStore((state) => state.auth.user?.role)
  const authorized = role === ROLE.SUPER_ADMIN
  const [confirmOpen, setConfirmOpen] = useState(false)
  const [requestId, setRequestId] = useState(props.requestId)
  const [result, setResult] = useState<OnboardingResult>()
  const [failure, setFailure] = useState('')
  const [requestMissing, setRequestMissing] = useState(false)
  // 不将密码放入 React Query mutation variables，以免成功后继续留在缓存里。
  const submission = useRef<OnboardingRequest | undefined>(undefined)
  useEffect(
    () => () => {
      submission.current = undefined
    },
    []
  )
  const form = useForm<OnboardingFormValues>({
    resolver: zodResolver(onboardingSchema),
    defaultValues: ONBOARDING_DEFAULTS,
  })
  const options = useQuery({
    queryKey: ['vm2api', 'onboarding', 'options'],
    queryFn: getOnboardingOptions,
    enabled: authorized,
  })
  const recovery = useQuery({
    queryKey: ['vm2api', 'onboarding', props.requestId],
    queryFn: () => getOnboardingResult(props.requestId ?? ''),
    enabled: authorized && !!props.requestId && !submission.current && !result,
    retry: false,
  })
  const currentResult = result ?? recovery.data
  const instances = options.data?.instances ?? []
  const selectedId = form.watch('instance_id')
  const selectedSlotId = form.watch('slot_id')
  const selectedInstance = instances.find((item) => item.id === selectedId)
  const selectedSlot = selectedInstance?.slots.find(
    (slot) => slot.id === selectedSlotId
  )
  const apiBaseUrl = docsApiBase(
    options.data?.api_base_url,
    window.location.origin
  )
  const locked = !!requestId
  const recovering =
    !!props.requestId && !submission.current && recovery.isPending
  const mutation = useMutation({
    mutationFn: async () => {
      if (currentResult) return retryOnboarding(currentResult.request_id)
      if (!submission.current && requestId) return retryOnboarding(requestId)
      if (!submission.current) {
        throw new Error(t('No account opening request is available.'))
      }
      return openAccount(submission.current)
    },
    retry: false,
    gcTime: 0,
    meta: { errorToast: false },
    onSuccess: (data) => {
      setResult(data)
      setFailure('')
      // 服务端已保存密码哈希；重试通过操作编号恢复，及时清除明文。
      submission.current = undefined
      form.setValue('password', '')
      void queryClient.invalidateQueries({ queryKey: ['users'] })
      void queryClient.invalidateQueries({
        queryKey: ['vm2api', 'onboarding', 'options'],
      })
    },
    onMutate: () => {
      setRequestMissing(false)
    },
    onError: async (error) => {
      setFailure(getServerErrorMessage(error, t('Account opening failed')))
      handleServerError(error, t('Account opening failed'))
      const id = submission.current?.request_id ?? requestId
      if (!id) return
      try {
        // 网络失败不等于未创建；先查询原编号，确认服务端已有事务后只恢复该事务。
        const saved = await getOnboardingResult(id)
        setResult(saved)
        submission.current = undefined
        form.setValue('password', '')
      } catch (lookupError) {
        // 只有明确 404 才允许修改内容并换编号；查状态也超时时继续保留原请求。
        if (getServerErrorStatus(lookupError) === 404) setRequestMissing(true)
      }
    },
  })

  /**
   * 校验当前可用性后冻结摘要，提交前仍由服务端再次核对槽位归属。
   * @param values 已通过字段校验的用户资料和槽位选择。
   */
  function review(values: OnboardingFormValues): void {
    if (!selectedInstance?.available || !selectedSlot?.available) {
      form.setError('slot_id', {
        message: t('Select an available account slot'),
      })
      return
    }
    submission.current = {
      username: values.username,
      display_name: values.display_name,
      password: values.password,
      instance_id: values.instance_id,
      slot_id: values.slot_id,
      quota: parseQuotaFromDollars(values.quota_dollars),
      request_id: crypto.randomUUID(),
    }
    setConfirmOpen(true)
  }

  /** 仅第一次提交生成编号；失败后的按钮复用原事务，避免重复创建账号和渠道。 */
  function confirmSubmission(): void {
    if (!submission.current) return
    const id = submission.current.request_id
    setRequestId(id)
    setConfirmOpen(false)
    void navigate({
      to: '/users/onboarding',
      search: { request_id: id },
      replace: true,
    })
    mutation.mutate()
  }

  /**
   * 登录或创建返回真实槽位后同步选择列表，避免刷新未完成时误清除当前选择。
   * @param slot 服务端返回的槽位安全视图。
   */
  function updateSlot(slot: OnboardingSlot): void {
    queryClient.setQueryData<OnboardingOptions>(
      ['vm2api', 'onboarding', 'options'],
      (previous) => {
        if (!previous) return previous
        return {
          ...previous,
          instances: previous.instances.map((instance) => {
            if (instance.id !== selectedId) return instance
            const slots = instance.slots.filter((item) => item.id !== slot.id)
            return { ...instance, slots: [...slots, slot] }
          }),
        }
      }
    )
    // 异步结果可能晚于管理员切换实例，只更新原实例列表，不覆盖另一实例的当前选择。
    if (form.getValues('instance_id') === selectedId) {
      form.setValue('slot_id', slot.id, { shouldValidate: true })
    }
  }

  /** 仅在确认不存在旧事务或开户完成后开始下一单，显式清空旧编号和表单。 */
  function startNewOpening(): void {
    submission.current = undefined
    setResult(undefined)
    setRequestId(undefined)
    setFailure('')
    setRequestMissing(false)
    form.reset(ONBOARDING_DEFAULTS)
    void navigate({ to: '/users/onboarding', search: {}, replace: true })
  }

  /** 服务端确认旧编号不存在后才允许修改原表单，避免不确定结果下重复开户。 */
  function editMissingOpening(): void {
    submission.current = undefined
    setRequestId(undefined)
    setRequestMissing(false)
    setFailure('')
    void navigate({ to: '/users/onboarding', search: {}, replace: true })
  }

  if (!authorized) return <ErrorState title={t('Access Denied')} />

  return (
    <SectionPageLayout>
      <SectionPageLayout.Title>{t('Open account')}</SectionPageLayout.Title>
      <SectionPageLayout.Actions>
        <Button variant='outline' render={<Link to='/users' />}>
          {t('Back to users')}
        </Button>
      </SectionPageLayout.Actions>
      <SectionPageLayout.Content>
        <div className='mx-auto max-w-3xl space-y-5 pb-4'>
          {currentResult && (
            <Card>
              <CardHeader>
                <CardTitle>{t('Account opening result')}</CardTitle>
                <CardDescription>
                  {t('Request ID')}: {currentResult.request_id}
                </CardDescription>
              </CardHeader>
              <CardContent className='space-y-4'>
                <AccountSummary
                  username={currentResult.username}
                  instance={
                    instances.find(
                      (item) => item.id === currentResult.instance_id
                    )?.name || currentResult.instance_id
                  }
                  slot={currentResult.slot_id}
                  quota={currentResult.quota}
                  apiBaseUrl={apiBaseUrl}
                  result={currentResult}
                />
                {currentResult.status !== 'completed' && (
                  <Alert>
                    <AlertTitle>
                      {t('Account opening is not complete')}
                    </AlertTitle>
                    <AlertDescription>
                      {currentResult.message
                        ? t(currentResult.message)
                        : t(
                            'User access is enabled only after every setup step succeeds.'
                          )}
                    </AlertDescription>
                  </Alert>
                )}
                {currentResult.status === 'completed' && (
                  <Button onClick={startNewOpening}>
                    {t('Open another account')}
                  </Button>
                )}
                {currentResult.status !== 'completed' && (
                  <Button
                    onClick={() => mutation.mutate()}
                    disabled={mutation.isPending}
                  >
                    {t('Retry this request')}
                  </Button>
                )}
              </CardContent>
            </Card>
          )}
          {!currentResult && recovering && <LoadingState />}
          {!currentResult && !recovering && (
            <>
              {recovery.isError && (
                <ErrorState
                  description={getServerErrorMessage(recovery.error)}
                  onRetry={() => void recovery.refetch()}
                  action={
                    getServerErrorStatus(recovery.error) === 404 ? (
                      <Button onClick={startNewOpening}>
                        {t('Start a new account opening')}
                      </Button>
                    ) : undefined
                  }
                />
              )}
              {options.isPending && <LoadingState />}
              {options.isError && (
                <ErrorState
                  description={getServerErrorMessage(options.error)}
                  onRetry={() => void options.refetch()}
                />
              )}
              {options.data && !instances.length && (
                <EmptyState
                  title={t('No vm2api instances configured')}
                  description={t(
                    'Configure a vm2api instance before opening an account.'
                  )}
                />
              )}
              {!!instances.length && (
                <Card>
                  <CardHeader>
                    <CardTitle>{t('Create a dedicated account')}</CardTitle>
                    <CardDescription>
                      {t(
                        'Choose an account slot and create its user, channel and quota mapping together.'
                      )}
                    </CardDescription>
                  </CardHeader>
                  <CardContent>
                    <Form {...form}>
                      <form
                        onSubmit={form.handleSubmit(review)}
                        className='space-y-6'
                      >
                        <SlotSelector
                          form={form}
                          instances={instances}
                          disabled={locked}
                          refreshing={options.isFetching}
                          onRefresh={() => void options.refetch()}
                        />
                        {selectedInstance?.available && (
                          <SlotSetup
                            key={selectedInstance.id}
                            instance={selectedInstance}
                            slot={selectedSlot}
                            disabled={locked}
                            onSlotUpdated={updateSlot}
                          />
                        )}
                        <OnboardingUserFields form={form} disabled={locked} />
                        {!locked && (
                          <Button
                            type='submit'
                            disabled={!selectedSlot?.available}
                          >
                            {t('Review account')}
                          </Button>
                        )}
                        {locked && submission.current && (
                          <Button
                            type='button'
                            disabled={mutation.isPending}
                            onClick={() => mutation.mutate()}
                          >
                            {mutation.isPending
                              ? t('Processing...')
                              : t('Retry this request')}
                          </Button>
                        )}
                      </form>
                    </Form>
                  </CardContent>
                </Card>
              )}
            </>
          )}
          {requestMissing && (
            <Button
              variant='outline'
              onClick={editMissingOpening}
              disabled={mutation.isPending}
            >
              {t('Edit account details')}
            </Button>
          )}
          {failure && (
            <Alert variant='destructive'>
              <AlertTitle>{t('Account opening failed')}</AlertTitle>
              <AlertDescription>{failure}</AlertDescription>
            </Alert>
          )}
        </div>
        <ConfirmDialog
          open={confirmOpen}
          onOpenChange={setConfirmOpen}
          title={t('Confirm account opening')}
          desc={t(
            'This creates a normal user bound exclusively to the selected slot.'
          )}
          confirmText={t('Open account')}
          handleConfirm={confirmSubmission}
        >
          {submission.current && (
            <AccountSummary
              username={submission.current.username}
              instance={selectedInstance?.name || selectedId}
              slot={selectedSlot?.name || selectedSlotId}
              quota={submission.current.quota}
              apiBaseUrl={apiBaseUrl}
            />
          )}
        </ConfirmDialog>
      </SectionPageLayout.Content>
    </SectionPageLayout>
  )
}
