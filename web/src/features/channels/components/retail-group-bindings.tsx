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
import { useMutation, useQuery } from '@tanstack/react-query'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { StaticDataTable } from '@/components/data-table'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Field, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Label } from '@/components/ui/label'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { Switch } from '@/components/ui/switch'
import { toIntlLocale } from '@/i18n/languages'
import { handleServerError } from '@/lib/handle-server-error'
import { getServerErrorMessage } from '@/lib/server-error-message'

import {
  getRetailGroupBindings,
  getRetailPublicRates,
  saveRetailGroupBindings,
  syncRetailGroupBindings,
  type RetailBindingState,
  type RetailGroupBinding,
} from '../retail-pricing-api'

/** Root-only configuration, isolated by session and refreshed after explicit writes. */
export function RetailGroupBindings(props: {
  identity: string
  onSaved: () => void
}) {
  const { t } = useTranslation()
  const query = useQuery({
    queryKey: ['retail-group-bindings', props.identity],
    queryFn: ({ signal }) => getRetailGroupBindings(signal),
    retry: false,
    refetchOnWindowFocus: false,
  })
  if (query.isPending) {
    return <LoadingState inline message={t('Loading group bindings...')} />
  }
  if (query.isError || !query.data?.local_groups) {
    return (
      <ErrorState
        title={t('Unable to load group bindings')}
        onRetry={() => void query.refetch()}
      />
    )
  }
  return (
    <BindingEditor
      key={`${props.identity}:${query.dataUpdatedAt}`}
      state={query.data}
      identity={props.identity}
      onSaved={() => {
        void query.refetch()
        props.onSaved()
      }}
      onReload={() => void query.refetch()}
    />
  )
}

