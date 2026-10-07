import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { ColorThemeContext, type ColorTheme } from '../../app/colorTheme'
import type { Holon, HolonIDE } from '../../data/types'
import { ProjectContext } from '../project/ProjectContext'
import { HolonStoreProvider } from '../project/HolonStore'
import { HolonIDEPane } from './HolonIDEPane'
import { HolonTerminal } from './HolonTerminal'
import { TerminalWorkspaceProvider } from "./TerminalWorkspaceProvider"

const now = '2026-08-02T10:00:00.000Z'

afterEach(() => {
  vi.unstubAllGlobals()
})

function ide(overrides: Partial<HolonIDE> = {}): HolonIDE {
  return {
    id: 'ide-one',
    holon_id: 'holon-one',
    provider: 'vscode',
    state: 'ready',
    desired_open: true,
    tab_order: 1,
    created_at: now,
    updated_at: now,
    ...overrides,
  }
}

it('inherits the Holark theme and keeps the IDE iframe mounted while its tab is inactive', () => {
  const retry = vi.fn()
  const ready = ide()
  const pane = (theme: ColorTheme, active: boolean, currentIDE: HolonIDE = ready) => (
    <ColorThemeContext.Provider value={{ theme, setTheme: vi.fn() }}>
      <HolonIDEPane holonId="holon-one" ide={currentIDE} active={active} activated onRetry={retry} />
    </ColorThemeContext.Provider>
  )
  const view = render(pane('light', true))
  const frame = screen.getByTitle('VS Code IDE')
  expect(frame).toHaveAttribute('src', '/api/v1/holons/holon-one/ides/ide-one/proxy/?holark-theme=light')

  view.rerender(pane('dark', false))
  expect(screen.getByTitle('VS Code IDE')).toBe(frame)
  expect(frame).toHaveAttribute('src', '/api/v1/holons/holon-one/ides/ide-one/proxy/?holark-theme=dark')

  const failed = ide({ state: 'failed', reason: 'Port unavailable.', updated_at: '2026-08-02T10:00:01.000Z' })
  view.rerender(pane('dark', true, failed))
  expect(screen.getByRole('alert')).toHaveTextContent('Port unavailable.')
  fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
  expect(retry).toHaveBeenCalledWith(failed)
})

it('shows the draining and suspended resume states with retry only while reopening', () => {
	const retry = vi.fn()
	const pane = (currentIDE: HolonIDE, retrySuspended = false) => (
		<HolonIDEPane holonId="holon-one" ide={currentIDE} active activated retrySuspended={retrySuspended} onRetry={retry} />
	)
	const view = render(pane(ide({ state: 'stopping' })))
	expect(screen.getByText('Stopping VS Code')).toBeInTheDocument()
	expect(screen.getByText('The current runtime is shutting down safely.')).toBeInTheDocument()
	expect(screen.queryByRole('button', { name: 'Retry' })).not.toBeInTheDocument()

	const suspended = ide({ state: 'suspended', updated_at: '2026-08-02T10:00:01.000Z' })
	view.rerender(pane(suspended))
	expect(screen.getByText('VS Code stopped')).toBeInTheDocument()
	expect(screen.getByText('It will reopen when the holon resumes.')).toBeInTheDocument()
	expect(screen.queryByRole('button', { name: 'Retry' })).not.toBeInTheDocument()

	view.rerender(pane(suspended, true))
	expect(screen.getByRole('alert')).toHaveTextContent('IDE did not reopen')
	fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
	expect(retry).toHaveBeenCalledWith(suspended)
})

it('keeps a failed IDE visible, retries it, reconciles readiness, and closes it explicitly', async () => {
  let currentIDE: HolonIDE | undefined
  let reportReady = false
  let createCount = 0
  const holon: Holon = {
    id: 'holon-one',
    repository_id: 'holark',
    runtime_id: 'node-one',
    prompt: 'work',
    worktree_path: '/tmp/holon-one',
    agent_sessions: [],
    manual_terminals: [],
    ides: [],
    status: 'completed',
    input_state: 'none',
    created_at: now,
  }
  vi.stubGlobal('fetch', vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
    const path = String(input)
    const method = input instanceof Request ? input.method : (init?.method ?? 'GET')
    if (path.endsWith('/pull-request')) {
      return Response.json({ code: 'pull_request_not_found', message: 'Not found.' }, { status: 404 })
    }
    if (path.endsWith('/ides') && method === 'POST') {
      createCount += 1
      currentIDE = ide({ id: `ide-${createCount}`, state: 'starting', updated_at: now })
      holon.ides = [currentIDE]
      return Response.json(currentIDE, { status: 201 })
    }
	if (path.endsWith('/close') && path.includes('/ides/') && method === 'POST') {
		currentIDE = { ...currentIDE!, state: 'closed', desired_open: false, updated_at: '2026-08-02T10:00:03.000Z' }
		holon.ides = []
		return Response.json(currentIDE)
	}
    if (path.endsWith('/holons')) {
      const polledIDE = currentIDE && reportReady
        ? { ...currentIDE, state: 'ready' as const, updated_at: '2026-08-02T10:00:02.000Z', ready_at: '2026-08-02T10:00:02.000Z' }
        : currentIDE
      return Response.json([{ ...holon, ides: polledIDE ? [polledIDE] : [] }])
    }
    return Response.json(holon)
  }))

  render(
    <ProjectContext.Provider value={{ id: 'holark', name: 'Holark', default_branch: 'main' }}>
      <HolonStoreProvider projectId="holark" interval={20}>
        <TerminalWorkspaceProvider>
        <MemoryRouter initialEntries={['/holons/holon-one']}>
          <Routes>
            <Route path="/holons/:holonId" element={<HolonTerminal />} />
          </Routes>
        </MemoryRouter>
        </TerminalWorkspaceProvider>
      </HolonStoreProvider>
    </ProjectContext.Provider>,
  )

  const newTab = await screen.findByLabelText('New tab')
  fireEvent.click(newTab)
  const addIDE = await screen.findByRole('button', { name: 'IDE' })
  await waitFor(() => expect(addIDE).toBeEnabled())
  fireEvent.click(addIDE)

  expect(await screen.findByRole('tab', { name: 'IDE' })).toHaveAttribute('aria-selected', 'true')
  expect(screen.getByText('Starting VS Code')).toBeInTheDocument()
  expect(createCount).toBe(1)

  currentIDE = { ...currentIDE!, state: 'failed', reason: 'Download failed. Log: /logs/serve-web.log', updated_at: '2026-08-02T10:00:01.000Z' }
  expect(await screen.findByRole('alert')).toHaveTextContent('Download failed. Log: /logs/serve-web.log')
  expect(screen.getByRole('tab', { name: 'IDE' })).toHaveAttribute('aria-selected', 'true')
  fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
  await waitFor(() => expect(createCount).toBe(2))
  expect(await screen.findByText('Starting VS Code')).toBeInTheDocument()

  reportReady = true
  expect(await screen.findByTitle('VS Code IDE')).toHaveAttribute('src', '/api/v1/holons/holon-one/ides/ide-2/proxy/?holark-theme=light')

  fireEvent.click(screen.getByLabelText('New tab'))
  fireEvent.click(screen.getByRole('button', { name: 'IDE' }))
  expect(createCount).toBe(2)
  expect(screen.getByRole('tab', { name: 'IDE' })).toHaveAttribute('aria-selected', 'true')

	fireEvent.click(screen.getByRole('button', { name: 'Close IDE' }))
	await waitFor(() => expect(screen.queryByRole('tab', { name: 'IDE' })).not.toBeInTheDocument())
})
