import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { StrictMode, useState } from 'react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { NewAgentDialog } from './NewAgentDialog'
import type { HarnessCapabilities } from '../../data/types'
import { HOLON_TITLE_MAX_CHARACTERS } from './holonTitle'
import { HolonStoreProvider } from '../project/HolonStore'
import { useOptionalHolonStore } from '../project/holonStoreContext'

const project = { id: 'holark', name: 'Holark', default_branch: 'main' }
const capabilities = [{ type: 'codex' as const, available: true, version: '1.0' }]

beforeEach(() => window.localStorage.clear())

afterEach(() => {
  window.localStorage.clear()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

function sessionResponse() {
  return new Response(JSON.stringify({
    id: 'holon-1', repository_id: 'holark', runtime_id: 'node-1', prompt: 'Fix it',
    status: 'preparing', agent_sessions: [], manual_terminals: [], created_at: new Date().toISOString(),
  }), { status: 202, headers: { 'Content-Type': 'application/json', Location: '/api/v1/holons/holon-1' } })
}

function jsonResponse(body: unknown) {
  return new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })
}

function preparationResponse() {
  return jsonResponse({ id: 'preparation-1', commit: 'a'.repeat(40), ref: 'main' })
}

function refsResponse() {
  return jsonResponse({
    repository_id: 'holark',
    default_ref: 'refs/heads/main',
    refs: [
      { name: 'refs/heads/main', short_name: 'main', kind: 'branch', target: 'a'.repeat(40), committed_at: new Date().toISOString() },
      { name: 'refs/heads/feature/foo', short_name: 'feature/foo', kind: 'branch', target: 'b'.repeat(40), committed_at: new Date().toISOString() },
      { name: 'refs/heads/release/fix', short_name: 'release/fix', kind: 'branch', target: 'c'.repeat(40), committed_at: new Date().toISOString() },
      { name: 'refs/holark/sessions/holon-1', short_name: 'holon-1', kind: 'holark_session', target: 'd'.repeat(40), committed_at: new Date().toISOString() },
    ],
  })
}

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

it('loads branch choices without preparing a repository when New Holon opens', async () => {
  const preparation = deferred<Response>()
  const fetchMock = vi.fn<(input: RequestInfo | URL, init?: RequestInit) => Promise<Response>>(async (input) => String(input).endsWith('/refs') ? refsResponse() : preparation.promise)
  vi.stubGlobal('fetch', fetchMock)
  const user = userEvent.setup()

  function Harness() {
    const [open, setOpen] = useState(false)
    return (
      <>
        <button type="button" onClick={() => setOpen(true)}>New Holon</button>
        <NewAgentDialog open={open} project={project} capabilities={capabilities} onClose={() => setOpen(false)} />
      </>
    )
  }

  render(<MemoryRouter><Harness /></MemoryRouter>)

  expect(fetchMock).not.toHaveBeenCalled()
  await user.click(screen.getByRole('button', { name: 'New Holon' }))
  await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1))
  expect(fetchMock.mock.calls[0][0]).toBe('/api/v1/repository/refs')
})

