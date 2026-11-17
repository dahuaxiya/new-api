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
import { useQuery } from '@tanstack/react-query'
import { useState } from 'react'
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
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { api } from '@/lib/api'
import { requireServerSuccess } from '@/lib/server-error-message'
import { useAuthStore } from '@/stores/auth-store'

import { RetailSupplyGroupForm } from './retail-supply-group-form'

type SupplyOverview = {
  state: string
  group: string
  items: Array<{
    site_id?: number
    base_url?: string
    models?: string
    channel_id: number
    name: string
    kind: string
    status: string
  }>
}

/** 超级管理员打开抽屉后读取供货资格快照；缓存按登录会话隔离。 */
export function RetailSupplies(
  props: { onGroupsChanged?: () => void; readOnly?: boolean } = {}
) {
  const { t } = useTranslation()
  const [open, setOpen] = useState(false)
  const [addingSite, setAddingSite] = useState<number | null>(null)
  const identity = useAuthStore(
    (s) => `${s.auth.user?.id}:${s.auth.session?.sid}`
  )
  const query = useQuery({
    queryKey: ['retail-supplies', identity],
    queryFn: async ({ signal }) => {
      const response = await api.get<{
        success: boolean
        data: SupplyOverview
      }>('/api/retail/admin/supplies', { signal })
      return requireServerSuccess(response.data).data
    },
    enabled: open,
    retry: false,
  })
  const roots = query.data?.items.filter((supply) => !supply.site_id) ?? []
  const stateLabels: Record<string, string> = {
    ready: t('Supply routing configured'),
    disabled: t('Retail sales disabled'),
    unsupported_runtime: t('Redis or batch quota updates block retail sales'),
    group_missing: t('Sales group ratio is missing'),
    no_eligible_supply: t('No eligible supply channels'),
  }
  const supplyLabels: Record<string, string> = {
    eligible: t('Eligible for routing'),
    disabled: t('Channel disabled'),
    missing: t('Channel not found'),
    excluded: t('Excluded by supply isolation rules'),
    not_evaluated: t('Not evaluated while sales are disabled'),
  }
  return (
    <>
      <Button variant='outline' onClick={() => setOpen(true)}>
        {t('Supply overview')}
      </Button>
      <Sheet
        open={open}
        onOpenChange={(value) => {
          setOpen(value)
          if (!value) setAddingSite(null)
        }}
      >
        <SheetContent className={sideDrawerContentClassName('sm:max-w-2xl')}>
          <SheetHeader className={sideDrawerHeaderClassName()}>
            <SheetTitle>{t('Supply overview')}</SheetTitle>
            <SheetDescription>
              {t(
                'Routing eligibility does not verify upstream connectivity, remaining quota or model availability.'
              )}
            </SheetDescription>
          </SheetHeader>
          <div className='flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto p-6'>
            <Button
              variant='outline'
              disabled={query.isFetching}
              onClick={() => void query.refetch()}
            >
              {t('Refresh')}
            </Button>
            {query.isPending && (
              <LoadingState inline message={t('Loading...')} />
            )}
            {query.isError && (
              <ErrorState
                title={t('Unable to load resources')}
                onRetry={() => void query.refetch()}
              />
            )}
            {!query.isError && query.data && (
              <>
                <Badge
                  variant={
                    query.data.state === 'ready' ? 'secondary' : 'warning'
                  }
                >
                  {stateLabels[query.data.state] ?? t('Unknown')}
                </Badge>
                {query.data.items.length === 0 && (
                  <EmptyState title={t('No supply channels registered')} />
                )}
                {roots.map((supply) => (
                  <Card key={supply.channel_id} size='sm' className='shrink-0'>
                    <CardHeader>
                      <CardTitle>
                        {supply.name || `#${supply.channel_id}`}
                      </CardTitle>
                      {supply.base_url && (
                        <p className='text-muted-foreground text-xs break-all'>
                          {supply.base_url}
                        </p>
                      )}
                    </CardHeader>
                    <CardContent className='flex flex-col gap-2'>
                      <p>
                        {supply.kind === 'subscription'
                          ? t('Platform subscription supply')
                          : t('Reseller supply')}
                      </p>
                      <Badge
                        variant={
                          supply.status === 'eligible' ? 'outline' : 'warning'
                        }
                      >
                        {supplyLabels[supply.status] ?? t('Unknown')}
                      </Badge>
                      {query.data.items
                        .filter((child) => child.site_id === supply.channel_id)
                        .map((child) => (
                          <div
                            key={child.channel_id}
                            className='flex items-center justify-between gap-3 rounded-md border px-3 py-2 text-sm'
                          >
                            <span>
                              {child.name}{' '}
                              <span className='text-muted-foreground'>
                                #{child.channel_id}
                              </span>
                            </span>
                            <Badge
                              variant={
                                child.status === 'eligible'
                                  ? 'outline'
                                  : 'warning'
                              }
                            >
                              {supplyLabels[child.status] ?? t('Unknown')}
                            </Badge>
                          </div>
                        ))}
                      {!props.readOnly &&
                        supply.kind === 'reseller' &&
                        supply.base_url &&
                        (addingSite === supply.channel_id ? (
                          <RetailSupplyGroupForm
                            siteID={supply.channel_id}
                            models={supply.models ?? ''}
                            onCancel={() => setAddingSite(null)}
                            onCreated={() => {
                              setAddingSite(null)
                              void query.refetch()
                              props.onGroupsChanged?.()
                            }}
                          />
                        ) : (
                          <Button
                            variant='outline'
                            className='self-start'
                            onClick={() => setAddingSite(supply.channel_id)}
                          >
                            {t('Add supply group')}
                          </Button>
                        ))}
                    </CardContent>
                  </Card>
                ))}
              </>
            )}
            <p className='text-muted-foreground text-sm'>
              {t(
                'One address per upstream site. Supply groups have separate Keys and models; bind their channel IDs in group pricing. Edit the site channel to change the shared address.'
              )}
            </p>
          </div>
        </SheetContent>
      </Sheet>
    </>
  )
}
