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
import '@/styles/workspace-sidebar.css'
import { AnimatePresence, motion, useReducedMotion } from 'motion/react'

import { Sidebar, SidebarContent, SidebarRail } from '@/components/ui/sidebar'
import { useLayout } from '@/context/layout-provider'
import { useSidebarView } from '@/hooks/use-sidebar-view'
import { MOTION_TRANSITION, MOTION_VARIANTS } from '@/lib/motion'

import { NavGroup } from './nav-group'
import { SidebarViewHeader } from './sidebar-view-header'

/**
 * 按现有路由和权限渲染导航，并为桌面及手机抽屉提供同一套视觉样式。
 * 样式容器必须放在 Sidebar 内部：手机导航通过 Portal 渲染，外层类名不会传入抽屉。
 * 导航过滤、折叠状态和返回入口继续由现有组件管理。
 */
export function AppSidebar() {
  const { collapsible, variant } = useLayout()
  const { key, view, navGroups } = useSidebarView()
  const shouldReduce = useReducedMotion()

  return (
    <Sidebar
      collapsible={collapsible}
      variant={variant}
      className='workspace-sidebar-container'
    >
      <div className='workspace-sidebar'>
        {view && <SidebarViewHeader view={view} />}

        <SidebarContent className='py-2'>
          <AnimatePresence mode='wait' initial={false}>
            <motion.div
              key={key}
              initial={
                shouldReduce ? false : MOTION_VARIANTS.sidebarSlide.initial
              }
              animate={MOTION_VARIANTS.sidebarSlide.animate}
              exit={
                shouldReduce ? undefined : MOTION_VARIANTS.sidebarSlide.exit
              }
              transition={MOTION_TRANSITION.fast}
              className='flex flex-col'
            >
              {navGroups.map((props) => (
                <NavGroup key={props.id || props.title} {...props} />
              ))}
            </motion.div>
          </AnimatePresence>
        </SidebarContent>
      </div>
      <SidebarRail />
    </Sidebar>
  )
}
