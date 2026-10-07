import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { StrictMode, useRef, useState } from 'react'
import { MemoryRouter, useLocation, useNavigate } from 'react-router-dom'
import { Dialog } from './Dialog'

it('isolates background content and contains programmatic focus', async () => {
  const user = userEvent.setup()

  function Harness() {
    const [open, setOpen] = useState(false)
    const initialFocus = useRef<HTMLInputElement>(null)
    return (
      <>
        <button type="button" onClick={() => setOpen(true)}>Open dialog</button>
        <input aria-label="Background control" />
        <Dialog
          open={open}
          title="Protected dialog"
          onClose={() => setOpen(false)}
          initialFocusRef={initialFocus}
          footer={<button type="button" onClick={() => setOpen(false)}>Dismiss</button>}
        >
          <input ref={initialFocus} aria-label="Dialog control" />
        </Dialog>
      </>
    )
  }

  const view = render(<MemoryRouter><Harness /></MemoryRouter>)
  const opener = screen.getByRole('button', { name: 'Open dialog' })
  const background = screen.getByLabelText('Background control')
  await user.click(opener)

  const dialogControl = screen.getByLabelText('Dialog control')
  expect(dialogControl).toHaveFocus()
  expect(view.container).toHaveAttribute('inert')
  expect(view.container).toHaveAttribute('aria-hidden', 'true')

  background.focus()

  expect(dialogControl).toHaveFocus()
  await user.click(screen.getByRole('button', { name: 'Dismiss' }))
  await waitFor(() => expect(opener).toHaveFocus())
  expect(view.container).not.toHaveAttribute('inert')
  expect(view.container).not.toHaveAttribute('aria-hidden')
})

it('lets only the topmost dialog handle Escape and restores its opener', async () => {
  const user = userEvent.setup()
  const firstClose = vi.fn()
  const secondClose = vi.fn()

  function Harness() {
    const [firstOpen, setFirstOpen] = useState(false)
    const [secondOpen, setSecondOpen] = useState(false)
    const firstFocus = useRef<HTMLInputElement>(null)
    const secondFocus = useRef<HTMLInputElement>(null)
    return (
      <>
        <button type="button" onClick={() => setFirstOpen(true)}>Open first</button>
        <Dialog
          open={firstOpen}
          title="First dialog"
          onClose={() => { firstClose(); setFirstOpen(false) }}
          initialFocusRef={firstFocus}
        >
          <input ref={firstFocus} aria-label="First control" />
          <button type="button" onClick={() => setSecondOpen(true)}>Open second</button>
        </Dialog>
        <Dialog
          open={secondOpen}
          title="Second dialog"
          onClose={() => { secondClose(); setSecondOpen(false) }}
          initialFocusRef={secondFocus}
        >
          <input ref={secondFocus} aria-label="Second control" />
        </Dialog>
      </>
    )
  }

  render(<MemoryRouter><Harness /></MemoryRouter>)
  await user.click(screen.getByRole('button', { name: 'Open first' }))
  const secondOpener = screen.getByRole('button', { name: 'Open second' })
  await user.click(secondOpener)
  expect(screen.getByLabelText('Second control')).toHaveFocus()

  fireEvent.keyDown(document, { key: 'Escape' })

  await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Second dialog' })).not.toBeInTheDocument())
  expect(firstClose).not.toHaveBeenCalled()
  expect(secondClose).toHaveBeenCalledOnce()
  await waitFor(() => expect(secondOpener).toHaveFocus())
  expect(screen.getByRole('dialog', { name: 'First dialog' })).toBeInTheDocument()

  fireEvent.keyDown(document, { key: 'Escape' })

  await waitFor(() => expect(screen.queryByRole('dialog', { name: 'First dialog' })).not.toBeInTheDocument())
  expect(firstClose).toHaveBeenCalledOnce()
})

it('does not restore an opener over focus established by navigation', async () => {
  const user = userEvent.setup()

  function Harness() {
    const [open, setOpen] = useState(false)
    const dialogFocus = useRef<HTMLInputElement>(null)
    const navigate = useNavigate()
    const location = useLocation()
    return (
      <>
        <button type="button" onClick={() => setOpen(true)}>Open workflow</button>
        {location.pathname === '/done' && <input aria-label="Destination control" autoFocus />}
        <Dialog
          open={open}
          title="Workflow"
          onClose={() => setOpen(false)}
          initialFocusRef={dialogFocus}
          footer={(
            <button type="button" onClick={() => { setOpen(false); navigate('/done') }}>
              Finish and navigate
            </button>
          )}
        >
          <input ref={dialogFocus} aria-label="Workflow control" />
        </Dialog>
      </>
    )
  }

  render(<MemoryRouter><Harness /></MemoryRouter>)
  const opener = screen.getByRole('button', { name: 'Open workflow' })
  await user.click(opener)
  await user.click(screen.getByRole('button', { name: 'Finish and navigate' }))

  const destination = await screen.findByLabelText('Destination control')
  await waitFor(() => expect(destination).toHaveFocus())
  expect(opener).not.toHaveFocus()
})

it('restores background attributes and listeners when an open dialog unmounts', async () => {
  const user = userEvent.setup()

  function Harness() {
    const [open, setOpen] = useState(false)
    return (
      <>
        <button type="button" onClick={() => setOpen(true)}>Open temporary</button>
        <Dialog open={open} title="Temporary" onClose={() => setOpen(false)}><p>Temporary content</p></Dialog>
      </>
    )
  }

  const view = render(<MemoryRouter><Harness /></MemoryRouter>)
  await user.click(screen.getByRole('button', { name: 'Open temporary' }))
  expect(view.container).toHaveAttribute('inert')

  view.unmount()

  expect(view.container).not.toHaveAttribute('inert')
  expect(view.container).not.toHaveAttribute('aria-hidden')
  expect(document.querySelector('[data-holark-dialog-portal]')).not.toBeInTheDocument()
})

it('restores the opener for a conditionally mounted open dialog in StrictMode', async () => {
  const user = userEvent.setup()

  function Harness() {
    const [open, setOpen] = useState(false)
    return (
      <>
        <button type="button" onClick={() => setOpen(true)}>Open conditional</button>
        {open && (
          <Dialog open title="Conditional" onClose={() => setOpen(false)}>
            <p>Conditional content</p>
          </Dialog>
        )}
      </>
    )
  }

  render(<StrictMode><MemoryRouter><Harness /></MemoryRouter></StrictMode>)
  const opener = screen.getByRole('button', { name: 'Open conditional' })
  await user.click(opener)
  await user.click(screen.getByRole('button', { name: 'Got it' }))

  await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Conditional' })).not.toBeInTheDocument())
  await waitFor(() => expect(opener).toHaveFocus())
})