it('submits the currently selected branch without resolving its commit in the dialog', async () => {
  const fetchMock = vi.fn<(input: RequestInfo | URL, init?: RequestInit) => Promise<Response>>(async (input, init) => {
    const path = String(input)
    if (path.endsWith('/refs')) return refsResponse()
    if (path.endsWith('/refs/refresh') && init?.method === 'POST') return refsResponse()
    return sessionResponse()
  })
  vi.stubGlobal('fetch', fetchMock)
  const user = userEvent.setup()

  render(
    <MemoryRouter initialEntries={['/projects/holark']}>
      <Routes>
        <Route path="/projects/holark" element={<NewAgentDialog open project={project} capabilities={capabilities} initialBranch="feature/foo" onClose={() => undefined} />} />
        <Route path="/holons/holon-1" element={<h1>Holon detail</h1>} />
      </Routes>
    </MemoryRouter>,
  )

  await waitFor(() => expect(fetchMock).toHaveBeenCalled())
  const dialog = screen.getByRole('dialog', { name: 'New Holon' })
  const branch = screen.getByRole('combobox', { name: 'Branch' })
  expect(branch).toHaveTextContent('feature/foo')

  await user.click(branch)
  expect(dialog).toContainElement(await screen.findByRole('listbox'))
  await user.keyboard('{Escape}')
  await waitFor(() => expect(screen.queryByRole('listbox')).not.toBeInTheDocument())
  expect(dialog).toBeInTheDocument()

  await user.click(branch)
  await user.type(await screen.findByPlaceholderText('Search branches…'), 'rls')
  await user.keyboard('{ArrowDown}{Enter}')
  expect(branch).toHaveTextContent('release/fix')

  await user.type(screen.getByLabelText('Task'), 'Fix it')
  await user.click(screen.getByRole('button', { name: 'Create Holon' }))
  expect(await screen.findByRole('heading', { name: 'Holon detail' })).toBeInTheDocument()

  const createCall = fetchMock.mock.calls.find(([path]) => String(path) === '/api/v1/holons')
  expect(createCall).toBeDefined()
  expect(JSON.parse(String(createCall?.[1]?.body))).toMatchObject({
    base_branch: 'release/fix',
  })
})

it.each(['Fix it', '', '   '])('creates an agent once with task %j and navigates to its terminal', async (prompt) => {
  vi.stubGlobal('innerWidth', 1440)
  vi.stubGlobal('innerHeight', 900)
  const fetchMock = vi.fn<(input: RequestInfo | URL, init?: RequestInit) => Promise<Response>>(async (input) => {
    if (String(input).endsWith('/refs')) return refsResponse()
    if (String(input).includes('/repository/prepare')) return preparationResponse()
    return sessionResponse()
  })
  vi.stubGlobal('fetch', fetchMock)
  const user = userEvent.setup()

  render(
    <MemoryRouter initialEntries={['/projects/holark']}>
      <Routes>
        <Route path="/projects/holark" element={<NewAgentDialog open project={project} capabilities={capabilities} onClose={() => undefined} />} />
        <Route path="/holons/holon-1" element={<h1>Holon detail</h1>} />
      </Routes>
    </MemoryRouter>,
  )

  await user.click(screen.getByRole('button', { name: 'Set a custom Holon name' }))
  await user.type(screen.getByLabelText('Holon name (optional)'), 'Checkout repair')
  if (prompt) await user.type(screen.getByLabelText('Task'), prompt)
  await user.click(screen.getByRole('button', { name: 'Create Holon' }))

  expect(await screen.findByRole('heading', { name: 'Holon detail' })).toBeInTheDocument()
  expect(fetchMock).toHaveBeenCalledTimes(2)
  expect(fetchMock.mock.calls.map(([path]) => String(path))).toEqual([
    '/api/v1/repository/refs',
    '/api/v1/holons',
  ])
  expect(JSON.parse(String(fetchMock.mock.calls[1][1]?.body))).toMatchObject({
    title: 'Checkout repair',
    prompt: prompt.trim(),
    base_branch: 'main',
  })
})

