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
import { DEFAULT_LOGO } from '@/lib/constants'
import { cn } from '@/lib/utils'

interface BrandArtworkProps {
  name: string
  logo: string
  compact?: boolean
  className?: string
}

/**
 * 统一展示已确认的组合字标，以高分辨率透明素材平滑小尺寸边缘；自定义品牌继续使用后台配置。
 * @param props 当前站名、图形地址及紧凑模式；compact 用于只有图标空间的侧栏。
 * @return 具有单一可访问名称的品牌图形，主题切换不依赖异步状态。
 */
export function BrandArtwork(props: BrandArtworkProps) {
  const branded =
    props.name.trim().toLowerCase() === 'luobocode' &&
    props.logo === DEFAULT_LOGO

  if (!branded) {
    // 只对内置 luobocode 品牌使用固定字标，管理员后续改名或上传 Logo 仍然有效。
    return (
      <span
        className={cn(
          'inline-flex min-w-0 items-center gap-2',
          props.className
        )}
      >
        <img
          src={props.logo}
          alt={props.name}
          className='size-8 shrink-0 object-contain'
        />
        {!props.compact && (
          <span className='truncate text-sm font-semibold'>{props.name}</span>
        )}
      </span>
    )
  }

  const asset = props.compact ? 'mark' : 'lockup'
  // 32px 页头直接栅格化轮廓字形时，1x 屏幕的曲线台阶较明显。
  // 从原 SVG 高倍采样后缩小显示，保留透明边缘及配色，不使用会模糊五官的 CSS blur。
  return (
    <span
      role='img'
      aria-label={props.name}
      className={cn(
        'inline-flex h-8 max-w-full shrink-0 items-center',
        props.className
      )}
    >
      {/* 两个配色由同一个 dark 选择器控制，避免首屏和切换主题时出现错色。 */}
      <img
        src={`/brand/luobocode-${asset}-v11.png`}
        alt=''
        aria-hidden='true'
        className='block h-full w-auto max-w-full object-contain dark:hidden'
      />
      <img
        src={`/brand/luobocode-${asset}-dark-v11.png`}
        alt=''
        aria-hidden='true'
        className='hidden h-full w-auto max-w-full object-contain dark:block'
      />
    </span>
  )
}
