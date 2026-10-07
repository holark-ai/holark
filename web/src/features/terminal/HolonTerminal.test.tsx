import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useCallback, useEffect, useMemo, useState, type ComponentProps, type ReactNode, type SetStateAction } from 'react'
import { Link, MemoryRouter, Route, Routes } from 'react-router-dom'
import { ColorThemeProvider } from '../../app/ColorThemeProvider'
import { colorThemeStorageKey } from '../../app/colorTheme'
import type { AgentSession, ApiProject, HarnessCapabilities, HarnessCapability, HarnessType, Holon } from '../../data/types'
import { OperationsPanel } from '../operations/OperationsPanel'
import { ProjectContext as BaseProjectContext } from '../project/ProjectContext'
import { HolonStoreProvider } from '../project/HolonStore'
import { HolonStoreContext, type HolonStoreValue } from '../project/holonStoreContext'

vi.mock('./TextComparison', () => ({
  default: ({ path, original, modified }: { path: string; original: string; modified: string }) => <div aria-label={`Comparison ${path}`}><pre>{original}</pre><pre>{modified}</pre></div>,
}))

const terminalMock = vi.hoisted(() => {
  type MockTerminalInstance = {
    id: number
    cols: number
    rows: number
    disableStdin: boolean
    theme?: Record<string, string>
    buffer: { baseY: number, viewportY: number }
    proposedDimensions?: { cols: number, rows: number }
    autoCompleteWrites?: boolean
    pendingWrites: Array<() => void>
    writes: Array<string | Uint8Array>
    input?: (data: string) => void
    host?: HTMLElement
    textarea?: HTMLTextAreaElement
    disposed: boolean
    focus: ReturnType<typeof vi.fn<() => void>>
    refresh: ReturnType<typeof vi.fn<(start: number, end: number) => void>>
    reset: ReturnType<typeof vi.fn<() => void>>
    resize: ReturnType<typeof vi.fn<(columns: number, rows: number) => void>>
    scrollToBottom: ReturnType<typeof vi.fn<() => void>>
    scrollToLine: ReturnType<typeof vi.fn<(line: number) => void>>
    dispose: ReturnType<typeof vi.fn<() => void>>
  }
  return {
    focus: vi.fn(),
    refresh: vi.fn(),
    reset: vi.fn(),
    resize: vi.fn(),
    scrollToBottom: vi.fn(),
    scrollToLine: vi.fn(),
    dispose: vi.fn(),
    write: vi.fn(),
    fitPropose: vi.fn(),
    disableStdin: false,
    autoCompleteWrites: true,
    pendingWrites: [] as Array<() => void>,
    input: undefined as ((data: string) => void) | undefined,
    theme: undefined as Record<string, string> | undefined,
    themeUpdates: [] as Array<Record<string, string> | undefined>,
    proposedDimensions: { cols: 120, rows: 40 } as { cols: number, rows: number } | undefined,
    instances: [] as MockTerminalInstance[],
    constructed: 0,
  }
})

vi.mock('@xterm/xterm', () => ({
  Terminal: class {
    __state: (typeof terminalMock.instances)[number]
    options: { disableStdin: boolean; theme?: Record<string, string> }
    buffer: { active: { baseY: number, viewportY: number } }
    unicode = { activeVersion: '6' }
    parser = { registerOscHandler: () => ({ dispose() {} }) }
    textarea: HTMLTextAreaElement | undefined

    constructor(options: { disableStdin?: boolean; theme?: Record<string, string> } = {}) {
      terminalMock.constructed += 1
      const state: (typeof terminalMock.instances)[number] = {
        id: terminalMock.constructed,
        cols: 120,
        rows: 40,
        disableStdin: true,
        theme: undefined,
        buffer: { baseY: 0, viewportY: 0 },
        pendingWrites: [],
        writes: [],
        disposed: false,
        focus: vi.fn(),
        refresh: vi.fn(),
        reset: vi.fn(),
        resize: vi.fn(),
        scrollToBottom: vi.fn(),
        scrollToLine: vi.fn(),
        dispose: vi.fn(),
      }
      terminalMock.instances.push(state)
      this.__state = state
      this.buffer = { active: state.buffer }
      this.options = Object.defineProperties({}, {
        disableStdin: {
          get: () => state.disableStdin,
          set: (value: boolean) => {
            state.disableStdin = value
            terminalMock.disableStdin = value
          },
          enumerable: true,
        },
        theme: {
          get: () => state.theme,
          set: (value: Record<string, string> | undefined) => {
            state.theme = value
            terminalMock.theme = value
            terminalMock.themeUpdates.push(value)
          },
          enumerable: true,
        },
      }) as { disableStdin: boolean; theme?: Record<string, string> }
      this.options.disableStdin = Boolean(options.disableStdin)
      this.options.theme = options.theme
    }

    get cols() { return this.__state.cols }
    set cols(value: number) { this.__state.cols = value }
    get rows() { return this.__state.rows }
    set rows(value: number) { this.__state.rows = value }

    loadAddon(addon: { activate?: (terminal: unknown) => void }) { addon.activate?.(this) }
    open(host: HTMLElement) {
      this.__state.host = host
      this.textarea = document.createElement('textarea')
      this.__state.textarea = this.textarea
      host.append(this.textarea)
    }
    focus() {
      this.__state.focus()
      terminalMock.focus()
      this.textarea?.focus()
    }
    refresh(start: number, end: number) {
      this.__state.refresh(start, end)
      terminalMock.refresh(start, end)
    }
    reset() {
      this.__state.reset()
      terminalMock.reset()
    }
    resize(columns: number, rows: number) {
      this.__state.cols = columns
      this.__state.rows = rows
      this.__state.resize(columns, rows)
      terminalMock.resize(columns, rows)
    }
    scrollToBottom() {
      this.__state.buffer.viewportY = this.__state.buffer.baseY
      this.__state.scrollToBottom()
      terminalMock.scrollToBottom()
    }
    scrollToLine(line: number) {
      this.__state.buffer.viewportY = line
      this.__state.scrollToLine(line)
      terminalMock.scrollToLine(line)
    }
    write(data: string | Uint8Array, callback?: () => void) {
      this.__state.writes.push(data)
      terminalMock.write(data)
      if (!callback) return
      if (this.__state.autoCompleteWrites ?? terminalMock.autoCompleteWrites) {
        callback()
        return
      }
      const complete = () => {
        const instanceIndex = this.__state.pendingWrites.indexOf(complete)
        if (instanceIndex >= 0) this.__state.pendingWrites.splice(instanceIndex, 1)
        const globalIndex = terminalMock.pendingWrites.indexOf(complete)
        if (globalIndex >= 0) terminalMock.pendingWrites.splice(globalIndex, 1)
        callback()
      }
      this.__state.pendingWrites.push(complete)
      terminalMock.pendingWrites.push(complete)
    }
    onBinary() {
      return { dispose: vi.fn() }
    }

    onScroll() {
      return { dispose: vi.fn() }
    }

    registerLinkProvider() {
      return { dispose: vi.fn() }
    }

    onData(callback: (data: string) => void) {
      this.__state.input = callback
      terminalMock.input = callback
      return {
        dispose: () => {
          if (this.__state.input === callback) this.__state.input = undefined
          if (terminalMock.input === callback) terminalMock.input = undefined
        },
      }
    }
    dispose() {
      this.__state.disposed = true
      this.__state.dispose()
      terminalMock.dispose()
      this.textarea?.remove()
    }
  },
}))
vi.mock('@xterm/addon-fit', () => ({
  FitAddon: class {
    terminal?: { __state: (typeof terminalMock.instances)[number] }
    activate(terminal: { __state: (typeof terminalMock.instances)[number] }) { this.terminal = terminal }
    proposeDimensions() {
      terminalMock.fitPropose()
      return this.terminal?.__state.proposedDimensions ?? terminalMock.proposedDimensions
    }
    fit() {}
    dispose() {}
  },
}))
vi.mock('@xterm/addon-unicode11', () => ({
  Unicode11Addon: class {
    activate() {}
    dispose() {}
  },
}))

import { applyTabOrderToHolon, HolonTerminal as HolonTerminalView } from './HolonTerminal'
import { TerminalWorkspaceProvider } from './TerminalWorkspaceProvider'
import { terminalProtocolVersion } from './terminalProtocol'

function HolonTerminal(props: ComponentProps<typeof HolonTerminalView>) {
  return (
    <ColorThemeProvider><TerminalWorkspaceProvider><HolonTerminalView {...props} /></TerminalWorkspaceProvider></ColorThemeProvider>
  )
}

const ProjectContext = {
  Provider({ value, children }: { value: ApiProject; children: ReactNode }) {
    return (
      <BaseProjectContext.Provider value={value}>
        <HolonStoreProvider projectId={value.id}>{children}</HolonStoreProvider>
      </BaseProjectContext.Provider>
    )
  },
}

class FakeSocket {
  readyState = 0
  binaryType = ''
  sent: string[] = []
  closed = false
  onopen: (() => void) | null = null
  onmessage: ((event: { data: string | ArrayBuffer }) => void) | null = null
  onclose: ((event: CloseEvent) => void) | null = null
  send(data: string) { this.sent.push(data) }
  close() { this.readyState = 3; this.closed = true; this.onclose?.(new CloseEvent('close')) }
  open() { this.readyState = 1; this.onopen?.() }
  message(value: object) { this.onmessage?.({ data: JSON.stringify({ version: terminalProtocolVersion, ...value }) }) }
  messageBinary(value: Uint8Array) {
    this.onmessage?.({ data: value.buffer.slice(value.byteOffset, value.byteOffset + value.byteLength) as ArrayBuffer })
  }
}

afterEach(() => {
  vi.restoreAllMocks()
  window.localStorage?.removeItem(colorThemeStorageKey)
  window.sessionStorage?.clear()
  vi.unstubAllGlobals()
  terminalMock.focus.mockReset()
  terminalMock.refresh.mockReset()
  terminalMock.reset.mockReset()
  terminalMock.resize.mockReset()
  terminalMock.scrollToBottom.mockReset()
  terminalMock.scrollToLine.mockReset()
  terminalMock.dispose.mockReset()
  terminalMock.write.mockReset()
  terminalMock.fitPropose.mockReset()
  terminalMock.disableStdin = false
  terminalMock.autoCompleteWrites = true
  terminalMock.pendingWrites = []
  terminalMock.input = undefined
  terminalMock.theme = undefined
  terminalMock.themeUpdates = []
  terminalMock.proposedDimensions = { cols: 120, rows: 40 }
  terminalMock.instances = []
  terminalMock.constructed = 0
})


function stubApi(holon: object, pullRequests: object[] = [], inspection: object | ((url: URL) => object | Response) = sessionInspection('', false), cancelError?: { code: string; message: string }) {
  const storedHolon = { ...(holon as Record<string, unknown>) }
  const fetchMock = vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
    const path = String(input)
    const url = new URL(path, 'http://test.local')
    const method = input instanceof Request ? input.method : (init?.method ?? 'GET')
    if (url.pathname.endsWith('/agent-capabilities')) {
      return new Response(JSON.stringify({ capabilities: [{ type: 'codex', available: true }] }), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    if (/\/holons\/[^/]+$/.test(url.pathname) && method === 'PATCH') {
      const title = JSON.parse(String(init?.body ?? '{}')).title ?? ''
      storedHolon.title = title
      return new Response(JSON.stringify(storedHolon), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    const sessionPullRequest = url.pathname.match(/\/holons\/([^/]+)\/pull-request$/)
    if (sessionPullRequest) {
      const holonId = decodeURIComponent(sessionPullRequest[1])
      const pullRequest = pullRequests.find((candidate) => Array.isArray((candidate as { linked_session_ids?: unknown }).linked_session_ids) && ((candidate as { linked_session_ids: string[] }).linked_session_ids).includes(holonId))
      if (pullRequest) {
        return new Response(JSON.stringify({ pull_request: pullRequest }), { status: 200, headers: { 'Content-Type': 'application/json' } })
      }
      return new Response(JSON.stringify({ code: 'pull_request_not_found', message: 'Pull request not found.' }), { status: 404, headers: { 'Content-Type': 'application/json' } })
    }
    if (url.pathname.endsWith('/end') && method === 'POST' && cancelError) {
      return new Response(JSON.stringify(cancelError), { status: 409, headers: { 'Content-Type': 'application/json' } })
    }
    const forkedAgent = url.pathname.match(/\/agent-sessions\/([^/]+)\/fork$/)
    if (forkedAgent && method === 'POST') {
      const now = new Date().toISOString()
      const existing = (storedHolon.agent_sessions as Array<Record<string, unknown>> | undefined) ?? []
      const sourceId = decodeURIComponent(forkedAgent[1])
      const source = existing.find((candidate) => candidate.id === sourceId) ?? {}
      const created = {
        ...source,
        id: `forked-agent-${existing.length + 1}`,
        holon_id: storedHolon.id,
        terminal_id: `forked-terminal-${existing.length + 1}`,
        title: 'Fork',
        status: 'running',
        resume_target: undefined,
        rollout_path: undefined,
        tab_order: existing.length + 1,
        created_at: now,
        updated_at: now,
      }
      storedHolon.agent_sessions = [...existing, created]
      return new Response(JSON.stringify(created), { status: 201, headers: { 'Content-Type': 'application/json' } })
    }
    if (url.pathname.endsWith('/agent-sessions') && method === 'POST') {
      const now = new Date().toISOString()
      const existing = (storedHolon.agent_sessions as Array<Record<string, unknown>> | undefined) ?? []
      const request = JSON.parse(String(init?.body ?? '{}')) as { agent_type?: HarnessType; prompt?: string }
      const created = {
        id: `created-agent-${existing.length + 1}`,
        holon_id: storedHolon.id,
        terminal_id: `created-terminal-${existing.length + 1}`,
        agent_type: request.agent_type ?? 'codex',
        title: 'Agent',
        prompt: request.prompt?.trim() ?? '',
        status: 'running',
        input_state: 'task_complete', activity: 'completed',
        tab_order: existing.length + 1,
        created_at: now,
        updated_at: now,
      }
      storedHolon.agent_sessions = [...existing, created]
      return new Response(JSON.stringify(created), { status: 201, headers: { 'Content-Type': 'application/json' } })
    }
    const terminalHolon = url.pathname.match(/\/holons\/([^/]+)\/terminals(?:\/([^/]+))?(?:\/close)?$/)?.[1] ?? 'holon-1'
    const terminal = manualTerminal(terminalHolon)
    const resolvedInspection = typeof inspection === 'function' ? inspection(url) : inspection
    if (url.pathname.endsWith('/workspace') && resolvedInspection instanceof Response) return resolvedInspection
    const body = url.pathname.endsWith('/holons') ? [storedHolon]
      : url.pathname.endsWith('/pull-requests') ? pullRequests
        : url.pathname.endsWith('/workspace') ? inspectionResponse(resolvedInspection, url.searchParams.get('summary') === 'true', url.searchParams.get('path') ?? '')
          : url.pathname.endsWith('/terminals') && method === 'POST' ? terminal
            : url.pathname.endsWith('/close') ? terminal
              : method === 'PATCH' && /\/terminals\/[^/]+$/.test(url.pathname) ? { ...terminal, title: JSON.parse(String(init?.body ?? '{}')).title ?? terminal.title, updated_at: new Date().toISOString() }
                : url.pathname.endsWith('/terminals') ? []
                : storedHolon
    return new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })
  })
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

function jsonResponse(body: unknown) {
  return new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })
}

function harnessCapabilities(harnessTypes: HarnessType[]): HarnessCapability[] {
  return harnessTypes.map((type) => ({ type, available: true }))
}