/** Keep selections local until confirmation; source reads never select or save a credential mapping. */
function BindingEditor(props: {
  state: RetailBindingState
  identity: string
  onSaved: () => void
  onReload: () => void
}) {
  const { t, i18n } = useTranslation()
  const [bindings, setBindings] = useState<
    Array<RetailGroupBinding & { rowID: string }>
  >(() =>
    (props.state.bindings ?? []).map((binding) => ({
      ...binding,
      rowID: crypto.randomUUID(),
    }))
  )
  // Row identity survives edits/removal; UI-only IDs never enter the price contract.
  const mapping = bindings.map(
    ({ local_group, channel_id, upstream_group }) => ({
      local_group,
      channel_id,
      upstream_group,
    })
  )
  const [enabled, setEnabled] = useState(props.state.sync.enabled)
  const [confirm, setConfirm] = useState(false)
  const [failure, setFailure] = useState('')
  const feed = useQuery({
    queryKey: ['retail-binding-source', props.identity],
    queryFn: ({ signal }) => getRetailPublicRates(signal),
    enabled: false,
    retry: false,
    gcTime: 0,
    meta: { errorToast: false },
  })
  const committed = props.state.sync.snapshot
  // Saving re-reads the feed on the server; an earlier preview must not hide that newer result.
  const source =
    committed && committed.fetched_at >= (feed.data?.fetched_at ?? 0)
      ? committed
      : (feed.data ?? committed)
  const changed =
    JSON.stringify(mapping) !== JSON.stringify(props.state.bindings ?? []) ||
    enabled !== props.state.sync.enabled
  const incomplete = bindings.some(
    (row) => !row.local_group || !row.channel_id || !row.upstream_group
  )
  const duplicates = bindings.some((row, index) =>
    bindings.some(
      (other, otherIndex) =>
        index !== otherIndex &&
        row.channel_id === other.channel_id &&
        (row.local_group === other.local_group ||
          row.upstream_group !== other.upstream_group)
    )
  )
  const valid = !incomplete && !duplicates && (!enabled || bindings.length > 0)
  const mutation = useMutation({
    mutationFn: async (action: 'save' | 'sync') => {
      setFailure('')
      if (action === 'save') {
        await saveRetailGroupBindings(props.state.version, mapping, enabled)
      } else {
        await syncRetailGroupBindings(props.state.version)
      }
    },
    onSuccess: () => {
      setConfirm(false)
      toast.success(t('Saved successfully'))
      props.onSaved()
    },
    onError: (error) => {
      setConfirm(false)
      setFailure(getServerErrorMessage(error))
      handleServerError(error)
      if (!changed) props.onSaved()
    },
  })
  const dateFormat = new Intl.DateTimeFormat(
    toIntlLocale(i18n.resolvedLanguage || i18n.language),
    { dateStyle: 'short', timeStyle: 'medium' }
  )
  const now = Math.max(props.state.server_time, Math.floor(Date.now() / 1000))
  const healthy =
    props.state.sync.enabled &&
    !props.state.sync.last_error &&
    committed &&
    Math.min(committed.updated_at, committed.fetched_at) + 300 > now
  return (
    <section
      className='space-y-4 rounded-lg border p-4'
      aria-label={t('Group multiplier bindings')}
    >
      <div className='flex flex-wrap items-start justify-between gap-3'>
        <div className='space-y-1'>
          <h3 className='font-medium'>{t('Group multiplier bindings')}</h3>
          <p className='text-muted-foreground text-xs'>
            {t('Local group → supply channel → Xingxing group')}
          </p>
        </div>
        <Badge variant={healthy ? 'secondary' : 'outline'}>
          {props.state.sync.enabled
            ? t('Automatic following')
            : t('Not following')}
        </Badge>
      </div>
      <p className='text-muted-foreground text-sm'>
        {t(
          'Bind the group used by this channel Key. This changes pricing only; it cannot change the upstream Key assignment.'
        )}
      </p>
      {!props.state.pricing_ready && (
        <p className='bg-muted rounded-md p-3 text-sm'>
          {t(
            'Bindings can be saved now. Configure official models and product routes before accepting priced requests.'
          )}
        </p>
      )}
      <div className='flex flex-wrap gap-2'>
        <Button
          variant='outline'
          size='sm'
          disabled={feed.isFetching || mutation.isPending}
          onClick={() => void feed.refetch()}
        >
          {t('Load upstream groups')}
        </Button>
        <Button
          variant='ghost'
          size='sm'
          disabled={mutation.isPending}
          onClick={props.onReload}
        >
          {t('Reload saved bindings')}
        </Button>
      </div>
      {feed.isFetching && <LoadingState inline message={t('Loading...')} />}
      {feed.isError && (
        <p role='alert' className='text-destructive text-sm'>
          {t('Unable to read upstream public multipliers')}
        </p>
      )}
      {props.state.channels.length === 0 && (
        <p className='text-muted-foreground text-sm'>
          {t(
            'No approved Xingxing supply channel. Register its channel in the supply allowlist first.'
          )}
        </p>
      )}
      <div className='space-y-3'>
        {bindings.map((row, index) => {
          const rate = source?.groups.find(
            (group) => group.group === row.upstream_group
          )
          const saved = props.state.bindings?.some(
            (binding) =>
              binding.local_group === row.local_group &&
              binding.channel_id === row.channel_id &&
              binding.upstream_group === row.upstream_group
          )
          const active =
            healthy &&
            committed?.groups.some(
              (group) => group.group === row.upstream_group
            ) &&
            saved
          return (
            <div key={row.rowID} className='space-y-3 rounded-md border p-3'>
              <FieldGroup className='grid gap-3 sm:grid-cols-3'>
                <Field>
                  <FieldLabel htmlFor={`binding-local-${row.rowID}`}>
                    {t('Local group')} {index + 1}
                  </FieldLabel>
                  <NativeSelect
                    id={`binding-local-${row.rowID}`}
                    className='w-full'
                    value={row.local_group}
                    disabled={mutation.isPending}
                    onChange={(event) =>
                      setBindings(
                        bindings.map((item, i) =>
                          i === index
                            ? { ...item, local_group: event.target.value }
                            : item
                        )
                      )
                    }
                  >
                    <NativeSelectOption value=''>
                      {t('Select group')}
                    </NativeSelectOption>
                    {[
                      ...new Set([
                        ...props.state.local_groups,
                        ...(row.local_group ? [row.local_group] : []),
                      ]),
                    ].map((group) => (
                      <NativeSelectOption key={group} value={group}>
                        {group}
                      </NativeSelectOption>
                    ))}
                  </NativeSelect>
                </Field>
                <Field>
                  <FieldLabel htmlFor={`binding-channel-${row.rowID}`}>
                    {t('Supply channel')} {index + 1}
                  </FieldLabel>
                  <NativeSelect
                    id={`binding-channel-${row.rowID}`}
                    className='w-full'
                    value={row.channel_id || ''}
                    disabled={mutation.isPending}
                    onChange={(event) =>
                      setBindings(
                        bindings.map((item, i) =>
                          i === index
                            ? {
                                ...item,
                                channel_id: Number(event.target.value),
                              }
                            : item
                        )
                      )
                    }
                  >
                    <NativeSelectOption value=''>
                      {t('Select channel')}
                    </NativeSelectOption>
                    {row.channel_id > 0 &&
                      !props.state.channels.some(
                        (channel) => channel.id === row.channel_id
                      ) && (
                        <NativeSelectOption value={row.channel_id}>
                          #{row.channel_id} · {t('Unavailable')}
                        </NativeSelectOption>
                      )}
                    {props.state.channels.map((channel) => (
                      <NativeSelectOption key={channel.id} value={channel.id}>
                        {channel.name} · #{channel.id}
                      </NativeSelectOption>
                    ))}
                  </NativeSelect>
                </Field>
                <Field>
                  <FieldLabel htmlFor={`binding-upstream-${row.rowID}`}>
                    {t('Upstream group')} {index + 1}
                  </FieldLabel>
                  <NativeSelect
                    id={`binding-upstream-${row.rowID}`}
                    className='w-full'
                    value={row.upstream_group}
                    disabled={mutation.isPending}
                    onChange={(event) =>
                      setBindings(
                        bindings.map((item, i) =>
                          i === index
                            ? { ...item, upstream_group: event.target.value }
                            : item
                        )
                      )
                    }
                  >
                    <NativeSelectOption value=''>
                      {t('Select group')}
                    </NativeSelectOption>
                    {row.upstream_group && !rate && (
                      <NativeSelectOption value={row.upstream_group}>
                        {row.upstream_group} · {t('Unavailable')}
                      </NativeSelectOption>
                    )}
                    {source?.groups.map((group) => (
                      <NativeSelectOption key={group.group} value={group.group}>
                        {group.group} · {group.multiplier}×
                      </NativeSelectOption>
                    ))}
                  </NativeSelect>
                </Field>
              </FieldGroup>
              <div className='flex flex-wrap items-center justify-between gap-2 text-xs'>
                <span className='text-muted-foreground'>
                  {active
                    ? t('Following saved multiplier')
                    : t('Paused: multiplier unavailable or expired')}
                </span>
                <Button
                  variant='ghost'
                  size='sm'
                  disabled={mutation.isPending}
                  onClick={() =>
                    setBindings(bindings.filter((_, i) => i !== index))
                  }
                >
                  {t('Remove binding')} {index + 1}
                </Button>
              </div>
            </div>
          )
        })}
        {bindings.length === 0 && (
          <p className='text-muted-foreground py-2 text-sm'>
            {t(
              'No group bindings yet. Add one and choose the corresponding upstream group.'
            )}
          </p>
        )}
      </div>
      <Button
        variant='outline'
        size='sm'
        disabled={mutation.isPending || bindings.length >= 200}
        onClick={() =>
          setBindings([
            ...bindings,
            {
              local_group: '',
              channel_id: 0,
              upstream_group: '',
              rowID: crypto.randomUUID(),
            },
          ])
        }
      >
        {t('Add group binding')}
      </Button>
      {duplicates && (
        <p role='alert' className='text-destructive text-sm'>
          {t(
            'Each local group and channel pair must be unique. One channel Key can bind only one upstream group.'
          )}
        </p>
      )}
      <div className='flex items-center gap-2'>
        <Switch
          id='binding-auto-sync'
          checked={enabled}
          disabled={mutation.isPending}
          onCheckedChange={setEnabled}
        />
        <Label htmlFor='binding-auto-sync'>
          {t('Automatically follow upstream multipliers')}
        </Label>
      </div>
      <p className='text-muted-foreground text-xs leading-relaxed'>
        {t(
          'Sync every minute. Bound routes pause on failure, missing groups or data older than five minutes. Selling prices keep your margin and user price limits. Changes between polls cannot be detected immediately.'
        )}
      </p>
      {committed && (
        <p className='text-muted-foreground text-xs'>
          {t('Last successful read')}:{' '}
          {dateFormat.format(new Date(committed.fetched_at * 1000))} ·{' '}
          {t('Upstream update time')}:{' '}
          {dateFormat.format(new Date(committed.updated_at * 1000))}
        </p>
      )}
      {props.state.sync.last_error && (
        <p role='alert' className='text-destructive text-sm'>
          {t(props.state.sync.last_error)}
        </p>
      )}
      {failure && (
        <p role='alert' className='text-destructive text-sm'>
          {failure}
        </p>
      )}
      <div className='flex flex-wrap gap-2'>
        <Button
          disabled={!changed || !valid || mutation.isPending}
          onClick={() => setConfirm(true)}
        >
          {t('Save bindings')}
        </Button>
        <Button
          variant='outline'
          disabled={changed || !props.state.sync.enabled || mutation.isPending}
          onClick={() => mutation.mutate('sync')}
        >
          {t('Sync bound multipliers now')}
        </Button>
      </div>
      {source && (
        <details>
          <summary className='text-muted-foreground cursor-pointer text-xs'>
            {t('Available upstream multipliers')}
          </summary>
          <StaticDataTable
            data={source.groups}
            getRowKey={(row) => row.group}
            columns={[
              {
                id: 'group',
                header: t('Upstream group'),
                cell: (row) => row.group,
              },
              {
                id: 'rate',
                header: t('Public multiplier'),
                cell: (row) => (
                  <span className='font-mono'>{row.multiplier}×</span>
                ),
              },
            ]}
          />
        </details>
      )}
      <ConfirmDialog
        open={confirm}
        onOpenChange={setConfirm}
        title={t('Save group bindings?')}
        desc={t(
          'Only select the group actually used by this upstream Key. New requests use the saved mapping; disabling following pauses bound routes.'
        )}
        confirmText={t('Confirm save')}
        disabled={!valid}
        isLoading={mutation.isPending}
        handleConfirm={() => mutation.mutate('save')}
      />
    </section>
  )
}
