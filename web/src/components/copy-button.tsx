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
import { Check, Copy } from 'lucide-react'
import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { useCopyToClipboard } from '@/hooks/use-copy-to-clipboard'
import { cn } from '@/lib/utils'

interface CopyButtonProps {
  value: string
  children?: ReactNode
  className?: string
  iconClassName?: string
  variant?: 'ghost' | 'outline' | 'default' | 'secondary' | 'destructive'
  size?: 'default' | 'sm' | 'lg' | 'icon'
  tooltip?: string
  successTooltip?: string
  'aria-label'?: string
}

/**
 * 使用原位图标确认复制成功，失败仍显示全局错误提示，不能静默丢失反馈。
 * @param props 复制内容和按钮外观；aria-label 应说明复制对象，不应包含密钥值。
 * @return 保留焦点和可访问名称的复制按钮。
 */
export function CopyButton({
  value,
  children,
  className,
  iconClassName,
  variant = 'ghost',
  size = 'icon',
  tooltip,
  successTooltip,
  'aria-label': ariaLabel,
}: CopyButtonProps) {
  const { t } = useTranslation()
  const { copiedText, copyToClipboard } = useCopyToClipboard({
    notify: false,
    notifyError: true,
  })
  const isCopied = copiedText === value
  const resolvedTooltip = tooltip ?? t('Copy to clipboard')
  const resolvedSuccessTooltip = successTooltip ?? t('Copied!')
  const resolvedAriaLabel = ariaLabel ?? resolvedTooltip
  const copiedAriaLabel = t('Copied')

  const button = (
    <Button
      variant={variant}
      size={size}
      className={cn('shrink-0', className)}
      onClick={() => copyToClipboard(value)}
      aria-label={resolvedAriaLabel}
    >
      <span
        key={isCopied ? 'copied' : 'copy'}
        className={cn('inline-flex', isCopied && 'copy-feedback')}
        aria-hidden='true'
      >
        {isCopied ? (
          <Check className={cn('size-4 text-success', iconClassName)} />
        ) : (
          <Copy className={cn('size-4', iconClassName)} />
        )}
      </span>
      {children}
    </Button>
  )

  return (
    <>
      {tooltip || successTooltip ? (
        <Tooltip>
          <TooltipTrigger render={button} />
          <TooltipContent>
            <p>{isCopied ? resolvedSuccessTooltip : resolvedTooltip}</p>
          </TooltipContent>
        </Tooltip>
      ) : (
        button
      )}
      {/* 实时状态放在按钮外，避免按钮子节点的展示语义吞掉读屏播报。 */}
      <span role='status' className='sr-only'>
        {isCopied ? copiedAriaLabel : ''}
      </span>
    </>
  )
}
