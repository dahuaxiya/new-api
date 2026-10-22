import { useTranslation } from 'react-i18next'

import { Progress } from '@/components/ui/progress'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber } from '@/lib/format'

import type { QuotaWindow } from './schema'

/** Render an official window; expired observations never imply a fresh full quota. */
export function WindowReading(props: { window: QuotaWindow; now: number }) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const window = props.window
  const expired =
    window.reset_at !== null && Date.parse(window.reset_at) <= props.now
  const known = window.used_percent !== null && !expired
  const label =
    window.id === '5h' ? t('GPT 5-hour window') : t('GPT 7-day window')

  return (
    <section
      className='bg-muted/30 min-w-0 rounded-xl border p-4'
      aria-label={label}
    >
      <h3 className='mb-2 text-sm font-medium'>{label}</h3>
      {known ? (
        <>
          <p className='mb-2 text-2xl font-semibold tabular-nums'>
            {t('GPT remaining {{percent}}%', {
              percent: formatNumber(window.remaining_percent, locale),
            })}
          </p>
          <Progress
            value={window.used_percent}
            aria-label={t('GPT used {{percent}}%', {
              percent: formatNumber(window.used_percent, locale),
            })}
          />
          <p className='text-muted-foreground mt-2 text-xs'>
            {t('GPT used {{percent}}%', {
              percent: formatNumber(window.used_percent, locale),
            })}
          </p>
        </>
      ) : (
        <p className='py-2 text-base font-medium'>
          {expired
            ? t('GPT window expired; awaiting a fresh reading')
            : t('GPT upstream has not returned this window')}
        </p>
      )}
      <p className='text-muted-foreground mt-3 text-xs leading-relaxed'>
        {window.reset_at
          ? t('GPT resets at {{time}}', {
              time: new Date(window.reset_at).toLocaleString(locale, {
                timeZoneName: 'short',
              }),
            })
          : t('GPT reset time unavailable')}
      </p>
    </section>
  )
}
