import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { Toaster, toast } from 'sonner'
import { afterEach, expect, it, vi } from 'vitest'

import { CopyButton } from '../copy-button'

const originalExecCommand = Object.getOwnPropertyDescriptor(
  document,
  'execCommand'
)
afterEach(() => {
  toast.dismiss()
  vi.restoreAllMocks()
  if (originalExecCommand) {
    Object.defineProperty(document, 'execCommand', originalExecCommand)
  } else {
    Reflect.deleteProperty(document, 'execCommand')
  }
})

/** 剪贴板拒绝且降级失败时给出可见提示，不能显示成功对勾。 */
it('reports a denied copy and lets the user retry successfully', async () => {
  const user = userEvent.setup()
  const write = vi
    .spyOn(navigator.clipboard, 'writeText')
    .mockRejectedValueOnce(new Error('Denied'))
    .mockResolvedValue(undefined)
  Object.defineProperty(document, 'execCommand', {
    configurable: true,
    value: vi.fn(() => false),
  })
  render(
    <>
      <Toaster />
      <CopyButton value='public endpoint' aria-label='Copy endpoint' />
    </>
  )
  await user.click(screen.getByRole('button', { name: 'Copy endpoint' }))
  expect(await screen.findByText('Failed to copy to clipboard')).toBeVisible()
  expect(screen.getByRole('status')).toBeEmptyDOMElement()
  await user.click(screen.getByRole('button', { name: 'Copy endpoint' }))
  await waitFor(() =>
    expect(screen.getByRole('status')).toHaveTextContent('Copied')
  )
  expect(screen.getByRole('button', { name: 'Copy endpoint' })).toHaveFocus()
  expect(screen.getByRole('status').closest('button')).toBeNull()
  expect(write).toHaveBeenCalledTimes(2)
})

/** 成功标记必须等待真实的剪贴板 Promise 完成。 */
it('keeps the copy action pending until the clipboard accepts the text', async () => {
  const user = userEvent.setup()
  let finish: () => void = () => undefined
  vi.spyOn(navigator.clipboard, 'writeText').mockImplementation(
    () =>
      new Promise<void>((resolve) => {
        finish = resolve
      })
  )
  render(<CopyButton value='public endpoint' aria-label='Copy endpoint' />)
  await user.click(screen.getByRole('button', { name: 'Copy endpoint' }))
  expect(screen.getByRole('status')).toBeEmptyDOMElement()
  finish()
  await waitFor(() =>
    expect(screen.getByRole('status')).toHaveTextContent('Copied')
  )
})