function stubReopenApi(holon: Holon, capabilities: HarnessCapabilities, inspection = sessionInspection(holon.worktree_branch ?? '', false), reopenFails = false, reopenGate?: Promise<void>) {
  let storedHolon = holon
  const fetchMock = vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
    const url = new URL(String(input), 'http://test.local')
    if (url.pathname.endsWith('/agent-capabilities')) return jsonResponse({ capabilities, default_harness: capabilities.default_harness, default_harness_explicit: capabilities.default_harness_explicit })
    if (url.pathname.endsWith('/holons')) return jsonResponse([storedHolon])
    if (url.pathname.endsWith('/pull-requests')) return jsonResponse([])
    if (url.pathname.endsWith('/pull-request')) {
      return new Response(JSON.stringify({ code: 'pull_request_not_found', message: 'Pull request not found.' }), { status: 404, headers: { 'Content-Type': 'application/json' } })
    }
    if (url.pathname.endsWith('/workspace')) return jsonResponse(inspectionResponse(inspection, false))
    if (url.pathname.endsWith('/publication-readiness')) return jsonResponse({
      target_branch: holon.upstream_branch ?? '', target_commit: holon.upstream_head_commit ?? '',
      workspace_head: inspection.head_commit, rebase_required: false, rebase_available: false,
      publication_available: Boolean(holon.upstream_head_commit && inspection.head_commit !== holon.upstream_head_commit),
    })
    if (url.pathname.endsWith('/reopen') && init?.method === 'POST') {
      await reopenGate
      if (reopenFails) return new Response(JSON.stringify({ message: 'Could not reopen workspace.' }), { status: 500, headers: { 'Content-Type': 'application/json' } })
      storedHolon = { ...holon, status: 'queued', archived_at: undefined }
      return jsonResponse(storedHolon)
    }
    return jsonResponse(storedHolon)
  })
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

function renamableHolon(title = 'Original holon', id = 'holon-rename') {
  const now = new Date().toISOString()
  const harness = {
    id: 'harness-1',
    holon_id: id,
    agent_type: 'codex',
    title: 'Agent',
    prompt: '',
    status: 'running',
    input_state: 'none', activity: 'working',
    tab_order: 1,
    terminal_id: 'terminal-agent',
    created_at: now,
    updated_at: now,
  }
  return {
    id,
    repository_id: 'holark',
    runtime_id: 'node-1',
    title,
    prompt: 'Fix difficult bug',
    worktree_branch: 'holark/holon-rename',
    status: 'running',
    input_state: 'none', activity: 'working',
    agent_session: harness,
    agent_sessions: [harness],
    created_at: now,
  }
}

function manualTerminal(holonId = 'holon-1', overrides: object = {}) {
  const now = new Date().toISOString()
  return {
    id: 'terminal-1',
    terminal_id: 'terminal-1',
    holon_id: holonId,
    title: 'Shell 1',
    cwd: `/tmp/${holonId}`,
    created_at: now,
    updated_at: now,
    ...overrides,
  }
}

it('keeps reordered manual terminals on the canonical Holon field', () => {
  const first = manualTerminal('holon-1', { id: 'terminal-1', tab_order: 1 })
  const second = manualTerminal('holon-1', { id: 'terminal-2', tab_order: 2 })
  const holon: Holon = {
    id: 'holon-1',
    prompt: 'Fix it',
    status: 'running',
    created_at: new Date().toISOString(),
    agent_sessions: [],
    manual_terminals: [first, second],
    ides: [],
  }

  const reordered = applyTabOrderToHolon(holon, [
    { type: 'terminal', id: 'terminal-2' },
    { type: 'terminal', id: 'terminal-1' },
  ])

  expect(reordered?.manual_terminals?.map((terminal) => terminal.id)).toEqual(['terminal-2', 'terminal-1'])
  expect(reordered).not.toHaveProperty('terminals')
})

function inspectionResponse(inspection: object, summary: boolean, path = '') {
  const typed = inspection as { files?: Array<Record<string, unknown>> }
  const files = (typed.files ?? []).filter((file) => !path || file.path === path)
  if (!summary) return { ...inspection, summary_only: false, files }
  return {
    ...inspection,
    summary_only: true,
    files: files.map((file) => {
      const copy = { ...file }
      delete copy.diff
      return copy
    }),
  }
}

function sessionInspection(branch: string, hasChanges: boolean, options: { dirty?: boolean; baseCommit?: string; headCommit?: string } = {}) {
  const baseCommit = options.baseCommit ?? 'abc123'
  return {
    branch,
    base_branch: 'main',
    base_commit: baseCommit,
    head_commit: options.headCommit ?? (hasChanges ? 'def456' : baseCommit),
    has_changes: hasChanges,
    dirty: options.dirty ?? false,
    files: hasChanges ? [{ path: 'README.md', status: 'M', additions: 2, deletions: 1, binary: false, content_status: 'ready', contents: { original: 'unchanged\nold\n', modified: 'unchanged\nnew\nextra\n' }, diff: 'diff --git a/README.md b/README.md\nindex 1111111..2222222 100644\n--- a/README.md\n+++ b/README.md\n@@ -102,2 +114,3 @@ func main() {\n unchanged\n-old\n+new\n+extra', diff_truncated: false }] : [],
    diff_truncated: false,
    selected_base_ref: 'session-start',
    selected_target_ref: 'worktree',
    ref_options: [
      { id: 'main', label: 'main (abc123)', kind: 'branch', commit: baseCommit },
      { id: 'session-start', label: `Holon start (${baseCommit})`, kind: 'session_start', commit: baseCommit },
      { id: 'worktree', label: `Worktree (HEAD ${options.headCommit ?? (hasChanges ? 'def456' : baseCommit)})`, kind: 'worktree', commit: options.headCommit ?? (hasChanges ? 'def456' : baseCommit) },
    ],
  }
}

function timelineInspection(branch = 'holark/holon-1') {
  const baseCommit = 'aaaaaaaa'
  const earlierCommit = 'bbbbbbbb'
  const olderCommit = '99999999'
  const penultimateCommit = '88888888'
  const oldestCommit = '77777777'
  const workSessionStartCommit = 'cccccccc'
  const firstSessionCommit = 'dddddddd'
  const newestSessionCommit = 'eeeeeeee'
  return {
    ...sessionInspection(branch, true, { baseCommit, headCommit: newestSessionCommit }),
    branch_base_commit: baseCommit,
    work_session_start_commit: workSessionStartCommit,
    workspace_head_commit: newestSessionCommit,
    commits: [
      { sha: newestSessionCommit, parent_commit: firstSessionCommit, subject: 'Newest holon change' },
      { sha: firstSessionCommit, parent_commit: workSessionStartCommit, subject: 'First holon change' },
      { sha: workSessionStartCommit, parent_commit: earlierCommit, subject: 'Holon starting point' },
      { sha: earlierCommit, parent_commit: olderCommit, subject: 'Pre-holon setup' },
      { sha: olderCommit, parent_commit: penultimateCommit, subject: 'Pre-holon refinement' },
      { sha: penultimateCommit, parent_commit: oldestCommit, subject: 'Older branch change' },
      { sha: oldestCommit, parent_commit: baseCommit, subject: 'Oldest branch change' },
    ],
  }
}

function sentTerminalMessages(socket: FakeSocket, type: string) {
  return socket.sent.map((value) => JSON.parse(value)).filter((message) => message.type === type)
}

it('shows OpenCode in the agent selector and uses its explicit tab icon', async () => {
  vi.stubGlobal('WebSocket', class { static OPEN = 1 })
  const now = new Date().toISOString()
  const openCodeHarness = {
    id: 'opencode-agent',
    agent_type: 'opencode' as const,
    title: 'OpenCode agent',
    status: 'running' as const,
    input_state: 'none' as const, activity: 'working' as const,
    created_at: now,
    updated_at: now,
  }
  const holon: Holon = {
    id: 'holon-opencode',
    repository_id: 'holark',
    runtime_id: 'opencode-node',
    prompt: 'Use OpenCode',
    worktree_branch: 'holark/opencode',
    worktree_path: '/tmp/holon-opencode',
    agent_session: openCodeHarness,
    agent_sessions: [openCodeHarness],
    status: 'running',
    input_state: 'none', activity: 'working',
    created_at: now,
  }
  const capabilities = harnessCapabilities(['codex', 'opencode']) as HarnessCapabilities
  capabilities[1] = { type: 'opencode', available: true, version: '1.18.26', support_status: 'unsupported', warning: 'Holark supports OpenCode <= 1.18.25. 1.18.26 may not work as expected.' }
  capabilities.default_harness = 'opencode'
  stubReopenApi(holon, capabilities)

  render(
    <ProjectContext.Provider value={{ id: 'holark', name: 'Holark', default_branch: 'main' }}>
      <MemoryRouter initialEntries={['/holons/holon-opencode']}>
        <Routes><Route path="/holons/:holonId" element={<HolonTerminal socketFactory={() => new FakeSocket() as unknown as WebSocket} />} /></Routes>
      </MemoryRouter>
    </ProjectContext.Provider>,
  )

  const tab = await screen.findByRole('tab', { name: 'OpenCode agent' })
  const logo = within(tab).getByTitle('OpenCode').querySelector('[data-harness-logo="opencode"]')
  expect(logo).toBeInTheDocument()
  expect(logo?.querySelectorAll('[fill="currentColor"]')).toHaveLength(2)
  expect(logo?.querySelectorAll('[fill="#131010"], [fill="#5A5858"], [fill="white"]')).toHaveLength(0)
  await userEvent.click(screen.getByRole('button', { name: 'New tab' }))
  expect(screen.getByRole('button', { name: 'OpenCode' })).toBeEnabled()
  expect(screen.queryByText(/may not work as expected/)).not.toBeInTheDocument()
})

it('offers available agents directly even when the default is unavailable', async () => {
  vi.stubGlobal('WebSocket', class { static OPEN = 1 })
  const holon = { ...renamableHolon('Unavailable default', 'holon-unavailable-default'), worktree_path: '/tmp/holon-unavailable-default' } as Holon
  const capabilities = [
    { type: 'codex' as const, available: true },
    { type: 'opencode' as const, available: false, unavailable_reason: 'OpenCode was not found' },
  ] as HarnessCapabilities
  capabilities.default_harness = 'opencode'
  stubReopenApi(holon, capabilities)

  render(
    <ProjectContext.Provider value={{ id: 'holark', name: 'Holark', default_branch: 'main' }}>
      <MemoryRouter initialEntries={['/holons/holon-unavailable-default']}>
        <Routes><Route path="/holons/:holonId" element={<HolonTerminal socketFactory={() => new FakeSocket() as unknown as WebSocket} />} /></Routes>
      </MemoryRouter>
    </ProjectContext.Provider>,
  )

  await screen.findByRole('tablist', { name: 'Holon terminals' })
  await userEvent.click(screen.getByRole('button', { name: 'New tab' }))

  expect(screen.getByRole('button', { name: 'OpenCode' })).toBeDisabled()
  expect(screen.getByRole('button', { name: 'Codex' })).toBeEnabled()
  expect(screen.queryByRole('link', { name: /Unavailable · Settings/ })).not.toBeInTheDocument()
})

it.each([
  {
    name: 'OpenCode-only',
    harnessTypes: ['opencode'] as HarnessType[],
    capabilities: harnessCapabilities(['opencode']),
  },
  {
    name: 'mixed-harness',
    harnessTypes: ['codex', 'opencode'] as HarnessType[],
    capabilities: harnessCapabilities(['codex', 'opencode']),
  },
])('reopens a $name holon from local capabilities', async ({ harnessTypes, capabilities }) => {
  vi.stubGlobal('WebSocket', class { static OPEN = 1 })
  const now = new Date().toISOString()
  const harnesses = harnessTypes.map((harnessType, index) => ({
    id: `agent-${index}`,
    agent_type: harnessType,
    title: `${harnessType} agent`,
    status: 'cancelled' as const,
    resume_target: `saved-conversation-${index}`,
    input_state: 'none' as const, activity: 'working' as const,
    created_at: now,
    updated_at: now,
  }))
  const holon: Holon = {
    id: 'holon-resume',
    repository_id: 'holark',
    runtime_id: '',
    prompt: 'Resume agents',
    worktree_branch: 'holark/resume',
    worktree_path: '/tmp/holon-resume',
    agent_session: harnesses[0],
    agent_sessions: harnesses,
    status: 'cancelled',
    archived_at: now,
    input_state: 'none', activity: 'working',
    created_at: now,
  }
  let releaseReopen: () => void = () => {}
  const reopenGate = new Promise<void>((resolve) => { releaseReopen = resolve })
  const fetchMock = stubReopenApi(holon, capabilities, undefined, false, reopenGate)

  render(
    <ProjectContext.Provider value={{ id: 'holark', name: 'Holark', default_branch: 'main' }}>
      <MemoryRouter initialEntries={['/holons/holon-resume']}>
        <Routes><Route path="/holons/:holonId" element={<HolonTerminal socketFactory={() => new FakeSocket() as unknown as WebSocket} />} /></Routes>
      </MemoryRouter>
    </ProjectContext.Provider>,
  )

  const resume = await screen.findByRole('button', { name: 'Reopen' })
  expect(resume).toBeEnabled()
  for (const harness of harnesses) {
    expect(screen.getByRole('tab', { name: harness.title })).toBeInTheDocument()
  }
  expect(screen.getByText('This workspace is archived. Reopen it to restore the saved conversation.')).toBeInTheDocument()
  fireEvent.click(resume)

  expect(await screen.findByRole('button', { name: 'Reopening...' })).toBeDisabled()
  releaseReopen()

  await waitFor(() => expect(fetchMock.mock.calls.some(([input, init]) => {
    const url = new URL(String(input), 'http://test.local')
    return url.pathname.endsWith('/reopen') && init?.method === 'POST' && init.body === undefined
  })).toBe(true))
  await waitFor(() => expect(screen.queryByRole('button', { name: 'Reopen' })).not.toBeInTheDocument())
})

it.each(['completed', 'failed'] as const)('reopens an unpublished review after its last agent %s', async (status) => {
  vi.stubGlobal('WebSocket', class { static OPEN = 1 })
  const now = new Date().toISOString()
  const harness = {
    id: 'review-agent', agent_type: 'codex' as const, title: 'Review agent', status,
    resume_target: 'saved-review', input_state: 'none' as const, activity: 'idle' as const,
    created_at: now, updated_at: now,
  }
  const holon: Holon = {
    id: 'review-draft', kind: 'pr_review', prompt: 'Review', status,
    worktree_branch: 'holark/review-draft', worktree_path: '/tmp/review-draft',
    agent_session: harness, agent_sessions: [harness],
    input_state: 'none', activity: 'idle', created_at: now,
  }
  const fetchMock = stubReopenApi(holon, harnessCapabilities(['codex']))
  render(
    <ProjectContext.Provider value={{ id: 'holark', name: 'Holark', default_branch: 'main' }}>
      <MemoryRouter initialEntries={['/holons/review-draft']}>
        <Routes><Route path="/holons/:holonId" element={<HolonTerminal socketFactory={() => new FakeSocket() as unknown as WebSocket} />} /></Routes>
      </MemoryRouter>
    </ProjectContext.Provider>,
  )
  const reopen = await screen.findByRole('button', { name: 'Reopen' })
  expect(reopen).toBeEnabled()
  fireEvent.click(reopen)
  await waitFor(() => expect(fetchMock.mock.calls.some(([input, init]) =>
    String(input).endsWith('/reopen') && init?.method === 'POST',
  )).toBe(true))
  await waitFor(() => expect(screen.queryByRole('button', { name: 'Reopen' })).not.toBeInTheDocument())
})

