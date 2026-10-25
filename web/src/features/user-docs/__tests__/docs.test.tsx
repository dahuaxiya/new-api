import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  createMemoryHistory,
  createRootRoute,
  createRouter,
  RouterProvider,
} from '@tanstack/react-router'
import {
  act,
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import i18next from 'i18next'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import zhResources from '@/i18n/locales/zh.json'
import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

import { UserDocs } from '..'
import { codexConfig, docsApiBase } from '../config'

let client: QueryClient
beforeEach(() => {
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  useAuthStore.setState(useAuthStore.getInitialState(), true)
  localStorage.clear()
  window.history.replaceState(null, '', '/docs#codex-setup')
  vi.spyOn(api, 'get').mockImplementation(async (url) => ({
    data: {
      success: true,
      data:
        url === '/api/status'
          ? {
              server_address: 'https://gateway.example.com',
              system_name: 'My API',
            }
          : '',
    },
  }))
})
afterEach(async () => {
  cleanup()
  client.clear()
  await i18next.changeLanguage('en')
  i18next.removeResourceBundle('zhCN', 'translation')
  vi.restoreAllMocks()
})

/** Exercise the public page with real routing and query providers; only HTTP is mocked. */
function mountDocs() {
  const router = createRouter({
    routeTree: createRootRoute({ component: UserDocs }),
    history: createMemoryHistory(),
  })
  return render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
}

describe('public user guide', () => {
  it('switches platforms and copies the actual site config without requiring login', async () => {
    const user = userEvent.setup()
    mountDocs()
    await screen.findByRole('heading', { name: 'Codex setup' })
    await waitFor(() =>
      expect(
        screen.getByText(/base_url = "https:\/\/gateway.example.com\/v1"/)
      ).toBeTruthy()
    )
    expect(screen.getByText('%USERPROFILE%\\.codex')).toBeTruthy()
    await user.click(screen.getByRole('tab', { name: 'macOS' }))
    expect(screen.getByText('~/.codex')).toBeTruthy()
    expect(screen.queryByText('%USERPROFILE%\\.codex')).toBeNull()
    await user.click(screen.getByRole('button', { name: 'Copy config.toml' }))
    expect(await navigator.clipboard.readText()).toBe(
      codexConfig('https://gateway.example.com/v1')
    )
    expect(await navigator.clipboard.readText()).not.toContain(
      'danger-full-access'
    )
  })

  /** 用户一次只阅读一条配置路径；已有文件只复制目标片段，避免覆盖其他设置。 */
  it('separates first setup from existing-file edits and copies each existing section', async () => {
    const user = userEvent.setup()
    mountDocs()
    await screen.findByRole('heading', { name: 'Codex setup' })
    const mode = screen.getByRole('tablist', { name: 'docs.config.mode' })
    expect(
      within(mode).getByRole('tab', { name: 'docs.config.newTab' })
    ).toHaveAttribute('aria-selected', 'true')
    await user.click(
      within(mode).getByRole('tab', { name: 'docs.config.existingTab' })
    )
    expect(
      screen.queryByRole('button', { name: 'Copy config.toml' })
    ).toBeNull()
    await user.click(
      screen.getByRole('button', { name: 'Copy docs.config.defaultsLabel' })
    )
    const defaults = await navigator.clipboard.readText()
    expect(defaults).toContain('model_provider = "my_api"')
    expect(defaults).not.toContain('[model_providers.my_api]')
    await user.click(
      screen.getByRole('button', { name: 'Copy docs.config.providerLabel' })
    )
    const provider = await navigator.clipboard.readText()
    expect(provider).toContain('[model_providers.my_api]')
    expect(provider).toContain('experimental_bearer_token = "YOUR_API_KEY"')
    expect(defaults + '\n\n' + provider).toBe(
      codexConfig('https://gateway.example.com/v1')
    )
  })

  /** Windows 主线直接编辑文件；仅在需要时展开文件夹和扩展名排错。 */
  it('shows direct Windows file editing and expands optional file help', async () => {
    const user = userEvent.setup()
    mountDocs()
    await screen.findByRole('heading', { name: 'Codex setup' })
    expect(screen.getByText('docs.windows.editFile')).toBeVisible()
    expect(screen.getByText('docs.windows.saveShort')).toBeVisible()
    const help = screen.getByRole('button', { name: 'docs.windows.fileHelp' })
    expect(help).toHaveAttribute('aria-expanded', 'false')
    await user.click(help)
    expect(await screen.findByText('docs.windows.createFolder')).toBeVisible()
    expect(screen.getByText('docs.windows.extensions')).toBeVisible()
    expect(document.querySelector('article')?.textContent).not.toMatch(
      /powershell|New-Item|Set-Content/i
    )
  })

  it('restores chapters from hash navigation and falls back for an unknown anchor', async () => {
    mountDocs()
    await screen.findByRole('heading', { name: 'Codex setup' })
    await act(async () => {
      window.location.hash = 'quota'
      window.dispatchEvent(new HashChangeEvent('hashchange'))
    })
    expect(
      await screen.findByRole('heading', { name: 'Quota and usage' })
    ).toBeTruthy()
    expect(screen.queryByRole('tab', { name: 'Windows' })).toBeNull()
    await act(async () => {
      window.location.hash = 'unknown'
      window.dispatchEvent(new HashChangeEvent('hashchange'))
    })
    expect(
      await screen.findByRole('heading', { name: 'Codex setup' })
    ).toBeTruthy()
  })

  it('renders the Chinese file instructions from the real locale bundle', async () => {
    i18next.addResourceBundle(
      'zhCN',
      'translation',
      zhResources.translation,
      true,
      true
    )
    await i18next.changeLanguage('zhCN')
    mountDocs()
    expect(
      await screen.findByRole('heading', { name: 'Codex 接入教程' })
    ).toBeTruthy()
    expect(
      screen.getByText(
        '已经安装 Codex？直接从第 2 步开始。下面的 Windows 配置只需修改文件。'
      )
    ).toBeTruthy()
    expect(
      screen.getByRole('button', { name: '复制 config.toml' })
    ).toBeTruthy()
    expect(screen.getByRole('tab', { name: '首次配置' })).toHaveAttribute(
      'aria-selected',
      'true'
    )
    expect(
      screen.getByRole('button', { name: '找不到文件夹，或文件名多了 .txt？' })
    ).toHaveAttribute('aria-expanded', 'false')
  })

  it.each([
    ['https://api.example.com/proxy/', 'https://api.example.com/proxy/v1'],
    ['https://api.example.com/v1/', 'https://api.example.com/v1'],
    ['https://api.example.com?secret=x#key', 'https://api.example.com/v1'],
    ['https://user:secret@api.example.com', 'https://site.example.com/v1'],
    ['javascript:alert(1)', 'https://site.example.com/v1'],
  ])('produces a safe copyable base for %s', (input, expected) => {
    expect(docsApiBase(input, 'https://site.example.com')).toBe(expected)
  })
})