it('edits the optional name in place of the dialog title', async () => {
  const fetchMock = vi.fn<(input: RequestInfo | URL, init?: RequestInit) => Promise<Response>>(async (input) => {
    if (String(input).endsWith('/refs')) return refsResponse()
    if (String(input).includes('/repository/prepare')) return preparationResponse()
    return sessionResponse()
  })
  vi.stubGlobal('fetch', fetchMock)
  const user = userEvent.setup()

  render(
    <MemoryRouter initialEntries={['/projects/holark']}>
      <Routes>
        <Route path="/projects/holark" element={<NewAgentDialog open project={project} capabilities={capabilities} onClose={() => undefined} />} />
        <Route path="/holons/holon-1" element={<h1>Holon detail</h1>} />
      </Routes>
    </MemoryRouter>,
  )

  const heading = screen.getByRole('heading', { name: 'New Holon' })
  const nameButton = screen.getByRole('button', { name: 'Set a custom Holon name' })
  const tooltip = screen.getByRole('tooltip', { name: 'Set a custom name. Otherwise, one will be generated automatically.' })
  expect(screen.queryByLabelText('Holon name (optional)')).not.toBeInTheDocument()
  expect(heading.parentElement).toContainElement(nameButton)
  expect(nameButton).toHaveAttribute('aria-describedby', tooltip.id)

  await user.click(nameButton)
  const nameInput = screen.getByLabelText('Holon name (optional)')
  expect(nameInput).toHaveFocus()
  expect(heading).toHaveClass('sr-only')
  expect(heading.nextElementSibling).toContainElement(nameInput)
  expect(screen.queryByRole('button', { name: 'Set a custom Holon name' })).not.toBeInTheDocument()
  await user.type(nameInput, 'Checkout repair')
  await user.click(screen.getByLabelText('Task'))

  expect(screen.queryByLabelText('Holon name (optional)')).not.toBeInTheDocument()
  expect(screen.getByRole('heading', { name: 'Checkout repair' })).toBeVisible()
  expect(screen.getByRole('button', { name: 'Edit Holon name' })).toBeVisible()
  const task = screen.getByLabelText('Task')
  await user.type(task, 'Fix it')
  await user.click(screen.getByRole('button', { name: 'Create Holon' }))
  await screen.findByRole('heading', { name: 'Holon detail' })

  const createCall = fetchMock.mock.calls.find(([path]) => String(path) === '/api/v1/holons')
  const body = JSON.parse(String(createCall?.[1]?.body)) as Record<string, unknown>
  expect(body).toHaveProperty('title', 'Checkout repair')
  expect(screen.queryByText(/leave blank/i)).not.toBeInTheDocument()
})

it('commits an edited name with Enter and cancels changes with Escape', async () => {
  const user = userEvent.setup()
  render(
    <MemoryRouter>
      <NewAgentDialog open project={project} capabilities={capabilities} onClose={() => undefined} />
    </MemoryRouter>,
  )

  await user.click(screen.getByRole('button', { name: 'Set a custom Holon name' }))
  await user.type(screen.getByLabelText('Holon name (optional)'), 'First name{Enter}')

  expect(screen.getByRole('heading', { name: 'First name' })).toBeVisible()
  await user.click(screen.getByRole('button', { name: 'Edit Holon name' }))
  await user.clear(screen.getByLabelText('Holon name (optional)'))
  await user.type(screen.getByLabelText('Holon name (optional)'), 'Discarded name')
  await user.keyboard('{Escape}')

  expect(screen.queryByLabelText('Holon name (optional)')).not.toBeInTheDocument()
  expect(screen.getByRole('heading', { name: 'First name' })).toBeVisible()
  expect(screen.getByRole('dialog', { name: 'First name' })).toBeInTheDocument()
})

