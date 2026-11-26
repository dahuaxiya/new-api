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
import { useTranslation } from 'react-i18next'

import { BadgeListCell } from '@/components/data-table'
import { GroupBadge } from '@/components/group-badge'
import { cn } from '@/lib/utils'

import { splitChannelGroups } from '../lib'

/**
 * Displays product and routing-only channel groups at different visual levels.
 *
 * They share one database field for compatibility, but mixing them in the same
 * badge row makes internal admission names look like purchasable products. The
 * component keeps both visible while giving readers an unambiguous hierarchy.
 *
 * @param props groups, sensitivity, mask text and card/table presentation
 * @return product badges plus a separate routing-only row
 */
export function ChannelGroups(props: {
  groups: string
  sensitiveVisible: boolean
  maskedLabel: string
  variant: 'card' | 'table'
}) {
  const { t } = useTranslation()
  const { productGroups, routingGroups } = splitChannelGroups(props.groups)

  if (productGroups.length === 0 && routingGroups.length === 0) {
    return <span className='text-muted-foreground text-sm'>-</span>
  }

  const productBadges = productGroups.map((group) => (
    <GroupBadge
      key={group}
      group={group}
      label={props.sensitiveVisible ? undefined : props.maskedLabel}
      size='sm'
    />
  ))

  return (
    <div
      className={cn(
        'flex min-w-0 flex-col',
        props.variant === 'card' ? 'gap-1.5' : 'gap-1'
      )}
    >
      {productGroups.length > 0 &&
        (props.variant === 'card' ? (
          <div
            role='group'
            aria-label={t('Groups')}
            className='-ml-1.5 flex min-w-0 flex-wrap gap-1'
          >
            {productBadges}
          </div>
        ) : (
          <div role='group' aria-label={t('Groups')}>
            <BadgeListCell items={productBadges} />
          </div>
        ))}
      {routingGroups.length > 0 && (
        <div
          role='group'
          aria-label={t('Internal routing')}
          className={cn(
            'flex min-w-0 flex-wrap items-center',
            props.variant === 'card'
              ? 'gap-1.5 text-[11px]'
              : 'gap-1 text-[10px] leading-4'
          )}
        >
          <span className='text-muted-foreground/80'>
            {t('Internal routing')}
          </span>
          <span
            className={cn(
              'bg-muted/70 text-muted-foreground rounded font-mono',
              props.variant === 'card'
                ? 'rounded-md px-1.5 py-0.5 text-[10px]'
                : 'px-1 py-0.5'
            )}
          >
            {props.sensitiveVisible
              ? routingGroups.join(' · ')
              : props.maskedLabel}
          </span>
        </div>
      )}
    </div>
  )
}
