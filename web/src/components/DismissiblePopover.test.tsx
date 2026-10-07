import { fireEvent, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { DismissiblePopoverGroup, useDismissiblePopover } from './DismissiblePopover'

function Popover({ id, label }: { id: string, label: string }) {
  const { open, rootRef, triggerRef, toggle, close } = useDismissiblePopover(id)
  return (
    <div ref={rootRef}>
      <button ref={triggerRef} type="button" aria-expanded={open} onClick={toggle}>{label}</button>
      {open && (
        <div aria-label={`${label} content`}>
          <button type="button" onClick={close}>{label} action</button>
        </div>
      )}
    </div>
  )
}

function Harness() {
  return (
    <DismissiblePopoverGroup>
      <Popover id="first" label="First" />
      <Popover id="second" label="Second" />
      <button type="button">Outside</button>
    </DismissiblePopoverGroup>
  )
}

it('toggles on one click and keeps only one popover open', async () => {
  const user = userEvent.setup()
  render(<Harness />)

  const first = screen.getByRole('button', { name: 'First' })
  const second = screen.getByRole('button', { name: 'Second' })
  await user.click(first)
  expect(first).toHaveAttribute('aria-expanded', 'true')
  expect(screen.getByLabelText('First content')).toBeInTheDocument()

  await user.click(second)
  expect(first).toHaveAttribute('aria-expanded', 'false')
  expect(second).toHaveAttribute('aria-expanded', 'true')
  expect(screen.queryByLabelText('First content')).not.toBeInTheDocument()

  await user.click(second)
  expect(second).toHaveAttribute('aria-expanded', 'false')
})

it('dismisses on outside pointer interaction and focus leaving the popover', async () => {
  const user = userEvent.setup()
  render(<Harness />)

  const first = screen.getByRole('button', { name: 'First' })
  await user.click(first)
  fireEvent.pointerDown(document.body)
  expect(first).toHaveAttribute('aria-expanded', 'false')

  await user.click(first)
  fireEvent.focusIn(screen.getByRole('button', { name: 'First action' }))
  fireEvent.focusIn(screen.getByRole('button', { name: 'Outside' }))
  expect(first).toHaveAttribute('aria-expanded', 'false')
})

it('dismisses on Escape, restores trigger focus, and exposes close for actions', async () => {
  const user = userEvent.setup()
  render(<Harness />)

  const first = screen.getByRole('button', { name: 'First' })
  await user.click(first)
  screen.getByRole('button', { name: 'First action' }).focus()
  fireEvent.keyDown(document, { key: 'Escape' })
  expect(first).toHaveAttribute('aria-expanded', 'false')
  expect(first).toHaveFocus()

  await user.click(first)
  await user.click(screen.getByRole('button', { name: 'First action' }))
  expect(first).toHaveAttribute('aria-expanded', 'false')
})