it.each([
  ['agent selection', 'Control'],
  ['name confirmation', 'Control'],
  ['name cancellation', 'Control'],
  ['agent selection', 'Meta'],
  ['name confirmation', 'Meta'],
])('submits with %s followed by %s+Enter', async (action, modifier) => {
  vi.spyOn(navigator, 'platform', 'get').mockReturnValue(modifier === 'Meta' ? 'MacIntel' : 'Linux')
  const fetchMock = vi.fn<(input: RequestInfo | URL, init?: RequestInit) => Promise<Response>>(async (input) => {
    if (String(input).endsWith('/refs')) return refsResponse()
    if (String(input).includes('/repository/prepare')) return preparationResponse()
    return sessionResponse()
  })
  vi.stubGlobal('fetch', fetchMock)
  const user = userEvent.setup()
  render(
    <MemoryRouter initialEntries={['/new']}>
      <Routes>
        <Route path="/new" element={<NewAgentDialog open project={project} capabilities={[
          ...capabilities,
          { type: 'opencode', available: true, version: '1.18.26' },
        ]} onClose={() => undefined} />} />
        <Route path="/holons/holon-1" element={<h1>Holon detail</h1>} />
      </Routes>
    </MemoryRouter>,
  )

  if (action === 'agent selection') {
    const trigger = screen.getByRole('combobox', { name: 'Start with' })
    await user.click(trigger)
    // The shortcut must not submit while choosing an option in the open list.
    await user.keyboard(`{${modifier}>}{Enter}{/${modifier}}`)
    expect(fetchMock.mock.calls.filter(([path]) => String(path) === '/api/v1/holons')).toHaveLength(0)
    if (!screen.queryByRole('listbox')) await user.click(trigger)
    await user.keyboard('OpenCode{Enter}')
    await waitFor(() => expect(trigger).toHaveFocus())
    expect(trigger).toHaveTextContent('OpenCode')
  } else {
    await user.click(screen.getByRole('button', { name: 'Set a custom Holon name' }))
    await user.type(screen.getByLabelText('Holon name (optional)'), 'Checkout repair')
    await user.keyboard(action === 'name confirmation' ? '{Enter}' : '{Escape}')
    expect(screen.getByRole('button', { name: action === 'name confirmation' ? 'Edit Holon name' : 'Set a custom Holon name' })).toHaveFocus()
  }

  expect(fetchMock.mock.calls.filter(([path]) => String(path) === '/api/v1/holons')).toHaveLength(0)
  await user.keyboard(`{${modifier}>}{Enter}{/${modifier}}`)
  expect(await screen.findByRole('heading', { name: 'Holon detail' })).toBeInTheDocument()
  const creates = fetchMock.mock.calls.filter(([path]) => String(path) === '/api/v1/holons')
  expect(creates).toHaveLength(1)
  expect(JSON.parse(String(creates[0][1]?.body))).toMatchObject({
    prompt: '',
    agent_type: action === 'agent selection' ? 'opencode' : 'codex',
    ...(action === 'name confirmation' ? { title: 'Checkout repair' } : {}),
  })
})

it('limits optional Holon names to 80 Unicode characters', async () => {
  const user = userEvent.setup()
  render(
    <MemoryRouter>
      <NewAgentDialog open project={project} capabilities={capabilities} onClose={() => undefined} />
    </MemoryRouter>,
  )

  const boundary = 'é'.repeat(HOLON_TITLE_MAX_CHARACTERS - 1) + '🙂'
  await user.click(screen.getByRole('button', { name: 'Set a custom Holon name' }))
  const input = screen.getByLabelText('Holon name (optional)')
  fireEvent.change(input, { target: { value: boundary + '界' } })

  expect(input).toHaveValue(boundary)
})

it('opens the accepted preparation immediately with the shared store already populated', async () => {
  const create = deferred<Response>()
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    if (String(input).endsWith('/refs')) return refsResponse()
    if (String(input) === '/api/v1/holons' && init?.method === 'POST') return create.promise
    if (String(input) === '/api/v1/holons') return jsonResponse({ holons: [] })
    throw new Error('Unexpected request: ' + input)
  })
  vi.stubGlobal('fetch', fetchMock)
  const onClose = vi.fn()
  function Detail() {
    const h = useOptionalHolonStore()?.getHolon('holon-1')
    return <h1>{h?.status === 'preparing' && !h.worktree_path && h.agent_sessions?.length === 0 ? 'Reserved holon detail' : 'Missing reservation'}</h1>
  }
  render(<MemoryRouter><HolonStoreProvider projectId="holark"><Routes>
    <Route path="/" element={<NewAgentDialog open project={project} capabilities={capabilities} onClose={onClose} />} />
    <Route path="/holons/holon-1" element={<Detail />} />
  </Routes></HolonStoreProvider></MemoryRouter>)
  const user = userEvent.setup()
  await user.type(screen.getByLabelText('Task'), 'Fix it')
  await user.click(screen.getByRole('button', { name: 'Create Holon' }))
  expect(screen.getByRole('button', { name: 'Creating holon...' })).toBeDisabled()
  expect(onClose).not.toHaveBeenCalled()
  const request = fetchMock.mock.calls.find(([, init]) => init?.method === 'POST')!
  expect(new Headers(request[1]?.headers).get('Prefer')).toBe('respond-async')
  expect(JSON.parse(String(request[1]?.body))).toMatchObject({ base_branch: 'main', prompt: 'Fix it' })
  expect(JSON.parse(String(request[1]?.body))).not.toHaveProperty('base_commit')
  // Keep the next list poll at the reservation too; no workspace or terminal is ready.
  fetchMock.mockImplementation(async (input, init) => {
    if (String(input) === '/api/v1/holons' && init?.method !== 'POST') return jsonResponse({ holons: [await sessionResponse().json()] })
    throw new Error('Unexpected request: ' + input)
  })
  await act(async () => { create.resolve(sessionResponse()) })
  expect(await screen.findByRole('heading', { name: 'Reserved holon detail' })).toBeInTheDocument()
  expect(onClose).toHaveBeenCalledTimes(1)
  expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  expect(fetchMock.mock.calls.some(([path]) => String(path).endsWith('/prepare'))).toBe(false)
})

