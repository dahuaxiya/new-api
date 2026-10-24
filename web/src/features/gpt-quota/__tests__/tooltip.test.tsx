import { act, render, waitFor } from '@testing-library/react'
import { createInstance } from 'i18next'
import { I18nextProvider } from 'react-i18next'
import { afterEach, assert, expect, it, vi } from 'vitest'

import en from '@/i18n/locales/en.json'
import fr from '@/i18n/locales/fr.json'
import ja from '@/i18n/locales/ja.json'
import ru from '@/i18n/locales/ru.json'
import viLocale from '@/i18n/locales/vi.json'
import zhTW from '@/i18n/locales/zh-TW.json'
import zhCN from '@/i18n/locales/zh.json'

import type { QuotaChartPoint } from '../history'
import QuotaHistoryChart from '../QuotaHistoryChart'

type TooltipPattern = {
  title: { value: (row: QuotaChartPoint) => string }
  content: {
    key: (row: QuotaChartPoint) => string
    value: (row: QuotaChartPoint) => string
  }[]
}
type ChartSpec = {
  data: { values: QuotaChartPoint[] }[]
  tooltip: Partial<Record<'mark' | 'dimension', TooltipPattern>>
}
const captured = vi.hoisted(() => ({ spec: null as ChartSpec | null }))

// 只替代 jsdom 不支持的 Canvas 绘制边界，保留真实组件、分段和国际化。
vi.mock('@visactor/react-vchart', () => ({
  VChart: (props: { spec: ChartSpec }) => {
    captured.spec = props.spec
    return null
  },
}))
vi.mock('@visactor/vchart', () => ({
  ThemeManager: { setCurrentTheme: () => {} },
}))

afterEach(() => {
  captured.spec = null
})

const resources = { en, fr, ja, ru, vi: viLocale, zhCN, zhTW }
const point = {
  observed_at: 1792871008878,
  segment: 'account-a',
  remaining_5h: 0,
  remaining_7d: 57,
  reset_5h: null,
  reset_7d: null,
}

/** 验证 VChart 两种悬停入口的公共配置契约，防止默认维度提示泄露原始字段。 */
it.each(Object.keys(resources) as (keyof typeof resources)[])(
  'formats both point and time-axis tooltips in %s, including zero remaining quota',
  async (language) => {
    const i18n = createInstance()
    await i18n.init({ lng: language, fallbackLng: 'en', resources })
    render(
      <I18nextProvider i18n={i18n}>
        <QuotaHistoryChart points={[point]} />
      </I18nextProvider>
    )
    await waitFor(() => expect(captured.spec).not.toBeNull())
    assert(captured.spec)
    for (const activeType of ['mark', 'dimension'] as const) {
      const pattern = captured.spec.tooltip[activeType]
      assert(pattern, `${activeType} tooltip must override raw defaults`)
      for (const row of captured.spec.data[0].values) {
        const title = pattern.title.value(row)
        expect(title).toContain('2026')
        expect(title).not.toContain(String(point.observed_at))
        expect(pattern.content[0].key(row)).toBe(
          resources[language].translation[
            row.window === '5h' ? 'GPT 5-hour window' : 'GPT 7-day window'
          ]
        )
        expect(pattern.content[0].value(row)).toBe(
          resources[language].translation['GPT remaining {{percent}}%'].replace(
            '{{percent}}',
            row.window === '5h' ? '0' : '57'
          )
        )
      }
    }
  }
)

/** 切换界面语言必须更新已有提示的名称和日期格式，不保留初始语言闭包。 */
it('updates existing tooltip formatters after switching the interface language', async () => {
  const i18n = createInstance()
  await i18n.init({ lng: 'en', fallbackLng: 'en', resources })
  render(
    <I18nextProvider i18n={i18n}>
      <QuotaHistoryChart points={[point]} />
    </I18nextProvider>
  )
  await waitFor(() => expect(captured.spec).not.toBeNull())
  assert(captured.spec?.tooltip.mark)
  const row = captured.spec.data[0].values.find((item) => item.window === '7d')
  assert(row)
  const before = captured.spec.tooltip.mark.title.value(row)
  await act(async () => {
    await i18n.changeLanguage('zhCN')
  })
  expect(captured.spec?.tooltip.dimension?.content[0].key(row)).toBe('7 天额度')
  expect(captured.spec?.tooltip.dimension?.content[0].value(row)).toBe(
    '剩余 57%'
  )
  expect(captured.spec?.tooltip.dimension?.title.value(row)).not.toBe(before)
})
