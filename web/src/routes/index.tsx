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
import { createFileRoute, redirect } from '@tanstack/react-router'

import { Home } from '@/features/home'

export const Route = createFileRoute('/')({
  beforeLoad: ({ preload }) => {
    // 只有这对生产域名共用公共首页。身份回调、找回密码及托管控制台都有独立路由。
    if (!preload && window.location.hostname === 'host.luobo.world') {
      throw redirect({
        href: 'https://luobo.world/',
        reloadDocument: true,
        replace: true,
      })
    }
  },
  component: Home,
})
