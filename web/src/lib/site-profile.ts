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
import type { LinkProps } from '@tanstack/react-router'
import {
  Boxes,
  Key,
  MessagesSquare,
  ShieldCheck,
  Wallet,
  type LucideIcon,
} from 'lucide-react'

type SiteLink = Readonly<{
  titleKey: string
  url: NonNullable<LinkProps['to']>
  icon: LucideIcon
}>

type SiteProfile = Readonly<{
  titleKey: string
  descriptionKey: string
  balanceDescriptionKey: string
  guideTitleKey: string
  showHostedQuota: boolean
  navigation: readonly SiteLink[]
}>

const keys: SiteLink = { titleKey: 'API Keys', url: '/keys', icon: Key }
const accounts: SiteLink = {
  titleKey: 'Hosted accounts',
  url: '/account/gpt',
  icon: ShieldCheck,
}
const models: SiteLink = {
  titleKey: 'Model Square',
  url: '/pricing',
  icon: Boxes,
}
const plans: SiteLink = {
  titleKey: 'Hosting plans',
  url: '/wallet',
  icon: Wallet,
}
const billing: SiteLink = {
  titleKey: 'Wallet',
  url: '/wallet',
  icon: Wallet,
}

// 同一路由在两站可以有不同业务名称；菜单和首页引用同一条目，避免入口逐渐分叉。
const profiles: Record<'hosting' | 'retail', SiteProfile> = {
  hosting: {
    titleKey: 'Hosting overview',
    descriptionKey:
      'Manage hosted accounts, subscription quota and hosting plans.',
    balanceDescriptionKey: 'Separate from GPT subscription quota',
    guideTitleKey: 'Codex setup guide',
    showHostedQuota: true,
    navigation: [accounts, plans, keys],
  },
  retail: {
    titleKey: 'API overview',
    descriptionKey: 'Manage API keys, wallet balance and model usage.',
    balanceDescriptionKey: 'Balance available for API usage',
    guideTitleKey: 'User guide',
    showHostedQuota: false,
    navigation: [
      { titleKey: 'Model studio', url: '/studio', icon: MessagesSquare },
      keys,
      billing,
      models,
    ],
  },
}

/**
 * 返回共享门户的业务展示配置，不承担授权、计费或数据库隔离。
 * @param mode 服务端状态接口给出的模式；旧部署缺少字段时保持托管站行为。
 * @return 只读站点配置。请求尚未完成时，调用方仍须等待状态再加载专属数据。
 */
export function getSiteProfile(mode?: 'hosting' | 'retail'): SiteProfile {
  return profiles[mode === 'retail' ? 'retail' : 'hosting']
}
