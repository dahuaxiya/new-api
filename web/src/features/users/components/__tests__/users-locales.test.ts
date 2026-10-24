import { createInstance } from 'i18next'
import { expect, it } from 'vitest'

import en from '@/i18n/locales/en.json'
import fr from '@/i18n/locales/fr.json'
import ja from '@/i18n/locales/ja.json'
import ru from '@/i18n/locales/ru.json'
import vi from '@/i18n/locales/vi.json'
import zhTW from '@/i18n/locales/zh-TW.json'
import zh from '@/i18n/locales/zh.json'

it('七种语言均能在实际命名空间中加载用户页和旧 Key 确认文案', async () => {
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
      'GPT sign-in is not required. Existing direct keys and credentials are retained; all retained keys share this hosting term. New API keeps using its existing upstream key.',
    ]) {
      expect(instance.exists(key, { count: 3 })).toBe(true)
    }
  }
  await instance.changeLanguage('zh')
  expect(instance.t('View and set terms')).toBe('查看并设置')
})
