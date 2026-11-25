import { useId, useState, type PointerEvent } from 'react'
import { useTranslation } from 'react-i18next'

interface TrendProps {
  points: { at: number; after: number }[]
  formatTime: (at: number) => string
  formatBalance: (quota: number) => string
}

/** 以真实时间为横轴展示余额快照；指针和键盘只能选择已有记录。
 * @param props 按时间升序且已排除时间冲突的快照，以及账本对应的格式化规则。
 * @return 带准确金额读数的趋势图；没有快照时由父组件显示空状态。
 */
export function BalanceTrend({
  points,
  formatTime,
  formatBalance,
}: TrendProps) {
  const { t } = useTranslation()
  const gradient = useId()
  // 分页会插入更早的点，按时间记住选中项，以免读数跳到另一笔。
  const [selectedTime, selectTime] = useState<number | null>(null)
  const first = points[0]
  const last = points.at(-1)
  if (!last) return null
  const timeSpan = last.at - first.at
  const selectedIndex = points.findIndex((p) => p.at === selectedTime)
  const index = selectedIndex < 0 ? points.length - 1 : selectedIndex
  const active = points[index]
  const min = points.reduce((v, p) => Math.min(v, p.after), first.after)
  const max = points.reduce((v, p) => Math.max(v, p.after), first.after)
  // 留出上下呼吸空间。时间只有一个或余额持平时在中线展示，避免除零。
  const coords = points.map((p) => ({
    x:
      last.at === first.at
        ? 50
        : (100 * (p.at - first.at)) / (last.at - first.at),
    y: min === max ? 50 : 86 - (72 * (p.after - min)) / (max - min),
  }))
  const path = coords.map((p, i) => `${i ? 'L' : 'M'}${p.x},${p.y}`).join(' ')
  const current = coords[index]
  const valueText = `${formatTime(active.at)} · ${formatBalance(active.after)}`

  /** 取指针所在时刻最近的真实快照，跨越稀疏区时同样按时间距离选择。
   * @param event 当前绘图区上的指针事件，坐标与绘图区使用同一尺寸。
   */
  function inspect(event: PointerEvent<HTMLDivElement>) {
    const box = event.currentTarget.getBoundingClientRect()
    const target =
      first.at + ((event.clientX - box.left) / box.width) * timeSpan
    const closest = points.reduce(
      (best, p) =>
        Math.abs(p.at - target) < Math.abs(best.at - target) ? p : best,
      first
    )
    selectTime(closest.at)
  }
  return (
    <div className='wl-chart'>
      <div className='wl-readout'>
        <strong>{formatBalance(active.after)}</strong>
        <time dateTime={new Date(active.at * 1000).toISOString()}>
          {formatTime(active.at)}
        </time>
      </div>
      <div className='wl-plot-area'>
        <div className='wl-scale' aria-hidden='true'>
          {min !== max ? (
            <>
              <span>{formatBalance(max)}</span>
              <span>{formatBalance(min)}</span>
            </>
          ) : (
            <span>{formatBalance(min)}</span>
          )}
        </div>
        <div
          className='wl-plot'
          role='slider'
          tabIndex={0}
          aria-label={t('Recorded balance snapshots')}
          aria-valuemin={1}
          aria-valuemax={points.length}
          aria-valuenow={index + 1}
          aria-valuetext={valueText}
          onPointerMove={inspect}
          onPointerDown={inspect}
          onKeyDown={(e) => {
            const keys: Record<string, number> = {
              ArrowLeft: index - 1,
              ArrowDown: index - 1,
              ArrowRight: index + 1,
              ArrowUp: index + 1,
              Home: 0,
              End: points.length - 1,
            }
            if (!(e.key in keys)) return
            e.preventDefault()
            selectTime(
              points[Math.max(0, Math.min(points.length - 1, keys[e.key]))].at
            )
          }}
        >
          <svg
            aria-hidden='true'
            viewBox='0 0 100 100'
            preserveAspectRatio='none'
          >
            <defs>
              <linearGradient id={gradient} x1='0' x2='0' y1='0' y2='1'>
                <stop offset='0%' stopColor='currentColor' stopOpacity='.16' />
                <stop
                  offset='100%'
                  stopColor='currentColor'
                  stopOpacity='.01'
                />
              </linearGradient>
            </defs>
            {[14, 50, 86].map((y) => (
              <line
                key={y}
                x1='0'
                x2='100'
                y1={y}
                y2={y}
                className='wl-guide'
                vectorEffect='non-scaling-stroke'
              />
            ))}
            {points.length > 1 && (
              <>
                <path
                  d={`${path} L100,100 L0,100 Z`}
                  fill={`url(#${gradient})`}
                />
                <path
                  d={path}
                  fill='none'
                  stroke='currentColor'
                  strokeWidth='2'
                  strokeLinejoin='round'
                  vectorEffect='non-scaling-stroke'
                />
              </>
            )}
          </svg>
          <span className='wl-cursor' style={{ left: `${current.x}%` }} />
          <span
            className='wl-point'
            style={{ left: `${current.x}%`, top: `${current.y}%` }}
          />
        </div>
      </div>
      <footer>
        <time>{formatTime(first.at)}</time>
        {points.length > 1 && <time>{formatTime(last.at)}</time>}
      </footer>
    </div>
  )
}
