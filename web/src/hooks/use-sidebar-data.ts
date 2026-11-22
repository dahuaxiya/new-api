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
import { BookOpen, FileText, LayoutDashboard, ShieldCheck } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import type { SidebarData } from '@/components/layout/types'
import { useStatus } from '@/hooks/use-status'
import { ROLE } from '@/lib/roles'
import { getSiteProfile } from '@/lib/site-profile'

/** 用户导航与首页共用站点配置；这里只组装入口，权限和菜单开关由既有过滤器处理。 */
export function useSidebarData(): SidebarData {
  const { t } = useTranslation()
  const { status } = useStatus()
  const profile = getSiteProfile(status?.site_mode)
  return {
    navGroups: [
      {
        id: 'general',
        title: t('User center'),
        items: [
          {
            title: t(profile.titleKey),
            url: '/dashboard/overview',
            icon: LayoutDashboard,
          },
          ...profile.navigation
            .filter((item) => item.url !== '/pricing')
            .map((item) => ({
              title: t(item.titleKey),
              url: item.url,
              icon: item.icon,
            })),
          { title: t('Usage Logs'), url: '/usage-logs/common', icon: FileText },
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
