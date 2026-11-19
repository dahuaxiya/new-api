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
import { Input } from '@/components/ui/input'
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
  type RetailSalePolicy,
  type RetailSupplyEdit,
} from '../retail-pricing-api'
import { RetailSupplyModelPicker } from './retail-supply-model-picker'

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
    Array<
      RetailGroupBinding & {
        rowID: string
        newRow?: boolean
        newCredential?: boolean
      }
    >
  >(() =>
    (props.state.bindings ?? []).map((binding) => ({
      ...binding,
      rowID: crypto.randomUUID(),
    }))
  )
  // Row identity survives edits/removal; UI-only IDs never enter the price contract.
  const mapping = bindings.map(
    ({ local_group, channel_id, upstream_group, follow_models }) => ({
      local_group,
      channel_id,
      upstream_group,
      ...(follow_models ? { follow_models: true } : {}),
    })
  )
  const [channels, setChannels] = useState(props.state.channels)
  const [supplies, setSupplies] = useState<Record<number, RetailSupplyEdit>>({})
  // Older servers expose registrations through saved routes only. Drafts never override saved ownership.
  const registered =
    props.state.credential_groups ??
    Object.fromEntries(
      (props.state.bindings ?? []).map((b) => [b.channel_id, b.upstream_group])
    )
  const [draftGroups, setDraftGroups] = useState<Record<number, string>>({})
  const [nextDraftID, setNextDraftID] = useState(-1)
  // Saved Keys can be paused even when no route uses them. Never submit an abandoned draft.
  const supplyEdits = Object.values(supplies).filter(
    (edit) =>
      edit.enabled !== undefined ||
      bindings.some((row) => row.channel_id === edit.id)
  )
  const sites = props.state.channels.filter(
    (channel) => channel.site_id === channel.id
  )

  /** Only dirty groups are sent. A blank existing Key retains the server credential. */
  function editSupply(id: number, patch: Partial<RetailSupplyEdit>): void {
    const channel = channels.find((item) => item.id === id)
    if (!channel) return
    setSupplies((previous) => ({
      ...previous,
      [id]: {
        ...(previous[id] ?? {
          id,
          site_id: channel.site_id,
          name: channel.name,
          models: channel.models,
          key: '',
        }),
        ...patch,
      },
    }))
  }

  /** Browser-only negative IDs are resolved by the server in the same transaction as the bindings. */
  function addSupply(siteID: number): void {
    const site = sites.find((item) => item.id === siteID)
    if (!site) return
    setChannels([
      ...channels,
      {
        ...site,
        id: nextDraftID,
        name: '',
        models: '',
        has_key: false,
        status: 2,
      },
    ])
    setSupplies({
      ...supplies,
      [nextDraftID]: {
        id: nextDraftID,
        site_id: siteID,
        name: '',
        key: '',
        models: '',
      },
    })
    setBindings([
      ...bindings,
      {
        local_group: '',
        channel_id: nextDraftID,
        newCredential: true,
        upstream_group: '',
        newRow: true,
        rowID: crypto.randomUUID(),
      },
    ])
    setNextDraftID(nextDraftID - 1)
  }
  const [enabled, setEnabled] = useState(props.state.sync.enabled)
  const [salePolicy, setSalePolicy] = useState<RetailSalePolicy>(() => ({
    ...props.state.sale_policy,
    receipt_ratio: props.state.sale_policy.receipt_ratio || '1',
  }))
  const initialPolicy = {
    ...props.state.sale_policy,
    receipt_ratio: props.state.sale_policy.receipt_ratio || '1',
  }
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
    supplyEdits.length > 0 ||
    JSON.stringify(mapping) !== JSON.stringify(props.state.bindings ?? []) ||
    enabled !== props.state.sync.enabled ||
    JSON.stringify(salePolicy) !== JSON.stringify(initialPolicy)
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
  const policyValid =
    /^(0|[1-9][0-9]{0,8})(\.[0-9]{1,8})?$/.test(salePolicy.receipt_ratio) &&
    Number(salePolicy.receipt_ratio) > 0 &&
    Number(salePolicy.receipt_ratio) <= 1 &&
    Number.isInteger(salePolicy.margin_bps) &&
    salePolicy.margin_bps >= 0 &&
    salePolicy.margin_bps <= 9500 &&
    Number.isInteger(salePolicy.fee_bps) &&
    salePolicy.fee_bps >= 0 &&
    salePolicy.fee_bps <= 5000 &&
    Number.isInteger(salePolicy.buffer_bps) &&
    salePolicy.buffer_bps >= 0 &&
    salePolicy.buffer_bps <= 10000
  const valid =
    policyValid &&
    !incomplete &&
    supplyEdits.every(
      (edit) =>
        edit.name.trim().length > 0 &&
        (!edit.key ||
          !channels.find((ch) => ch.id === edit.id)?.has_key ||
          (!!edit.confirm_key_group &&
            bindings.every(
              (b) =>
                b.channel_id !== edit.id ||
                b.upstream_group === edit.confirm_key_group
            )))
    ) &&
    !duplicates &&
    (!enabled || bindings.length > 0)
  const mutation = useMutation({
    retry: false,
    gcTime: 0,
    mutationFn: async (action: 'save' | 'sync') => {
      setFailure('')
      if (action === 'save') {
        await saveRetailGroupBindings(
          props.state.version,
          mapping,
          enabled,
          salePolicy,
          supplyEdits
        )
      } else {
        await syncRetailGroupBindings(props.state.version)
      }
    },
    onSettled: () => {
      // Keep non-secret drafts for retry, but never retain a submitted Key after an error.
      setSupplies((previous) =>
        Object.fromEntries(
          Object.entries(previous).map(([id, edit]) => [
            id,
            { ...edit, key: '' },
          ])
        )
      )
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
      className='shrink-0 space-y-4 rounded-lg border p-4'
      aria-label={t('Supply routes')}
    >
      <div className='flex flex-wrap items-start justify-between gap-3'>
        <div className='space-y-1'>
          <h3 className='font-medium'>{t('Group multiplier bindings')}</h3>
        </div>
        <Badge variant={healthy ? 'secondary' : 'outline'}>
          {props.state.sync.enabled
            ? t('Automatic following')
            : t('Not following')}
        </Badge>
      </div>
      <div className='bg-muted/50 space-y-3 rounded-md p-3'>
        <Field>
          <FieldLabel htmlFor='following-margin'>
            {t('Target margin (%)')}
          </FieldLabel>
          <Input
            id='following-margin'
            type='number'
            min={0}
            max={95}
            step='0.01'
            value={
              Number.isFinite(salePolicy.margin_bps)
                ? salePolicy.margin_bps / 100
                : ''
            }
            disabled={mutation.isPending}
            onChange={(event) =>
              setSalePolicy({
                ...salePolicy,
                margin_bps: Math.round(event.target.valueAsNumber * 100),
              })
            }
          />
        </Field>
        <details className='space-y-3'>
          <summary className='cursor-pointer text-xs'>
            {t('Fees, buffer and credit discount')}
          </summary>
          <FieldGroup className='grid gap-3 sm:grid-cols-3'>
            <Field>
              <FieldLabel htmlFor='following-receipts'>
                {t('Cash received per CNY of credit')}
              </FieldLabel>
              <Input
                id='following-receipts'
                inputMode='decimal'
                value={salePolicy.receipt_ratio}
                disabled={mutation.isPending}
                onChange={(event) =>
                  setSalePolicy({
                    ...salePolicy,
                    receipt_ratio: event.target.value,
                  })
                }
              />
            </Field>
            {(
              [
                { key: 'fee_bps', label: t('Payment fee (%)'), max: 50 },
                { key: 'buffer_bps', label: t('Cost buffer (%)'), max: 100 },
              ] as const
            ).map((field) => (
              <Field key={field.key}>
                <FieldLabel htmlFor={`following-${field.key}`}>
                  {field.label}
                </FieldLabel>
                <Input
                  id={`following-${field.key}`}
                  type='number'
                  min={0}
                  max={field.max}
                  step='0.01'
                  value={
                    Number.isFinite(salePolicy[field.key])
                      ? salePolicy[field.key] / 100
                      : ''
                  }
                  disabled={mutation.isPending}
                  onChange={(event) =>
                    setSalePolicy({
                      ...salePolicy,
                      [field.key]: Math.round(event.target.valueAsNumber * 100),
                    })
                  }
                />
              </Field>
            ))}
          </FieldGroup>
          <p className='text-muted-foreground text-xs'>
            {t(
              'The estimate includes credit discount, payment fees and cost buffer. Final charges use the server price book.'
            )}
          </p>
        </details>
      </div>
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
      {channels.length === 0 && (
        <p className='text-muted-foreground text-sm'>
          {t(
            'No approved Xingxing supply channel. Register its channel in the supply allowlist first.'
          )}
        </p>
      )}
      <div className='space-y-2'>
        {sites.map((site) => (
          <div
            key={site.id}
            className='bg-muted/40 space-y-3 rounded-md border p-3'
          >
            <div className='flex flex-wrap items-center justify-between gap-3'>
              <div className='min-w-0 space-y-1'>
                <p className='text-sm font-medium'>{site.name}</p>
                <p className='text-muted-foreground font-mono text-xs break-all'>
                  {site.base_url}
                </p>
              </div>
              <Button
                variant='outline'
                size='sm'
                disabled={
                  mutation.isPending ||
                  bindings.length >= 200 ||
                  channels.length >= 100
                }
                onClick={() => addSupply(site.id)}
              >
                {t('Add Key and bind group')}
              </Button>
            </div>
            <ul className='divide-y border-t'>
              {props.state.channels
                .filter((ch) => ch.site_id === site.id)
                .map((ch) => {
                  const draft = supplies[ch.id]
                  const savedEnabled = ch.status === 1
                  const keyEnabled = draft?.enabled ?? savedEnabled
                  const missingKeyOrModels =
                    (!ch.has_key && !draft?.key.trim()) ||
                    !(draft?.models ?? ch.models).trim()
                  return (
                    <li
                      key={ch.id}
                      className='flex flex-wrap items-center justify-between gap-2 pt-2 pb-1'
                    >
                      <div className='min-w-0 text-sm break-words'>
                        <span>
                          {ch.name} · #{ch.id}
                        </span>
                        {registered[ch.id] && (
                          <span className='text-muted-foreground'>
                            {' '}
                            · {registered[ch.id]}
                          </span>
                        )}
                      </div>
                      <Label className='flex items-center gap-2'>
                        {keyEnabled ? t('Enabled') : t('Disabled')}
                        <Switch
                          aria-label={`${t('Upstream API Key')}: ${ch.name} #${ch.id}`}
                          checked={keyEnabled}
                          disabled={
                            mutation.isPending ||
                            (!keyEnabled && missingKeyOrModels)
                          }
                          onCheckedChange={(value) =>
                            editSupply(ch.id, {
                              enabled:
                                value === savedEnabled ? undefined : value,
                            })
                          }
                        />
                      </Label>
                      {missingKeyOrModels && (
                        <p className='text-muted-foreground w-full text-xs'>
                          {t(
                            'Add a Key and at least one model before enabling it'
                          )}
                        </p>
                      )}
                    </li>
                  )
                })}
            </ul>
          </div>
        ))}
      </div>
      <div className='space-y-3'>
        {bindings.map((row, index) => {
          const channel = channels.find((item) => item.id === row.channel_id)
          const draft = supplies[row.channel_id]
          const rate = source?.groups.find(
            (group) => group.group === row.upstream_group
          )
          const saved = props.state.bindings?.find(
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
          let status = t('Paused: multiplier unavailable or expired')
          if (active) status = t('Following saved multiplier')
          // The sync record can outlive its binding. A draft toggle must not revive an old result.
          const modelState =
            row.follow_models && saved?.follow_models
              ? props.state.model_sync?.[`${row.channel_id}:${row.local_group}`]
              : undefined
          if (changed) status = t('Unsaved changes')
          return (
            <details
              key={row.rowID}
              open={row.newRow}
              className='space-y-3 rounded-md border p-3'
            >
              <summary className='cursor-pointer text-sm font-medium'>
                {row.local_group || t('Select group')} ←{' '}
                {row.upstream_group || t('Select upstream group')}
                {rate && (
                  <span className='text-muted-foreground ml-2'>
                    {t('Purchase multiplier')} {rate.multiplier}×
                  </span>
                )}
                {/* Display is approximate; server decimal arithmetic determines quotes and charges. */}
                {rate && policyValid && (
                  <output
                    className='ml-2 font-mono tabular-nums'
                    aria-label={`${t('Estimated selling multiplier')} ${index + 1}`}
                  >
                    {t('Estimated selling multiplier')}: ≈
                    {(
                      (Number(rate.multiplier) *
                        (1 + salePolicy.buffer_bps / 10000)) /
                      Number(salePolicy.receipt_ratio) /
                      (1 - salePolicy.fee_bps / 10000) /
                      (1 - salePolicy.margin_bps / 10000)
                    )
                      .toFixed(8)
                      .replace(/0+$/, '')
                      .replace(/\.$/, '')}
                    ×
                  </output>
                )}
                {(!active || changed) && (
                  <span className='text-muted-foreground ml-2'>{status}</span>
                )}
                {!!modelState?.pending?.length && (
                  <span className='text-muted-foreground ml-2'>
                    {t('Skipped on last sync')} ({modelState.pending.length})
                  </span>
                )}
              </summary>
              <FieldGroup
                className={
                  row.newCredential
                    ? 'grid gap-3 sm:grid-cols-2'
                    : 'grid gap-3 sm:grid-cols-3'
                }
              >
                <Field>
                  <FieldLabel htmlFor={`binding-local-${row.rowID}`}>
                    {t('Customer group')} {index + 1}
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
                {/* A new Key already belongs to the selected site. Offering #1 here would turn creation into a shared credential edit. */}
                {!row.newCredential && (
                  <Field>
                    <FieldLabel htmlFor={`binding-channel-${row.rowID}`}>
                      {t('Supply credential')} {index + 1}
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
                                  // Resolve both fields together so a quote cannot use the previous credential's rate.
                                  upstream_group:
                                    registered[Number(event.target.value)] ??
                                    draftGroups[Number(event.target.value)] ??
                                    '',
                                }
                              : item
                          )
                        )
                      }
                    >
                      <NativeSelectOption value=''>
                        {t('Select channel')}
                      </NativeSelectOption>
                      {row.channel_id !== 0 &&
                        !channels.some(
                          (channel) => channel.id === row.channel_id
                        ) && (
                          <NativeSelectOption value={row.channel_id}>
                            #{row.channel_id} · {t('Unavailable')}
                          </NativeSelectOption>
                        )}
                      {channels.map((channel) => (
                        <NativeSelectOption key={channel.id} value={channel.id}>
                          {supplies[channel.id]?.name ||
                            channel.name ||
                            t('New supply group')}{' '}
                          {channel.id > 0 ? `· #${channel.id}` : ''}
                        </NativeSelectOption>
                      ))}
                    </NativeSelect>
                  </Field>
                )}
                <Field>
                  <FieldLabel htmlFor={`binding-upstream-${row.rowID}`}>
                    {t('Upstream group')} {index + 1}
                  </FieldLabel>
                  <NativeSelect
                    id={`binding-upstream-${row.rowID}`}
                    className='w-full'
                    value={row.upstream_group}
                    disabled={
                      mutation.isPending ||
                      !row.channel_id ||
                      !!registered[row.channel_id]
                    }
                    onChange={(event) => {
                      const upstream = event.target.value
                      setDraftGroups({
                        ...draftGroups,
                        [row.channel_id]: upstream,
                      })
                      setBindings(
                        bindings.map((item) =>
                          item.channel_id === row.channel_id
                            ? { ...item, upstream_group: upstream }
                            : item
                        )
                      )
                      // Use the upstream label as the initial local remark; preserve an operator's rename.
                      if (
                        row.channel_id < 0 &&
                        (!draft?.name || draft.name === row.upstream_group)
                      ) {
                        editSupply(row.channel_id, { name: upstream })
                      }
                    }}
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
              {channel && (
                <FieldGroup className='grid gap-3 sm:grid-cols-2'>
                  <Field>
                    <FieldLabel htmlFor={`supply-name-${row.rowID}`}>
                      {t('Credential label')} {index + 1}
                    </FieldLabel>
                    <Input
                      id={`supply-name-${row.rowID}`}
                      value={draft?.name ?? channel.name}
                      maxLength={120}
                      disabled={mutation.isPending}
                      onChange={(event) =>
                        editSupply(channel.id, { name: event.target.value })
                      }
                    />
                  </Field>
                  {(!row.newRow || row.newCredential || !channel.has_key) && (
                    <Field>
                      <FieldLabel htmlFor={`supply-key-${row.rowID}`}>
                        {t('Upstream API Key')} {index + 1}
                      </FieldLabel>
                      <Input
                        id={`supply-key-${row.rowID}`}
                        type='password'
                        autoComplete='new-password'
                        spellCheck={false}
                        maxLength={4096}
                        value={draft?.key ?? ''}
                        placeholder={t(
                          channel.has_key
                            ? 'Configured; leave blank to keep'
                            : 'Key for the selected upstream group'
                        )}
                        disabled={mutation.isPending}
                        onChange={(event) =>
                          editSupply(channel.id, {
                            key: event.target.value,
                            confirm_key_group: undefined,
                          })
                        }
                      />
                    </Field>
                  )}
                  {!row.newRow && channel.has_key && draft?.key && (
                    <label className='text-sm sm:col-span-2'>
                      <input
                        type='checkbox'
                        disabled={!row.upstream_group || mutation.isPending}
                        checked={
                          !!row.upstream_group &&
                          draft.confirm_key_group === row.upstream_group
                        }
                        onChange={(event) =>
                          editSupply(channel.id, {
                            confirm_key_group: event.target.checked
                              ? row.upstream_group
                              : undefined,
                          })
                        }
                      />{' '}
                      {t('The replacement Key belongs to this upstream group')}:{' '}
                      {row.upstream_group}
                    </label>
                  )}
                  <Field className='sm:col-span-2'>
                    <RetailSupplyModelPicker
                      key={channel.id}
                      channel={channel}
                      draftKey={draft?.key ?? ''}
                      value={draft?.models ?? channel.models}
                      label={`${t('Allowed models')} ${index + 1}`}
                      following={bindings.some(
                        (other) =>
                          other.channel_id === channel.id && other.follow_models
                      )}
                      disabled={mutation.isPending}
                      onChange={(models) => editSupply(channel.id, { models })}
                    />
                  </Field>
                </FieldGroup>
              )}
              <div className='space-y-1 text-sm'>
                <div className='flex items-center gap-2'>
                  <Switch
                    id={`follow-models-${row.rowID}`}
                    checked={!!row.follow_models}
                    disabled={mutation.isPending}
                    onCheckedChange={(on) =>
                      setBindings(
                        bindings.map((item, i) =>
                          i === index ? { ...item, follow_models: on } : item
                        )
                      )
                    }
                  />
                  <Label htmlFor={`follow-models-${row.rowID}`}>
                    {t('Follow this supply group’s models')}
                  </Label>
                </div>
                {row.follow_models && (
                  <p className='text-muted-foreground text-xs'>
                    {t(
                      'Every 10 minutes while sales are enabled. Missing prices need review; two complete reads confirm removals.'
                    )}
                  </p>
                )}
                {modelState?.failed && (
                  <p role='status'>
                    {t('Model list unavailable. Previous routes retained.')}
                  </p>
                )}
                {!!modelState?.pending?.length && (
                  <details>
                    <summary className='cursor-pointer text-sm'>
                      {t('Skipped on last sync')} ({modelState.pending.length})
                    </summary>
                    <p className='text-muted-foreground mt-2 text-xs'>
                      {t(
                        'Check the official price, model ID and call type before publishing these models.'
                      )}
                    </p>
                    <ul className='mt-2 list-inside list-disc text-xs'>
                      {modelState.pending.map((name) => (
                        <li key={name}>{name}</li>
                      ))}
                    </ul>
                  </details>
                )}
                {!!modelState?.paused?.length && (
                  <p>
                    {t('Paused on this route')}: {modelState.paused.join(', ')}
                  </p>
                )}
              </div>
              <div className='flex flex-wrap items-center justify-between gap-2 text-xs'>
                <Button
                  variant='ghost'
                  size='sm'
                  disabled={mutation.isPending}
                  onClick={() => {
                    setBindings(bindings.filter((_, i) => i !== index))
                    if (
                      !bindings.some(
                        (other, i) =>
                          i !== index && other.channel_id === row.channel_id
                      )
                    ) {
                      // Removing the last reference also removes its unsaved credential from memory.
                      if (row.channel_id < 0) {
                        // An abandoned credential has no server ID and must not remain available for reuse.
                        setChannels(
                          channels.filter((item) => item.id !== row.channel_id)
                        )
                      }
                      setSupplies((previous) => {
                        const remaining = { ...previous }
                        delete remaining[row.channel_id]
                        return remaining
                      })
                    }
                  }}
                >
                  {t('Remove binding')} {index + 1}
                </Button>
              </div>
            </details>
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
              newRow: true,
              rowID: crypto.randomUUID(),
            },
          ])
        }
      >
        {t('Reuse an existing Key')}
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
          {t('Save bindings and margin')}
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
        title={t('Save bindings and margin?')}
        desc={t(
          'Save supply credentials, model permissions, group bindings and margin together. Existing Keys stay unchanged when blank. This does not enable sales.'
        )}
        confirmText={t('Confirm save')}
        disabled={!valid}
        isLoading={mutation.isPending}
        handleConfirm={() => mutation.mutate('save')}
      />
    </section>
  )
}
