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

import { StaticDataTable } from '@/components/data-table'
import { Switch } from '@/components/ui/switch'
import {
  TableHead,
  TableHeader,
  TableRow,
  TableCell,
  TableBody,
} from '@/components/ui/table'

import {
  supportsRetailModelSync,
  type RetailBindingState,
  type RetailGroupBinding,
} from '../retail-pricing-api'

/** 对齐本站分组、库存、报价与模型同步开关，窄屏继续使用同一组开关与字段。
 * @param props 已保存的站点报价、保序的绑定草稿和跟随模型的编辑回调。
 * @return 每条供货线路的表格；缺少对应站点的有效快照时展示缺价。
 */
export function SupplyRouteTable(props: {
  state: RetailBindingState
  bindings: RetailGroupBinding[]
  busy: boolean
  onFollowChange: (index: number, enabled: boolean) => void
}) {
  const { t } = useTranslation()
  const { state } = props
  const channels = new Map(
    [...state.channels, ...(state.rate_channels ?? [])].map((c) => [c.id, c])
  )
  const keys = new Map(state.inventory_keys?.map((k) => [k.id, k]))
  const legacyChannels = new Set(state.channels.map((c) => c.id))
  return (
    <StaticDataTable
      className='gm-supply-wrap'
      tableClassName='gm-supply-table'
      tableProps={{ 'aria-label': t('Supply routes') }}
    >
      <TableHeader>
        <TableRow>
          <TableHead scope='col'>{t('Call group')}</TableHead>
          <TableHead scope='col'>
            {t('Inventory Key')} / {t('Upstream group')}
          </TableHead>
          <TableHead scope='col'>{t('Purchase multiplier')}</TableHead>
          <TableHead scope='col'>{t('Automatically sync models')}</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {!props.bindings.length && (
          <TableRow>
            <TableCell colSpan={4} className='gm-supply-empty'>
              {t('No routes configured')}
            </TableCell>
          </TableRow>
        )}
        {props.bindings.map((binding, index) => {
          const channel = channels.get(binding.channel_id)
          const key = keys.get(channel?.library_id ?? 0)
          const group = key?.upstream_group || binding.upstream_group
          // 定价按已发布的线路 → 站点映射隔离。专属站点缺价或读取失败时，不能用公共源的同名倍率填上。
          const siteId = state.rate_site_by_channel?.[binding.channel_id]
          let feed:
            | Pick<typeof state.sync, 'snapshot' | 'last_error'>
            | undefined
          if (siteId) {
            feed = state.site_feeds?.[siteId]
          } else if (
            legacyChannels.has(binding.channel_id) &&
            state.sync.enabled
          ) {
            feed = state.sync
          }
          const rate =
            !feed?.last_error && group === binding.upstream_group
              ? feed?.snapshot?.groups.find((r) => r.group === group)
                  ?.multiplier
              : undefined
          return (
            <TableRow key={`${binding.local_group}/${binding.channel_id}`}>
              <TableHead scope='row' className='gm-supply-group'>
                {binding.local_group}
              </TableHead>
              <TableCell className='gm-supply-key'>
                <span>
                  {key?.name || channel?.name || `#${binding.channel_id}`}
                </span>
                <small>{group}</small>
              </TableCell>
              <TableCell className='gm-supply-rate'>
                <span className='gm-narrow-label'>
                  {t('Purchase multiplier')}{' '}
                </span>
                <b>{rate ? `${rate}×` : t('Rate unavailable')}</b>
              </TableCell>
              <TableCell className='gm-supply-follow'>
                <span className='gm-narrow-label'>
                  {t('Automatically sync models')}{' '}
                </span>
                {supportsRetailModelSync(channel) || binding.follow_models ? (
                  <Switch
                    aria-label={`${t('Follow this supply group’s models')} · ${binding.local_group}`}
                    title={t(
                      'Every 10 minutes while sales are enabled. Missing prices need review; two complete reads confirm removals.'
                    )}
                    disabled={props.busy}
                    checked={!!binding.follow_models}
                    onCheckedChange={(value) =>
                      props.onFollowChange(index, value)
                    }
                  />
                ) : (
                  <span className='text-muted-foreground'>
                    {t('Not applicable')}
                  </span>
                )}
              </TableCell>
            </TableRow>
          )
        })}
      </TableBody>
    </StaticDataTable>
  )
}
