import { z } from 'zod'

export const REGISTRATION_ABUSE_DEFAULTS = {
  code_ttl_minutes: 10,
  resend_seconds: 60,
  code_max_attempts: 5,
  email_daily: 5,
  ip_email_hourly: 10,
  ip_email_daily: 30,
  registration_ip_hourly: 20,
  registration_ip_daily: 3,
  email_global_daily: 80,
  email_global_monthly: 2500,
}

/** Keep client ranges aligned with server validation; the server remains authoritative. */
function limit(min: number, max: number): z.ZodNumber {
  return z
    .number()
    .int('Enter a whole number')
    .min(min, 'Value is below the allowed range')
    .max(max, 'Value exceeds the allowed range')
}

export const registrationAbuseSchema = z
  .object({
    code_ttl_minutes: limit(1, 30),
    resend_seconds: limit(30, 600),
    code_max_attempts: limit(1, 10),
    email_daily: limit(1, 1000),
    ip_email_hourly: limit(1, 10000),
    ip_email_daily: limit(1, 100000),
    registration_ip_hourly: limit(1, 10000),
    registration_ip_daily: limit(1, 10000),
    email_global_daily: limit(1, 1000000),
    email_global_monthly: limit(1, 31000000),
  })
  .superRefine((value, ctx) => {
    const pairs: [keyof typeof value, number][] = [
      ['resend_seconds', value.code_ttl_minutes * 60],
      ['ip_email_hourly', value.ip_email_daily],
      ['ip_email_daily', value.email_global_daily],
      ['email_global_daily', value.email_global_monthly],
    ]
    for (const [key, maximum] of pairs) {
      if (value[key] > maximum) {
        ctx.addIssue({
          code: 'custom',
          path: [key],
          message:
            'Check the resend interval and hourly, daily, and monthly email limits',
        })
      }
    }
  })

export type RegistrationAbuseValues = z.infer<typeof registrationAbuseSchema>

/** Reject malformed stored settings instead of silently replacing them on save. */
export function parseRegistrationAbusePolicy(
  value: string
): RegistrationAbuseValues | undefined {
  try {
    const result = registrationAbuseSchema.safeParse(JSON.parse(value))
    return result.success ? result.data : undefined
  } catch {
    return undefined
  }
}

export const REGISTRATION_ABUSE_GROUPS = [
  {
    title: 'Registration verification codes',
    description:
      'Codes are single-use. Changes to validity apply to newly sent codes. Resending keeps failed attempts; increasing the attempt limit does not unlock an existing code.',
    fields: [
      {
        name: 'code_ttl_minutes',
        label: 'Code validity (minutes)',
        min: 1,
        max: 30,
      },
      {
        name: 'resend_seconds',
        label: 'Resend interval (seconds)',
        min: 30,
        max: 600,
      },
      {
        name: 'code_max_attempts',
        label: 'Maximum incorrect code attempts',
        min: 1,
        max: 10,
      },
    ],
  },
  {
    title: 'Anonymous email limits',
    description:
      'Registration and password reset share these budgets. Sending failures also count. Limits use UTC calendar hours, days, and months; IPv6 addresses in the same /64 share an IP budget.',
    fields: [
      {
        name: 'email_daily',
        label: 'Emails per address per day',
        min: 1,
        max: 1000,
      },
      {
        name: 'ip_email_hourly',
        label: 'Emails per IP per hour',
        min: 1,
        max: 10000,
      },
      {
        name: 'ip_email_daily',
        label: 'Emails per IP per day',
        min: 1,
        max: 100000,
      },
      {
        name: 'email_global_daily',
        label: 'Site email budget per day',
        min: 1,
        max: 1000000,
      },
      {
        name: 'email_global_monthly',
        label: 'Site email budget per month',
        min: 1,
        max: 31000000,
      },
    ],
  },
  {
    title: 'Registration limits',
    description:
      'Invalid forms and incorrect codes count as attempts. The daily limit counts successful registrations only.',
    fields: [
      {
        name: 'registration_ip_hourly',
        label: 'Registration attempts per IP per hour',
        min: 1,
        max: 10000,
      },
      {
        name: 'registration_ip_daily',
        label: 'Successful registrations per IP per day',
        min: 1,
        max: 10000,
      },
    ],
  },
] as const