it('keeps a failed Reopen action available for retry', async () => {
  vi.stubGlobal('WebSocket', class { static OPEN = 1 })
  const now = new Date().toISOString()
  const harness = {
    id: 'agent-retry', agent_type: 'codex' as const, title: 'Saved agent', status: 'failed' as const,
    input_state: 'none' as const, activity: 'failed' as const, created_at: now, updated_at: now,
  }
  const holon: Holon = {
    id: 'holon-retry', prompt: 'Retry reopen', worktree_branch: 'holark/retry', worktree_path: '/tmp/holon-retry',
    agent_session: harness, agent_sessions: [harness], status: 'failed', archived_at: now,
    input_state: 'none', activity: 'failed', created_at: now,
  }
  const fetchMock = stubReopenApi(holon, harnessCapabilities(['codex']), sessionInspection(holon.worktree_branch!, false), true)

  render(
    <ProjectContext.Provider value={{ id: 'holark', name: 'Holark', default_branch: 'main' }}>
      <MemoryRouter initialEntries={['/holons/holon-retry']}>
        <Routes><Route path="/holons/:holonId" element={<HolonTerminal socketFactory={() => new FakeSocket() as unknown as WebSocket} />} /></Routes>
      </MemoryRouter>
    </ProjectContext.Provider>,
  )

  const reopen = await screen.findByRole('button', { name: 'Reopen' })
  await userEvent.click(reopen)
  expect(await screen.findByRole('alert')).toHaveTextContent('Could not reopen workspace.')
  expect(reopen).toBeEnabled()
  await userEvent.click(reopen)
  await waitFor(() => expect(fetchMock.mock.calls.filter(([input]) => new URL(String(input), 'http://test.local').pathname.endsWith('/reopen'))).toHaveLength(2))
})

it('hides pull request preparation while Reopen is primary', async () => {
  vi.stubGlobal('WebSocket', class { static OPEN = 1 })
  const now = new Date().toISOString()
  const harness = {
    id: 'agent-resume',
    agent_type: 'codex' as const,
    title: 'Codex agent',
    status: 'cancelled' as const,
    input_state: 'none' as const, activity: 'working' as const,
    created_at: now,
    updated_at: now,
  }
  const holon: Holon = {
    id: 'holon-resume-actions',
    repository_id: 'holark',
    runtime_id: '',
    prompt: 'Resume changes',
    worktree_branch: 'holark/resume-actions',
    proposed_upstream_branch: 'holark/proposed-resume-actions',
    worktree_path: '/tmp/holon-resume-actions',
    agent_session: harness,
    agent_sessions: [harness],
    status: 'cancelled',
    archived_at: now,
    input_state: 'none', activity: 'working',
    created_at: now,
  }
  stubReopenApi(holon, harnessCapabilities(['codex']), sessionInspection(holon.worktree_branch!, true))

  render(
    <ProjectContext.Provider value={{ id: 'holark', name: 'Holark', default_branch: 'main' }}>
      <MemoryRouter initialEntries={['/holons/holon-resume-actions']}>
        <Routes><Route path="/holons/:holonId" element={<HolonTerminal socketFactory={() => new FakeSocket() as unknown as WebSocket} />} /></Routes>
      </MemoryRouter>
    </ProjectContext.Provider>,
  )

  expect(await screen.findByRole('button', { name: 'Reopen' })).toBeInTheDocument()
  expect(screen.queryByRole('button', { name: 'Open PR' })).not.toBeInTheDocument()
  expect(screen.queryByRole('button', { name: 'Create WIP' })).not.toBeInTheDocument()
})

it('hides Push while Reopen is primary', async () => {
  vi.stubGlobal('WebSocket', class { static OPEN = 1 })
  const now = new Date().toISOString()
  const harness = {
    id: 'agent-resume',
    agent_type: 'codex' as const,
    title: 'Codex agent',
    status: 'cancelled' as const,
    input_state: 'none' as const, activity: 'working' as const,
    created_at: now,
    updated_at: now,
  }
  const holon: Holon = {
    id: 'holon-resume-publish',
    repository_id: 'holark',
    runtime_id: '',
    prompt: 'Resume publish',
    worktree_branch: 'holark/resume-publish',
    upstream_branch: 'holark/resume-publish',
    upstream_head_commit: 'def456',
    worktree_path: '/tmp/holon-resume-publish',
    agent_session: harness,
    agent_sessions: [harness],
    status: 'cancelled',
    archived_at: now,
    input_state: 'none', activity: 'working',
    created_at: now,
  }
  stubReopenApi(holon, harnessCapabilities(['codex']), sessionInspection(holon.worktree_branch!, true, { headCommit: 'fed789' }))

  render(
    <ProjectContext.Provider value={{ id: 'holark', name: 'Holark', default_branch: 'main' }}>
      <MemoryRouter initialEntries={['/holons/holon-resume-publish']}>
        <Routes><Route path="/holons/:holonId" element={<HolonTerminal socketFactory={() => new FakeSocket() as unknown as WebSocket} />} /></Routes>
      </MemoryRouter>
    </ProjectContext.Provider>,
  )

  expect(await screen.findByRole('button', { name: 'Reopen' })).toBeInTheDocument()
  expect(screen.queryByRole('button', { name: 'Push' })).not.toBeInTheDocument()
})

function tabOverflowHolon() {
  const holon = renamableHolon('Overflow holon', 'holon-overflow') as Record<string, unknown>
  holon.manual_terminals = ['Shell 1', 'Shell 2', 'Shell 3'].map((title, index) => manualTerminal('holon-overflow', {
    id: `terminal-${index + 1}`,
    terminal_id: undefined,
    title,
    tab_order: index + 2,
  }))
  return holon
}

function mockTabStripWidth(width: number) {
  const getBoundingClientRect = HTMLElement.prototype.getBoundingClientRect
  vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(function (this: HTMLElement) {
    if (this.firstElementChild?.getAttribute('data-testid') !== 'tab-viewport') return getBoundingClientRect.call(this)
    return {
    x: 0,
    y: 0,
    left: 0,
    right: width,
    top: 0,
    bottom: 35,
    width,
    height: 35,
    toJSON: () => ({}),
    } as DOMRect
  })
}

it('switches tabs and Holons from terminal input, restores the selected tab, and keeps navigation out of the terminal', async () => {
  vi.stubGlobal('WebSocket', class { static OPEN = 1 })
  const first = renamableHolon('First holon', 'holon-first')
  first.worktree_branch = 'first'
  first.created_at = '2026-09-01T10:00:00Z'
  first.agent_sessions.push({ ...first.agent_session, id: 'agent-second', terminal_id: 'terminal-agent-second', title: 'Second agent', tab_order: 3 })
  const firstWithShell = { ...first, manual_terminals: [manualTerminal(first.id, { tab_order: 2 })] }
  const second = renamableHolon('Second holon', 'holon-second')
  second.worktree_branch = 'second'
  second.created_at = '2026-09-01T11:00:00Z'
  second.agent_session = { ...second.agent_session, id: 'other-agent', terminal_id: 'other-terminal' }
  second.agent_sessions = [second.agent_session]
  const baseFetch = stubApi(firstWithShell).getMockImplementation()!
  vi.stubGlobal('fetch', vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
    const url = new URL(String(input), 'http://test.local')
    if (url.pathname.endsWith('/holons')) return jsonResponse([firstWithShell, second])
    if (url.pathname.endsWith(`/holons/${second.id}`)) return jsonResponse(second)
    return baseFetch(input, init)
  }))
  const sockets = new Map<string, FakeSocket>()
  const socketFactory = (url: string) => {
    const socket = new FakeSocket()
    sockets.set(url, socket)
    return socket as unknown as WebSocket
  }
  const view = render(
    <ProjectContext.Provider value={{ id: 'holark', name: 'Holark', default_branch: 'main' }}>
      <MemoryRouter initialEntries={['/holons/holon-first']}>
        <Routes><Route path="/holons/:holonId" element={<><HolonTerminal socketFactory={socketFactory} /><OperationsPanel /></>} /></Routes>
      </MemoryRouter>
    </ProjectContext.Provider>,
  )
  const activeInput = () => view.container.querySelector<HTMLTextAreaElement>('[data-terminal-pane][data-active="true"] textarea')!
  const attach = async (terminalId: string) => {
    await waitFor(() => expect([...sockets.keys()].some((url) => url.includes(`/terminals/${terminalId}/`))).toBe(true))
    const socket = [...sockets.entries()].find(([url]) => url.includes(`/terminals/${terminalId}/`))![1]
    act(() => {
      socket.open()
      socket.message({ type: 'attached', restore: {
        attachment: { token: `attachment-${terminalId}` },
        checkpoint: { terminal_id: terminalId, sequence: 0, dimensions: { columns: 120, rows: 40 },
          format_version: 'holark.ansi.v1', replay_payload: '', checksum: '0'.repeat(64), quality: 'trusted' },
        tail: [], last_sequence: 0,
      } })
    })
    await waitFor(() => expect(activeInput()?.closest('[data-terminal-pane]')).toHaveAttribute('data-restoring', 'false'))
  }
  const move = (key: string) => fireEvent.keyDown(activeInput(), { key, altKey: true, shiftKey: true })

  await screen.findByRole('tab', { name: 'Agent' })
  await attach('terminal-agent')
  act(() => activeInput().focus())
  const terminalKey = vi.fn()
  activeInput().addEventListener('keydown', terminalKey)
  expect(move('ArrowRight')).toBe(false)
  expect(terminalKey).not.toHaveBeenCalled()
  expect(screen.getByRole('tab', { name: 'Shell 1' })).toHaveAttribute('aria-selected', 'true')
  await attach('terminal-1')
  await waitFor(() => expect(activeInput()).toHaveFocus())

  move('ArrowRight')
  expect(screen.getByRole('tab', { name: 'Second agent' })).toHaveAttribute('aria-selected', 'true')
  await attach('terminal-agent-second')
  await waitFor(() => expect(activeInput()).toHaveFocus())
  move('ArrowRight')
  expect(screen.getByRole('tab', { name: 'Agent' })).toHaveAttribute('aria-selected', 'true')
  move('ArrowLeft')
  expect(screen.getByRole('tab', { name: 'Second agent' })).toHaveAttribute('aria-selected', 'true')

  move('ArrowDown')
  await attach('other-terminal')
  await waitFor(() => expect(activeInput()).toHaveFocus())
  expect(activeInput().closest('[data-terminal-pane]')).toHaveAttribute('data-terminal-id', 'other-terminal')
  move('ArrowUp')
  expect(screen.getByRole('tab', { name: 'Second agent' })).toHaveAttribute('aria-selected', 'true')
  await waitFor(() => expect(activeInput()).toHaveFocus())
  expect(activeInput().closest('[data-terminal-pane]')).toHaveAttribute('data-terminal-id', 'terminal-agent-second')
})

it('keeps the terminal usable when queued output overtakes a checkpoint', async () => {
  vi.stubGlobal('WebSocket', class { static OPEN = 1 })
  stubApi(renamableHolon('Checkpoint holon', 'holon-checkpoint'))
  const socket = new FakeSocket()
  const view = render(
    <ProjectContext.Provider value={{ id: 'holark', name: 'Holark', default_branch: 'main' }}>
      <MemoryRouter initialEntries={['/holons/holon-checkpoint']}>
        <Routes><Route path="/holons/:holonId" element={<HolonTerminal socketFactory={() => socket as unknown as WebSocket} />} /></Routes>
      </MemoryRouter>
    </ProjectContext.Provider>,
  )
  await screen.findByRole('tab', { name: 'Agent' })
  const pane = view.container.querySelector('[data-terminal-pane][data-active="true"]')!
  const checkpoint = {
    terminal_id: 'terminal-agent', sequence: 0, dimensions: { columns: 120, rows: 40 },
    format_version: 'holark.ansi.v1', replay_payload: '', checksum: '0'.repeat(64), quality: 'trusted',
  }
  await act(async () => {
    socket.open()
    socket.message({ type: 'attached', restore: {
      attachment: { token: 'checkpoint-attachment' }, checkpoint, tail: [], last_sequence: 0,
    } })
  })
  await waitFor(() => expect(pane).toHaveAttribute('data-restoring', 'false'))

  terminalMock.autoCompleteWrites = false
  await act(async () => {
    socket.message({ type: 'updates', update: {
      terminal_id: 'terminal-agent', last_sequence: 2,
      notifications: [1, 2].map((sequence) => ({
        terminal_id: 'terminal-agent', sequence, kind: 'output',
        data: btoa(`output ${sequence}\r\n`), characters: 10,
      })),
    } })
    // The server has delivered through 2 and publishes its checkpoint at 1.
    // The browser is still at 0, with both output notifications queued first.
    socket.message({ type: 'updates', update: {
      terminal_id: 'terminal-agent', last_sequence: 2, notifications: [],
      checkpoint: { ...checkpoint, sequence: 1, replay_payload: btoa('output 1\r\n') },
    } })
  })
  await act(async () => {
    terminalMock.autoCompleteWrites = true
    terminalMock.pendingWrites[0]()
  })

  expect(pane).toHaveAttribute('data-restoring', 'false')
  expect(within(pane as HTMLElement).queryByText('Reconnecting...')).not.toBeInTheDocument()
  act(() => terminalMock.input?.('still usable'))
  expect(socket.sent.map((message) => JSON.parse(message))).toContainEqual(expect.objectContaining({
    type: 'input', payload: expect.objectContaining({ data_base64: btoa('still usable') }),
  }))
})

it('lists tabs outside the visible window and moves the window after overflow selection', async () => {
  vi.stubGlobal('WebSocket', class { static OPEN = 1 })
  mockTabStripWidth(170)
  stubApi(tabOverflowHolon())
  const fake = new FakeSocket()
  const scrollIntoView = vi.spyOn(Element.prototype, 'scrollIntoView')

  render(
    <ProjectContext.Provider value={{ id: 'holark', name: 'Holark', default_branch: 'main' }}>
      <MemoryRouter initialEntries={['/holons/holon-overflow']}>
        <Routes><Route path="/holons/:holonId" element={<HolonTerminal socketFactory={() => fake as unknown as WebSocket} />} /></Routes>
      </MemoryRouter>
    </ProjectContext.Provider>,
  )

  await screen.findByRole('tablist', { name: 'Holon terminals' })
  expect(screen.queryByTestId('holon-identity')).not.toBeInTheDocument()
  expect(screen.queryByTestId('holon-status')).not.toBeInTheDocument()
  expect(await screen.findByRole('tab', { name: 'Agent' })).toBeInTheDocument()
  expect(screen.getByRole('tab', { name: 'Shell 1' })).toBeInTheDocument()
  expect(screen.queryByRole('tab', { name: 'Shell 2' })).not.toBeInTheDocument()
  expect(screen.queryByRole('tab', { name: 'Shell 3' })).not.toBeInTheDocument()

  const overflow = await screen.findByRole('button', { name: '2 hidden tabs' })
  await userEvent.click(overflow)
  const menu = overflow.parentElement
  expect(overflow).toHaveAttribute('aria-expanded', 'true')
  expect(within(menu!).queryByRole('button', { name: 'Agent' })).not.toBeInTheDocument()
  expect(within(menu!).queryByRole('button', { name: 'Shell 1' })).not.toBeInTheDocument()
  expect(within(menu!).getByRole('button', { name: 'Shell 2' })).toBeInTheDocument()

  await userEvent.click(within(menu!).getByRole('button', { name: 'Shell 3' }))

  expect(screen.getByRole('tab', { name: 'Shell 3' })).toHaveAttribute('aria-selected', 'true')
  // Shell 3 has no action button, so every tab now fits and the overflow menu goes away.
  expect(screen.getByRole('tab', { name: 'Shell 2' })).toBeInTheDocument()
  expect(screen.getByRole('tab', { name: 'Agent' })).toBeInTheDocument()
  expect(screen.queryByRole('button', { name: /hidden tabs?/ })).not.toBeInTheDocument()
  expect(scrollIntoView).not.toHaveBeenCalled()
})