it('resets task text and submit state after a successful start', async () => {
  vi.stubGlobal('innerWidth', 1440)
  vi.stubGlobal('innerHeight', 900)
  const fetchMock = vi.fn<(input: RequestInfo | URL, init?: RequestInit) => Promise<Response>>(async (input) => {
    if (String(input).endsWith('/session-startup-refresh')) return preparationResponse()
    return sessionResponse()
  })
  vi.stubGlobal('fetch', fetchMock)
  const user = userEvent.setup()

  function Harness() {
    const [open, setOpen] = useState(true)
    return (
      <>
        <button type="button" onClick={() => setOpen(true)}>New Holon</button>
        <NewAgentDialog open={open} project={project} capabilities={capabilities} onClose={() => setOpen(false)} />
      </>
    )
  }

  render(
    <MemoryRouter initialEntries={['/projects/holark']}>
      <Harness />
      <Routes>
        <Route path="/holons/holon-1" element={<h1>Holon detail</h1>} />
      </Routes>
    </MemoryRouter>,
  )

  await user.click(screen.getByRole('button', { name: 'Set a custom Holon name' }))
  await user.type(screen.getByLabelText('Holon name (optional)'), 'Temporary name')
  await user.type(screen.getByLabelText('Task'), 'Fix it')
  await user.click(screen.getByRole('button', { name: 'Create Holon' }))

  expect(await screen.findByRole('heading', { name: 'Holon detail' })).toBeInTheDocument()
  expect(screen.queryByRole('dialog', { name: 'New Holon' })).not.toBeInTheDocument()

  await user.click(screen.getByRole('button', { name: 'New Holon' }))

  expect(screen.queryByLabelText('Holon name (optional)')).not.toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Set a custom Holon name' })).toBeVisible()
  expect(screen.getByLabelText('Task')).toHaveValue('')
  expect(screen.getByRole('button', { name: 'Create Holon' })).toBeEnabled()
})

it('restores and clears an unfinished new-agent task', async () => {
  const user = userEvent.setup()

  function Harness() {
    const [open, setOpen] = useState(true)
    return (
      <>
        <button type="button" onClick={() => setOpen(true)}>New Holon</button>
        <NewAgentDialog open={open} project={project} capabilities={capabilities} onClose={() => setOpen(false)} />
      </>
    )
  }

  render(
    <MemoryRouter>
      <Harness />
    </MemoryRouter>,
  )

  await user.type(screen.getByLabelText('Task'), 'Fix it later')
  await user.click(screen.getByRole('button', { name: 'Cancel' }))
  await user.click(screen.getByRole('button', { name: 'New Holon' }))

  const task = screen.getByLabelText('Task')
  expect(task).toHaveValue('Fix it later')

  await user.click(screen.getByRole('button', { name: 'Clear' }))

  expect(task).toHaveValue('')
  expect(task).toHaveFocus()
  expect(screen.getByRole('button', { name: 'Create Holon' })).toBeEnabled()
})

