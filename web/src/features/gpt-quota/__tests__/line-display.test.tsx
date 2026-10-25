import { cleanup, render, waitFor } from '@testing-library/react'
import { afterEach, assert, expect, it, vi } from 'vitest'

import type { QuotaChartPoint, QuotaHistoryPoint } from '../history'
import QuotaHistoryChart from '../QuotaHistoryChart'

type ChartSpec = {
  type: string
  data: { values: QuotaChartPoint[] }[]
  seriesField: string
  line: {
    visible?: boolean
    style: { lineWidth: number; lineDash?: number[]; curveType: string }
  }
  point: {
    style: { visible?: (row: QuotaChartPoint) => boolean }
    state?: { dimension_hover: { visible: boolean } }
  }
  activePoint?: boolean
}
const captured = vi.hoisted(() => ({ spec: null as ChartSpec | null }))

// 仅替代 jsdom 无法绘制的 Canvas 边界，组件和数据分段均使用真实实现。
vi.mock('@visactor/react-vchart', () => ({
  VChart: (props: { spec: ChartSpec }) => {
    captured.spec = props.spec
    return null
  },
}))
vi.mock('@visactor/vchart', () => ({
  ThemeManager: { setCurrentTheme: () => {} },
}))

afterEach(() => {
  cleanup()
  captured.spec = null
})

/**
 * 生成真实时间间隔的最小读数，保持未返回的窗口为 null。
 * @param time 官方观测时间，单位为毫秒。
 * @param remaining 七天窗口的剩余百分比，范围为 0 到 100。
 * @return 当前账号的一条官方历史读数。
 */
function reading(time: number, remaining: number): QuotaHistoryPoint {
  return {
    observed_at: time,
    segment: 'account-a',
    remaining_5h: null,
    remaining_7d: remaining,
    reset_5h: null,
    reset_7d: null,
  }
}

/** 连续记录以实线表达趋势，常态隐藏采样点，悬停仍可定位读数。 */
it('连续读数使用直线连接，隐藏常态圆点并保留悬停高亮', async () => {
  render(
    <QuotaHistoryChart
      points={[reading(1792871000000, 60), reading(1792871300000, 55)]}
    />
  )
  await waitFor(() => expect(captured.spec).not.toBeNull())
  assert(captured.spec)
  expect(captured.spec.type).toBe('line')
  expect(captured.spec.line.visible).toBe(true)
  expect(captured.spec.line.style.curveType).toBe('linear')
  expect(captured.spec.line.style.lineDash).toEqual([])
  assert(captured.spec.point.style.visible)
  for (const row of captured.spec.data[0].values) {
    expect(captured.spec.point.style.visible(row)).toBe(false)
  }
  expect(captured.spec.activePoint).toBe(true)
  expect(captured.spec.point.state?.dimension_hover.visible).toBe(true)
})

/** 缺失数据后的孤立记录无法形成线段，必须保留圆点，不能用连线跨越断档。 */
it('连续片段隐藏圆点，断档后的孤立读数仍可见且与旧片段分开', async () => {
  render(
    <QuotaHistoryChart
      points={[
        reading(1792871000000, 60),
        reading(1792871300000, 55),
        reading(1792872500000, 50),
      ]}
    />
  )
  await waitFor(() => expect(captured.spec).not.toBeNull())
  assert(captured.spec?.point.style.visible)
  const rows = captured.spec.data[0].values
  expect(rows).toHaveLength(3)
  expect(rows[0].series).toBe(rows[1].series)
  expect(rows[2].series).not.toBe(rows[1].series)
  expect(captured.spec.seriesField).toBe('series')
  expect(rows.map(captured.spec.point.style.visible)).toEqual([
    false,
    false,
    true,
  ])
})
