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
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  createMemoryHistory,
  createRootRoute,
  createRouter,
  RouterProvider,
} from '@tanstack/react-router'
import { cleanup, render, screen, within } from '@testing-library/react'
import { afterEach, expect, it } from 'vitest'

import { useSystemConfigStore } from '@/stores/system-config-store'

import { Footer } from '../footer'

let client: QueryClient

afterEach(() => {
  cleanup()
  client?.clear()
  useSystemConfigStore.setState(useSystemConfigStore.getInitialState(), true)
  localStorage.clear()
})

/**
 * 用真实配置和路由验证源码入口，缓存公共状态以隔离网络。
 * @param variant 普通、自定义或紧凑布局，均必须保留源码入口。
 * @return 已渲染的页脚区域，供检查可见入口和原项目署名。
 */
async function mountFooter(variant: 'default' | 'custom' | 'compact') {
  client = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Infinity } },
  })
  client.setQueryData(['status'], {})
  useSystemConfigStore.getState().setConfig({
    systemName: 'Test Portal',
    footerHtml: variant === 'custom' ? '<p>Site notice</p>' : '',
  })
  const router = createRouter({
    routeTree: createRootRoute({
      component: () => <Footer compact={variant === 'compact'} />,
    }),
    history: createMemoryHistory(),
  })
  await router.load()
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
  return screen.findByRole('contentinfo')
}

/** 普通页脚与自定义页脚均保留可见源码下载及上游署名。 */
it.each(['default', 'custom', 'compact'] as const)(
  '%s footer keeps the site source and license offer',
  async (variant) => {
    const footer = await mountFooter(variant)
    expect(
      within(footer).getByRole('link', { name: 'Site source code and license' })
    ).toHaveAttribute('href', '/quota-source.tar.gz')
    expect(
      within(footer).getByRole('link', { name: 'New API' })
    ).toHaveAttribute('href', 'https://github.com/QuantumNous/new-api')
  }
)