it('keeps the task and re-enables the form when reservation admission fails', async () => {
  const fetchMock = vi.fn<(input: RequestInfo | URL, init?: RequestInit) => Promise<Response>>(async (input) => String(input).endsWith('/refs') ? refsResponse() : new Response(JSON.stringify({
    code: 'repository_unavailable',
    message: 'Repository unavailable.',
  }), { status: 503, headers: { 'Content-Type': 'application/json' } }))
  vi.stubGlobal('fetch', fetchMock)
  const user = userEvent.setup()

  render(
    <MemoryRouter>
      <NewAgentDialog open project={project} capabilities={capabilities} onClose={() => undefined} />
    </MemoryRouter>,
  )

  await user.type(screen.getByLabelText('Task'), 'Fix it')
  await user.click(screen.getByRole('button', { name: 'Create Holon' }))

  expect(await screen.findByRole('alert')).toHaveTextContent('Repository unavailable.')
  expect(screen.getByLabelText('Task')).toHaveValue('Fix it')
  expect(screen.getByRole('button', { name: 'Create Holon' })).toBeEnabled()
  expect(fetchMock.mock.calls.filter(([path]) => String(path) === '/api/v1/holons')).toHaveLength(1)
})

it('keeps task input focused across parent rerenders', async () => {
  const user = userEvent.setup()
  const view = render(
    <MemoryRouter>
      <NewAgentDialog open project={project} capabilities={capabilities} onClose={() => undefined} />
    </MemoryRouter>,
  )

  const taskInput = screen.getByLabelText('Task')
  expect(taskInput).toHaveFocus()
  await user.type(taskInput, 'Fix it')

  view.rerender(
    <MemoryRouter>
      <NewAgentDialog
        open
        project={project}
        capabilities={[...capabilities]}
        onClose={() => undefined}
      />
    </MemoryRouter>,
  )

  expect(screen.getByLabelText('Task')).toHaveFocus()
  expect(screen.getByLabelText('Task')).toHaveValue('Fix it')
})

it('selects all installed harnesses and starts an unsupported version without confirmation', async () => {
  const allCapabilities = [
    ...capabilities,
    { type: 'claude-code' as const, available: true, version: '2.1.220' },
    { type: 'opencode' as const, available: true, version: '1.18.26', support_status: 'unsupported' as const, warning: 'Holark supports OpenCode <= 1.18.25. 1.18.26 may not work as expected.' },
  ]
  const fetchMock = vi.fn<(input: RequestInfo | URL, init?: RequestInit) => Promise<Response>>(async (input) => {
    if (String(input).endsWith('/refs')) return refsResponse()
    if (String(input).includes('/repository/prepare')) return preparationResponse()
    return sessionResponse()
  })
  vi.stubGlobal('fetch', fetchMock)
  const user = userEvent.setup()

  render(
    <MemoryRouter initialEntries={['/projects/holark']}>
      <Routes>
        <Route path="/projects/holark" element={<NewAgentDialog open project={project} capabilities={allCapabilities} onClose={() => undefined} />} />
        <Route path="/holons/holon-1" element={<h1>Holon detail</h1>} />
      </Routes>
    </MemoryRouter>,
  )

  const dialog = screen.getByRole('dialog', { name: 'New Holon' })
  const harness = screen.getByRole('combobox', { name: 'Start with' })
  expect(dialog).toContainElement(harness)
  expect(harness).toHaveTextContent('Codex')
  expect(harness).toHaveAttribute('aria-expanded', 'false')

  await user.click(harness)
  const listbox = screen.getByRole('listbox')
  expect(dialog).toContainElement(listbox)
  expect(harness).toHaveAttribute('aria-expanded', 'true')
  expect(within(listbox).getByRole('option', { name: 'Codex' })).toHaveAttribute('aria-selected', 'true')
  expect(within(listbox).getByRole('option', { name: 'OpenCode' })).toHaveTextContent('v1.18.26')
  expect(within(listbox).queryByLabelText('Version warning')).not.toBeInTheDocument()

  await user.keyboard('{Escape}')
  expect(screen.queryByRole('listbox')).not.toBeInTheDocument()
  expect(dialog).toBeInTheDocument()
  expect(harness).toHaveFocus()

  await user.click(harness)
  await user.click(screen.getByRole('option', { name: 'Claude Code' }))
  expect(harness).toHaveTextContent('Claude Code')
  expect(screen.getByPlaceholderText('Describe what Claude Code should do…')).toBeInTheDocument()

  await user.click(harness)
  await user.click(screen.getByRole('option', { name: 'OpenCode' }))
  expect(harness).toHaveTextContent('OpenCode')
  expect(screen.queryByText('Holark supports OpenCode <= 1.18.25. 1.18.26 may not work as expected.')).not.toBeInTheDocument()
  expect(screen.getByPlaceholderText('Describe what OpenCode should do…')).toBeInTheDocument()

  await user.type(screen.getByLabelText('Task'), 'Fix it')
  await user.click(screen.getByRole('button', { name: 'Create Holon' }))
  await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2))

  const createCall = fetchMock.mock.calls.find(([path]) => String(path) === '/api/v1/holons')
  expect(JSON.parse(String(createCall?.[1]?.body))).toMatchObject({
    prompt: 'Fix it',
    agent_type: 'opencode',
  })
})

