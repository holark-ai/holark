import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { ColorThemeProvider } from '../app/ColorThemeProvider'
import { TerminalWorkspaceProvider } from '../features/terminal/TerminalWorkspaceProvider'
import { DevPreviewRoutes } from './DevPreviewRoutes'
import { installDevPreviewApi } from './devPreviewApi'

function renderPreview(route: string) {
  return render(
    <ColorThemeProvider>
      <TerminalWorkspaceProvider>
        <MemoryRouter basename="/__dev" initialEntries={[route]}>
          <DevPreviewRoutes />
        </MemoryRouter>
      </TerminalWorkspaceProvider>
    </ColorThemeProvider>,
  )
}

it('navigates from a PR detail to its holon through the shared status panel', async () => {
  const uninstall = installDevPreviewApi()
  const view = renderPreview('/__dev/pulls/pr-preview')
  const user = userEvent.setup()
  try {
    expect(await screen.findByRole('heading', { name: 'Improve workspace navigation' })).toBeInTheDocument()
    await user.click(screen.getAllByRole('link', { name: /Explore shared components/ })[0])
    expect(await screen.findByText('Terminal preview')).toBeInTheDocument()
    expect(screen.getAllByRole('link', { name: /Explore shared components/ })[0]).toHaveAttribute('aria-current', 'page')
    const pullRequestGroup = screen.getByRole('region', { name: 'PR #142 · Improve workspace navigation' })
    expect(within(pullRequestGroup).getByRole('link', { name: /Explore shared components/ })).toHaveAttribute('aria-current', 'page')

  } finally {
    view.unmount()
    uninstall()
  }
})

it('loads a preview pull request URL directly without contacting fetch', async () => {
  const fetchSpy = vi.spyOn(globalThis, 'fetch')
  const uninstall = installDevPreviewApi()
  const view = renderPreview('/__dev/pulls/pr-preview')
  try {
    expect(await screen.findByRole('heading', { name: 'Improve workspace navigation' })).toBeInTheDocument()
    await waitFor(() => expect(fetchSpy).not.toHaveBeenCalled())
  } finally {
    view.unmount()
    uninstall()
    fetchSpy.mockRestore()
  }
})

it('shows commit message generation only on agent tabs with Changes closed', async () => {
  const uninstall = installDevPreviewApi()
  const view = renderPreview('/__dev/holons/holon-A')
  const user = userEvent.setup()
  try {
    expect(await screen.findByRole('button', { name: 'Generate commit message' })).toBeEnabled()
    await user.click(screen.getByRole('tab', { name: 'Shell' }))
    expect(screen.queryByRole('button', { name: 'Generate commit message' })).not.toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'New tab' }))
    await user.click(screen.getByRole('button', { name: 'IDE' }))
    expect(screen.queryByRole('button', { name: 'Generate commit message' })).not.toBeInTheDocument()

    await user.click(screen.getByRole('tab', { name: 'Agent CLI' }))
    expect(screen.getByRole('button', { name: 'Generate commit message' })).toBeEnabled()
    await user.click(screen.getByRole('button', { name: /^Open changes panel/ }))
    expect(screen.queryByRole('button', { name: 'Generate commit message' })).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Close changes panel' }))
    expect(screen.getByRole('button', { name: 'Generate commit message' })).toBeEnabled()
  } finally {
    view.unmount()
    uninstall()
  }
})

it('keeps generation hidden after a message is generated or edited until the textbox is empty', async () => {
  const uninstall = installDevPreviewApi()
  const view = renderPreview('/__dev/holons/holon-A')
  const user = userEvent.setup()
  try {
    const generate = await screen.findByRole('button', { name: 'Generate commit message' })
    vi.useFakeTimers()
    fireEvent.click(generate)
    expect(screen.queryByRole('button', { name: 'Generate commit message' })).not.toBeInTheDocument()
    await act(async () => { await vi.advanceTimersByTimeAsync(10000) })
    vi.useRealTimers()

    const textbox = await screen.findByRole('textbox', { name: 'Commit message' })
    expect(textbox).not.toHaveValue('')
    await user.click(screen.getByRole('button', { name: 'Close changes panel' }))
    expect(screen.queryByRole('button', { name: 'Generate commit message' })).not.toBeInTheDocument()
    await user.click(screen.getByRole('tab', { name: 'Review CLI' }))
    expect(screen.queryByRole('button', { name: 'Generate commit message' })).not.toBeInTheDocument()

    for (const message of ['Manually edited commit message', ' ']) {
      await user.click(screen.getByRole('button', { name: /^Open changes panel/ }))
      fireEvent.change(screen.getByRole('textbox', { name: 'Commit message' }), { target: { value: message } })
      await user.click(screen.getByRole('button', { name: 'Close changes panel' }))
      expect(screen.queryByRole('button', { name: 'Generate commit message' })).not.toBeInTheDocument()
    }

    await user.click(screen.getByRole('button', { name: /^Open changes panel/ }))
    fireEvent.change(screen.getByRole('textbox', { name: 'Commit message' }), { target: { value: '' } })
    expect(screen.queryByRole('button', { name: 'Generate commit message' })).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Close changes panel' }))
    expect(screen.getByRole('button', { name: 'Generate commit message' })).toBeEnabled()
  } finally {
    view.unmount()
    uninstall()
    vi.useRealTimers()
  }
})


it('keeps loaded history when selecting an older commit from the default branch', async () => {
  const uninstall = installDevPreviewApi()
  const view = renderPreview('/__dev/')
  try {
    const history = await screen.findByLabelText('Commit history')
    const commitButtons = () => within(history).getAllByRole('button').filter(button => button.hasAttribute('aria-pressed'))
    await waitFor(() => expect(commitButtons()).toHaveLength(50))
    Object.defineProperties(history, {
      clientHeight: { configurable: true, value: 400 },
      scrollHeight: { configurable: true, get: () => history.querySelectorAll('button[aria-pressed]').length * 52 },
    })
    history.scrollTop = 2050
    fireEvent.scroll(history)
    await waitFor(() => expect(commitButtons()).toHaveLength(100))
    const older = commitButtons()[80]
    fireEvent.click(older)
    await waitFor(() => expect(screen.getByRole('button', { name: 'Back to latest' })).toBeInTheDocument())
    expect(screen.getByLabelText('Commit history')).toBe(history)
    expect(commitButtons()).toHaveLength(100)
    expect(older).toHaveAttribute('aria-pressed', 'true')
    expect(history.scrollTop).toBe(2050)
  } finally {
    view.unmount()
    uninstall()
  }
})
