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
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import {
  sideDrawerContentClassName,
  sideDrawerHeaderClassName,
} from '@/components/drawer-layout'
import { EmptyState } from '@/components/empty-state'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import {
  getHosting,
  postHosting,
  type HostingAdmin,
} from '@/features/gpt-hosting/api'
import { getOnboardingOptions } from '@/features/onboarding/api'
import { SlotSetup } from '@/features/onboarding/components/slot-setup'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber } from '@/lib/format'
import { handleServerError } from '@/lib/handle-server-error'
import { useAuthStore } from '@/stores/auth-store'

/** 按节点展示槽位快照和接新状态；统计不承诺可售容量，分配仍由服务端事务复核。 */
export function HostingResources(props: {
  open: boolean
  onOpenChange: (value: boolean) => void
}) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const identity = useAuthStore(
    (s) => `${s.auth.user?.id}:${s.auth.session?.sid}`
  )
  const cache = useQueryClient()
  const prefix = ['hosting-resources', identity]
  const inventory = useQuery({
    queryKey: [...prefix, 'inventory'],
    queryFn: ({ signal }) => getHosting<HostingAdmin>('admin', signal),
    enabled: props.open,
    retry: false,
  })
  const options = useQuery({
    queryKey: [...prefix, 'options'],
    queryFn: getOnboardingOptions,
    enabled: props.open,
    retry: false,
  })
  const update = useMutation({
    mutationFn: (body: {
      instance_id: string
      slot_id: string
      enabled: boolean
    }) => postHosting('admin/inventory', body),
    retry: false,
    onError: (error) => handleServerError(error),
    onSuccess: () => cache.invalidateQueries({ queryKey: prefix }),
  })
  return (
    <Sheet open={props.open} onOpenChange={props.onOpenChange}>
      <SheetContent className={sideDrawerContentClassName('sm:max-w-2xl')}>
        <SheetHeader className={sideDrawerHeaderClassName()}>
          <SheetTitle>{t('Slot resources')}</SheetTitle>
          <SheetDescription>
            {t(
              'Prepare empty slots and outbound connections here. Users sign in to their own GPT or Claude accounts. Create Claude slots in vm2api before enabling assignment here.'
            )}
          </SheetDescription>
        </SheetHeader>
        <div className='min-h-0 flex-1 space-y-6 overflow-y-auto p-6'>
          <Button
            variant='outline'
            disabled={inventory.isFetching || options.isFetching}
            onClick={() => cache.invalidateQueries({ queryKey: prefix })}
          >
            {t('Refresh')}
          </Button>
          {inventory.isPending && <LoadingState inline />}
          {(inventory.isError || options.isError) && (
            <ErrorState
              title={t('Unable to load resources')}
              onRetry={() => cache.invalidateQueries({ queryKey: prefix })}
            />
          )}
          {!inventory.isError && inventory.data?.instances.length === 0 && (
            <EmptyState
              title={t('No hosting nodes registered')}
              description={t(
                'Register a node in the server configuration before preparing slots.'
              )}
            />
          )}
          {!inventory.isError &&
            inventory.data?.instances.map((instance) => (
              <section
                key={instance.id}
                className='space-y-4 rounded-xl border p-4'
              >
                <h3 className='font-semibold'>{instance.name}</h3>
                <Badge variant={instance.draining ? 'warning' : 'secondary'}>
                  {instance.draining
                    ? t('New assignments stopped')
                    : t('Accepting new accounts')}
                </Badge>
                {instance.draining && (
                  <p className='text-muted-foreground text-sm'>
                    {t(
                      'Existing accounts and reserved orders continue. Migrate accounts from user management before removing this node.'
                    )}
                  </p>
                )}
                {instance.available && (
                  <div className='flex flex-col gap-2'>
                    <dl className='grid grid-cols-2 gap-3 text-sm'>
                      {[
                        [t('Total slots'), instance.slots.length],
                        [
                          t('Occupied slots'),
                          instance.slots.filter((slot) => !slot.empty).length,
                        ],
                        [
                          t('Empty slots'),
                          instance.slots.filter((slot) => slot.empty).length,
                        ],
                        [
                          t('Published inventory'),
                          instance.slots.filter((slot) => slot.enabled).length,
                        ],
                      ].map(([label, value]) => (
                        <div key={label}>
                          <dt className='text-muted-foreground'>{label}</dt>
                          <dd>{formatNumber(Number(value), locale)}</dd>
                        </div>
                      ))}
                    </dl>
                    <p className='text-muted-foreground text-xs'>
                      {t(
                        'Slot counts are snapshots, not available stock. Orders and account reservations are checked again during assignment.'
                      )}
                    </p>
                  </div>
                )}
                {!instance.available && (
                  <p className='text-destructive text-sm'>
                    {t('Instance unavailable')}
                  </p>
                )}
                {instance.available && instance.slots.length === 0 && (
                  <p className='text-muted-foreground text-sm'>
                    {t('No slots yet')}
                  </p>
                )}
                {instance.slots.map((slot) => (
                  <div
                    key={slot.id}
                    className='flex flex-wrap items-center justify-between gap-2 border-b pb-3 text-sm'
                  >
                    <div>
                      <p>
                        {slot.name || slot.id} ·{' '}
                        {slot.platform === 'claude' ? 'Claude' : 'GPT'}
                      </p>
                      <p className='text-muted-foreground text-xs'>
                        {slot.id} ·{' '}
                        {slot.exit_ready
                          ? t('Outbound connection ready')
                          : t('Outbound connection unavailable')}
                      </p>
                      <p className='text-muted-foreground text-xs'>
                        {slot.empty ? t('Empty slot') : t('Occupied slot')}
                      </p>
                    </div>
                    <Button
                      size='sm'
                      variant='outline'
                      disabled={
                        update.isPending ||
                        (!!instance.draining && !slot.enabled) ||
                        (!slot.managed && !slot.empty) ||
                        !instance.available
                      }
                      onClick={() =>
                        update.mutate({
                          instance_id: instance.id,
                          slot_id: slot.id,
                          enabled: !slot.enabled,
                        })
                      }
                    >
                      {slot.enabled
                        ? t('Stop new assignments')
                        : t('Allow self-service assignment')}
                    </Button>
                  </div>
                ))}
                {options.data?.instances
                  .filter(
                    (option) => option.id === instance.id && option.available
                  )
                  .map((option) => (
                    <SlotSetup
                      key={option.id}
                      instance={option}
                      createOnly
                      disabled={update.isPending}
                      onSlotUpdated={() =>
                        cache.invalidateQueries({ queryKey: prefix })
                      }
                    />
                  ))}
              </section>
            ))}
          <p className='text-muted-foreground text-xs'>
            {t(
              'To stop new assignments for a node, set draining to true in its server configuration and restart the hosting service. Keep the node registered while accounts or reservations remain.'
            )}
          </p>
        </div>
      </SheetContent>
    </Sheet>
  )
}