it('preserves an unavailable saved default and links to Settings', async () => {
  const savedDefault = [
    { type: 'codex' as const, available: true, version: '1.0' },
    { type: 'opencode' as const, available: false, unavailable_reason: 'OpenCode CLI was not found; configure the node PATH' },
  ] as HarnessCapabilities
  savedDefault.default_harness = 'opencode'

  render(
    <MemoryRouter>
      <NewAgentDialog open project={project} capabilities={savedDefault} onClose={() => undefined} />
    </MemoryRouter>,
  )

  expect(screen.getByRole('combobox', { name: 'Start with' })).toHaveTextContent('OpenCode')
  expect(screen.getByRole('button', { name: 'Create Holon' })).toBeDisabled()
  expect(screen.getByText(/OpenCode CLI was not found; configure the node PATH/)).toBeInTheDocument()
  expect(await screen.findByRole('link', { name: 'Settings' })).toHaveAttribute('href', '/settings')
  await userEvent.setup().click(screen.getByRole('combobox', { name: 'Start with' }))
  const option = screen.getByRole('option', { name: 'OpenCode' })
  expect(option).toHaveTextContent('Unavailable :(')
  expect(option).not.toHaveTextContent('PATH')
})

it('allows an empty agent task and preserves it when switching to and from Terminal', async () => {
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => String(input).endsWith('/refs') ? refsResponse() : preparationResponse()))
  const user = userEvent.setup()
  render(<MemoryRouter><NewAgentDialog open project={project} capabilities={capabilities} onClose={() => undefined} /></MemoryRouter>)

  const submit = screen.getByRole('button', { name: 'Create Holon' })
  const choice = screen.getByRole('combobox', { name: 'Start with' })
  expect(submit).toBeEnabled()
  await user.click(choice)
  await user.click(screen.getByRole('option', { name: 'Terminal' }))
  expect(screen.getByLabelText('Task (optional)')).toHaveValue('')
  expect(submit).toBeEnabled()
  expect(screen.getByText('The task describes your work. It is not executed in the terminal.')).toBeInTheDocument()

  await user.type(screen.getByLabelText('Task (optional)'), 'Inspect the checkout')
  await user.click(choice)
  await user.click(screen.getByRole('option', { name: 'Codex' }))
  expect(screen.getByLabelText('Task')).toHaveValue('Inspect the checkout')
  await user.click(choice)
  await user.click(screen.getByRole('option', { name: 'Terminal' }))
  expect(screen.getByLabelText('Task (optional)')).toHaveValue('Inspect the checkout')
})

