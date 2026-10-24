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
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
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
import { handleServerError } from '@/lib/handle-server-error'
import { useAuthStore } from '@/stores/auth-store'

/** 资源工具留在用户页抽屉，创建空槽、选择预置出口和发布库存复用现有受限协议。 */
export function HostingResources(props: {
  open: boolean
  onOpenChange: (value: boolean) => void
}) {
  const { t } = useTranslation()
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
              'Prepare empty slots and outbound connections here. Users sign in to their own GPT accounts.'
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
          {inventory.data?.instances.map((instance) => (
            <section
              key={instance.id}
              className='space-y-4 rounded-xl border p-4'
            >
              <h3 className='font-semibold'>{instance.name}</h3>
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
                    <p>{slot.name || slot.id}</p>
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
        </div>
      </SheetContent>
    </Sheet>
  )
}
