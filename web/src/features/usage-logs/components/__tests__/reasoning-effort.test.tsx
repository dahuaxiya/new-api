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
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  getCoreRowModel,
  useReactTable,
  type VisibilityState,
} from '@tanstack/react-table'
import { act, cleanup, render, screen, within } from '@testing-library/react'
import i18next from 'i18next'
import { afterEach, expect, test } from 'vitest'

import { DataTableView } from '@/components/data-table'
import zh from '@/i18n/locales/zh.json'

import { usageLogSchema, type UsageLog } from '../../data/schema'
import { useCommonLogsColumns } from '../columns/common-logs-columns'
import { UsageLogsMobileList } from '../usage-logs-mobile-card'
import { UsageLogsProvider } from '../usage-logs-provider'

const clients: QueryClient[] = []

/** 创建最小消费记录；模型名故意带 high，防止界面用模型名猜测缺失强度。 */
function makeLog(other: string, type = 2): UsageLog {
  return usageLogSchema.parse({
    id: 1,
    user_id: 1,
    created_at: 1788840000,
    type,
    content: '',
    model_name: 'model-high',
    other,
  })
}

/** 使用真实列和两种列表布局，验证新增字段实际出现在用户可见列表中。 */
function LogsFixture(props: {
  log: UsageLog
  admin?: boolean
  mobile?: boolean
  visibility?: VisibilityState
}) {
  const columns = useCommonLogsColumns(props.admin ?? false, false)
  const table = useReactTable({
    data: [props.log],
    columns,
    getCoreRowModel: getCoreRowModel(),
    state: { columnVisibility: props.visibility ?? {} },
  })
  if (props.mobile) {
    return <UsageLogsMobileList table={table} logCategory='common' />
  }
  return <DataTableView table={table} />
}

/** 缓存无关网络数据，保持真实列渲染、日志解析和权限布局。 */
function renderLogs(props: Parameters<typeof LogsFixture>[0]) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  clients.push(client)
  const updatedAt = Date.now() + 60_000
  client.setQueryData(['status'], {}, { updatedAt })
  client.setQueryData(['pricing'], { data: [], vendors: [] }, { updatedAt })
  return render(
    <QueryClientProvider client={client}>
      <UsageLogsProvider>
        <LogsFixture {...props} />
      </UsageLogsProvider>
    </QueryClientProvider>
  )
}

afterEach(async () => {
  cleanup()
  for (const client of clients) client.clear()
  clients.length = 0
  await i18next.changeLanguage('en')
  localStorage.clear()
})

test.each([false, true])(
  'shows recorded reasoning beside the model in the table (admin: %s)',
  (admin) => {
    renderLogs({ admin, log: makeLog('{"reasoning_effort":"xhigh"}') })
    const headers = screen
      .getAllByRole('columnheader')
      .map((el) => el.textContent)
    expect(headers[headers.indexOf('Model') + 1]).toBe('Reasoning Effort')
    expect(screen.getByRole('cell', { name: 'xhigh' })).toBeVisible()
  }
)

test.each(['', '{}', '{"reasoning_effort":"  "}', '{"reasoning_effort":42}'])(
  'shows not recorded for missing or invalid effort without guessing from the model (%s)',
  (other) => {
    renderLogs({ log: makeLog(other) })
    expect(screen.getByRole('cell', { name: 'Not recorded' })).toBeVisible()
    expect(screen.queryByRole('cell', { name: 'high' })).not.toBeInTheDocument()
  }
)

test.each(['none', 'ultra'])(
  'preserves the explicit recorded value %s',
  (effort) => {
    renderLogs({ log: makeLog(JSON.stringify({ reasoning_effort: effort })) })
    expect(screen.getByRole('cell', { name: effort })).toBeVisible()
    expect(screen.queryByText('Not recorded')).not.toBeInTheDocument()
  }
)

test('does not label unrelated audit logs with a reasoning effort', () => {
  renderLogs({ log: makeLog('{"reasoning_effort":"high"}', 3) })
  expect(screen.queryByRole('cell', { name: 'high' })).not.toBeInTheDocument()
  expect(screen.queryByText('Not recorded')).not.toBeInTheDocument()
})

test('shows reasoning on mobile and respects the hidden-column preference', () => {
  const log = makeLog('{"reasoning_effort":"high"}')
  const view = renderLogs({ log, mobile: true })
  expect(screen.getByText('Reasoning Effort')).toBeVisible()
  expect(screen.getByText('high')).toBeVisible()
  view.unmount()
  renderLogs({ log, mobile: true, visibility: { reasoning_effort: false } })
  expect(screen.queryByText('Reasoning Effort')).not.toBeInTheDocument()
  expect(screen.queryByText('high')).not.toBeInTheDocument()
})

test('updates the reasoning header and missing-value label when the language changes', async () => {
  i18next.addResourceBundle('zh', 'translation', zh.translation)
  renderLogs({ log: makeLog('{}') })
  await act(async () => {
    await i18next.changeLanguage('zh')
  })
  const table = within(screen.getByRole('table'))
  expect(table.getByRole('columnheader', { name: '推理强度' })).toBeVisible()
  expect(table.getByRole('cell', { name: '未记录' })).toBeVisible()
})
