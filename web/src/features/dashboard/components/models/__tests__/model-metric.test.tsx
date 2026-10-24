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
import { fireEvent, render, screen } from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'

import { ModelCharts } from '../model-charts'

// jsdom 没有 Canvas；仅替代外部绘制边界，指标聚合和切换控件使用真实实现。
vi.mock('@visactor/react-vchart', () => ({
  VChart: (props: { spec: { title: { text: string } } }) => (
    <div role='img' aria-label={props.spec.title.text} />
  ),
}))
vi.mock('@visactor/vchart', () => ({
  ThemeManager: { setCurrentTheme: () => {} },
}))

afterEach(() => {
  localStorage.clear()
  vi.restoreAllMocks()
})

const data = [
  {
    created_at: 1792886400,
    model_name: 'test-model',
    count: 3,
    token_used: 1234567,
    quota: 0,
  },
]

/** 指标切换必须同步更新总数、图标题和选中态，并保留用户选中的排行视图。 */
it('switches between token and request analytics without losing the selected chart', async () => {
  render(<ModelCharts data={data} />)
  expect(
    await screen.findByRole('img', { name: 'Token Usage Trend' })
  ).toBeInTheDocument()
  expect(screen.getByText('1,234,567 Tokens')).toBeInTheDocument()
  expect(
    screen.getByRole('button', { name: 'Token Usage', pressed: true })
  ).toBeInTheDocument()
  fireEvent.click(screen.getByRole('button', { name: 'Token Usage Ranking' }))
  expect(
    await screen.findByRole('img', { name: 'Token Usage Ranking' })
  ).toBeInTheDocument()
  fireEvent.click(screen.getByRole('button', { name: 'Call Count' }))
  expect(screen.getByText('3')).toBeInTheDocument()
  expect(
    await screen.findByRole('img', { name: 'Call Count Ranking' })
  ).toBeInTheDocument()
  expect(
    screen.getByRole('button', { name: 'Call Count', pressed: true })
  ).toBeInTheDocument()
})

/** 再次点击当前指标不能取消选择；范围加载时不能继续显示上一范围的总量。 */
it('retains the metric selection and clears previous totals while a range loads', async () => {
  const view = render(<ModelCharts data={data} />)
  await screen.findByRole('img', { name: 'Token Usage Trend' })
  fireEvent.click(screen.getByRole('button', { name: 'Token Usage' }))
  expect(
    screen.getByRole('button', { name: 'Token Usage', pressed: true })
  ).toBeInTheDocument()
  view.rerender(<ModelCharts data={data} loading />)
  expect(screen.queryByText('1,234,567 Tokens')).not.toBeInTheDocument()
  view.rerender(<ModelCharts data={[{ ...data[0], token_used: 42 }]} />)
  expect(screen.getByText('42 Tokens')).toBeInTheDocument()
})
