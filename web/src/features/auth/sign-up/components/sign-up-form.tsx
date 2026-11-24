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
import { REGEXP_ONLY_DIGITS } from 'input-otp'
import { ArrowLeft, ArrowRight, Check, Loader2, Mail } from 'lucide-react'
import {
  useEffect,
  useMemo,
  useRef,
  useState,
  type FormEvent,
  type ReactNode,
} from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import type { z } from 'zod'

import { Dialog } from '@/components/dialog'
import { PasswordInput } from '@/components/password-input'
import { Turnstile } from '@/components/turnstile'
import { Button } from '@/components/ui/button'
import {
  Form,
  FormControl,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import {
  InputOTP,
  InputOTPGroup,
  InputOTPSlot,
} from '@/components/ui/input-otp'
import { Label } from '@/components/ui/label'
import { register, wechatLoginByCode } from '@/features/auth/api'
import { LegalConsent } from '@/features/auth/components/legal-consent'
import { OAuthProviders } from '@/features/auth/components/oauth-providers'
import { OTP_REGEX, registerFormSchema } from '@/features/auth/constants'
import { useAuthRedirect } from '@/features/auth/hooks/use-auth-redirect'
import { useEmailVerification } from '@/features/auth/hooks/use-email-verification'
import { useTurnstile } from '@/features/auth/hooks/use-turnstile'
import { isIssuerFrame, retailOrigin } from '@/features/auth/lib/embedded-login'
import {
  getAffiliateCode,
  saveAffiliateCode,
} from '@/features/auth/lib/storage'
import { useStatus } from '@/hooks/use-status'
import { handleServerError } from '@/lib/handle-server-error'
import { AuthOperationError } from '@/lib/secure-verification'
import { createServerError } from '@/lib/server-error-message'
import { cn } from '@/lib/utils'

import { EmailAutocomplete } from './email-autocomplete'

type SignUpStep = 'email' | 'verification' | 'account'

/**
 * 将注册拆成邮箱、验证码和账户资料三步，首屏只要求邮箱。三个步骤共用
 * 同一份表单状态，验证码发送成功后不会因切换界面而丢失输入。
 * @param props 外部表单类名和原生表单属性。
 * @return 支持邮箱建议、六位验证码和最终账户信息提交的注册表单。
 */
export function SignUpForm({
  className,
  ...props
}: React.HTMLAttributes<HTMLFormElement>) {
  const { t } = useTranslation()
  const [step, setStep] = useState<SignUpStep>('email')
  const [isLoading, setIsLoading] = useState(false)
  const [verificationEmail, setVerificationEmail] = useState('')
  const [verificationCode, setVerificationCode] = useState('')
  const [verificationError, setVerificationError] = useState('')
  const [agreedToLegal, setAgreedToLegal] = useState(false)
  const [wechatCode, setWeChatCode] = useState('')
  const [isWeChatDialogOpen, setIsWeChatDialogOpen] = useState(false)
  const [isWeChatSubmitting, setIsWeChatSubmitting] = useState(false)
  const [turnstileWidgetKey, setTurnstileWidgetKey] = useState(0)
  const otpInputRef = useRef<HTMLInputElement>(null)
  const legalConsentErrorMessage = t('Please agree to the legal terms first')

  const { status, loading: isStatusLoading } = useStatus()
  const {
    isTurnstileEnabled,
    turnstileSiteKey,
    turnstileToken,
    setTurnstileToken,
    validateTurnstile,
  } = useTurnstile()
  const { redirectToLogin, handleLoginResult } = useAuthRedirect()
  const {
    isSending: isSendingCode,
    secondsLeft,
    isActive,
    sendCode,
  } = useEmailVerification({
    turnstileToken,
    validateTurnstile,
  })

  const form = useForm<z.infer<typeof registerFormSchema>>({
    resolver: zodResolver(registerFormSchema),
    defaultValues: {
      username: '',
      email: '',
      password: '',
      confirmPassword: '',
    },
  })

  const emailVerificationRequired = Boolean(status?.email_verification)
  const hasUserAgreement = Boolean(status?.user_agreement_enabled)
  const hasPrivacyPolicy = Boolean(status?.privacy_policy_enabled)
  const requiresLegalConsent = hasUserAgreement || hasPrivacyPolicy
  const oauthRegisterEnabled =
    status?.oauth_register_enabled ??
    status?.data?.oauth_register_enabled ??
    true
  const hasWeChatLogin = Boolean(status?.wechat_login)
  const turnstileReady = !isTurnstileEnabled || Boolean(turnstileToken)
  const resolvedStep =
    !isStatusLoading && status && !emailVerificationRequired ? 'account' : step
  const accountStepVisible = resolvedStep === 'account'

  const wechatQrCodeUrl = useMemo(() => {
    return (
      status?.wechat_qrcode ||
      status?.wechat_qr_code ||
      status?.wechat_qrcode_image_url ||
      status?.wechat_qr_code_image_url ||
      status?.wechat_account_qrcode_image_url ||
      status?.WeChatAccountQRCodeImageURL ||
      status?.data?.wechat_qrcode ||
      status?.data?.WeChatAccountQRCodeImageURL ||
      ''
    )
  }, [status])

  useEffect(() => {
    const affiliateCode = new URLSearchParams(window.location.search)
      .get('aff')
      ?.trim()
    if (affiliateCode) saveAffiliateCode(affiliateCode)
  }, [])

  useEffect(() => {
    if (resolvedStep === 'verification') otpInputRef.current?.focus()
  }, [resolvedStep])

  async function requestVerificationCode(email: string): Promise<boolean> {
    if (!(await sendCode(email))) return false

    // Turnstile 令牌由本次发码挑战消费，下一阶段必须重新完成人机校验。
    setVerificationCode('')
    setVerificationError('')
    setTurnstileToken('')
    setTurnstileWidgetKey((current) => current + 1)
    return true
  }

  async function startEmailVerification() {
    if (!(await form.trigger('email', { shouldFocus: true }))) return

    const rawEmail = (form.getValues('email') ?? '').trim()
    const atIndex = rawEmail.indexOf('@')
    const normalizedEmail =
      atIndex < 0
        ? rawEmail
        : `${rawEmail.slice(0, atIndex)}@${rawEmail.slice(atIndex + 1).toLowerCase()}`
    form.setValue('email', normalizedEmail, {
      shouldDirty: true,
      shouldValidate: true,
    })

    if (!(await requestVerificationCode(normalizedEmail))) return
    setVerificationEmail(normalizedEmail)
    setStep('verification')
  }

  function completeEmailVerification(code = verificationCode) {
    if (!OTP_REGEX.test(code)) {
      setVerificationError(t('Verification code must be 6 digits'))
      otpInputRef.current?.focus()
      return
    }
    setVerificationCode(code)
    setVerificationError('')
    setStep('account')
  }

  async function handleFormSubmit(
    event: FormEvent<HTMLFormElement>
  ): Promise<void> {
    event.preventDefault()

    if (emailVerificationRequired && resolvedStep === 'email') {
      await startEmailVerification()
      return
    }

    if (emailVerificationRequired && resolvedStep === 'verification') {
      completeEmailVerification()
      return
    }

    await form.handleSubmit(onSubmit)(event)
  }

  /** 完成统一账号注册后，在承载注册流程的站点打开登录页。 */
  async function onSubmit(data: z.infer<typeof registerFormSchema>) {
    if (requiresLegalConsent && !agreedToLegal) {
      toast.error(legalConsentErrorMessage)
      return
    }

    if (emailVerificationRequired) {
      if (!verificationEmail) {
        toast.error(t('Please enter your email'))
        setStep('email')
        return
      }
      if (!OTP_REGEX.test(verificationCode)) {
        toast.error(t('Please enter the verification code'))
        setStep('verification')
        return
      }
    }

    if (!validateTurnstile()) return

    setIsLoading(true)
    try {
      const response = await register({
        username: data.username,
        password: data.password,
        email: emailVerificationRequired
          ? verificationEmail
          : data.email || undefined,
        verification_code: emailVerificationRequired
          ? verificationCode
          : undefined,
        aff_code: getAffiliateCode(),
        turnstile: turnstileToken,
      })

      if (response?.success) {
        toast.success(t('Account created! Please sign in'))
        if (isIssuerFrame()) {
          // Registration returns no retail session. Start a fresh login in the outer page.
          window.parent.postMessage(
            { type: 'luobo.form.sign-in' },
            retailOrigin
          )
        } else {
          redirectToLogin()
        }
      } else {
        handleServerError(
          createServerError(response, t('Failed to create account'))
        )
      }
    } catch (error) {
      handleServerError(
        AuthOperationError.from(error, t('Failed to create account'))
      )
    } finally {
      setIsLoading(false)
    }
  }

  async function resendVerificationCode() {
    if (!verificationEmail) {
      setStep('email')
      return
    }
    if (await requestVerificationCode(verificationEmail)) {
      setStep('verification')
    }
  }

  function changeEmail() {
    setVerificationCode('')
    setVerificationError('')
    setStep('email')
  }

  const handleOpenWeChatDialog = () => {
    if (requiresLegalConsent && !agreedToLegal) {
      toast.error(legalConsentErrorMessage)
      return
    }
    setIsWeChatDialogOpen(true)
  }

  const handleWeChatDialogChange = (open: boolean) => {
    setIsWeChatDialogOpen(open)
    if (!open) {
      setWeChatCode('')
      setIsWeChatSubmitting(false)
    }
  }

  async function handleWeChatLogin() {
    if (!wechatCode.trim()) {
      toast.error(t('Please enter the verification code'))
      return
    }

    setIsWeChatSubmitting(true)
    try {
      const response = await wechatLoginByCode(wechatCode)
      if (response?.success) {
        handleWeChatDialogChange(false)
        if (await handleLoginResult(response.data)) {
          toast.success(t('Signed in via WeChat'))
        }
      } else {
        handleServerError(createServerError(response, t('Login failed')))
      }
    } catch (error: unknown) {
      handleServerError(
        new AuthOperationError(t('Login failed'), undefined, { cause: error })
      )
    } finally {
      setIsWeChatSubmitting(false)
    }
  }

  let verificationCodeAction: ReactNode = t('Send code')
  if (isActive) {
    verificationCodeAction = t('Resend ({{seconds}}s)', {
      seconds: secondsLeft,
    })
  } else if (isSendingCode) {
    verificationCodeAction = <Loader2 className='h-4 w-4 animate-spin' />
  }

  const submitDisabled =
    isLoading ||
    (accountStepVisible &&
      ((requiresLegalConsent && !agreedToLegal) || !turnstileReady))
  let primaryActionDisabled = submitDisabled
  if (resolvedStep === 'email') {
    primaryActionDisabled = isSendingCode || !turnstileReady
  } else if (resolvedStep === 'verification') {
    primaryActionDisabled = verificationCode.length !== 6
  }

  return (
    <Form {...form}>
      <form
        onSubmit={(event) => void handleFormSubmit(event)}
        className={cn('grid gap-5', className)}
        noValidate
        {...props}
      >
        <div key={resolvedStep} className='luobo-signup-step grid gap-5'>
          {resolvedStep === 'email' && emailVerificationRequired && (
            <>
              <div className='space-y-2'>
                <p className='text-muted-foreground text-sm leading-6'>
                  {t(
                    "We'll email you a 6-digit code to verify this address. You'll set your username and password next."
                  )}
                </p>
              </div>

              <FormField
                control={form.control}
                name='email'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Email')}</FormLabel>
                    <FormControl>
                      <EmailAutocomplete
                        id={field.name}
                        value={field.value ?? ''}
                        onValueChange={field.onChange}
                        onBlur={field.onBlur}
                        disabled={isSendingCode}
                        invalid={Boolean(form.formState.errors.email)}
                        aria-describedby={`${field.name}-hint`}
                        autoFocus
                      />
                    </FormControl>
                    <p
                      id={`${field.name}-hint`}
                      className='text-muted-foreground text-xs leading-5'
                    >
                      {t('Suggestions appear as you type after the @ symbol.')}
                    </p>
                    <FormMessage />
                  </FormItem>
                )}
              />

              <Button
                type='submit'
                className='luobo-primary h-12 w-full justify-center rounded-xl text-base'
                disabled={primaryActionDisabled}
              >
                {isSendingCode ? (
                  <Loader2 className='size-4 animate-spin' />
                ) : (
                  <Mail className='size-4' />
                )}
                {t('Send verification code')}
                {!isSendingCode && <ArrowRight className='size-4' />}
              </Button>
            </>
          )}

          {resolvedStep === 'verification' && emailVerificationRequired && (
            <>
              <div className='space-y-2'>
                <div className='bg-primary/8 text-primary flex size-10 items-center justify-center rounded-full'>
                  <Mail className='size-5' aria-hidden='true' />
                </div>
                <p className='text-muted-foreground text-sm leading-6'>
                  {t('Enter the 6-digit code sent to')}{' '}
                  <strong className='text-foreground font-medium break-all'>
                    {verificationEmail}
                  </strong>
                </p>
              </div>

              <div className='grid gap-2'>
                <Label htmlFor='registration-code'>
                  {t('Verification code')}
                </Label>
                <InputOTP
                  ref={otpInputRef}
                  id='registration-code'
                  value={verificationCode}
                  onChange={(value) => {
                    setVerificationCode(value)
                    setVerificationError('')
                  }}
                  onComplete={(code) => completeEmailVerification(code)}
                  maxLength={6}
                  pattern={REGEXP_ONLY_DIGITS}
                  inputMode='numeric'
                  autoComplete='one-time-code'
                  aria-label={t('6-digit email verification code')}
                  aria-invalid={verificationError ? true : undefined}
                  aria-describedby='registration-code-help'
                  containerClassName='w-full'
                >
                  <InputOTPGroup className='grid w-full grid-cols-6 gap-2'>
                    {Array.from({ length: 6 }, (_, index) => (
                      <InputOTPSlot
                        key={index}
                        index={index}
                        className='h-12 w-full rounded-xl border text-lg font-medium sm:h-13'
                      />
                    ))}
                  </InputOTPGroup>
                </InputOTP>
                <p
                  id='registration-code-help'
                  className={cn(
                    'text-xs leading-5',
                    verificationError
                      ? 'text-destructive'
                      : 'text-muted-foreground'
                  )}
                  role={verificationError ? 'alert' : 'status'}
                >
                  {verificationError ||
                    t(
                      'The form continues automatically after the sixth digit.'
                    )}
                </p>
              </div>

              <Button
                type='submit'
                className='luobo-primary h-12 w-full justify-center rounded-xl text-base'
                disabled={primaryActionDisabled}
              >
                {t('Continue')}
                <ArrowRight className='size-4' />
              </Button>

              <div className='flex flex-wrap items-center justify-between gap-2 text-sm'>
                <Button
                  type='button'
                  variant='ghost'
                  className='h-9 px-2'
                  onClick={changeEmail}
                >
                  <ArrowLeft className='size-4' />
                  {t('Change email')}
                </Button>
                <Button
                  type='button'
                  variant='ghost'
                  className='h-9 px-2'
                  disabled={isSendingCode || isActive || !turnstileReady}
                  onClick={() => void resendVerificationCode()}
                >
                  {verificationCodeAction}
                </Button>
              </div>
            </>
          )}

          {resolvedStep === 'account' && (
            <>
              {emailVerificationRequired && verificationEmail && (
                <div className='border-border/70 bg-muted/35 flex items-center gap-3 rounded-xl border px-3.5 py-3'>
                  <span className='bg-primary/10 text-primary flex size-8 shrink-0 items-center justify-center rounded-full'>
                    <Check className='size-4' aria-hidden='true' />
                  </span>
                  <span className='min-w-0 flex-1'>
                    <span className='text-muted-foreground block text-xs'>
                      {t('Verified email')}
                    </span>
                    <strong className='block truncate text-sm font-medium'>
                      {verificationEmail}
                    </strong>
                  </span>
                  <Button
                    type='button'
                    variant='ghost'
                    size='sm'
                    className='shrink-0'
                    onClick={changeEmail}
                  >
                    {t('Change')}
                  </Button>
                </div>
              )}

              <p className='text-muted-foreground text-sm leading-6'>
                {t('Choose the credentials you will use to sign in.')}
              </p>

              <FormField
                control={form.control}
                name='username'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Username')}</FormLabel>
                    <FormControl>
                      <Input
                        placeholder={t('Enter your username')}
                        autoComplete='username'
                        className='h-12 rounded-xl px-4'
                        {...field}
                      />
                    </FormControl>
                    <FormMessage />
                  </FormItem>
                )}
              />

              <FormField
                control={form.control}
                name='password'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Password')}</FormLabel>
                    <FormControl>
                      <PasswordInput
                        placeholder={t('Enter password (15–128 characters)')}
                        autoComplete='new-password'
                        className='[&_input]:h-12 [&_input]:rounded-xl [&_input]:px-4'
                        {...field}
                      />
                    </FormControl>
                    <FormMessage />
                  </FormItem>
                )}
              />

              <FormField
                control={form.control}
                name='confirmPassword'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Confirm password')}</FormLabel>
                    <FormControl>
                      <PasswordInput
                        placeholder={t('Confirm password')}
                        autoComplete='new-password'
                        className='[&_input]:h-12 [&_input]:rounded-xl [&_input]:px-4'
                        {...field}
                      />
                    </FormControl>
                    <FormMessage />
                  </FormItem>
                )}
              />

              <LegalConsent
                status={status}
                checked={agreedToLegal}
                onCheckedChange={setAgreedToLegal}
              />

              <Button
                type='submit'
                className='luobo-primary h-12 w-full justify-center rounded-xl text-base'
                disabled={primaryActionDisabled}
              >
                {isLoading ? <Loader2 className='size-4 animate-spin' /> : null}
                {t('Create account')}
              </Button>
            </>
          )}
        </div>

        {!accountStepVisible && emailVerificationRequired && (
          <p className='text-muted-foreground text-center text-xs leading-5'>
            {t(
              'Your address is only used for verification and account recovery.'
            )}
          </p>
        )}

        {isTurnstileEnabled && (
          <div className='border-border/60 bg-muted/20 flex min-h-16 items-center justify-center rounded-xl border px-3 py-2'>
            <Turnstile
              key={turnstileWidgetKey}
              siteKey={turnstileSiteKey}
              onVerify={setTurnstileToken}
              onExpire={() => setTurnstileToken('')}
            />
          </div>
        )}

        {oauthRegisterEnabled && resolvedStep !== 'verification' && (
          <OAuthProviders
            status={status}
            disabled={isLoading || (requiresLegalConsent && !agreedToLegal)}
            onWeChatLogin={hasWeChatLogin ? handleOpenWeChatDialog : undefined}
            isWeChatLoading={isWeChatSubmitting}
          />
        )}
      </form>

      {hasWeChatLogin && (
        <Dialog
          open={isWeChatDialogOpen}
          onOpenChange={handleWeChatDialogChange}
          title={t('WeChat sign in')}
          description={t(
            'Scan the QR code to follow the official account and reply with “验证码” to receive your verification code.'
          )}
          contentClassName='max-w-sm'
          headerClassName='text-left'
          contentHeight='auto'
          bodyClassName='space-y-4'
          footer={
            <>
              <Button
                type='button'
                variant='outline'
                onClick={() => handleWeChatDialogChange(false)}
                disabled={isWeChatSubmitting}
              >
                {t('Cancel')}
              </Button>
              <Button
                type='button'
                onClick={() => void handleWeChatLogin()}
                disabled={
                  isWeChatSubmitting ||
                  !wechatCode.trim() ||
                  (requiresLegalConsent && !agreedToLegal)
                }
                className='gap-2'
              >
                {isWeChatSubmitting ? (
                  <Loader2 className='h-4 w-4 animate-spin' />
                ) : null}
                {t('Confirm')}
              </Button>
            </>
          }
        >
          {wechatQrCodeUrl ? (
            <div className='flex justify-center'>
              <img
                src={wechatQrCodeUrl}
                alt={t('WeChat login QR code')}
                className='h-40 w-40 rounded-md border object-contain'
              />
            </div>
          ) : (
            <p className='text-muted-foreground text-sm'>
              {t('QR code is not configured. Please contact support.')}
            </p>
          )}
          <div className='grid gap-2'>
            <Label htmlFor='wechat-code'>{t('Verification code')}</Label>
            <Input
              id='wechat-code'
              placeholder={t('Enter the verification code')}
              value={wechatCode}
              onChange={(event) => setWeChatCode(event.target.value)}
              autoComplete='one-time-code'
            />
          </div>
        </Dialog>
      )}
    </Form>
  )
}
