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
import i18n, { type BackendModule } from 'i18next'
import LanguageDetector from 'i18next-browser-languagedetector'
import { initReactI18next } from 'react-i18next'

import { convertDetectedLanguage } from './languages'

// Each literal import produces an independent, content-hashed cache entry.
const catalogs = {
  en: () => import(/* webpackChunkName: "locale-en" */ './locales/en.json'),
  zhCN: () => import(/* webpackChunkName: "locale-zhCN" */ './locales/zh.json'),
  zhTW: () =>
    import(/* webpackChunkName: "locale-zhTW" */ './locales/zh-TW.json'),
  ja: () => import(/* webpackChunkName: "locale-ja" */ './locales/ja.json'),
  fr: () => import(/* webpackChunkName: "locale-fr" */ './locales/fr.json'),
  ru: () => import(/* webpackChunkName: "locale-ru" */ './locales/ru.json'),
  vi: () => import(/* webpackChunkName: "locale-vi" */ './locales/vi.json'),
}

const catalogBackend: BackendModule = {
  type: 'backend',
  /** The loader needs no instance-specific configuration. */
  init() {},
  /** i18next owns pending loads and reuses loaded languages when switching back. */
  async read(language: keyof typeof catalogs, _namespace: string) {
    try {
      return (await catalogs[language]()).default.translation
    } catch (error) {
      // A failed translation download must still leave the site usable in English.
      if (language === 'en') throw error
      return (await catalogs.en()).default.translation
    }
  },
}

i18n
  .use(catalogBackend)
  .use(LanguageDetector)
  .use(initReactI18next)
  .init({
    // Each shipped catalog already includes missing English keys from the build.
    // Keep English for unsupported browser languages without fetching two packs.
    fallbackLng: (code) =>
      code && Object.hasOwn(catalogs, code) ? [] : ['en'],
    supportedLngs: Object.keys(catalogs),
    load: 'currentOnly',
    nsSeparator: false, // Allow literal colons in keys (e.g., URLs, labels)
    debug: import.meta.env.DEV,
    interpolation: {
      escapeValue: false, // not needed for react as it escapes by default
    },
    detection: {
      order: ['localStorage', 'navigator'],
      caches: ['localStorage'],
      // Browsers report `zh-CN`/`zh-TW`/`zh`; map them onto our `zhCN`/`zhTW`
      // codes (non-Chinese codes pass through for normal supportedLngs matching).
      convertDetectedLanguage,
    },
  })

export default i18n
