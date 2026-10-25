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
import { createInstance } from 'i18next'
import { describe, expect, it } from 'vitest'

import en from '@/i18n/locales/en.json'
import fr from '@/i18n/locales/fr.json'
import ja from '@/i18n/locales/ja.json'
import ru from '@/i18n/locales/ru.json'
import vi from '@/i18n/locales/vi.json'
import zhTW from '@/i18n/locales/zh-TW.json'
import zh from '@/i18n/locales/zh.json'

const locales = { en, fr, ja, ru, vi, zhTW, zh }
const messages = [
  'Hosting does not include an official subscription or additional official quota.',
  'Each seat hosts one account. Reauthorization must use the same account; unbind first to change accounts.',
  'Choose this account for a New API key on the API Keys page. Each key uses only its selected account.',
] as const

describe('hosted account translations', () => {
  // 使用真实资源和 i18next，防止条目放在命名空间之外时被模拟翻译函数掩盖。
  it.each(Object.entries(locales))(
    '%s 页面能解析账号、订阅和 Key 绑定说明',
    async (language, resource) => {
      const translator = createInstance()
      await translator.init({
        lng: language,
        resources: { [language]: resource },
        fallbackLng: false,
        nsSeparator: false,
      })
      for (const message of messages) {
        expect(translator.exists(message)).toBe(true)
      }
      if (language === 'zh') {
        expect(translator.t(messages[0])).toBe(
          '托管服务不包含官方订阅，也不会增加官方额度。'
        )
      }
    }
  )
})
