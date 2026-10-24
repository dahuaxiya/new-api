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
  Box,
  ClipboardList,
  CreditCard,
  FileText,
  LayoutDashboard,
  PlugZap,
  Radio,
  ServerCog,
  Settings,
  Ticket,
  Users,
} from 'lucide-react'

import { ROLE } from '@/lib/roles'

import type { SidebarView } from '../types'

/** 管理工作区沿用既有受保护路由，导航切换不替代服务端的角色检查。 */
export const ADMIN_VIEW: SidebarView = {
  id: 'administration',
  pathPattern:
    /^\/(?:user-usage|users|channels|models|redemption-codes|subscriptions|system-info|task-plugins|usage-logs|dashboard\/(?:models|flow|users))(?:\/|$)/,
  parent: { to: '/dashboard/overview', label: 'Back to user center' },
  getNavGroups: (t) => [
    {
      id: 'admin',
      title: t('Administration'),
      items: [
        { title: t('Usage Logs'), url: '/usage-logs/common', icon: FileText },
        {
          title: t('Audit Logs'),
          url: '/usage-logs/audit',
          icon: ClipboardList,
        },
        {
          title: t('Model Call Analytics'),
          url: '/dashboard/models',
          icon: LayoutDashboard,
        },
        {
          title: t('Channels'),
          url: '/channels',
          icon: Radio,
        },
        {
          title: t('Models'),
          url: '/models/metadata',
          icon: Box,
        },
        {
          title: t('Users'),
          url: '/users',
          icon: Users,
        },
        {
          title: t('Redemption Codes'),
          url: '/redemption-codes',
          icon: Ticket,
        },
        {
          title: t('Subscriptions'),
          url: '/subscriptions',
          icon: CreditCard,
        },
        {
          title: t('System Info'),
          url: '/system-info',
          icon: ServerCog,
          requiredRole: ROLE.SUPER_ADMIN,
        },
        {
          title: t('Task Plugins'),
          url: '/task-plugins',
          icon: PlugZap,
          requiredRole: ROLE.SUPER_ADMIN,
        },
        {
          title: t('System Settings'),
          url: '/system-settings/site',
          activeUrls: ['/system-settings'],
          icon: Settings,
          requiredRole: ROLE.SUPER_ADMIN,
        },
      ],
    },
  ],
}