it('drops the new tab button while tabs overflow and keeps holon popovers mutually exclusive', async () => {
  vi.stubGlobal('WebSocket', class { static OPEN = 1 })
  mockTabStripWidth(170)
  stubApi(tabOverflowHolon())
  const fake = new FakeSocket()

  render(
    <ProjectContext.Provider value={{ id: 'holark', name: 'Holark', default_branch: 'main' }}>
      <MemoryRouter initialEntries={['/holons/holon-overflow']}>
        <Routes><Route path="/holons/:holonId" element={<HolonTerminal socketFactory={() => fake as unknown as WebSocket} />} /></Routes>
      </MemoryRouter>
    </ProjectContext.Provider>,
  )

  await screen.findByRole('tablist', { name: 'Holon terminals' })
  expect(screen.queryByRole('button', { name: 'New tab' })).not.toBeInTheDocument()
  const hiddenTabs = await screen.findByRole('button', { name: '2 hidden tabs' })
  const actions = screen.getByRole('button', { name: 'More holon actions' })

  await userEvent.click(hiddenTabs)
  expect(hiddenTabs).toHaveAttribute('aria-expanded', 'true')

  await userEvent.click(actions)
  expect(hiddenTabs).toHaveAttribute('aria-expanded', 'false')
  expect(actions).toHaveAttribute('aria-expanded', 'true')
  expect(screen.getByRole('button', { name: 'End holon' })).toBeInTheDocument()
  expect(screen.queryByRole('button', { name: 'Rename holon' })).not.toBeInTheDocument()

  await userEvent.click(screen.getByRole('button', { name: 'End holon' }))
  expect(actions).toHaveAttribute('aria-expanded', 'false')
  expect(screen.getByRole('heading', { name: 'End holon' })).toBeInTheDocument()
})

it('renames a holon from the sidebar and saves on blur', async () => {
  vi.stubGlobal('WebSocket', class { static OPEN = 1 })
  const fetchMock = stubApi(renamableHolon())
  const fake = new FakeSocket()

  render(
    <ProjectContext.Provider value={{ id: 'holark', name: 'Holark', default_branch: 'main' }}>
      <MemoryRouter initialEntries={['/holons/holon-rename']}>
        <Routes>
          <Route path="/holons/:holonId" element={<><HolonTerminal socketFactory={() => fake as unknown as WebSocket} /><OperationsPanel /></>} />
        </Routes>
      </MemoryRouter>
    </ProjectContext.Provider>,
  )

  const agents = await screen.findByRole('region', { name: 'Holons' })
  const renameButton = await within(agents).findByRole('button', { name: 'Rename Original holon' })
  await userEvent.click(renameButton)
  const input = within(agents).getByRole('textbox', { name: 'Rename Original holon' })
  fireEvent.change(input, { target: { value: 'Sidebar name' } })
  fireEvent.blur(input)

  expect(await within(agents).findByRole('button', { name: 'Rename Sidebar name' })).toBeInTheDocument()
  expect(fetchMock.mock.calls.some(([inputValue, init]) => (
    String(inputValue).endsWith('/holons/holon-rename')
    && init?.method === 'PATCH'
    && JSON.parse(String(init.body)).title === 'Sidebar name'
  ))).toBe(true)
})

it('keeps the terminal available and shows the commit button when changes are detected', async () => {
  vi.stubGlobal('WebSocket', class { static OPEN = 1 })
  const holon = renamableHolon('Prompt holon', 'holon-commit-prompt') as Record<string, unknown>
  holon.worktree_path = '/tmp/holon-commit-prompt'
  const harness = {
    ...(holon.agent_session as Record<string, unknown>),
    input_state: 'task_complete', activity: 'completed',
    commit_prompt: { state: 'dirty_prompt_visible' },
  }
  holon.agent_session = harness
  holon.agent_sessions = [harness]
  const inspection = sessionInspection('holark/holon-commit-prompt', true, { dirty: true })
  const fetchMock = stubApi(holon, [], inspection)
  const fake = new FakeSocket()
  const socketFactory = () => fake as unknown as WebSocket

  render(
    <ProjectContext.Provider value={{ id: 'holark', name: 'Holark', default_branch: 'main' }}>
      <MemoryRouter initialEntries={['/holons/holon-commit-prompt']}>
        <Routes><Route path="/holons/:holonId" element={<HolonTerminal socketFactory={socketFactory} />} /></Routes>
      </MemoryRouter>
    </ProjectContext.Provider>,
  )

  expect(await screen.findByRole('button', { name: 'Commit' })).toBeInTheDocument()
  expect(screen.queryByRole('dialog', { name: 'Uncommitted changes' })).not.toBeInTheDocument()

  act(() => {
    fake.open()
    fake.message({
      type: 'attached',
      restore: {
        attachment: { token: 'attachment-holon-commit-prompt' },
        checkpoint: {
          terminal_id: 'terminal-agent', sequence: 0,
          dimensions: { columns: 120, rows: 40 }, format_version: 'holark.ansi.v1',
          replay_payload: '', checksum: '0'.repeat(64), quality: 'trusted',
        },
        tail: [], last_sequence: 0,
      },
    })
  })

  await waitFor(() => expect(terminalMock.instances.some((instance) => !instance.disableStdin)).toBe(true))
  expect(fetchMock.mock.calls.some(([input]) => {
    const url = new URL(String(input), 'http://test.local')
    return url.pathname.endsWith('/changes') && url.searchParams.get('base')?.startsWith('commit:')
  })).toBe(false)
})

it('renders branch-wide change metadata and fetches only summaries while closed', async () => {
  vi.stubGlobal('WebSocket', class { static OPEN = 1 })
  const sessionRange = sessionInspection('holark/holon-1', true)
  const branchRange = {
    ...sessionRange,
    files: [
      ...sessionRange.files,
      { path: 'branch.txt', status: 'A', additions: 3, deletions: 0, binary: false, diff: 'branch diff', diff_truncated: false },
    ],
  }
  const fetchMock = stubApi({
    id: 'holon-1',
    repository_id: 'holark',
    runtime_id: 'node-1',
    prompt: 'Fix it',
    worktree_branch: 'holark/holon-1',
    status: 'running',
    input_state: 'none', activity: 'working',
    created_at: new Date().toISOString(),
  }, [], (url) => url.searchParams.get('base') === 'main' ? branchRange : sessionRange)
  const fake = new FakeSocket()
  const socketFactory = () => fake as unknown as WebSocket

  render(
    <ProjectContext.Provider value={{ id: 'holark', name: 'Holark', default_branch: 'main' }}>
      <MemoryRouter initialEntries={['/holons/holon-1']}>
        <Routes><Route path="/holons/:holonId" element={<HolonTerminal socketFactory={socketFactory} />} /></Routes>
      </MemoryRouter>
    </ProjectContext.Provider>,
  )

  const branchChanges = await screen.findByRole('button', { name: 'Open changes panel, 2 changed files, 1 deletion, 5 additions' })
  expect(branchChanges).toHaveTextContent('Changes · Review & Publish2 files+5−1')
  expect(screen.queryByRole('button', { name: 'Holon changes' })).not.toBeInTheDocument()
  expect(await screen.findByRole('button', { name: /^Open changes panel/ })).toBeInTheDocument()
  await waitFor(() => expect(fetchMock.mock.calls.some(([input]) => {
    const url = new URL(String(input), 'http://test.local')
    return url.pathname.endsWith('/workspace') && url.searchParams.get('base') === 'main' && url.searchParams.get('target') === 'worktree' && url.searchParams.get('summary') === 'true'
  })).toBe(true))
  expect(fetchMock.mock.calls.some(([input]) => {
    const url = new URL(String(input), 'http://test.local')
    return url.pathname.endsWith('/workspace') && url.searchParams.get('summary') !== 'true'
  })).toBe(false)
})

it('opens the changed-file tree with always-on refresh and closes file review', async () => {
  vi.stubGlobal('WebSocket', class { static OPEN = 1 })
  const fetchMock = stubApi({
    id: 'holon-1',
    repository_id: 'holark',
    runtime_id: 'node-1',
    prompt: 'Fix it',
    worktree_branch: 'holark/holon-1',
    status: 'running',
    input_state: 'none', activity: 'working',
    created_at: new Date().toISOString(),
  }, [], sessionInspection('holark/holon-1', true))
  const fake = new FakeSocket()
  const socketFactory = () => fake as unknown as WebSocket

  render(
    <ProjectContext.Provider value={{ id: 'holark', name: 'Holark', default_branch: 'main' }}>
      <MemoryRouter initialEntries={['/holons/holon-1']}>
        <Routes><Route path="/holons/:holonId" element={<HolonTerminal socketFactory={socketFactory} />} /></Routes>
      </MemoryRouter>
    </ProjectContext.Provider>,
  )

  fireEvent.click(await screen.findByRole('button', { name: /^Open changes panel/ }))
  const changesPanel = await screen.findByRole('dialog', { name: 'Holon changes' })
  await waitFor(() => expect(fetchMock.mock.calls.some(([input]) => {
    const url = new URL(String(input), 'http://test.local')
    return url.pathname.endsWith('/workspace') && url.searchParams.get('base') === 'main' && url.searchParams.get('target') === 'worktree'
  })).toBe(true))
  const resizeHandle = screen.getByRole('separator', { name: 'Resize changed files panel' })
  fireEvent.pointerDown(resizeHandle, { button: 0, clientX: 318 })
  fireEvent.pointerMove(window, { clientX: 398 })
  fireEvent.pointerUp(window)
  await waitFor(() => expect(changesPanel.style.getPropertyValue('--diff-panel-width')).toBe('370px'))
  expect(screen.getByRole('heading', { name: 'Commits' })).toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Select Uncommitted changes' })).toHaveAttribute('aria-pressed', 'false')
  expect(screen.getByRole('button', { name: 'View all' })).toBeDisabled()
  expect(screen.queryByLabelText('From')).not.toBeInTheDocument()
  expect(screen.queryByLabelText('To')).not.toBeInTheDocument()
  expect(changesPanel.querySelector('header > div svg.lucide-git-branch')).not.toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Close changes panel' })).toHaveTextContent('Changes · Review & Publish1 file+2−1')
  expect(screen.queryByRole('checkbox', { name: 'Auto-refresh' })).not.toBeInTheDocument()
  expect(screen.queryByRole('button', { name: /^Refresh$/ })).not.toBeInTheDocument()
  fireEvent.click(screen.getByRole('button', { name: 'Close changes panel' }))
  expect(await screen.findByRole('button', { name: /^Open changes panel/ })).toBeInTheDocument()
  fireEvent.click(screen.getByRole('button', { name: /^Open changes panel/ }))
  expect(await screen.findByRole('dialog', { name: 'Holon changes' })).toBeInTheDocument()
  const fileRow = await screen.findByRole('button', { name: /README\.md/ })
  act(() => {
    fake.open()
    fake.message({
      type: 'attached',
      restore: {
        attachment: { token: 'attachment-holon-1' },
        checkpoint: {
          terminal_id: 'harness-holon-1', sequence: 0,
          dimensions: { columns: 120, rows: 40 }, format_version: 'holark.ansi.v1',
          replay_payload: '', checksum: '0'.repeat(64), quality: 'trusted',
        },
        tail: [], last_sequence: 0,
      },
    })
  })
  await waitFor(() => expect(terminalMock.instances.filter((instance) => !instance.disposed).every((instance) => instance.disableStdin)).toBe(true))
  const inputMessagesBeforeReview = sentTerminalMessages(fake, 'input').length
  expect(fireEvent.mouseDown(fileRow)).toBe(false)
  fireEvent.click(fileRow)
  const reviewPanel = await screen.findByRole('dialog', { name: 'Holon changes' })
  const fileReview = screen.getByRole('region', { name: 'Review README.md' })
  await waitFor(() => expect(fileReview).toHaveFocus())
  await waitFor(() => expect(terminalMock.instances.filter((instance) => !instance.disposed).every((instance) => instance.disableStdin)).toBe(true))
  act(() => { terminalMock.input?.('\u001b[O') })
  expect(sentTerminalMessages(fake, 'input')).toHaveLength(inputMessagesBeforeReview)
  expect(screen.getByRole('separator', { name: 'Resize changed files panel' })).toBeInTheDocument()
  expect(reviewPanel.style.getPropertyValue('--diff-panel-width')).toBe('370px')
  expect(within(fileReview).getByRole('heading')).toHaveTextContent('README.md')
  await waitFor(() => expect(fetchMock.mock.calls.some(([input]) => {
    const url = new URL(String(input), 'http://test.local')
    return url.pathname.endsWith('/workspace') && url.searchParams.get('summary') !== 'true' && url.searchParams.get('path') === 'README.md'
  })).toBe(true))
  expect(await screen.findByLabelText('Comparison README.md')).toBeInTheDocument()
  expect(screen.queryByRole('table', { name: 'File diff' })).not.toBeInTheDocument()
  fireEvent.click(screen.getByRole('button', { name: /README\.md/ }))
  expect(await screen.findByRole('dialog', { name: 'Holon changes' })).toBeInTheDocument()
  expect(terminalMock.instances.filter((instance) => !instance.disposed).every((instance) => instance.disableStdin)).toBe(true)
  fireEvent.click(screen.getByRole('button', { name: /README\.md/ }))
  expect(await screen.findByRole('dialog', { name: 'Holon changes' })).toBeInTheDocument()
  const reopenedFileReview = screen.getByRole('region', { name: 'Review README.md' })
  await waitFor(() => expect(reopenedFileReview).toHaveFocus())
  fireEvent.click(screen.getByRole('button', { name: 'Close changes panel' }))
  expect(await screen.findByRole('button', { name: /^Open changes panel/ })).toBeInTheDocument()
  expect(screen.queryByRole('dialog', { name: 'Holon changes' })).not.toBeInTheDocument()
  expect(screen.queryByRole('complementary', { name: 'Holon changes' })).not.toBeInTheDocument()
  expect(terminalMock.disableStdin).toBe(false)

  fireEvent.click(screen.getByRole('button', { name: /^Open changes panel/ }))
  expect(await screen.findByRole('dialog', { name: 'Holon changes' })).toBeInTheDocument()
  fireEvent.click(await screen.findByRole('button', { name: /README\.md/ }))
  expect(await screen.findByRole('dialog', { name: 'Holon changes' })).toBeInTheDocument()
  const escapeReview = screen.getByRole('region', { name: 'Review README.md' })
  await waitFor(() => expect(escapeReview).toHaveFocus())
  fireEvent.keyDown(escapeReview, { key: 'Escape' })
  expect(await screen.findByRole('button', { name: /^Open changes panel/ })).toBeInTheDocument()
  expect(screen.queryByRole('dialog', { name: 'Holon changes' })).not.toBeInTheDocument()

  for (const key of ['ArrowLeft', 'ArrowRight']) {
    const selectedTab = screen.getAllByRole('tab').find((tab) => tab.getAttribute('aria-selected') === 'true')!
    fireEvent.click(screen.getByRole('button', { name: /^Open changes panel/ }))
    const panel = await screen.findByRole('dialog', { name: 'Holon changes' })
    const comparison = await screen.findByLabelText('Comparison README.md')
    fireEvent.keyDown(comparison, { key, altKey: true })
    expect(panel).toBeInTheDocument()
    expect(fireEvent.keyDown(comparison, { key, shiftKey: true, altKey: true })).toBe(false)
    expect(screen.queryByRole('dialog', { name: 'Holon changes' })).not.toBeInTheDocument()
    expect(selectedTab).toHaveAttribute('aria-selected', 'true')
  }

  const toggleShortcut = { code: 'KeyC', key: 'Ç', shiftKey: true, altKey: true }
  expect(screen.getByRole('button', { name: /^Open changes panel/ })).toHaveAttribute('aria-keyshortcuts', 'Shift+Alt+C')
  expect(fireEvent.keyDown(document.body, toggleShortcut)).toBe(false)
  const toggledPanel = await screen.findByRole('dialog', { name: 'Holon changes' })
  const comparison = await screen.findByLabelText('Comparison README.md')
  fireEvent.keyDown(comparison, { ...toggleShortcut, repeat: true })
  expect(toggledPanel).toBeInTheDocument()
  fireEvent.keyDown(comparison, { ...toggleShortcut, ctrlKey: true })
  expect(toggledPanel).toBeInTheDocument()
  expect(fireEvent.keyDown(comparison, toggleShortcut)).toBe(false)
  expect(screen.queryByRole('dialog', { name: 'Holon changes' })).not.toBeInTheDocument()
})

