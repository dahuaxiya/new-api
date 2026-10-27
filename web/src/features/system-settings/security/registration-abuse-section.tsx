import { zodResolver } from '@hookform/resolvers/zod'
import { useEffect, useMemo } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import { Alert, AlertDescription } from '@/components/ui/alert'
import { FieldDescription, FieldLegend, FieldSet } from '@/components/ui/field'
import {
  Form,
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'

import {
  SettingsForm,
  SettingsFormGrid,
} from '../components/settings-form-layout'
import { SettingsPageFormActions } from '../components/settings-page-context'
import { SettingsSection } from '../components/settings-section'
import { useUpdateOption } from '../hooks/use-update-option'
import {
  parseRegistrationAbusePolicy,
  REGISTRATION_ABUSE_DEFAULTS,
  REGISTRATION_ABUSE_GROUPS,
  registrationAbuseSchema,
  type RegistrationAbuseValues,
} from './lib/registration-abuse-schema'

/** Edit and save the whole abuse policy through the root-only system options API. */
export function RegistrationAbuseSection(props: { value: string }) {
  const { t } = useTranslation()
  const saved = useMemo(
    () => parseRegistrationAbusePolicy(props.value),
    [props.value]
  )
  const updateOption = useUpdateOption()
  const form = useForm<RegistrationAbuseValues>({
    resolver: zodResolver(registrationAbuseSchema),
    mode: 'onChange',
    defaultValues: saved ?? REGISTRATION_ABUSE_DEFAULTS,
  })
  // Subscribe to both flags on every render; short-circuit reads can miss validity updates.
  const { isDirty, isValid } = form.formState
  useEffect(() => {
    if (saved) form.reset(saved)
  }, [saved, form])

  /** Persist one JSON option so a failed save cannot leave only half the limits applied. */
  const onSubmit = async (values: RegistrationAbuseValues): Promise<void> => {
    if (!saved) return
    try {
      await updateOption.mutateAsync({
        key: 'RegistrationAbusePolicy',
        value: JSON.stringify(values),
      })
      form.reset(values)
    } catch {
      // The shared mutation displays the server error. Keep edits visible for correction or retry.
    }
  }

  return (
    <SettingsSection title={t('Registration and email abuse protection')}>
      <Form {...form}>
        <SettingsForm onSubmit={form.handleSubmit(onSubmit)}>
          <SettingsPageFormActions
            onSave={form.handleSubmit(onSubmit)}
            isSaving={updateOption.isPending}
            isSaveDisabled={!saved || !isDirty || !isValid}
            saveLabel='Save registration and email limits'
          />
          <p className='text-muted-foreground text-sm'>
            {t(
              'Saved limits apply to subsequent requests without resetting existing counters. Email provider quotas still apply.'
            )}
          </p>
          {!saved && (
            <Alert variant='destructive'>
              <AlertDescription>
                {t('Invalid registration and email abuse settings')}
              </AlertDescription>
            </Alert>
          )}
          {REGISTRATION_ABUSE_GROUPS.map((group) => (
            <FieldSet
              key={group.title}
              disabled={!saved || updateOption.isPending}
            >
              <FieldLegend>{t(group.title)}</FieldLegend>
              <FieldDescription>{t(group.description)}</FieldDescription>
              <SettingsFormGrid>
                {group.fields.map((config) => (
                  <FormField
                    key={config.name}
                    control={form.control}
                    name={config.name}
                    render={({ field }) => (
                      <FormItem>
                        <FormLabel>{t(config.label)}</FormLabel>
                        <FormControl>
                          <Input
                            {...field}
                            type='number'
                            min={config.min}
                            max={config.max}
                            step={1}
                            value={Number.isNaN(field.value) ? '' : field.value}
                            onChange={(event) =>
                              field.onChange(event.target.valueAsNumber)
                            }
                          />
                        </FormControl>
                        <FormDescription>
                          {t('Allowed range: {{min}}–{{max}}', {
                            min: config.min,
                            max: config.max,
                          })}
                        </FormDescription>
                        <FormMessage />
                      </FormItem>
                    )}
                  />
                ))}
              </SettingsFormGrid>
            </FieldSet>
          ))}
        </SettingsForm>
      </Form>
    </SettingsSection>
  )
}
