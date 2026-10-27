import { createInstance } from 'i18next'
import { expect, it } from 'vitest'

import en from '@/i18n/locales/en.json'
import fr from '@/i18n/locales/fr.json'
import ja from '@/i18n/locales/ja.json'
import ru from '@/i18n/locales/ru.json'
import vi from '@/i18n/locales/vi.json'
import zhTW from '@/i18n/locales/zh-TW.json'
import zh from '@/i18n/locales/zh.json'

/** 验证真实 i18next 命名空间，避免翻译误放到 JSON 顶层后只显示英文键。 */
it('七种语言均能加载用户页、连接状态和旧 Key 确认文案', async () => {
  const resources = { en, fr, ja, ru, vi, zh, 'zh-TW': zhTW }
  const instance = createInstance()
  await instance.init({
    resources,
    lng: 'zh',
    fallbackLng: false,
    keySeparator: false,
  })
  for (const lng of Object.keys(resources)) {
    await instance.changeLanguage(lng)
    for (const key of [
      'View accounts, renew hosting and manage quotas in one place.',
      '{{count}} users have no hosting term',
      'View and set terms',
      'Refresh data',
      'Slot',
      'Slot not assigned',
      'Connection details unavailable',
      'No outbound connection configured',
      'Bound slot not found',
      'Outbound connection disabled',
      'API keys for {{user}}',
      '{{count}} keys',
      'View full API key',
      "You do not have permission to manage this user's API keys",
      'An expired or exhausted API key cannot be enabled',
      'GPT sign-in is not required. Existing direct keys and credentials are retained; all retained keys share this hosting term. New API keeps using its existing upstream key.',
    ]) {
      expect(instance.exists(key, { count: 3 })).toBe(true)
    }
  }
  await instance.changeLanguage('zh')
  expect(instance.t('View and set terms')).toBe('查看并设置')
  expect(instance.t('Slot')).toBe('槽位')
  expect(instance.t('Slot not assigned')).toBe('尚未分配槽位')
})