it('refreshes the file tree and open patch when the commit range changes', async () => {
  vi.stubGlobal('WebSocket', class { static OPEN = 1 })
  const timeline = timelineInspection()
  const rangeInspection = (path: string, contents: string) => ({
    ...timeline,
    files: [{
      path,
      status: 'M',
      additions: 1,
      deletions: 1,
      binary: false,
      content_status: 'ready',
      contents: { original: 'old\n', modified: contents + '\n' },
      diff: `diff --git a/${path} b/${path}\n--- a/${path}\n+++ b/${path}\n@@ -1 +1 @@\n-old\n+${contents}`,
      diff_truncated: false,
    }],
  })
  const initial = rangeInspection('README.md', 'initial range')
  const firstRange = rangeInspection('holon.txt', 'first commit range')
  const newestRange = rangeInspection('newest.txt', 'newest commit range')
  initial.files.push(...newestRange.files)
  stubApi({
    id: 'holon-1',
    repository_id: 'holark',
    runtime_id: 'node-1',
    prompt: 'Fix it',
    worktree_branch: 'holark/holon-1',
    status: 'running',
    input_state: 'none', activity: 'working',
    created_at: new Date().toISOString(),
  }, [], (url) => {
    const baseRef = url.searchParams.get('base')
    const targetRef = url.searchParams.get('target')
    if (baseRef === 'commit:dddddddd' && targetRef === 'commit:eeeeeeee') return { ...newestRange, selected_base_commit: 'dddddddd', selected_target_commit: 'eeeeeeee' }
    if (baseRef === 'commit:cccccccc' && targetRef === 'commit:dddddddd') return { ...firstRange, selected_base_commit: 'cccccccc', selected_target_commit: 'dddddddd' }
    return initial
  })
  const socketFactory = () => new FakeSocket() as unknown as WebSocket

  render(
    <ProjectContext.Provider value={{ id: 'holark', name: 'Holark', default_branch: 'main' }}>
      <MemoryRouter initialEntries={['/holons/holon-1']}>
        <Routes><Route path="/holons/:holonId" element={<HolonTerminal socketFactory={socketFactory} />} /></Routes>
      </MemoryRouter>
    </ProjectContext.Provider>,
  )

  fireEvent.click(await screen.findByRole('button', { name: /^Open changes panel/ }))
  expect(await screen.findByRole('button', { name: /README\.md/ })).toBeInTheDocument()
  fireEvent.click(screen.getByRole('button', { name: 'Select First holon change' }))

  const sessionFile = await screen.findByRole('button', { name: /holon\.txt/ })
  expect(screen.getByRole('button', { name: /README\.md/ })).toBeDisabled()
  fireEvent.click(sessionFile)
  expect(await screen.findByRole('dialog', { name: 'Holon changes' })).toBeInTheDocument()
  expect(await screen.findByText(/first commit range/)).toBeInTheDocument()
  expect(screen.queryByText('Loading patch...')).not.toBeInTheDocument()

  fireEvent.click(screen.getByRole('button', { name: 'Select First holon change' }))
  fireEvent.click(screen.getByRole('button', { name: 'Select Newest holon change' }))
  expect(await screen.findByRole('button', { name: /newest\.txt/ })).toBeInTheDocument()
  await waitFor(() => expect(screen.queryByRole('button', { name: /holon\.txt/ })).not.toBeInTheDocument())
  expect(await screen.findByRole('region', { name: 'Review newest.txt' })).toBeInTheDocument()
  expect(await screen.findByText(/newest commit range/)).toBeInTheDocument()
  expect(screen.queryByText('Loading patch...')).not.toBeInTheDocument()
})

it('selects commit ranges and collapses history above the files without a resize divider', async () => {
  vi.stubGlobal('WebSocket', class { static OPEN = 1 })
  const fetchMock = stubApi({
    id: 'holon-1',
    repository_id: 'holark',
    runtime_id: 'node-1',
    prompt: 'Fix it',
    worktree_branch: 'holark/holon-1',
    status: 'running',
    input_state: 'none', activity: 'working',
    created_at: new Date().toISOString(),
  }, [], timelineInspection())
  const fake = new FakeSocket()
  const socketFactory = () => fake as unknown as WebSocket

  render(
    <ProjectContext.Provider value={{ id: 'holark', name: 'Holark', default_branch: 'main' }}>
      <MemoryRouter initialEntries={['/holons/holon-1']}>
        <Routes><Route path="/holons/:holonId" element={<HolonTerminal socketFactory={socketFactory} />} /></Routes>
      </MemoryRouter>
    </ProjectContext.Provider>,
  )

  fireEvent.click(await screen.findByRole('button', { name: /^Open changes panel/ }))
  await screen.findByRole('dialog', { name: 'Holon changes' })
  const newest = await screen.findByRole('button', { name: 'Select Newest holon change' })
  const first = screen.getByRole('button', { name: 'Select First holon change' })
  const workingTree = screen.getByRole('button', { name: 'Select Uncommitted changes' })
  const requestExists = (base: string, target: string) => fetchMock.mock.calls.some(([input]) => {
    const url = new URL(String(input), 'http://test.local')
    return url.pathname.endsWith('/workspace') && url.searchParams.get('base') === base && url.searchParams.get('target') === target
  })
  expect(screen.getByRole('button', { name: 'View all' })).toBeDisabled()
  expect(newest).toHaveAttribute('aria-pressed', 'false')
  fireEvent.click(first)
  await waitFor(() => expect(first).toHaveAttribute('aria-pressed', 'true'))
  // Nonadjacent selections keep the intervening commit out of both comparisons.
  fireEvent.click(workingTree)
  await waitFor(() => expect(workingTree).toHaveAttribute('aria-pressed', 'true'))
  expect(newest).toHaveAttribute('aria-pressed', 'false')
  await waitFor(() => expect(requestExists('commit:cccccccc', 'commit:dddddddd')).toBe(true))
  await waitFor(() => expect(requestExists('commit:eeeeeeee', 'worktree')).toBe(true))
  const review = await screen.findByRole('region', { name: 'Review README.md' })
  expect(within(review).getByText('First holon change')).toBeInTheDocument()
  expect(within(review).getByText('Uncommitted changes')).toBeInTheDocument()
  // Selecting the intervening commit coalesces adjacent revisions.
  fireEvent.click(newest)
  await waitFor(() => expect(requestExists('commit:cccccccc', 'worktree')).toBe(true))
  fireEvent.click(screen.getByRole('button', { name: 'View all' }))
  expect(first).toHaveAttribute('aria-pressed', 'false')
  expect(workingTree).toHaveAttribute('aria-pressed', 'false')
  expect(screen.getByRole('button', { name: 'View all' })).toBeDisabled()

  expect(screen.queryByRole('separator', { name: 'Resize commits and files' })).not.toBeInTheDocument()

  const commitsDisclosure = screen.getByRole('button', { name: 'Commits' })
  fireEvent.click(commitsDisclosure)
  expect(commitsDisclosure).toHaveAttribute('aria-expanded', 'false')
  expect(screen.queryByRole('button', { name: 'Select Uncommitted changes' })).not.toBeInTheDocument()
  expect(screen.queryByRole('separator', { name: 'Resize commits and files' })).not.toBeInTheDocument()
  fireEvent.click(commitsDisclosure)
  expect(screen.getByRole('button', { name: 'Select Uncommitted changes' })).toHaveAttribute('aria-pressed', 'false')
  expect(screen.getByRole('button', { name: 'View all' })).toBeDisabled()
})

it.each(['ref_not_found', 'invalid_ref'])('recovers stale commit selections after %s', async (code) => {
  vi.stubGlobal('WebSocket', class { static OPEN = 1 })
  const original = timelineInspection()
  const rewritten = {
    ...sessionInspection('holark/holon-1', true, { baseCommit: 'aaaaaaaa', headCommit: 'ffffffff' }),
    branch_base_commit: 'aaaaaaaa',
    work_session_start_commit: 'aaaaaaaa',
    workspace_head_commit: 'ffffffff',
    commits: [{ sha: 'ffffffff', parent_commit: 'aaaaaaaa', subject: 'Rewritten holon change' }],
  }
  let historyRewritten = false
  const fetchMock = stubApi({
    id: 'holon-1', repository_id: 'holark', runtime_id: 'node-1', prompt: 'Fix it',
    worktree_branch: 'holark/holon-1', status: 'running', input_state: 'none', activity: 'working', created_at: new Date().toISOString(),
  }, [], (url) => {
    if (url.searchParams.get('base') === 'commit:cccccccc' && url.searchParams.get('target') === 'commit:dddddddd') {
      historyRewritten = true
      return new Response(JSON.stringify({ code, message: 'Reference not found.' }), { status: 404, headers: { 'Content-Type': 'application/json' } })
    }
    if (url.searchParams.get('base') === 'main' && url.searchParams.get('target') === 'main') {
      return { ...rewritten, has_changes: false, files: [] }
    }
    return historyRewritten ? rewritten : original
  })
  const socketFactory = () => new FakeSocket() as unknown as WebSocket

  render(
    <ProjectContext.Provider value={{ id: 'holark', name: 'Holark', default_branch: 'main' }}>
      <MemoryRouter initialEntries={['/holons/holon-1']}>
        <Routes><Route path="/holons/:holonId" element={<HolonTerminal socketFactory={socketFactory} />} /></Routes>
      </MemoryRouter>
    </ProjectContext.Provider>,
  )

  fireEvent.click(await screen.findByRole('button', { name: /^Open changes panel/ }))
  const first = await screen.findByRole('button', { name: 'Select First holon change' })
  const defaultRequestCount = () => fetchMock.mock.calls.filter(([input]) => {
    const url = new URL(String(input), 'http://test.local')
    return url.pathname.endsWith('/workspace') && url.searchParams.get('base') === 'main' && url.searchParams.get('target') === 'worktree'
  }).length
  const defaultsBeforeRewrite = defaultRequestCount()

  fireEvent.click(first)

  const rewrittenCommit = await screen.findByRole('button', { name: 'Select Rewritten holon change' })
  await waitFor(() => expect(rewrittenCommit).toHaveAttribute('aria-pressed', 'false'))
  await waitFor(() => expect(defaultRequestCount()).toBeGreaterThan(defaultsBeforeRewrite))
  expect(screen.queryByRole('button', { name: 'Select First holon change' })).not.toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'View all' })).toBeDisabled()

})

it('keeps terminal pull request actions tied to the default diff range', async () => {
  vi.stubGlobal('WebSocket', class { static OPEN = 1 })
  const holon = {
    id: 'holon-1',
    repository_id: 'holark',
    runtime_id: 'node-1',
    prompt: 'Fix it',
    worktree_branch: 'holark/holon-1',
    proposed_upstream_branch: 'holark/proposed-holon-1',
    status: 'running',
    input_state: 'none', activity: 'working',
    created_at: new Date().toISOString(),
  }
  const defaultInspection = sessionInspection('holark/holon-1', true)
  const sameRangeInspection = {
    ...sessionInspection('holark/holon-1', false, { baseCommit: 'def456', headCommit: 'def456' }),
    selected_base_ref: 'commit:def456',
    selected_target_ref: 'worktree',
    ref_options: defaultInspection.ref_options,
  }
  let requestBody: Record<string, unknown> | undefined
  const fetchMock = vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
    const url = new URL(String(input), 'http://test.local')
    if (url.pathname.endsWith('/holons/holon-1/pull-request') && init?.method === 'POST') {
      requestBody = JSON.parse(String(init.body)) as Record<string, unknown>
      return new Response(JSON.stringify({ id: 'pr-open', view_revision: 1 }), { status: 201, headers: { 'Content-Type': 'application/json' } })
    }
    if (url.pathname.endsWith('/holons/holon-1/pull-request')) {
      return new Response(JSON.stringify({ code: 'pull_request_not_found' }), { status: 404 })
    }
    const body = url.pathname.endsWith('/holons')
      ? [holon]
      : url.pathname.endsWith('/pull-requests')
      ? []
      : url.pathname.endsWith('/workspace')
        ? inspectionResponse(
          url.searchParams.get('base') === 'commit:def456' && url.searchParams.get('target') === 'worktree'
            ? sameRangeInspection
            : defaultInspection,
          url.searchParams.get('summary') === 'true',
        )
        : holon
    return new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })
  })
  vi.stubGlobal('fetch', fetchMock)
  const fake = new FakeSocket()
  const socketFactory = () => fake as unknown as WebSocket

  render(
    <ProjectContext.Provider value={{ id: 'holark', name: 'Holark', default_branch: 'main' }}>
      <MemoryRouter initialEntries={['/holons/holon-1']}>
        <Routes>
          <Route path="/holons/:holonId" element={<HolonTerminal socketFactory={socketFactory} />} />
          <Route path="/pulls/:pullRequestId" element={<div>Detail route</div>} />
        </Routes>
      </MemoryRouter>
    </ProjectContext.Provider>,
  )

  expect(await screen.findByRole('button', { name: 'Open PR' })).toBeInTheDocument()
  fireEvent.click(screen.getByRole('button', { name: 'More holon actions' }))
  fireEvent.click(screen.getByRole('button', { name: 'Pull request creation options' }))
  expect(await screen.findByRole('menuitem', { name: 'Create WIP' })).toBeInTheDocument()
  expect(screen.getByRole('menuitem', { name: 'Create draft PR' })).toBeInTheDocument()
  expect(screen.getByRole('menuitem', { name: 'Open PR' })).toBeInTheDocument()

  const creationOptions = screen.getByRole('button', { name: 'Pull request creation options' })
  fireEvent.focusIn(screen.getByRole('button', { name: 'More holon actions' }))
  expect(screen.queryByRole('menuitem', { name: 'Create WIP' })).not.toBeInTheDocument()

  fireEvent.click(creationOptions)
  fireEvent.focusIn(await screen.findByRole('menuitem', { name: 'Create WIP' }))
  fireEvent.keyDown(document, { key: 'Escape' })
  expect(screen.queryByRole('menuitem', { name: 'Create WIP' })).not.toBeInTheDocument()
  expect(creationOptions).toHaveFocus()

  fireEvent.click(creationOptions)
  expect(await screen.findByRole('menuitem', { name: 'Create WIP' })).toBeInTheDocument()
  fireEvent.click(screen.getByRole('button', { name: 'More holon actions' }))
  expect(screen.queryByRole('menuitem', { name: 'Create WIP' })).not.toBeInTheDocument()
  expect(screen.getByRole('button', { name: /End holon/ })).toBeInTheDocument()
  fireEvent.click(screen.getByRole('button', { name: 'More holon actions' }))
  fireEvent.click(creationOptions)
  expect(await screen.findByRole('menuitem', { name: 'Create WIP' })).toBeInTheDocument()

  expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  fireEvent.click(creationOptions)
  fireEvent.click(await screen.findByRole('button', { name: /^Open changes panel/ }))
  expect(await screen.findByRole('dialog', { name: 'Holon changes' })).toBeInTheDocument()
  fireEvent.click(screen.getByRole('button', { name: 'Select Uncommitted changes' }))

  await waitFor(() => expect(fetchMock.mock.calls.some(([input]) => {
    const url = new URL(String(input), 'http://test.local')
    return url.pathname.endsWith('/workspace') && url.searchParams.get('base') === 'commit:def456' && url.searchParams.get('target') === 'worktree'
  })).toBe(true))
  expect(await screen.findByText('No changes in this range.')).toBeInTheDocument()
  fireEvent.click(screen.getByRole('button', { name: 'Close changes panel' }))
  fireEvent.click(screen.getByRole('button', { name: 'More holon actions' }))
  fireEvent.click(screen.getByRole('button', { name: 'Open PR' }))
  expect(screen.queryByRole('menu')).not.toBeInTheDocument()
  await waitFor(() => expect(requestBody).toEqual({ title: 'Agent changes', summary: '', target: 'open' }))
  expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  expect(await screen.findByText('Detail route')).toBeInTheDocument()
})

