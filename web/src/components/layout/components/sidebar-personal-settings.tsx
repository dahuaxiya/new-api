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
import { Link, useLocation } from '@tanstack/react-router'
import {
  Check,
  ChevronRight,
  Settings,
  ShieldCheck,
  UserRound,
} from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { ProfileDropdown } from '@/components/profile-dropdown'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import {
  SidebarFooter,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  useSidebar,
} from '@/components/ui/sidebar'
import { useIsSidebarModuleVisible } from '@/hooks/use-sidebar-config'
import { useUserDisplay } from '@/hooks/use-user-display'
import { useAuthStore } from '@/stores/auth-store'

/**
 * 将个人资料和账户安全固定在侧栏底部，并复用菜单的焦点、关闭与碰撞处理。
 * 仅改变导航位置；各子项仍遵守管理员与用户的显示设置，目标路由继续执行原有鉴权。
 */
export function SidebarPersonalSettings() {
  const { t } = useTranslation()
  const pathname = useLocation({ select: (location) => location.pathname })
  const { isMobile, state, setOpenMobile } = useSidebar()
  const [open, setOpen] = useState(false)
  const user = useAuthStore((store) => store.auth.user)
  const { displayName, roleLabel } = useUserDisplay(user)
  const showProfile = useIsSidebarModuleVisible('/profile')
  const showSecurity = useIsSidebarModuleVisible('/security')
  // 折叠图标的 Tooltip 结构保持稳定，开关菜单时不替换触发节点，否则焦点与锚点会丢失。
  const collapsed = state === 'collapsed' && !isMobile
  const items = [
    {
      to: '/profile' as const,
      title: t('Basic profile'),
      description: t('Name, email and language'),
      icon: UserRound,
      visible: showProfile,
    },
    {
      to: '/security' as const,
      title: t('Account security'),
      description: t('Password, verification and sessions'),
      icon: ShieldCheck,
      visible: showSecurity,
    },
  ].filter((item) => item.visible)

  return (
    <SidebarFooter
      className='personal-settings-footer'
      data-collapsed={collapsed || undefined}
    >
      {items.length > 0 && (
        <>
          {!collapsed && (
            <p className='personal-settings-label'>{t('My account')}</p>
          )}
          <SidebarMenu>
            <SidebarMenuItem>
              <DropdownMenu modal={false} open={open} onOpenChange={setOpen}>
                <DropdownMenuTrigger
                  aria-label={t('Personal settings')}
                  render={
                    <SidebarMenuButton
                      size='lg'
                      className='personal-settings-trigger'
                      tooltip={t('Personal settings')}
                      isActive={items.some((item) => item.to === pathname)}
                    />
                  }
                >
                  <Settings aria-hidden='true' />
                  {!collapsed && (
                    <>
                      <span className='personal-settings-trigger-copy'>
                        <span>{t('Personal settings')}</span>
                        <small>{t('Profile and account security')}</small>
                      </span>
                      <ChevronRight
                        className='personal-settings-chevron'
                        aria-hidden='true'
                      />
                    </>
                  )}
                </DropdownMenuTrigger>
                <DropdownMenuContent
                  aria-label={t('Personal settings')}
                  className='personal-settings-flyout'
                  side={isMobile ? 'top' : 'right'}
                  align={isMobile ? 'start' : 'end'}
                  sideOffset={12}
                >
                  <DropdownMenuGroup>
                    <DropdownMenuLabel className='personal-settings-menu-label'>
                      {t('Personal settings')}
                    </DropdownMenuLabel>
                    <DropdownMenuSeparator />
                    {items.map((item) => (
                      <DropdownMenuItem
                        key={item.to}
                        className='personal-settings-item'
                        aria-label={item.title}
                        aria-current={pathname === item.to ? 'page' : undefined}
                        label={item.title}
                        render={
                          <Link
                            to={item.to}
                            preload={isMobile ? false : undefined}
                          />
                        }
                        onClick={() => {
                          // 子菜单与手机抽屉分开关闭，避免跳转后留下覆盖新页面的导航层。
                          setOpen(false)
                          setOpenMobile(false)
                        }}
                      >
                        <span className='personal-settings-item-icon'>
                          <item.icon aria-hidden='true' />
                        </span>
                        <span className='personal-settings-item-copy'>
                          <span>{item.title}</span>
                          <small>{item.description}</small>
                        </span>
                        {pathname === item.to && (
                          <Check
                            className='personal-settings-check'
                            aria-hidden='true'
                          />
                        )}
                      </DropdownMenuItem>
                    ))}
                  </DropdownMenuGroup>
                </DropdownMenuContent>
              </DropdownMenu>
            </SidebarMenuItem>
          </SidebarMenu>
        </>
      )}
      <div className='personal-settings-account'>
        <ProfileDropdown />
        {!collapsed && (
          <div className='min-w-0'>
            <p className='text-foreground truncate text-xs font-medium'>
              {displayName}
            </p>
            <p className='text-muted-foreground mt-1 text-[10px]'>
              {roleLabel}
            </p>
          </div>
        )}
      </div>
    </SidebarFooter>
  )
}
