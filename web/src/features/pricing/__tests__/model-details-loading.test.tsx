import { act, fireEvent, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState, type ComponentType } from 'react'
import { beforeEach, expect, it, vi } from 'vitest'

import type { ModelDetailsContentProps } from '../components/model-details'
import { ModelDetailsDrawer } from '../components/model-details-drawer'

const resource = vi.hoisted(() => ({ load: vi.fn() }))
vi.mock('../components/load-model-details', () => ({
  loadModelDetailsContent: resource.load,
}))

/** 加载边界的完成内容；计费准确性继续由真实详情组件的既有测试覆盖。 */
function LoadedContent(props: ModelDetailsContentProps) {
  return <p>Loaded {props.model.model_name}</p>
}

/** 保留真实抽屉和开关状态，只有异步代码模块加载由测试控制。 */
function Harness() {
  const [open, setOpen] = useState(false)
  return (
    <>
      <button type='button' onClick={() => setOpen(true)}>
        Open details
      </button>
      <ModelDetailsDrawer
        open={open}
        onOpenChange={setOpen}
        model={
          { model_name: 'test-model' } as ModelDetailsContentProps['model']
        }
        groupRatio={{}}
        usableGroup={{}}
        endpointMap={{}}
        autoGroups={[]}
        priceRate={1}
        usdExchangeRate={1}
        tokenUnit='M'
      />
    </>
  )
}

beforeEach(() => {
  resource.load.mockReset()
})

/** 模块还未到达时抽屉即有名称、加载状态和关闭操作；迟到结果不重开抽屉。 */
it('opens a named loading shell immediately and stays closed after late completion', async () => {
  const user = userEvent.setup()
  let resolve: (
    content: ComponentType<ModelDetailsContentProps>
  ) => void = () => undefined
  resource.load.mockImplementation(
    () =>
      new Promise((done) => {
        resolve = done
      })
  )
  render(<Harness />)
  await user.click(screen.getByRole('button', { name: 'Open details' }))
  expect(screen.getByRole('dialog', { name: 'test-model' })).toBeVisible()
  expect(screen.getByRole('status')).toHaveTextContent(
    'Loading model details...'
  )
  await user.keyboard('{Escape}')
  await act(async () => resolve(LoadedContent))
  expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Open details' })).toHaveFocus()
})

/** 加载失败留在同一个抽屉，重试触发新加载，不让错误吞掉首页。 */
it('keeps a failed module local to the drawer and retries successfully', async () => {
  resource.load
    .mockRejectedValueOnce(new Error('Chunk unavailable'))
    .mockResolvedValue(LoadedContent)
  render(<Harness />)
  fireEvent.click(screen.getByRole('button', { name: 'Open details' }))
  expect(await screen.findByRole('alert')).toHaveTextContent(
    'Model details could not be loaded.'
  )
  fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
  expect(await screen.findByText('Loaded test-model')).toBeVisible()
  expect(resource.load).toHaveBeenCalledTimes(2)
  expect(screen.queryByRole('alert')).not.toBeInTheDocument()
})

/** 重试按钮消失后焦点保留在详情区域，用户仍可按 Tab 找到关闭按钮。 */
it('keeps focus inside the drawer while retrying a failed load', async () => {
  const user = userEvent.setup()
  resource.load
    .mockRejectedValueOnce(new Error('Chunk unavailable'))
    .mockImplementation(() => new Promise(() => undefined))
  render(<Harness />)
  await user.click(screen.getByRole('button', { name: 'Open details' }))
  await screen.findByRole('alert')
  await user.click(screen.getByRole('button', { name: 'Retry' }))
  expect(screen.getByRole('dialog')).toContainElement(
    document.activeElement as HTMLElement
  )
  expect(screen.getByRole('status')).toHaveTextContent(
    'Loading model details...'
  )
  await user.keyboard('{Escape}')
  expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
})