it.each(['success', 'failure'])('keeps PR creation scoped to its holon after %s', async (outcome) => {
  vi.stubGlobal('WebSocket', class { static OPEN = 1 })
  const holons = ['holon-1', 'holon-2'].map((id) => ({
    ...renamableHolon(id, id), proposed_upstream_branch: `proposed/${id}`,
  }))
  let complete!: (response: Response) => void
  const pending = new Promise<Response>((resolve) => { complete = resolve })
  const fetchMock = stubApi(holons[0], [], sessionInspection(holons[0].worktree_branch, true))
  const defaultResponse = fetchMock.getMockImplementation()!
  fetchMock.mockImplementation((input, init) => {
    const path = new URL(String(input), 'http://test.local').pathname
    if (path.endsWith('/holons')) return Promise.resolve(jsonResponse(holons))
    if (path.endsWith('/pull-request') && init?.method === 'POST') return pending
    return defaultResponse(input, init)
  })
  render(
    <ProjectContext.Provider value={{ id: 'holark', name: 'Holark', default_branch: 'main' }}>
      <MemoryRouter initialEntries={['/holons/holon-1']}>
        <Link to="/holons/holon-1">First holon</Link>
        <Link to="/holons/holon-2">Second holon</Link>
        <Routes>
          <Route path="/holons/:holonId" element={<HolonTerminal socketFactory={() => new FakeSocket() as unknown as WebSocket} />} />
          <Route path="/pulls/:pullRequestId" element={<div>Created PR page</div>} />
        </Routes>
      </MemoryRouter>
    </ProjectContext.Provider>,
  )
  fireEvent.click(await screen.findByRole('button', { name: 'Open PR' }))
  expect(await screen.findByRole('button', { name: 'Creating...' })).toBeDisabled()
  expect(screen.getByRole('button', { name: 'Pull request creation options' })).toBeDisabled()
  fireEvent.click(screen.getByRole('link', { name: 'Second holon' }))
  expect(await screen.findByRole('button', { name: 'Open PR' })).toBeEnabled()
  fireEvent.click(screen.getByRole('link', { name: 'First holon' }))
  expect(await screen.findByRole('button', { name: 'Creating...' })).toBeDisabled()
  fireEvent.click(screen.getByRole('link', { name: 'Second holon' }))
  await act(async () => {
    complete(outcome === 'success'
      ? jsonResponse({ id: 'pr-created', view_revision: 1 })
      : new Response(JSON.stringify({ message: 'PR creation failed' }), { status: 500 }))
  })
  expect(await screen.findByRole('button', { name: 'Open PR' })).toBeEnabled()
  expect(screen.queryByText('Created PR page')).not.toBeInTheDocument()
  expect(screen.queryByText('PR creation failed')).not.toBeInTheDocument()
  if (outcome === 'failure') {
    fireEvent.click(screen.getByRole('link', { name: 'First holon' }))
    expect(await screen.findByText('PR creation failed')).toBeInTheDocument()
  }
})

it('does not treat a proposed upstream branch as a publish target', async () => {
  vi.stubGlobal('WebSocket', class { static OPEN = 1 })
  stubApi({
    id: 'holon-1',
    repository_id: 'holark',
    runtime_id: 'node-1',
    prompt: 'Fix it',
    worktree_branch: 'holark/holon-1',
    proposed_upstream_branch: 'holark/proposed-holon-1',
    status: 'running',
    input_state: 'none', activity: 'working',
    created_at: new Date().toISOString(),
  }, [], sessionInspection('holark/holon-1', true, { baseCommit: 'abc123', headCommit: 'fed789' }))
  const fake = new FakeSocket()
  const socketFactory = () => fake as unknown as WebSocket

  render(
    <ProjectContext.Provider value={{ id: 'holark', name: 'Holark', default_branch: 'main' }}>
      <MemoryRouter initialEntries={['/holons/holon-1']}>
        <Routes><Route path="/holons/:holonId" element={<HolonTerminal socketFactory={socketFactory} />} /></Routes>
      </MemoryRouter>
    </ProjectContext.Provider>,
  )

  expect(await screen.findByRole('tablist', { name: 'Holon terminals' })).toBeInTheDocument()
  expect(await screen.findByRole('button', { name: 'Open PR' })).toBeInTheDocument()
  expect(screen.queryByRole('button', { name: 'Push' })).not.toBeInTheDocument()
})

it('shows pull request actions as soon as a running branch has changes, including renamed branches', async () => {
  vi.stubGlobal('WebSocket', class { static OPEN = 1 })
  stubApi({
    id: 'holon-1',
    repository_id: 'holark',
    runtime_id: 'node-1',
    prompt: 'Fix it',
    worktree_branch: 'holark/holon-1',
    status: 'running',
    input_state: 'none', activity: 'working',
    created_at: new Date().toISOString(),
  }, [], sessionInspection('holark/renamed-branch', true))
  const fake = new FakeSocket()
  const socketFactory = () => fake as unknown as WebSocket

  render(
    <ProjectContext.Provider value={{ id: 'holark', name: 'Holark', default_branch: 'main' }}>
      <MemoryRouter initialEntries={['/holons/holon-1']}>
        <Routes><Route path="/holons/:holonId" element={<HolonTerminal socketFactory={socketFactory} />} /></Routes>
      </MemoryRouter>
    </ProjectContext.Provider>,
  )

  expect(await screen.findByRole('tablist', { name: 'Holon terminals' })).toBeInTheDocument()
  expect(await screen.findByRole('button', { name: 'Open PR' })).toBeInTheDocument()
  await userEvent.click(screen.getByRole('button', { name: 'More holon actions' }))
  expect(screen.getByRole('button', { name: /End holon/ })).toBeInTheDocument()
  expect(screen.queryByText('Turn complete. Type another prompt in the terminal to continue.')).not.toBeInTheDocument()
})

function commitForkHolon(): Holon {
  const holon = renamableHolon('Commit holon', 'holon-1') as Holon
  const source: AgentSession = {
    ...holon.agent_session!, title: 'Source', resume_target: 'source-conversation',

  }
  return { ...holon, worktree_path: '/tmp/holon-1', base_commit: 'abc123', agent_session: source, agent_sessions: [source], manual_terminals: [manualTerminal()] }
}

function renderCommitFork(holon: Holon, respond: (input: string | URL | Request, init?: RequestInit) => Promise<Response>) {
  vi.stubGlobal('WebSocket', class { static OPEN = 1 })
  // Let initial inspections run, but hold periodic polls outside the click window.
  const setTimeout = window.setTimeout.bind(window)
  vi.spyOn(window, 'setTimeout').mockImplementation((callback, delay, ...args) =>
    delay === 3000 ? 0 : setTimeout(callback, delay, ...args))
  const fetchMock = stubApi(holon, [], sessionInspection(holon.worktree_branch!, true, { dirty: true }))
  const defaultResponse = fetchMock.getMockImplementation()!
  fetchMock.mockImplementation((input, init) => String(input).endsWith('/commit-agent') ? respond(input, init) : defaultResponse(input, init))
  const sockets = new Map<string, FakeSocket>()
  const socketFactory = (url: string) => {
    const socket = new FakeSocket()
    sockets.set(url, socket)
    return socket as unknown as WebSocket
  }
  let stored = holon
  let publish: (next: Holon) => void = () => { throw new Error('store not mounted') }
  function ControlledStore({ children }: { children: ReactNode }) {
    const [current, setCurrent] = useState(holon)
    useEffect(() => { stored = current; publish = setCurrent }, [current])
    const updateHolon = useCallback((id: string, update: SetStateAction<Holon | undefined>) => {
      setCurrent((previous) => previous.id === id ? (typeof update === 'function' ? update(previous) : update) ?? previous : previous)
    }, [])
    const store = useMemo<HolonStoreValue>(() => ({
      holons: [current], loading: false, error: undefined,
      selectHolonTab: (id, tabId, expectedTabId) => setCurrent((previous) => previous.id === id && (expectedTabId === undefined || previous.last_selected_tab_id === expectedTabId)
        ? { ...previous, last_selected_tab_id: tabId } : previous),
      activateHolon: () => {}, selectionErrors: {}, dismissSelectionError: () => {},
      getHolon: (id) => id === current.id ? current : undefined, updateHolon, refresh: async () => {},
    }), [current, updateHolon])
    return <BaseProjectContext.Provider value={{ id: 'holark', name: 'Holark', default_branch: 'main' }}>
      <HolonStoreContext.Provider value={store}>{children}</HolonStoreContext.Provider>
    </BaseProjectContext.Provider>
  }
  render(<ControlledStore>
    <MemoryRouter initialEntries={['/holons/holon-1']}>
      <Routes><Route path="/holons/:holonId" element={<HolonTerminal socketFactory={socketFactory} />} /></Routes>
    </MemoryRouter>
  </ControlledStore>)
  return { fetchMock, sockets, current: () => stored, publish: (next: Holon) => act(() => publish(next)) }
}

it('keeps the agent fork button immediately before close and selects the forked tab', async () => {
  const setup = renderCommitFork(commitForkHolon(), async () => { throw new Error('unexpected commit fork') })
  const sourceTab = await screen.findByRole('tab', { name: 'Source' })
  const fork = within(sourceTab).getByRole('button', { name: 'Fork Source' })
  const close = within(sourceTab).getByRole('button', { name: 'Close Source' })

  expect(fork).toBeVisible()
  expect(fork.nextElementSibling).toBe(close)
  await userEvent.click(fork)

  expect(setup.fetchMock.mock.calls.filter(([input]) => String(input).endsWith('/agent-sessions/harness-1/fork'))).toHaveLength(1)
  expect(await screen.findByRole('tab', { name: 'Fork' })).toHaveAttribute('aria-selected', 'true')
})

it('explains an unavailable selected commit source on hover and keyboard focus without falling back', async () => {
  const holon = commitForkHolon()
  const older = holon.agent_sessions![0]
  const latest: AgentSession = {
    ...older, id: 'latest', title: 'Latest', terminal_id: 'latest-terminal', resume_target: ' ',
    agent_type: 'claude-code',
  }
  // The selected tab alone determines availability.
  const newTab = { ...older, id: 'new', title: 'New', terminal_id: 'new-terminal' }
  const closed = { ...older, id: 'closed', closed_at: older.updated_at }
  const setup = renderCommitFork({ ...holon, agent_sessions: [older, latest, newTab, closed] }, async () => { throw new Error('unexpected fork') })
  const commit = await screen.findByRole('button', { name: 'Commit' })
  await userEvent.click(screen.getByRole('tab', { name: 'Latest' }))
  expect(commit).toBeDisabled()
  const explanation = screen.getByRole('group', { name: 'Commit unavailable' })
  await userEvent.hover(explanation)
  expect(screen.getByRole('tooltip')).toHaveTextContent("This agent's conversation is not ready to fork yet.")
  await userEvent.unhover(explanation)
  expect(screen.queryByRole('tooltip')).not.toBeInTheDocument()
  // Tab from the preceding action reaches the disabled action's explanation.
  act(() => screen.getByRole('button', { name: 'More holon actions' }).focus())
  await userEvent.tab({ shift: true })
  expect(explanation).toHaveFocus()
  expect(screen.getByRole('tooltip')).toHaveTextContent('not ready to fork yet')
  fireEvent.keyDown(explanation, { key: 'Escape' })
  expect(screen.queryByRole('tooltip')).not.toBeInTheDocument()
  await userEvent.click(screen.getByRole('tab', { name: 'New' }))
  expect(commit).toBeEnabled()
  await userEvent.click(screen.getByRole('tab', { name: 'Latest' }))

  const updateLatest = (changes: Partial<AgentSession>) => setup.publish({
    ...setup.current(), agent_sessions: setup.current().agent_sessions!.map((agent) => agent.id === latest.id ? { ...agent, ...changes } : agent),
  })
  updateLatest({ resume_target: 'claude-conversation' })
  expect(commit).toBeDisabled() // Claude also needs its transcript path.
  updateLatest({ rollout_path: '/tmp/claude.jsonl' })
  await userEvent.hover(explanation)
  expect(screen.getByRole('tooltip')).toHaveTextContent('Claude Code is unavailable.')
  updateLatest({ agent_type: 'codex' })
  expect(commit).toBeEnabled()
  setup.publish({ ...setup.current(), agent_sessions: [] })
  expect(screen.queryByRole('button', { name: 'Commit' })).not.toBeInTheDocument()
  expect(screen.queryByRole('group', { name: 'Commit unavailable' })).not.toBeInTheDocument()
})

it.each(['task_complete', 'failed'] as const)('forks once without changing the selected tab or focus and restores Commit after %s', async (endState) => {
  const holon = commitForkHolon()
  const source = holon.agent_sessions![0]
  let resolve!: (response: Response) => void
  const pending = new Promise<Response>((done) => { resolve = done })
  const setup = renderCommitFork(holon, () => pending)
  const sourceTab = await screen.findByRole('tab', { name: 'Source' })
  const shellTab = screen.getByRole('tab', { name: 'Shell 1' })
  expect(await screen.findByRole('button', { name: 'Commit' })).toBeEnabled()
  await userEvent.click(shellTab)
  expect(screen.queryByRole('button', { name: 'Commit' })).not.toBeInTheDocument()
  await userEvent.click(sourceTab)
  const commit = screen.getByRole('button', { name: 'Commit' })
  expect(commit).toBeEnabled()
  expect(screen.queryByRole('button', { name: /Prepare/ })).not.toBeInTheDocument()
  const moreActions = screen.getByRole('button', { name: 'More holon actions' })
  await userEvent.click(moreActions)
  expect(within(moreActions.parentElement!).queryByRole('button', { name: 'Commit' })).not.toBeInTheDocument()
  const inspectionCount = () => setup.fetchMock.mock.calls.filter(([input]) => String(input).includes('/workspace')).length
  const before = inspectionCount()
  fireEvent.pointerDown(commit)
  act(() => { commit.click(); commit.click() })
  expect(screen.getByRole('button', { name: 'Starting…' })).toBeDisabled()
  fireEvent.click(commit)
  await userEvent.click(shellTab)
  expect(shellTab).toHaveAttribute('aria-selected', 'true')
  expect(moreActions).toHaveAttribute('aria-expanded', 'false')
  const requests = setup.fetchMock.mock.calls.filter(([input]) => String(input).endsWith('/commit-agent'))
  expect(requests).toHaveLength(1)
  expect(requests[0][1]).toMatchObject({ method: 'POST' })
  expect(JSON.parse(String(requests[0][1]?.body))).toEqual({ source_agent_id: source.id })
  expect(inspectionCount()).toBe(before)
  expect(setup.fetchMock.mock.calls.some(([input]) => String(input).includes('/commit-prompt'))).toBe(false)

  const destination: AgentSession = {
    id: 'commit-agent', holon_id: holon.id, terminal_id: 'commit-terminal', title: 'Commit', agent_type: 'codex',
    status: 'running', input_state: 'none', activity: 'working', commit_prompt: { state: 'commit_discussion_started' },
    tab_order: 3, created_at: new Date().toISOString(), updated_at: new Date().toISOString(),
  }
  await act(async () => resolve(new Response(JSON.stringify(destination), { status: 201 })))
  expect(await screen.findByRole('tab', { name: 'Commit' })).toHaveAttribute('aria-selected', 'false')
  expect(sourceTab).toHaveAttribute('aria-selected', 'false')
  expect(shellTab).toHaveAttribute('aria-selected', 'true')
  expect(shellTab).toHaveFocus()
  expect(setup.current().agent_sessions!.find((agent) => agent.id === source.id)).toEqual(source)
  expect(setup.current().manual_terminals).toEqual(holon.manual_terminals)
  expect(inspectionCount()).toBe(before)
  expect(screen.queryByRole('button', { name: 'Commit' })).not.toBeInTheDocument()

  // The commit terminal stays available when the user explicitly selects it.
  await userEvent.click(screen.getByRole('tab', { name: 'Commit' }))
  const activeCommit = screen.getByRole('button', { name: 'Commit' })
  expect(activeCommit).toBeDisabled()
  await userEvent.hover(screen.getByRole('group', { name: 'Commit unavailable' }))
  expect(screen.getByRole('tooltip')).toHaveTextContent('A commit agent is already active.')
  await waitFor(() => expect([...setup.sockets.keys()].some((url) => url.includes('commit-terminal'))).toBe(true))
  const forkSocket = [...setup.sockets.entries()].find(([url]) => url.includes('commit-terminal'))![1]
  act(() => {
    forkSocket.open()
    forkSocket.message({ type: 'attached', restore: {
      attachment: { token: 'commit-attachment' },
      checkpoint: { terminal_id: 'commit-terminal', sequence: 0, dimensions: { columns: 120, rows: 40 },
        format_version: 'holark.ansi.v1', replay_payload: '', checksum: '0'.repeat(64), quality: 'trusted' },
      tail: [], last_sequence: 0,
    } })
  })
  await waitFor(() => {
    expect(document.activeElement).toBeInstanceOf(HTMLTextAreaElement)
    expect(document.activeElement?.closest('[data-terminal-pane]')).toHaveAttribute('data-terminal-id', 'commit-terminal')
  })

  setup.publish({ ...setup.current(), agent_sessions: [source, { ...destination, input_state: 'user_input_required', activity: 'needs_input' }] })
  expect(activeCommit).toBeDisabled()
  setup.publish({ ...setup.current(), agent_sessions: [source, { ...destination,
    ...(endState === 'failed' ? { status: 'failed' as const, reason: 'Launch failed' } : { input_state: 'task_complete' as const, activity: 'completed' as const }),
  }] })
  await userEvent.click(sourceTab)
  expect(activeCommit).toBeEnabled()
})

