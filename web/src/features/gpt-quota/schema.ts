import { z } from 'zod'

const windowSchema = z.object({
  id: z.enum(['5h', '7d']),
  used_percent: z.number().min(0).max(100).nullable(),
  remaining_percent: z.number().min(0).max(100).nullable(),
  reset_at: z.string().datetime({ offset: true }).nullable(),
})
export const quotaSchema = z.discriminatedUnion('bound', [
  z.object({ bound: z.literal(false) }),
  z.object({
    bound: z.literal(true),
    account_label: z.string(),
    plan: z.string().nullable(),
    windows: z.array(windowSchema).length(2),
    observed_at: z.string().datetime({ offset: true }).nullable(),
    last_checked_at: z.string().datetime({ offset: true }).nullable(),
    sync_status: z.enum(['ok', 'pending', 'error']),
    stale: z.boolean(),
    poll_seconds: z.number(),
  }),
])
export type QuotaWindow = z.infer<typeof windowSchema>
