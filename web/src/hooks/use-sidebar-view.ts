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
import { useLocation } from '@tanstack/react-router'
import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'

import { resolveSidebarView } from '@/components/layout/lib/sidebar-view-registry'
import type { NavGroup, ResolvedSidebarView } from '@/components/layout/types'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

import { useSidebarConfig } from './use-sidebar-config'
import { useSidebarData } from './use-sidebar-data'

/** Sentinel key used for the root navigation in animation `key=` props */
const ROOT_VIEW_KEY = '__root'

/**
 * 按当前路径选择用户区或管理工作区，两类导航都应用后台配置与个人隐藏设置。
 * 管理入口按角色过滤；具体页面和接口仍执行各自已有的权限检查。
 */
export function useSidebarView(): ResolvedSidebarView {
  const { t } = useTranslation()
  const pathname = useLocation({ select: (l) => l.pathname })
  const userRole = useAuthStore((s) => s.auth.user?.role)
  const rootSidebarData = useSidebarData()
  const configFilteredRoot = useSidebarConfig(rootSidebarData.navGroups)

  const rootNavGroups = useMemo<NavGroup[]>(() => {
    const role = userRole ?? ROLE.GUEST
    const isAdmin = role >= ROLE.ADMIN
    return configFilteredRoot
      .filter((group) => (group.id === 'admin' ? isAdmin : true))
      .map((group) => {
        const items = group.items.filter(
          (item) => item.requiredRole === undefined || role >= item.requiredRole
        )
        return items.length === group.items.length ? group : { ...group, items }
      })
  }, [configFilteredRoot, userRole])

  // 管理工作区也经过原有角色及菜单配置过滤，避免嵌套导航绕过隐藏设置。
  const candidateView = resolveSidebarView(pathname)
  const view = (userRole ?? ROLE.GUEST) >= ROLE.ADMIN ? candidateView : null
  const configuredViewGroups = useSidebarConfig(view?.getNavGroups(t) ?? [])

  if (view) {
    return {
      key: view.id,
      view,
      navGroups: configuredViewGroups
        .map((group) => ({
          ...group,
          items: group.items.filter(
            (item) =>
              item.requiredRole === undefined ||
              (userRole ?? ROLE.GUEST) >= item.requiredRole
          ),
        }))
        .filter((group) => group.items.length > 0),
    }
  }

  return {
    key: ROOT_VIEW_KEY,
    view: null,
    navGroups: rootNavGroups,
  }
}