it('preserves the selected tab on a failed commit fork and allows another attempt', async () => {
  const setup = renderCommitFork(commitForkHolon(), async () =>
    new Response(JSON.stringify({ code: 'commit_source_not_forkable', message: 'The selected conversation is no longer available.' }), { status: 409 }))
  const shellTab = await screen.findByRole('tab', { name: 'Source' })
  await userEvent.click(shellTab)
  const before = setup.current()
  const commit = await screen.findByRole('button', { name: 'Commit' })
  await userEvent.click(commit)
  expect(await screen.findByText('The selected conversation is no longer available.')).toBeInTheDocument()
  expect(shellTab).toHaveAttribute('aria-selected', 'true')
  expect(screen.queryByRole('tab', { name: 'Commit' })).not.toBeInTheDocument()
  expect(setup.current()).toEqual(before)
  expect(commit).toBeEnabled()
})

it('hides the commit action while Changes is open and restores it on the agent tab', async () => {
  renderCommitFork(commitForkHolon(), async () => { throw new Error('unexpected commit fork') })
  expect(await screen.findByRole('button', { name: 'Commit' })).toBeEnabled()

  await userEvent.click(screen.getByRole('button', { name: /^Open changes panel/ }))
  expect(await screen.findByRole('dialog', { name: 'Holon changes' })).toBeInTheDocument()
  expect(screen.queryByRole('button', { name: 'Commit' })).not.toBeInTheDocument()
  await userEvent.click(screen.getByRole('button', { name: 'More holon actions' }))
  expect(screen.queryByRole('button', { name: 'Commit' })).not.toBeInTheDocument()

  await userEvent.click(screen.getByRole('button', { name: 'Close changes panel' }))
  expect(screen.getByRole('button', { name: 'Commit' })).toBeEnabled()
})

it('hides pull request actions when the branch has no changes', async () => {
  vi.stubGlobal('WebSocket', class { static OPEN = 1 })
  const fetchMock = stubApi({
    id: 'holon-1',
    repository_id: 'holark',
    runtime_id: 'node-1',
    prompt: 'Fix it',
    worktree_branch: 'holark/holon-1',
    status: 'completed',
    input_state: 'none', activity: 'working',
    created_at: new Date().toISOString(),
  }, [], sessionInspection('holark/holon-1', false))
  const fake = new FakeSocket()
  const socketFactory = () => fake as unknown as WebSocket

  render(
    <ProjectContext.Provider value={{ id: 'holark', name: 'Holark', default_branch: 'main' }}>
      <MemoryRouter initialEntries={['/holons/holon-1']}>
        <Routes><Route path="/holons/:holonId" element={<HolonTerminal socketFactory={socketFactory} />} /></Routes>
      </MemoryRouter>
    </ProjectContext.Provider>,
  )

  expect(await screen.findByRole('tablist', { name: 'Holon terminals' })).toBeInTheDocument()
  await waitFor(() => expect(fetchMock.mock.calls.some(([input]) => new URL(String(input), 'http://test.local').pathname.endsWith('/workspace'))).toBe(true))
  await userEvent.click(screen.getByRole('button', { name: 'More holon actions' }))
  expect(screen.queryByRole('button', { name: /Prepare/ })).not.toBeInTheDocument()
})

it('shows the opened pull request from the terminal', async () => {
  vi.stubGlobal('WebSocket', class { static OPEN = 1 })
  stubApi({
    id: 'holon-1',
    repository_id: 'holark',
    runtime_id: 'node-1',
    prompt: 'Fix it',
    worktree_branch: 'holark/holon-1',
    status: 'running',
    input_state: 'task_complete', activity: 'completed',
    created_at: new Date().toISOString(),
  }, [{ view_revision: 1,
    id: 'pr-1',
    repository_id: 'holark',
    title: 'Fix it',
    summary: '',
    base_branch: 'main',
    base_commit: 'abc123',
    head_branch: 'holark/holon-1',
    head_commit: 'def456',
    status: 'open',
    sync_data: {},
    linked_session_ids: ['holon-1'],
    created_at: new Date().toISOString(),
    updated_at: new Date().toISOString(),
  }], sessionInspection('holark/holon-1', true, { baseCommit: 'abc123', headCommit: 'def456' }))
  const fake = new FakeSocket()
  const socketFactory = () => fake as unknown as WebSocket

  render(
    <ProjectContext.Provider value={{ id: 'holark', name: 'Holark', default_branch: 'main' }}>
      <MemoryRouter initialEntries={['/holons/holon-1']}>
        <Routes><Route path="/holons/:holonId" element={<HolonTerminal socketFactory={socketFactory} />} /></Routes>
      </MemoryRouter>
    </ProjectContext.Provider>,
  )

  expect(await screen.findByRole('tablist', { name: 'Holon terminals' })).toBeInTheDocument()
  expect(screen.queryByRole('link', { name: /View pull request/ })).not.toBeInTheDocument()
  await userEvent.click(screen.getByRole('button', { name: 'More holon actions' }))
  expect(screen.getByRole('link', { name: /View pull request/ })).toHaveAttribute('href', '/pulls/pr-1')
  expect(screen.queryByRole('button', { name: /Prepare/ })).not.toBeInTheDocument()
  expect(screen.queryByRole('button', { name: 'Push' })).not.toBeInTheDocument()
  fireEvent.click(await screen.findByRole('button', { name: /^Open changes panel/ }))
  expect(await screen.findByRole('dialog', { name: 'Holon changes' })).toBeInTheDocument()
  expect(screen.queryByText('pull request open')).not.toBeInTheDocument()
  expect(screen.queryByText(/Holon start .* -> Worktree/)).not.toBeInTheDocument()
})

it('reads a linked GitHub pull request without full sync and removes publication actions after it is merged', async () => {
  vi.stubGlobal('WebSocket', class { static OPEN = 1 })
  const now = new Date().toISOString()
  const holon = {
    id: 'holon-1', repository_id: 'holark', runtime_id: 'node-1', prompt: 'Fix it',
    worktree_branch: 'holark/holon-1', upstream_branch: 'feature', upstream_head_commit: 'def45678',
    worktree_path: '/tmp/holon-1', status: 'running', input_state: 'task_complete', created_at: now,
  }
  let pullRequest = { view_revision: 1,
    id: 'pr-1', repository_id: 'holark', title: 'Fix it', summary: '', base_branch: 'main', base_commit: 'abc12345',
    head_branch: 'feature', head_commit: 'def45678', status: 'open', sync_provider: 'github', sync_data: {},
    linked_session_ids: ['holon-1'], created_at: now, updated_at: now,
  }
  let retired = false
  const fetchMock = vi.fn(async (input: string | URL | Request, _init?: RequestInit) => {
    const path = String(input)
    const url = new URL(path, 'http://test.local')
    if (url.pathname.endsWith('/agent-capabilities')) return jsonResponse({ capabilities: [{ type: 'codex', available: true }] })
    if (url.pathname.endsWith('/holons')) return jsonResponse([holon])
    if (url.pathname.endsWith('/holons/holon-1/pull-request')) return jsonResponse({ pull_request: pullRequest })
    if (url.pathname.endsWith('/publication-readiness')) return jsonResponse({
      target_branch: pullRequest.head_branch, target_commit: pullRequest.head_commit, workspace_head: 'fed78900',
      rebase_required: !retired, rebase_available: !retired, publication_available: !retired,
    })
    if (url.pathname.endsWith('/workspace')) return jsonResponse(inspectionResponse(
      sessionInspection('holark/holon-1', true, { baseCommit: 'abc12345', headCommit: 'fed78900' }), false,
    ))
    return jsonResponse(holon)
  })
  vi.stubGlobal('fetch', fetchMock)

  render(
    <ProjectContext.Provider value={{ id: 'holark', name: 'Holark', default_branch: 'main' }}>
      <MemoryRouter initialEntries={['/holons/holon-1']}>
        <Routes><Route path="/holons/:holonId" element={<HolonTerminal socketFactory={() => new FakeSocket() as unknown as WebSocket} />} /></Routes>
      </MemoryRouter>
    </ProjectContext.Provider>,
  )

  expect(await screen.findByRole('button', { name: 'Rebase' })).toBeInTheDocument()
  await userEvent.click(screen.getByRole('button', { name: 'More holon actions' }))
  expect(screen.getByRole('button', { name: 'Push' })).toBeInTheDocument()
  expect(fetchMock.mock.calls.some(([input, request]) => String(input).endsWith('/pull-requests/pr-1/sync') && request?.method === 'POST')).toBe(false)

  pullRequest = { ...pullRequest, view_revision: 2, status: 'merged' }
  retired = true
  window.dispatchEvent(new Event('focus'))
  await waitFor(() => expect(screen.queryByRole('button', { name: 'Rebase' })).not.toBeInTheDocument(), { timeout: 4000 })
  expect(screen.queryByRole('button', { name: 'Push' })).not.toBeInTheDocument()
}, 6000)