it.each(['unavailable', 'failed'] as const)('creates a promptless Terminal when agent capabilities are %s', async (capabilityResult) => {
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = String(input)
    if (path.endsWith('/agent-capabilities')) {
      return capabilityResult === 'failed'
        ? new Response('{}', { status: 503 })
        : jsonResponse({ capabilities: [{ type: 'codex', available: false }] })
    }
    if (path.endsWith('/refs')) return refsResponse()
    if (path.endsWith('/prepare')) return jsonResponse({ ref: 'feature/foo', commit: 'b'.repeat(40) })
    void init
    return sessionResponse()
  })
  vi.stubGlobal('fetch', fetchMock)
  const user = userEvent.setup()
  render(
    <MemoryRouter>
      <Routes>
        <Route path="/" element={<NewAgentDialog open project={project} initialBranch="feature/foo" onClose={() => undefined} />} />
        <Route path="/holons/holon-1" element={<h1>Holon detail</h1>} />
      </Routes>
    </MemoryRouter>,
  )

  await user.click(screen.getByRole('combobox', { name: 'Start with' }))
  await user.click(screen.getByRole('option', { name: 'Terminal' }))
  await user.click(screen.getByRole('button', { name: 'Create Holon' }))
  expect(await screen.findByRole('heading', { name: 'Holon detail' })).toBeInTheDocument()
  const createCall = fetchMock.mock.calls.find(([path]) => String(path) === '/api/v1/holons')
  expect(JSON.parse(String(createCall?.[1]?.body))).toEqual({
    prompt: '', startup_mode: 'terminal', kind: 'normal', base_branch: 'feature/foo',
  })
})

it('keeps custom submission agent-only', async () => {
  const submit = vi.fn(async () => ({ id: 'holon-1', status: 'queued' as const, prompt: 'Fix it', created_at: new Date().toISOString() }))
  const user = userEvent.setup()
  render(<MemoryRouter><NewAgentDialog open project={project} capabilities={capabilities} onSubmit={submit} onClose={() => undefined} /></MemoryRouter>)
  await user.click(screen.getByRole('combobox', { name: 'Start with' }))
  expect(screen.queryByRole('option', { name: 'Terminal' })).not.toBeInTheDocument()
  await user.keyboard('{Escape}')
  expect(screen.getByRole('button', { name: 'Start agent' })).toBeEnabled()
  await user.type(screen.getByLabelText('Task'), 'Fix it')
  await user.click(screen.getByRole('button', { name: 'Start agent' }))
  expect(submit).toHaveBeenCalledWith('Fix it', undefined, 'codex')
})

it('dismissing an unsubmitted dialog creates no reservation or preparation', async () => {
  const fetchMock = vi.fn(async (_input: RequestInfo | URL) => refsResponse())
  vi.stubGlobal('fetch', fetchMock)
  function Harness() {
    const [open, setOpen] = useState(true)
    return <NewAgentDialog open={open} project={project} capabilities={capabilities} onClose={() => setOpen(false)} />
  }
  render(<StrictMode><MemoryRouter><Harness /></MemoryRouter></StrictMode>)
  const user = userEvent.setup()
  await user.type(screen.getByLabelText('Task'), 'Keep my task')
  await user.click(screen.getByRole('button', { name: 'Cancel' }))
  expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  expect(fetchMock.mock.calls.every(([path]) => String(path).endsWith('/refs'))).toBe(true)
})

it('recovers available agents after a failed capability request', async () => {
  let attempts = 0
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
    if (String(input).endsWith('/agent-capabilities')) {
      if (++attempts === 1) return new Response('{}', { status: 503 })
      return jsonResponse({ capabilities })
    }
    if (String(input).endsWith('/refs')) return refsResponse()
    if (String(input).endsWith('/diagnostics')) return new Response(null, { status: 204 })
    return preparationResponse()
  }))
  render(<MemoryRouter><NewAgentDialog open project={project} onClose={() => undefined} /></MemoryRouter>)
  const user = userEvent.setup()
  expect(await screen.findByRole('alert')).toHaveTextContent('Couldn’t load available agents.')
  await user.type(screen.getByLabelText('Task'), 'Fix it')
  expect(screen.getByRole('button', { name: 'Create Holon' })).toBeDisabled()
  await user.click(screen.getByRole('button', { name: 'Retry' }))
  await waitFor(() => expect(screen.getByRole('button', { name: 'Create Holon' })).toBeEnabled())
  expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  expect(attempts).toBe(2)
})
