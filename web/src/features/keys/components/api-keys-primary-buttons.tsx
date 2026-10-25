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
import { ChevronDown, Globe, Plus } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { CopyButton } from '@/components/copy-button'
import { LoadingState } from '@/components/loading-state'
import { Button } from '@/components/ui/button'
import {
  Item,
  ItemActions,
  ItemContent,
  ItemDescription,
  ItemGroup,
  ItemTitle,
} from '@/components/ui/item'
import {
  Popover,
  PopoverContent,
  PopoverTitle,
  PopoverTrigger,
} from '@/components/ui/popover'
import { useApiInfo } from '@/features/dashboard/hooks/use-status-data'
import { docsApiBase } from '@/features/user-docs/config'
import { useStatus } from '@/hooks/use-status'

import { useApiKeys } from './api-keys-provider'

/**
 * 提供密钥创建入口及可复制地址；默认优先展示与教程一致的 OpenAI 基础地址。
 * 管理员显式配置的线路可能有专用路径，保持原值，不自动添加或删除其版本后缀。
 * @return 带地址选择弹窗的密钥页操作按钮。
 */
export function ApiKeysPrimaryButtons() {
  const { t } = useTranslation()
  const { setOpen } = useApiKeys()
  const { status, loading } = useStatus()
  const { items } = useApiInfo()
  const serverAddress =
    (typeof status?.server_address === 'string' &&
      status.server_address.trim()) ||
    ''
  // 与教程共用地址规范化，保留反向代理前缀，并避免已有 /v1 时重复追加。
  // 服务器地址仅移除末尾的版本段，供会自动补 /v1 的客户端使用。
  const apiBase = docsApiBase(serverAddress, window.location.origin)
  const addresses = items.length
    ? items
    : [
        {
          url: apiBase,
          route: t('OpenAI-compatible URL (with /v1)'),
          description: t(
            'Use this as the Base URL in Codex or an OpenAI SDK. Do not add /v1 again.'
          ),
        },
        {
          url: apiBase.slice(0, -3),
          route: t('Server URL (without /v1)'),
          description: t(
            'Only use this if your client automatically adds /v1.'
          ),
        },
      ]

  return (
    <div className='flex flex-wrap gap-2'>
      <Popover>
        <PopoverTrigger render={<Button variant='outline' size='sm' />}>
          <Globe aria-hidden='true' />
          {t('API Addresses')}
          <ChevronDown aria-hidden='true' />
        </PopoverTrigger>
        <PopoverContent
          align='end'
          className='max-h-[min(28rem,var(--available-height))] w-96 max-w-[calc(100vw-2rem)] overflow-y-auto'
        >
          <PopoverTitle>{t('API Addresses')}</PopoverTitle>
          {loading ? (
            <LoadingState inline size='sm' message={t('Loading...')} />
          ) : (
            <ItemGroup>
              {addresses.map((address) => (
                <Item
                  key={address.url}
                  role='listitem'
                  variant='muted'
                  size='xs'
                  className='flex-nowrap items-start'
                >
                  <ItemContent className='min-w-0 gap-1'>
                    <ItemTitle className='line-clamp-none break-all'>
                      {address.route}
                    </ItemTitle>
                    <code className='text-xs break-all select-text'>
                      {address.url}
                    </code>
                    {address.description && (
                      <ItemDescription className='line-clamp-none break-words'>
                        {address.description}
                      </ItemDescription>
                    )}
                  </ItemContent>
                  <ItemActions>
                    <CopyButton
                      value={address.url}
                      size='sm'
                      tooltip={t('Copy API URL')}
                      aria-label={`${t('Copy API URL')}: ${address.url}`}
                    />
                  </ItemActions>
                </Item>
              ))}
            </ItemGroup>
          )}
        </PopoverContent>
      </Popover>
      <Button size='sm' onClick={() => setOpen('create')}>
        <Plus className='h-4 w-4' />
        {t('Create API Key')}
      </Button>
    </div>
  )
}