it.each(['success', 'publication failure', 'reconciliation failure'])('reconciles terminal Push once without full sync: %s', async (outcome) => {
  vi.stubGlobal('WebSocket', class { static OPEN = 1 })
  const user = userEvent.setup()
  const now = new Date().toISOString()
  const publishRequests: Array<{ path: string; body: unknown }> = []
  const holon = {
    id: 'holon-1',
    repository_id: 'holark',
    runtime_id: 'node-1',
    prompt: 'Fix it',
    kind: 'normal',
    worktree_branch: 'holark/holon-1',
    upstream_branch: 'holark/holon-1',
    upstream_head_commit: 'def45678',
    worktree_path: '/tmp/holon-1',
    status: 'running',
    input_state: 'task_complete', activity: 'completed',
    created_at: now,
  }
  const pullRequest = { view_revision: 1,
    id: 'pr-1',
    repository_id: 'holark',
    title: 'Fix it',
    summary: '',
    base_branch: 'main',
    base_commit: 'abc12345',
    head_branch: 'holark/holon-1',
    head_commit: 'def45678',
    status: 'open',
    sync_data: {},
    linked_session_ids: ['holon-1'],
    created_at: now,
    updated_at: now,
  }
  const fetchMock = vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
    const path = String(input)
    if (outcome === 'reconciliation failure' && publishRequests.length > 0 && !path.endsWith('/publish')) throw new Error('Follow-up read failed')
    if (path.endsWith('/publication-readiness')) return jsonResponse({
      target_branch: pullRequest.head_branch, target_commit: pullRequest.head_commit,
      workspace_head: 'fed78900', rebase_required: false, rebase_available: false, publication_available: true,
    })
    if (path.endsWith('/agent-capabilities')) {
      return new Response(JSON.stringify({ capabilities: [{ type: 'codex', available: true }] }), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    if (path.endsWith('/holons/holon-1/pull-request')) {
      return new Response(JSON.stringify({ pull_request: pullRequest }), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    if (new URL(path, 'http://test.local').pathname.endsWith('/holons/holon-1/workspace')) {
      return new Response(JSON.stringify(sessionInspection('holark/holon-1', true, { baseCommit: 'abc12345', headCommit: 'fed78900' })), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    if (path.endsWith('/holons/holon-1/publish') && init?.method === 'POST') {
      publishRequests.push({ path, body: JSON.parse(String(init.body)) })
      if (outcome === 'publication failure') return new Response(JSON.stringify({ code: 'stale_head', message: 'Publication rejected' }), { status: 409, headers: { 'Content-Type': 'application/json' } })
      return new Response(JSON.stringify({
        pull_request: { ...pullRequest, head_commit: 'fed78900', updated_at: now },
        head_commit: 'fed78900',
      }), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    if (path.endsWith('/holons')) {
      return new Response(JSON.stringify([holon]), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    return new Response(JSON.stringify(holon), { status: 200, headers: { 'Content-Type': 'application/json' } })
  })
  vi.stubGlobal('fetch', fetchMock)
  const fake = new FakeSocket()
  const socketFactory = () => fake as unknown as WebSocket

  render(
    <ProjectContext.Provider value={{ id: 'holark', name: 'Holark', default_branch: 'main' }}>
      <MemoryRouter initialEntries={['/holons/holon-1']}>
        <Routes><Route path="/holons/:holonId" element={<HolonTerminal socketFactory={socketFactory} />} /></Routes>
      </MemoryRouter>
    </ProjectContext.Provider>,
  )

  expect(await screen.findByRole('button', { name: 'Push' })).toBeInTheDocument()
  expect(screen.queryByRole('link', { name: /View pull request/ })).not.toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'More holon actions' }))
  expect(screen.getByRole('link', { name: /View pull request/ })).toHaveAttribute('href', '/pulls/pr-1')
  expect(screen.queryByRole('button', { name: /Prepare/ })).not.toBeInTheDocument()

  fetchMock.mockClear()
  await user.click(await screen.findByRole('button', { name: 'Push' }))

  await waitFor(() => expect(publishRequests).toEqual([{ path: '/api/v1/holons/holon-1/publish', body: { remote: 'origin', upstream_branch: 'holark/holon-1', expected_remote_head: 'def45678' } }]))
  await waitFor(() => expect(screen.queryByRole('button', { name: 'Pushing...' })).not.toBeInTheDocument())
  const paths = fetchMock.mock.calls.map(([input]) => new URL(String(input), 'http://test.local').pathname)
  expect(paths.filter((path) => path.endsWith('/publication-readiness'))).toHaveLength(1)
  expect(paths.filter((path) => path.endsWith('/workspace'))).toHaveLength(1)
  expect(paths.filter((path) => path.endsWith('/holons'))).toHaveLength(1)
  expect(paths.filter((path) => path.endsWith('/sync') || path.endsWith('/pull-request'))).toHaveLength(0)
  await user.click(screen.getByRole('button', { name: 'More holon actions' }))
  if (outcome === 'publication failure') expect(screen.getByText('Publication rejected')).toBeInTheDocument()
  else expect(screen.queryByText(/Push failed|Follow-up read failed/)).not.toBeInTheDocument()

})

it('publishes linked continue worker changes from the terminal', async () => {
  vi.stubGlobal('WebSocket', class { static OPEN = 1 })
  const user = userEvent.setup()
  const now = new Date().toISOString()
  const publishRequests: Array<{ path: string; body: unknown }> = []
  const holon = {
    id: 'holon-continue',
    repository_id: 'holark',
    runtime_id: 'node-1',
    prompt: 'Continue PR',
    kind: 'pr_worker',
    worktree_branch: 'holark/worker/holon-continue',
    upstream_branch: 'feature',
    upstream_head_commit: 'def45678',
    worktree_path: '/tmp/holon-continue',
    status: 'running',
    input_state: 'task_complete', activity: 'completed',
    created_at: now,
  }
  const pullRequest = { view_revision: 1,
    id: 'pr-1',
    repository_id: 'holark',
    title: 'Local PR',
    summary: 'Summary',
    base_branch: 'main',
    base_commit: 'abc12345',
    head_branch: 'feature',
    head_commit: 'def45678',
    status: 'open',
    sync_data: {},
    linked_session_ids: ['holon-continue'],
    created_at: now,
    updated_at: now,
  }
  const fetchMock = vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
    const path = String(input)
    if (path.endsWith('/publication-readiness')) return jsonResponse({
      target_branch: pullRequest.head_branch, target_commit: pullRequest.head_commit,
      workspace_head: 'fed78900', rebase_required: false, rebase_available: false, publication_available: true,
    })
    if (path.endsWith('/agent-capabilities')) {
      return new Response(JSON.stringify({ capabilities: [{ type: 'codex', available: true }] }), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    if (path.endsWith('/holons/holon-continue/pull-request')) {
      return new Response(JSON.stringify({ pull_request: pullRequest }), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    if (new URL(path, 'http://test.local').pathname.endsWith('/holons/holon-continue/workspace')) {
      return new Response(JSON.stringify(sessionInspection('holark/worker/holon-continue', true, { baseCommit: 'abc12345', headCommit: 'fed78900' })), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    if (path.endsWith('/holons/holon-continue/publish') && init?.method === 'POST') {
      publishRequests.push({ path, body: JSON.parse(String(init.body)) })
      return new Response(JSON.stringify({
        pull_request: { ...pullRequest, head_commit: 'fed78900', updated_at: now },
        worker: { id: 'worker-continue', pull_request_id: 'pr-1', holon_id: 'holon-continue', mode: 'continue', status: 'running', base_head_commit: 'fed78900', result_head_commit: 'fed78900', created_at: now },
        head_commit: 'fed78900',
      }), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    if (path.endsWith('/holons')) {
      return new Response(JSON.stringify([holon]), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    return new Response(JSON.stringify(holon), { status: 200, headers: { 'Content-Type': 'application/json' } })
  })
  vi.stubGlobal('fetch', fetchMock)
  const fake = new FakeSocket()
  const socketFactory = () => fake as unknown as WebSocket

  render(
    <ProjectContext.Provider value={{ id: 'holark', name: 'Holark', default_branch: 'main' }}>
      <MemoryRouter initialEntries={['/holons/holon-continue']}>
        <Routes><Route path="/holons/:holonId" element={<HolonTerminal socketFactory={socketFactory} />} /></Routes>
      </MemoryRouter>
    </ProjectContext.Provider>,
  )

  expect(await screen.findByRole('button', { name: 'Push' })).toBeInTheDocument()
  expect(screen.queryByRole('link', { name: /View pull request/ })).not.toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'More holon actions' }))
  expect(screen.getByRole('link', { name: /View pull request/ })).toHaveAttribute('href', '/pulls/pr-1')
  expect(screen.queryByRole('button', { name: /Prepare/ })).not.toBeInTheDocument()

  await user.click(await screen.findByRole('button', { name: 'Push' }))

  await waitFor(() => expect(publishRequests).toEqual([{ path: '/api/v1/holons/holon-continue/publish', body: { remote: 'origin', upstream_branch: 'feature', expected_remote_head: 'def45678' } }]))
})

it.each(['available', 'disabled', 'getItem', 'setItem', 'removeItem'] as const)('recovers a lost agent response and allows another creation with session storage %s', async (storageMode) => {
  vi.stubGlobal('WebSocket', class { static OPEN = 1 })
  const holonId = `holon-storage-${storageMode}`
  const holon = { ...renamableHolon('Work', holonId), worktree_path: '/tmp/holon-retry' } as Holon
  const storage = window.sessionStorage
  if (storageMode === 'disabled') {
    vi.spyOn(window, 'sessionStorage', 'get').mockImplementation(() => { throw new DOMException('Storage disabled', 'SecurityError') })
  } else if (storageMode !== 'available') {
    const original: (this: Storage, key: string, value: string) => string | null | void = Storage.prototype[storageMode]
    vi.spyOn(Storage.prototype, storageMode).mockImplementation(function (this: Storage, key: string, value?: string) {
      if (this === storage) throw new DOMException('Storage unavailable', storageMode === 'setItem' ? 'QuotaExceededError' : 'SecurityError')
      return original.call(this, key, value!)
    })
  }
  const fetchMock = stubApi(holon)
  const normal = fetchMock.getMockImplementation()!
  const identities: string[] = []
  let rejectFirst: (error: Error) => void = () => undefined
  let created: Response | undefined
  fetchMock.mockImplementation(async (input, init) => {
    if (String(input).endsWith('/agent-sessions') && init?.method === 'POST') {
      identities.push(new Headers(init.headers).get('Idempotency-Key')!)
      if (identities.length === 1) {
        created = await normal(input, init)
        return new Promise<Response>((_, reject) => { rejectFirst = reject })
      }
      if (identities.length === 2) return created!
      return normal(input, init)
    }
    return normal(input, init)
  })
  const view = render(
    <ProjectContext.Provider value={{ id: 'holark', name: 'Holark', default_branch: 'main' }}>
      <MemoryRouter initialEntries={[`/holons/${holonId}`]}>
        <Routes><Route path="/holons/:holonId" element={<HolonTerminal socketFactory={() => new FakeSocket() as unknown as WebSocket} />} /></Routes>
      </MemoryRouter>
    </ProjectContext.Provider>,
  )
  await screen.findByRole('tablist', { name: 'Holon terminals' })
  await userEvent.click(screen.getByRole('button', { name: 'New tab' }))
  await userEvent.click(screen.getByRole('button', { name: 'Codex' }))
  expect(screen.getByRole('button', { name: 'Adding agent' })).toHaveAttribute('aria-busy', 'true')
  // Reopen the menu if its normal action behavior closed it.
  if (!screen.queryByRole('button', { name: 'Codex' })) await userEvent.click(screen.getByRole('button', { name: 'Adding agent' }))
  expect(screen.getByRole('button', { name: 'Codex' })).toBeDisabled()
  await act(async () => { rejectFirst(new TypeError('Failed to fetch')) })
  expect(await screen.findByRole('alert')).toHaveTextContent('Couldn’t confirm whether the agent was added.')
  await userEvent.click(within(screen.getByRole('alert')).getByRole('button', { name: 'Retry' }))
  await waitFor(() => expect(identities).toHaveLength(2))
  expect(identities[0]).toBeTruthy()
  expect(identities[1]).toBe(identities[0])
  await waitFor(() => expect(screen.queryByRole('alert')).not.toBeInTheDocument())
  if (storageMode === 'available' || storageMode === 'setItem') {
    expect(storage.getItem(`holark:add-agent:${holonId}`)).toBeNull()
  }
  await userEvent.click(screen.getByRole('button', { name: 'New tab' }))
  await userEvent.click(screen.getByRole('button', { name: 'Codex' }))
  await waitFor(() => expect(identities).toHaveLength(3))
  expect(identities[2]).toBeTruthy()
  expect(identities[2]).not.toBe(identities[0])
  await waitFor(() => expect(screen.queryByRole('button', { name: 'Adding agent' })).not.toBeInTheDocument())
  expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  view.unmount()
})

it.each([
  { status: 503, code: 'harness_unavailable', rejected: true },
  { status: 400, code: 'invalid_harness', rejected: true },
  { status: 400, code: 'invalid_request', rejected: true },
  { status: 404, code: 'holon_not_found', rejected: true },
  { status: 408, code: 'request_canceled', rejected: false },
  { status: 504, code: 'operation_timeout', rejected: false },
  { status: 500, code: 'holon_failed', rejected: false },
])('retains an agent attempt only for uncertain creation outcomes: $code', async ({ status, code, rejected }) => {
  vi.stubGlobal('WebSocket', class { static OPEN = 1 })
  const holonId = `holon-rejection-${code}`
  const storageKey = `holark:add-agent:${holonId}`
  const holon = { ...renamableHolon('Work', holonId), worktree_path: '/tmp/holon-rejection' } as Holon
  const fetchMock = stubApi(holon)
  const normal = fetchMock.getMockImplementation()!
  const attempts: Array<{ id: string; harness: HarnessType }> = []
  const message = `Creation failed: ${code}`
  fetchMock.mockImplementation(async (input, init) => {
    if (String(input).endsWith('/agent-capabilities')) {
      return jsonResponse({ capabilities: harnessCapabilities(['codex', 'opencode']) })
    }
    if (String(input).endsWith('/agent-sessions') && init?.method === 'POST') {
      attempts.push({ id: new Headers(init.headers).get('Idempotency-Key')!, harness: JSON.parse(String(init.body)).agent_type })
      if (attempts.length === 1) return new Response(JSON.stringify({ code, message }), { status })
    }
    return normal(input, init)
  })
  const renderTerminal = () => render(
    <ProjectContext.Provider value={{ id: 'holark', name: 'Holark', default_branch: 'main' }}>
      <MemoryRouter initialEntries={[`/holons/${holonId}`]}>
        <Routes><Route path="/holons/:holonId" element={<HolonTerminal socketFactory={() => new FakeSocket() as unknown as WebSocket} />} /></Routes>
      </MemoryRouter>
    </ProjectContext.Provider>,
  )
  const view = renderTerminal()
  await screen.findByRole('tablist', { name: 'Holon terminals' })
  await userEvent.click(screen.getByRole('button', { name: 'New tab' }))
  await userEvent.click(screen.getByRole('button', { name: 'Codex' }))
  expect(await screen.findByRole('alert')).toHaveTextContent(rejected ? message : 'Couldn’t confirm whether the agent was added.')
  expect(attempts[0].id).toBeTruthy()
  expect(attempts[0].harness).toBe('codex')
  // Persist only uncertain attempts so a reload cannot restore a rejected one.
  expect(sessionStorage.getItem(storageKey)).toBe(rejected ? null : JSON.stringify(attempts[0]))
  view.unmount()

  renderTerminal()
  await screen.findByRole('tablist', { name: 'Holon terminals' })
  await userEvent.click(screen.getByRole('button', { name: 'New tab' }))
  await userEvent.click(screen.getByRole('button', { name: 'OpenCode' }))
  await waitFor(() => expect(attempts).toHaveLength(2))
  expect(attempts[1].harness).toBe(rejected ? 'opencode' : 'codex')
  if (rejected) expect(attempts[1].id).not.toBe(attempts[0].id)
  else expect(attempts[1].id).toBe(attempts[0].id)
  await waitFor(() => expect(screen.queryByRole('button', { name: 'Adding agent' })).not.toBeInTheDocument())
  expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  expect(sessionStorage.getItem(storageKey)).toBeNull()
})

it('aborts publication polling when leaving the Holon', async () => {
  vi.stubGlobal('WebSocket', class { static OPEN = 1 })
  const holon = { ...renamableHolon('Work', 'holon-poll'), worktree_path: '/tmp/holon-poll' } as Holon
  const fetchMock = stubApi(holon)
  const normal = fetchMock.getMockImplementation()!
  let pending: AbortSignal | undefined
  fetchMock.mockImplementation(async (input, init) => {
    if (String(input).endsWith('/publication-readiness')) {
      pending = init?.signal ?? undefined
      return new Promise<Response>((_, reject) => pending!.addEventListener('abort', () => reject(pending!.reason), { once: true }))
    }
    return normal(input, init)
  })
  const view = render(
    <ProjectContext.Provider value={{ id: 'holark', name: 'Holark', default_branch: 'main' }}>
      <MemoryRouter initialEntries={['/holons/holon-poll']}>
        <Routes><Route path="/holons/:holonId" element={<HolonTerminal socketFactory={() => new FakeSocket() as unknown as WebSocket} />} /></Routes>
      </MemoryRouter>
    </ProjectContext.Provider>,
  )
  await waitFor(() => expect(pending).toBeDefined())
  view.unmount()
  expect(pending?.aborted).toBe(true)
})

it('opens a preparing reservation without tabs, keeps End available, and adds resources when ready', async () => {
  const ready = commitForkHolon()
  const reserved: Holon = { ...ready, status: 'preparing', worktree_path: undefined, worktree_branch: undefined, agent_session: undefined, agent_sessions: [], manual_terminals: [], ides: [] }
  const setup = renderCommitFork(reserved, async () => { throw new Error('unexpected commit fork') })
  expect(await screen.findByText('Preparing your workspace…')).toBeVisible()
  expect(screen.queryByRole('tab')).toBeNull()
  expect(setup.sockets.size).toBe(0)
  expect(setup.fetchMock.mock.calls.some(([input]) => String(input).includes('/workspace'))).toBe(false)
  await userEvent.click(screen.getByRole('button', { name: 'More holon actions' }))
  expect(screen.getByRole('button', { name: 'End holon' })).toBeEnabled()
  await userEvent.keyboard('{Escape}')
  setup.publish(ready)
  expect(await screen.findByRole('tab', { name: 'Source' })).toBeVisible()
  await waitFor(() => expect(setup.sockets.size).toBeGreaterThan(0))
  expect(screen.queryByText('Preparing your workspace…')).toBeNull()
})

it.each(['agent', 'holon'] as const)('shows the saved %s failure when startup ends before a terminal is bound', async (reasonSource) => {
  const ready = commitForkHolon()
  const agent: AgentSession = { ...ready.agent_session!, status: 'queued', terminal_id: undefined }
  const preparing: Holon = { ...ready, status: 'preparing', agent_session: agent, agent_sessions: [agent], manual_terminals: [] }
  const setup = renderCommitFork(preparing, async () => { throw new Error('unexpected commit fork') })
  expect(await screen.findByText('Preparing your workspace…')).toBeVisible()
  expect(setup.sockets.size).toBe(0)

  const reason = 'Agent setup failed before the terminal could start.'
  const failedAgent: AgentSession = { ...agent, status: 'failed', reason: reasonSource === 'agent' ? reason : undefined }
  const failed: Holon = {
    ...preparing, status: 'failed', reason: reasonSource === 'holon' ? reason : 'Holon startup failed.',
    agent_session: failedAgent, agent_sessions: [failedAgent],
  }
  setup.publish(failed)
  expect(await screen.findByRole('alert')).toHaveTextContent(reason)
  expect(screen.getByRole('tab', { name: 'Source' })).toHaveAttribute('aria-selected', 'true')
  expect(screen.queryByText('Preparing your workspace…')).not.toBeInTheDocument()
  expect(setup.sockets.size).toBe(0)

  setup.publish({ ...failed, manual_terminals: ready.manual_terminals })
  await userEvent.click(screen.getByRole('tab', { name: 'Shell 1' }))
  expect(screen.queryByText(reason)).not.toBeInTheDocument()
  await userEvent.click(screen.getByRole('tab', { name: 'Source' }))
  expect(screen.getByRole('alert')).toHaveTextContent(reason)
})

it('keeps terminal access and child activity while Finalizing displays a retry reason', async () => {
  const ready = commitForkHolon()
  const finalizing: Holon = { ...ready, application_phase: 'finalizing', reason: 'Publication failed. Retry the push.', agent_sessions: ready.agent_sessions!.map((agent) => ({ ...agent, activity: 'working' })) }
  const setup = renderCommitFork(finalizing, async () => { throw new Error('unexpected commit fork') })
  expect(await screen.findByRole('alert')).toHaveTextContent('Publication failed. Retry the push.')
  expect(screen.queryByText('Finalizing')).not.toBeInTheDocument()
  expect(await screen.findByRole('tab', { name: 'Source' })).toBeVisible()
  await waitFor(() => expect(setup.sockets.size).toBeGreaterThan(0))
  const connections = [...setup.sockets.values()]
  setup.publish({ ...finalizing, application_phase: undefined, reason: '' })
  expect(screen.queryByText('Finalizing')).toBeNull()
  expect(screen.queryByText('Publication failed. Retry the push.')).toBeNull()
  expect(connections.some((socket) => !socket.closed)).toBe(true)
})
