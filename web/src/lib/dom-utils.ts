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
import { DEFAULT_LOGO } from '@/lib/constants'

/**
 * 把站点配置的图标应用到标签页，兼容服务端仍返回旧的内置 Logo 标识。
 * @param url 后台配置的 Logo 地址；仅默认值映射到当前品牌素材，自定义地址保持原样。
 */
export function applyFaviconToDom(url: string) {
  if (typeof document === 'undefined' || !url) return
  // 不修改后台默认值：现有配置与缓存继续有效，自定义品牌也不会被覆盖。
  if (url === DEFAULT_LOGO) url = '/brand/luobocode-favicon-v13.png'
  try {
    const next = new URL(url, window.location.href).href
    const existing =
      document.querySelectorAll<HTMLLinkElement>('link[rel~="icon"]')
    if (existing.length === 1 && existing[0].href === next) return
    const link = document.createElement('link')
    link.rel = 'icon'
    link.href = url
    existing.forEach((l) => l.remove())
    document.head.appendChild(link)
  } catch {
    // Ignore malformed URLs
  }
}
