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
import {
  BookOpen,
  FileText,
  Key,
  LayoutDashboard,
  Settings,
  ShieldCheck,
  Wallet,
} from 'lucide-react'
import { useTranslation } from 'react-i18next'

import type { SidebarData } from '@/components/layout/types'
import { ROLE } from '@/lib/roles'

/** 用户导航只保留日常接入功能；管理入口另开工作区，不混排资源管理菜单。 */
export function useSidebarData(): SidebarData {
  const { t } = useTranslation()
  return {
    navGroups: [
      {
        id: 'general',
        title: t('User center'),
        items: [
          {
            title: t('Dashboard'),
            url: '/dashboard/overview',
            icon: LayoutDashboard,
          },
          {
            title: t('My GPT account'),
            url: '/account/gpt',
            icon: ShieldCheck,
          },
          { title: t('API Keys'), url: '/keys', icon: Key },
          { title: t('Usage Logs'), url: '/usage-logs/common', icon: FileText },
          { title: t('Billing and redemption'), url: '/wallet', icon: Wallet },
          {
            title: t('Personal settings'),
            url: '/profile',
            activeUrls: ['/security'],
            icon: Settings,
          },
        ],
      },
      {
        id: 'help',
        title: t('Help'),
        items: [{ title: t('User guide'), url: '/docs', icon: BookOpen }],
      },
      {
        id: 'admin',
        title: t('Admin'),
        items: [
          {
            title: t('Administration'),
            url: '/user-usage',
            icon: ShieldCheck,
            requiredRole: ROLE.ADMIN,
          },
        ],
      },
    ],
  }
}
