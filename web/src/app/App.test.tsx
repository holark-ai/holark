import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { ColorThemeProvider } from './ColorThemeProvider'
import { colorThemeStorageKey } from './colorTheme'
import { TerminalWorkspaceProvider } from '../features/terminal/TerminalWorkspaceProvider'
import { AppRoutes } from './routes'

const longIssueLabelName = 'documentation: needs review from subject-matter experts'

vi.mock('monaco-editor', () => ({ editor: {
  defineTheme: () => undefined,
  setTheme: () => undefined,
  colorize: async (content: string) => {
    const element = document.createElement('code')
    element.textContent = content
    return element.innerHTML
  },
} }))

function renderRoute(route: string) {
  return render(
    <ColorThemeProvider>
      <TerminalWorkspaceProvider>
      <MemoryRouter initialEntries={[route]}>
        <AppRoutes />
      </MemoryRouter>
      </TerminalWorkspaceProvider>
    </ColorThemeProvider>,
  )
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

beforeEach(() => {
  window.localStorage.clear()
  document.documentElement.removeAttribute('data-theme')
  document.documentElement.removeAttribute('style')
  let meta = document.querySelector<HTMLMetaElement>('meta[name="theme-color"]')
  if (!meta) {
    meta = document.createElement('meta')
    meta.name = 'theme-color'
    document.head.append(meta)
  }
  vi.stubGlobal('fetch', vi.fn(async (input: string | URL | Request) => {
    const path = String(input)
    if (path.endsWith('/api/v1/github-profile')) {
      return Response.json({ code: 'gh_unavailable', message: 'GitHub profile is unavailable.' }, { status: 503 })
    }
    if (path.includes('/api/v1/pull-requests/search?')) {
      return Response.json({ rows: [], total: 0, page: 1, per_page: 30, identity: null, sync: [] })
    }
    if (path.endsWith('/prompt-templates')) {
      return new Response(JSON.stringify([
        { key: 'issue_agent_plan', name: 'Issue agent plan prompt', use: 'Issue holons', variables: [{ name: 'issue_title', description: 'Issue title.' }], value: 'Issue {{issue_title}}', default_value: 'Default issue {{issue_title}}', overridden: true },
        { key: 'pull_request_review', name: 'Pull request review prompt', use: 'PR reviews', variables: [], value: 'Review', default_value: 'Review', overridden: false },
        { key: 'commit_follow_up', name: 'Post-task commit follow-up prompt', use: 'Commit follow-up', variables: [], value: 'Commit', default_value: 'Commit', overridden: false },
      ]), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    if (path.includes('/tree')) {
      const url = new URL(path, 'http://test.local')
      const treePath = url.searchParams.get('path') ?? ''
      const entries = treePath === 'src'
        ? [
            { name: 'main.tsx', path: 'src/main.tsx', type: 'file', mode: '100644', size: 96, language: 'typescript' },
          ]
        : [
            { name: 'src', path: 'src', type: 'directory', mode: '040000' },
            { name: 'README.md', path: 'README.md', type: 'file', mode: '100644', size: 12, language: 'markdown' },
          ]
      return new Response(JSON.stringify({ repository_id: 'holark', ref: 'main', commit: 'abcdef123456', path: treePath, entries }), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    if (path.includes('/blob')) {
      const url = new URL(path, 'http://test.local')
      const blobPath = url.searchParams.get('path')
      if (blobPath === 'README.md') {
        return new Response(JSON.stringify({ repository_id: 'holark', ref: 'main', commit: 'abcdef123456', path: blobPath, language: 'markdown', content: '# Holark\n', encoding: 'utf-8', size: 11, truncated: false }), { status: 200, headers: { 'Content-Type': 'application/json' } })
      }
      if (blobPath === 'src/main.tsx') {
        return new Response(JSON.stringify({ repository_id: 'holark', ref: 'main', commit: 'abcdef123456', path: blobPath, language: 'typescript', content: 'console.log("holark")\n', encoding: 'utf-8', size: 24, truncated: false }), { status: 200, headers: { 'Content-Type': 'application/json' } })
      }
      return new Response(JSON.stringify({ code: 'path_not_found', message: 'Path not found.' }), { status: 404, headers: { 'Content-Type': 'application/json' } })
    }
    if (path.endsWith('/api/v1/repository/refs')) {
      return new Response(JSON.stringify({
        repository_id: 'holark',
        default_ref: 'refs/heads/main',
        refs: [
          { name: 'refs/heads/main', short_name: 'main', kind: 'branch', target: 'abcdef123456', committed_at: '2026-07-23T10:00:00Z' },
          { name: 'refs/heads/feature/foo', short_name: 'feature/foo', kind: 'branch', target: 'fedcba654321', committed_at: '2026-07-23T10:30:00Z' },
          { name: 'refs/heads/no-src', short_name: 'no-src', kind: 'branch', target: 'aaaaaa654321', committed_at: '2026-07-23T10:45:00Z' },
          { name: 'refs/heads/holark/holon-1', short_name: 'holon-1', kind: 'holark_branch', target: '123456abcdef', committed_at: '2026-07-23T11:00:00Z' },
        ],
      }), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    if (path.includes('/api/v1/repository/commit-changes')) {
      const ref = new URL(path, 'http://test.local').searchParams.get('ref') ?? 'abcdef123456'
      return Response.json({ commit: ref, parent: '', files: [] })
    }
    if (path.includes('/commits')) {
      const url = new URL(path, 'http://test.local')
      const ref = url.searchParams.get('ref')
      const commit = ref === 'refs/heads/holark/holon-1'
        ? { sha: '123456abcdef', message: 'Autopush commit', author_name: 'Holark Test', authored_at: '2026-07-23T11:00:00Z' }
        : { sha: 'abcdef123456', message: 'Initial commit', author_name: 'Holark Test', authored_at: '2026-07-23T10:00:00Z' }
      return new Response(JSON.stringify({ repository_id: 'holark', ref: ref || 'refs/heads/main', commits: [commit] }), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    const issueLabels = [
      { id: 'label-dark', name: 'bug', color: '000000', description: 'Something is broken' },
      { id: 'label-light', name: longIssueLabelName, color: 'ffffff', description: '' },
      { id: 'label-priority', name: 'high priority', color: 'd73a4a', description: 'Needs prompt attention' },
      { id: 'label-help', name: 'help wanted', color: '008672', description: 'Extra attention is needed' },
    ]
    const labeledIssue = { id: 'issue-1', repository_id: 'holark', title: 'Fix issue navigation', body: '', status: 'open', sync_data: {}, labels: issueLabels, linked_pull_request_ids: [], created_at: new Date().toISOString(), updated_at: new Date().toISOString() }
    const unlabeledIssue = { id: 'issue-2', repository_id: 'holark', title: 'Unlabeled issue', body: '', status: 'open', sync_data: {}, labels: [], linked_pull_request_ids: [], created_at: new Date().toISOString(), updated_at: new Date().toISOString() }
    if (path.includes('/issues/search?')) {
      return new Response(JSON.stringify({ rows: [labeledIssue, unlabeledIssue].map((issue) => ({ ...issue, kind: 'issue', number: 0, assignee_ids: [], reviewer_ids: [], reasons: [] })), total: 2, page: 1, per_page: 50, identity: null, sync: [] }), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    if (/\/issues\/[^/]+\/comments(?:\/sync)?$/.test(path)) {
      return new Response(JSON.stringify({ comments: [], synced_at: null, can_comment: null }), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    if (path.includes('/api/v1/issues/search?')) {
      return Response.json({ rows: [labeledIssue, unlabeledIssue].map(issue => ({
        id: issue.id, kind: 'issue', title: issue.title, number: 0, status: issue.status,
        assignee_ids: [], reviewer_ids: [], updated_at: issue.updated_at, reasons: [],
        issue: { labels: issue.labels, linked_pull_request_count: 0, url: '' },
      })), total: 2, page: 1, per_page: 50, identity: null, sync: [] })
    }
    const body = path.endsWith('/issues')
      ? [labeledIssue, unlabeledIssue]
      : path.endsWith('/issues/issue-1')
        ? labeledIssue
        : path.endsWith('/pull-requests/pr-1/changes')
          ? { inputs: { head_commit: '1234567', diff_base_commit: 'abcdef0' }, data: { branch: 'feature', base_branch: 'main', base_commit: 'abcdef0', head_commit: '1234567', has_changes: false, dirty: false, files: [], diff_truncated: false } }
          : path.endsWith('/pull-requests/pr-1/commits')
            ? { inputs: { head_commit: '1234567', diff_base_commit: 'abcdef0' }, data: [] }
          : path.endsWith('/pull-requests/pr-1')
        ? { view_revision: 1, id: 'pr-1', repository_id: 'holark', title: 'Fix navigation', summary: '', base_branch: 'main', base_commit: 'abcdef0', head_branch: 'feature', head_commit: '1234567', status: 'open', sync_data: {}, linked_session_ids: ['holon-1'], created_at: new Date().toISOString(), updated_at: new Date().toISOString() }
        : path.endsWith('/pull-requests')
          ? []
          : path.endsWith('/api/v1/repository')
            ? { id: 'holark', root: '/work/holark', common_dir: '/work/holark/.git', default_branch: 'main' }
            : path.endsWith('/agent-capabilities')
              ? { capabilities: [{ type: 'codex', available: true, version: '1.0' }] }
              : []
    return new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })
  }))
})

afterEach(() => vi.unstubAllGlobals())

describe('Holark application routes', () => {
  it('keeps model preferences during discovery failure and saves custom pairs', async () => {
    const user = userEvent.setup()
    const baseFetch = fetch
    const defaults: Record<string, { harness_type: string; explicit: boolean; model?: string }> = { default: { harness_type: 'codex', explicit: true, model: 'saved-model' } }
    let discoveries = 0
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input)
      if (path.endsWith('/agent-capabilities')) return Response.json({ capabilities: [
        { type: 'codex', available: true, automated_workflows: true },
        { type: 'claude-code', available: true, automated_workflows: true },
      ], harness_defaults: defaults })
      if (path.endsWith('/agents/codex/models')) {
        discoveries++
        return discoveries === 1 ? Response.json({ message: 'Discovery failed' }, { status: 503 }) : Response.json([{ id: 'new-model', label: 'New model' }])
      }
      if (path.includes('/agent-harness-defaults/')) {
        const workflow = path.split('/').at(-1)!
        if (init?.method === 'DELETE') { delete defaults[workflow]; return new Response(null, { status: 204 }) }
        defaults[workflow] = { ...JSON.parse(String(init?.body)), explicit: true }
        return Response.json(defaults[workflow])
      }
      return baseFetch(input, init)
    }))
    renderRoute('/settings')
    const general = await screen.findByRole('combobox', { name: 'Default harness model' })
    await waitFor(() => expect(general).toHaveTextContent('saved-model'))
    expect(screen.queryByRole('combobox', { name: 'Work on an issue model' })).not.toBeInTheDocument()
    expect(discoveries).toBe(0)
    await user.click(general)
    expect(await screen.findByText('Could not load models.')).toBeInTheDocument()
    expect(screen.getByRole('option', { name: 'saved-model' })).toBeInTheDocument()
    await user.keyboard('{Escape}')
    expect(defaults.default.model).toBe('saved-model')
    await user.click(screen.getByRole('button', { name: 'Retry' }))
    await user.click(general)
    await user.click(await screen.findByRole('option', { name: 'New model' }))
    await waitFor(() => expect(defaults.default.model).toBe('new-model'))
    await user.click(screen.getByRole('combobox', { name: 'Work on an issue harness' }))
    await user.click(screen.getByRole('option', { name: /Codex/ }))
    const issueModel = await screen.findByRole('combobox', { name: 'Work on an issue model' })
    expect(issueModel).toHaveTextContent('Agent default')
    await user.click(issueModel)
    await user.click(screen.getByRole('option', { name: 'Custom model…' }))
    await user.type(screen.getByRole('textbox', { name: 'Work on an issue custom model ID' }), 'provider/exact-version')
    await user.click(within(screen.getByRole('textbox', { name: 'Work on an issue custom model ID' }).closest('form')!).getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(defaults.issue.model).toBe('provider/exact-version'))
    expect(discoveries).toBe(2)
    await user.click(screen.getByRole('combobox', { name: 'Work on an issue harness' }))
    await user.click(screen.getByRole('option', { name: /Claude Code/ }))
    await waitFor(() => expect(defaults.issue.model).toBe(''))
    await user.click(screen.getByRole('combobox', { name: 'Work on an issue harness' }))
    await user.click(screen.getByRole('option', { name: 'Use default' }))
    await waitFor(() => expect(screen.queryByRole('combobox', { name: 'Work on an issue model' })).not.toBeInTheDocument())
    expect(screen.getByLabelText('Work on an issue model')).toHaveTextContent('New model')
  })

  it('shows unsupported versions and saves them as general and workflow defaults', async () => {
    const user = userEvent.setup()
    const baseFetch = fetch
    const defaults: Record<string, { harness_type: string; explicit: boolean }> = { default: { harness_type: 'codex', explicit: false } }
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input)
      if (path.endsWith('/agent-capabilities')) return new Response(JSON.stringify({
        capabilities: [
          { type: 'codex', available: true, version: '0.153.4', support_status: 'supported' },
          { type: 'claude-code', available: false, unavailable_reason: 'Claude Code CLI was not found' },
          { type: 'opencode', available: true, automated_workflows: true, version: '1.18.26', support_status: 'unsupported', supported_ranges: ['=1.18.25'], latest_supported_version: '1.18.25', warning: 'Holark supports OpenCode <= 1.18.25. 1.18.26 may not work as expected.' },
        ],
        default_harness: defaults.default.harness_type,
        harness_defaults: defaults,
      }))
      if (path.includes('/agent-harness-defaults/') && init?.method === 'PUT') {
        const workflow = path.split('/').at(-1)!
        defaults[workflow] = { ...JSON.parse(String(init.body)), explicit: true }
        return new Response(JSON.stringify(defaults[workflow]))
      }
      return baseFetch(input, init)
    }))
    const view = renderRoute('/settings')
    expect(await screen.findByText('Holark supports OpenCode <= 1.18.25. 1.18.26 may not work as expected.')).toBeInTheDocument()
    for (const label of ['Default harness', 'Work on an issue harness']) {
      await user.click(screen.getByRole('combobox', { name: label }))
      expect(screen.getByRole('option', { name: /Claude Code/ })).toHaveAttribute('aria-disabled', 'true')
      const option = screen.getByRole('option', { name: /OpenCode/ })
      expect(option).not.toHaveAttribute('aria-disabled', 'true')
      await user.click(option)
      await waitFor(() => expect(screen.getByRole('combobox', { name: label })).toHaveTextContent('OpenCode'))
    }
    expect(defaults.default.harness_type).toBe('opencode')
    expect(defaults.issue.harness_type).toBe('opencode')
    view.unmount()
    renderRoute('/settings')
    await waitFor(() => expect(screen.getByRole('combobox', { name: 'Default harness' })).toHaveTextContent('OpenCode'))
    expect(screen.getByRole('combobox', { name: 'Work on an issue harness' })).toHaveTextContent('OpenCode')
  })

  it.each(['focus', 'visibilitychange'])('refreshes shared agent availability on %s', async (event) => {
    const user = userEvent.setup()
    const baseFetch = fetch
    let claudeAvailable = false
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      if (String(input).endsWith('/agent-capabilities')) return Response.json({ capabilities: [
        { type: 'codex', available: true },
        { type: 'claude-code', available: claudeAvailable },
      ] })
      return baseFetch(input, init)
    }))
    const visibility = vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible')
    const { unmount } = renderRoute('/settings')
    try {
      await user.click(await screen.findByRole('combobox', { name: 'Default harness' }))
      expect(screen.getByRole('option', { name: /Claude Code/ })).toHaveAttribute('aria-disabled', 'true')
      await user.keyboard('{Escape}')

      // Another repository fixes the shared launch command while this window is inactive.
      visibility.mockReturnValue('hidden')
      fireEvent(document, new Event('visibilitychange'))
      claudeAvailable = true
      visibility.mockReturnValue('visible')
      fireEvent(event === 'focus' ? window : document, new Event(event))

      await user.click(screen.getByRole('combobox', { name: 'Default harness' }))
      await waitFor(() => expect(screen.getByRole('option', { name: /Claude Code/ })).not.toHaveAttribute('aria-disabled', 'true'))
    } finally {
      unmount()
      visibility.mockRestore()
    }
  })

  it('renders the project overview from repository APIs', async () => {
    renderRoute('/')

    expect(await screen.findByRole('region', { name: 'Directory contents' })).toBeInTheDocument()
    expect(await screen.findByRole('heading', { name: 'Holons' })).toBeInTheDocument()
    expect(screen.queryByRole('heading', { name: 'Harnesses' })).not.toBeInTheDocument()
    expect(screen.queryByText('Codex')).not.toBeInTheDocument()
    expect(await screen.findByRole('link', { name: /src/ })).toHaveAttribute('href', '/tree/src?ref=main')
    expect(await screen.findByRole('heading', { name: 'README.md' })).toBeInTheDocument()
  })

  it('shares capability discovery between Settings and Operations without polling while visible or hidden', async () => {
    await import('../features/settings/SettingsView')
    vi.useFakeTimers()
    const visibility = vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible')
    const { unmount } = renderRoute('/settings')
    const capabilityRequests = () => vi.mocked(fetch).mock.calls.filter(([input]) => String(input).endsWith('/agent-capabilities'))
    try {
      await act(async () => { await vi.advanceTimersByTimeAsync(100) })
      await act(async () => { await vi.advanceTimersByTimeAsync(100) })
      expect(capabilityRequests()).toHaveLength(1)

      await act(async () => { await vi.advanceTimersByTimeAsync(120_000) })
      expect(capabilityRequests()).toHaveLength(1)

      visibility.mockReturnValue('hidden')
      fireEvent(document, new Event('visibilitychange'))
      await act(async () => { await vi.advanceTimersByTimeAsync(120_000) })
      expect(capabilityRequests()).toHaveLength(1)
    } finally {
      unmount()
      visibility.mockRestore()
      vi.useRealTimers()
    }
  })

  it('navigates from the root listing to a directory route', async () => {
    const user = userEvent.setup()
    renderRoute('/')

    await user.click(await screen.findByRole('link', { name: /src/ }))

    expect(await screen.findByRole('heading', { name: 'src' })).toBeInTheDocument()
    const directory = await screen.findByRole('region', { name: 'Directory contents' })
    expect(within(directory).getByRole('link', { name: /main\.tsx/ })).toHaveAttribute('href', '/blob/src/main.tsx?ref=main')
    expect(screen.queryByRole('heading', { name: 'README.md' })).not.toBeInTheDocument()
  })

  it('toggles the repository branch picker closed without reopening it', async () => {
    const user = userEvent.setup()
    renderRoute('/')

    const branch = await screen.findByRole('combobox', { name: 'Branch' })
    await user.click(branch)
    expect(await screen.findByRole('listbox')).toBeInTheDocument()
    expect(branch).toHaveAttribute('aria-expanded', 'true')

    await user.pointer({ target: branch, keys: '[MouseLeft>]' })
    await waitFor(() => expect(branch).toHaveAttribute('aria-expanded', 'false'))
    await user.pointer({ keys: '[/MouseLeft]' })
    expect(branch).toHaveAttribute('aria-expanded', 'false')
    await waitFor(() => expect(screen.queryByRole('listbox')).not.toBeInTheDocument())

    await user.click(branch)
    expect(await screen.findByRole('listbox')).toBeInTheDocument()
    expect(branch).toHaveAttribute('aria-expanded', 'true')
  })

  it('switches provider branches without losing the repository path and inherits it in New Holon', async () => {
    const refresh = deferred<Response>()
    const baseFetch = fetch as typeof globalThis.fetch
    const fetchMock = vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      const path = String(input)
      if (path.endsWith('/api/v1/repository/refs/refresh') && init?.method === 'POST') return (await refresh.promise).clone()
      if (path.includes('/tree')) {
        const url = new URL(path, 'http://test.local')
        if (url.searchParams.get('ref') === 'aaaaaa654321' && url.searchParams.get('path') === 'src') {
          return new Response(JSON.stringify({ code: 'path_not_found', message: 'Path not found.' }), { status: 404, headers: { 'Content-Type': 'application/json' } })
        }
      }
      return baseFetch(input, init)
    })
    vi.stubGlobal('fetch', fetchMock)
    const user = userEvent.setup()
    renderRoute('/tree/src?ref=main')

    expect(await screen.findByRole('heading', { name: 'src' })).toBeInTheDocument()
    const branch = screen.getByRole('combobox', { name: 'Branch' })
    await waitFor(() => expect(branch).toHaveTextContent('main'))
    await user.click(branch)
    await waitFor(() => expect(fetchMock.mock.calls.filter(([path, init]) => String(path).endsWith('/api/v1/repository/refs/refresh') && init?.method === 'POST')).toHaveLength(1))
    const listbox = await screen.findByRole('listbox')
    expect(within(listbox).getByRole('option', { name: 'feature/foo' })).toBeInTheDocument()

    await user.type(screen.getByPlaceholderText('Search branches…'), 'ftr')
    await user.keyboard('{ArrowDown}{Enter}')
    expect(await screen.findByRole('heading', { name: 'src' })).toBeInTheDocument()
    expect(screen.getByRole('combobox', { name: 'Branch' })).toHaveTextContent('feature/foo')
    const directory = await screen.findByRole('region', { name: 'Directory contents' })
    expect(within(directory).getByRole('link', { name: /main\.tsx/ })).toHaveAttribute('href', '/blob/src/main.tsx?ref=feature%2Ffoo')
    expect(screen.getByRole('heading', { name: 'src' })).toBeInTheDocument()

    await act(async () => {
      refresh.resolve(new Response(JSON.stringify({ code: 'repository_unavailable', message: 'Refresh failed.' }), { status: 503, headers: { 'Content-Type': 'application/json' } }))
      await refresh.promise
    })
    await user.click(screen.getByRole('combobox', { name: 'Branch' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Couldn’t refresh branches. Try again.')
    expect(screen.getByRole('option', { name: 'feature/foo' })).toBeInTheDocument()
    await user.type(screen.getByPlaceholderText('Search branches…'), 'nsrc')
    await user.keyboard('{ArrowDown}{Enter}')

    expect(await screen.findByText('This path does not exist on no-src.')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Go to repository root' })).toHaveAttribute('href', '/tree/?ref=no-src')
    await user.click(screen.getByRole('button', { name: 'New Agent' }))
    const dialog = screen.getByRole('dialog', { name: 'New Holon' })
    expect(within(dialog).getByRole('combobox', { name: 'Branch' })).toHaveTextContent('no-src')
  })

  it('renders a file in the shared repository layout with parent breadcrumbs', async () => {
    renderRoute('/blob/src/main.tsx?ref=main')

    expect(await screen.findByRole('heading', { name: 'main.tsx' })).toBeInTheDocument()
    expect(screen.queryByRole('complementary', { name: 'Repository tree' })).not.toBeInTheDocument()
    const crumbs = screen.getByRole('navigation', { name: 'Repository breadcrumb' })
    expect(within(crumbs).getByRole('link', { name: 'src' })).toHaveAttribute('href', '/tree/src?ref=main')
    await waitFor(() => expect(screen.getByLabelText('Source code')).toHaveAttribute('data-language', 'typescript'))
  })

  it('clears old file contents during branch navigation and ignores canceled responses', async () => {
    const user = userEvent.setup()
    const pending = deferred<Response>()
    const baseFetch = fetch
    let pendingSignal: AbortSignal | undefined
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), 'http://test.local')
      if (url.pathname.endsWith('/blob') && url.searchParams.get('ref') === 'fedcba654321') {
        pendingSignal = init?.signal ?? undefined
        return pending.promise
      }
      if (url.pathname.endsWith('/blob') && url.searchParams.get('ref') === 'aaaaaa654321') {
        return Response.json({ code: 'binary_file', message: 'Binary file.' }, { status: 415 })
      }
      return baseFetch(input, init)
    }))
    renderRoute('/blob/src/main.tsx?ref=main')
    expect(await screen.findByLabelText('Source code')).toHaveTextContent('console.log("holark")')
    await user.click(screen.getByRole('combobox', { name: 'Branch' }))
    await user.click(await screen.findByRole('option', { name: 'feature/foo' }))
    expect(await screen.findByText('Loading file…')).toBeInTheDocument()
    expect(screen.queryByLabelText('Source code')).not.toBeInTheDocument()
    await user.click(screen.getByRole('combobox', { name: 'Branch' }))
    await user.click(await screen.findByRole('option', { name: 'no-src' }))
    expect(await screen.findByText('Binary files cannot be displayed.')).toBeInTheDocument()
    expect(pendingSignal?.aborted).toBe(true)
    await act(async () => {
      pending.resolve(Response.json({ ref: 'feature/foo', commit: 'fedcba654321', path: 'src/main.tsx', content: 'obsolete response', language: 'typescript', size: 17 }))
    })
    expect(screen.queryByText('obsolete response')).not.toBeInTheDocument()
    expect(screen.getByText('Binary files cannot be displayed.')).toBeInTheDocument()
  })

  it('reads README from the directory snapshot and does not poll immutable contents', async () => {
    const baseFetch = fetch
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), 'http://test.local')
      if (url.pathname.endsWith('/tree')) {
        return Response.json({ ref: 'main', commit: '111111111111', path: '', entries: [{ name: 'README.md', path: 'README.md', type: 'file', mode: '100644' }] })
      }
      return baseFetch(input, init)
    })
    vi.stubGlobal('fetch', fetchMock)
    const view = renderRoute('/')
    expect(await screen.findByRole('heading', { name: 'Holark' })).toBeInTheDocument()
    const requests = () => fetchMock.mock.calls.map(([input]) => new URL(String(input), 'http://test.local'))
    expect(requests().find((url) => url.pathname.endsWith('/tree'))?.searchParams.get('ref')).toBe('abcdef123456')
    expect(requests().find((url) => url.pathname.endsWith('/blob'))?.searchParams.get('ref')).toBe('111111111111')
    expect(requests().filter((url) => url.pathname.endsWith('/refs'))).toHaveLength(1)
    vi.useFakeTimers()
    try {
      await act(async () => { await vi.advanceTimersByTimeAsync(60_000) })
      expect(requests().filter((url) => url.pathname.endsWith('/tree'))).toHaveLength(1)
      expect(requests().filter((url) => url.pathname.endsWith('/blob'))).toHaveLength(1)
    } finally {
      view.unmount()
      vi.useRealTimers()
    }
  })

  it('opens filenames containing literal percent escapes without decoding them twice', async () => {
    const baseFetch = fetch
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), 'http://test.local')
      if (url.pathname.endsWith('/blob')) {
        expect(url.searchParams.get('path')).toBe('src/100%25.ts')
        return Response.json({ commit: 'abcdef123456', ref: 'main', path: 'src/100%25.ts', language: 'typescript', content: 'percent filename', size: 16 })
      }
      return baseFetch(input, init)
    })
    vi.stubGlobal('fetch', fetchMock)
    renderRoute('/blob/src/100%2525.ts?ref=main')
    expect(await screen.findByLabelText('Source code')).toHaveTextContent('percent filename')
    expect(screen.getByRole('heading', { name: '100%25.ts' })).toBeInTheDocument()
  })

  it('marks the pull request navigation item active on pull request routes', async () => {
    const user = userEvent.setup()
    const root = renderRoute('/')

    expect(await screen.findByRole('link', { name: 'Repository' })).toHaveAttribute('aria-current', 'page')

    await user.click(screen.getByRole('link', { name: 'Pull requests' }))
    expect(await screen.findByRole('heading', { name: 'Pull requests' })).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Pull requests' })).toHaveAttribute('aria-current', 'page')
    expect(screen.getByRole('link', { name: 'Repository' })).not.toHaveAttribute('aria-current')

    root.unmount()
    renderRoute('/pulls/pr-1')
    expect(await screen.findByRole('link', { name: 'Pull requests' })).toHaveAttribute('aria-current', 'page')
    expect(screen.getByRole('link', { name: 'Repository' })).not.toHaveAttribute('aria-current')
  })

  it.each(['/pulls', '/my-work', '/issues'])('opens repository search from %s after submitting the shared header', async (route) => {
    const user = userEvent.setup()
    const baseFetch = fetch
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), 'http://test.local')
      if (url.pathname === '/api/v1/my-work') {
        return Response.json({ rows: [], total: 0, page: 1, per_page: 20, identity: null, sync: [] })
      }
      if (url.pathname === '/api/v1/repository/search') {
        return Response.json({ ref: 'main', matches: [{ path: 'src/main.tsx', name_match: false, content_match: true }], truncated: false })
      }
      return baseFetch(input, init)
    }))
    renderRoute(route)
    const search = await screen.findByRole('searchbox', { name: 'Search files and content' })
    fireEvent.keyDown(window, { key: 'F', code: 'KeyF', shiftKey: true, altKey: true })
    expect(search).toHaveFocus()
    await user.type(search, 'console.log')
    expect(screen.queryByRole('region', { name: 'Repository search results' })).not.toBeInTheDocument()
    await user.keyboard('{Enter}')
    const results = await screen.findByRole('region', { name: 'Repository search results' })
    expect(await within(results).findByRole('link', { name: /src\/main.tsx/ })).toHaveAttribute('href', '/blob/src/main.tsx?ref=main')
    expect(screen.getByRole('searchbox', { name: 'Search files and content' })).toHaveValue('console.log')
    await user.click(screen.getByRole('searchbox', { name: 'Search files and content' }))
    await user.keyboard('{Escape}')
    expect(screen.queryByRole('region', { name: 'Repository search results' })).not.toBeInTheDocument()
  })

  it('searches repository files and focuses search with its shortcut', async () => {
    const user = userEvent.setup()
    const baseFetch = fetch
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), 'http://test.local')
      if (url.pathname === '/api/v1/repository/search') {
        return Response.json({ ref: url.searchParams.get('ref'), matches: [{ path: 'src/main.tsx', name_match: false, content_match: true }], truncated: false })
      }
      return baseFetch(input, init)
    }))
    renderRoute('/?view=branches&ref=refs%2Fheads%2Ffeature%2Ffoo')
    const search = await screen.findByRole('searchbox', { name: 'Search files and content' })
    expect(search).toHaveAttribute('aria-keyshortcuts', 'Shift+Alt+F')
    fireEvent.keyDown(window, { key: 'Ï', code: 'KeyF', shiftKey: true, altKey: true })
    expect(search).toHaveFocus()
    await user.type(search, 'console.log')
    const results = await screen.findByRole('region', { name: 'Repository search results' })
    const file = await within(results).findByRole('link', { name: /src\/main.tsx/ })
    expect(file).toHaveAttribute('href', '/blob/src/main.tsx?ref=refs%2Fheads%2Ffeature%2Ffoo')
    expect(within(results).getByText('Content')).toBeInTheDocument()
    await user.click(search)
    await user.keyboard('{Escape}')
    expect(screen.queryByRole('region', { name: 'Repository search results' })).not.toBeInTheDocument()
    expect(await screen.findByRole('region', { name: 'Directory contents' })).toBeInTheDocument()
    expect(search).toHaveValue('')
  })

  it('browses commit snapshots and switches to an agent branch', async () => {
    const baseFetch = fetch
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), 'http://test.local')
      if (url.pathname === '/api/v1/repository/commits' && !url.searchParams.get('ref')?.includes('holon-1')) {
        return Response.json({ commits: [
          { sha: 'abcdef123456', message: 'Latest change', author_name: 'Holark Test', authored_at: '2026-07-23T10:00:00Z' },
          { sha: 'older1234567', message: 'Earlier change\n\n**Summary**\n\n- Review files\n- Update docs', author_name: 'Holark Test', authored_at: '2026-07-22T10:00:00Z' },
        ] })
      }
      return baseFetch(input, init)
    }))
    const user = userEvent.setup()
    renderRoute('/')
    expect(await screen.findByRole('region', { name: 'Repository files' })).toBeInTheDocument()
    expect(screen.queryByRole('region', { name: 'Repository commits' })).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Changes' }))
    expect(await screen.findByRole('region', { name: 'Repository commits' })).toBeInTheDocument()
    await user.click(await screen.findByRole('button', { name: /Earlier change/ }))
    expect(screen.getByRole('button', { name: /Earlier change/ })).toHaveAttribute('aria-pressed', 'true')
    const details = screen.getByRole('region', { name: 'Commit details' })
    expect(await within(details).findByText('Summary', { selector: 'strong' })).toBeInTheDocument()
    expect(within(details).getByText('Review files', { selector: 'li' })).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Files' }))
    expect(screen.queryByRole('region', { name: 'Repository commits' })).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Back to latest' })).toBeInTheDocument()
    await user.click(await screen.findByRole('link', { name: /src/ }))
    const directory = await screen.findByRole('region', { name: 'Directory contents' })
    expect(within(directory).getByRole('link', { name: /main\.tsx/ })).toHaveAttribute('href', '/blob/src/main.tsx?ref=main&commit=older1234567')
    await user.click(within(directory).getByRole('link', { name: /main\.tsx/ }))
    await waitFor(() => expect(screen.getByLabelText('Source code')).toHaveAttribute('data-language', 'typescript'))
    const crumbs = screen.getByRole('navigation', { name: 'Repository breadcrumb' })
    expect(within(crumbs).getByRole('link', { name: 'src' })).toHaveAttribute('href', '/tree/src?ref=main&commit=older1234567')
    expect(vi.mocked(fetch).mock.calls.some(([input]) => {
      const url = new URL(String(input), 'http://test.local')
      return url.pathname === '/api/v1/repository/blob' && url.searchParams.get('ref') === 'older1234567'
    })).toBe(true)
    await user.click(screen.getByRole('button', { name: 'Back to latest' }))
    expect(await screen.findByText('Latest')).toBeInTheDocument()
    expect(await screen.findByRole('link', { name: /src/ })).toHaveAttribute('href', '/tree/src?ref=main')

    await user.click(screen.getByRole('combobox', { name: 'Branch' }))
    await user.click(await screen.findByRole('option', { name: 'holon-1' }))
    await user.click(screen.getByRole('button', { name: 'Changes' }))
    expect(await screen.findByRole('button', { name: /Autopush commit/ })).toHaveAttribute('aria-pressed', 'true')
    await user.click(screen.getByRole('button', { name: 'Files' }))
    expect(await screen.findByRole('link', { name: /src/ })).toHaveAttribute('href', '/tree/src?ref=refs%2Fheads%2Fholark%2Fholon-1')
  })

  it('loads detached HEAD history before and after refs arrive without a matching branch', async () => {
    const baseFetch = fetch
    const refs = deferred<void>()
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input)
      if (path.endsWith('/api/v1/repository')) {
        const response = await (await baseFetch(input, init)).json()
        return Response.json({ ...response, default_branch: 'HEAD' })
      }
      if (path.endsWith('/api/v1/repository/refs')) {
        await refs.promise
        return Response.json({ repository_id: 'holark', default_ref: 'HEAD', refs: [] })
      }
      if (path.includes('/api/v1/repository/commits')) {
        const url = new URL(path, 'http://test.local')
        if (url.searchParams.get('ref') !== 'HEAD') {
          return Response.json({ code: 'ref_not_found', message: 'Reference not found' }, { status: 404 })
        }
      }
      return baseFetch(input, init)
    }))
    renderRoute('/?view=changes')

    expect(await screen.findByRole('button', { name: /Initial commit/ })).toBeInTheDocument()
    await act(async () => { refs.resolve() })
    expect(screen.getByRole('combobox', { name: 'Branch' })).toHaveTextContent('HEAD')
    expect(screen.getByRole('button', { name: /Initial commit/ })).toBeInTheDocument()
    const requests = vi.mocked(fetch).mock.calls.filter(([input]) => String(input).includes('/api/v1/repository/commits'))
    expect(requests.length).toBeGreaterThan(0)
    for (const [input] of requests) {
      expect(new URL(String(input), 'http://test.local').searchParams.get('ref')).toBe('HEAD')
    }
  })

  it.each(['refs/heads/', 'refs/holark/browse/origin/'])('retains paginated history when %s metadata loads later and a return refresh fails', async (prefix) => {
    const user = userEvent.setup()
    const baseFetch = fetch
    const refs = deferred<void>()
    let failCommits = false
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input)
      if (path.endsWith('/api/v1/repository/refs')) {
        await refs.promise
        const response = await (await baseFetch(input, init)).json()
        return Response.json({ ...response, default_ref: `${prefix}main`, refs: response.refs.map((ref: { name: string; kind: string }) =>
          ref.kind === 'branch' ? { ...ref, name: ref.name.replace('refs/heads/', prefix) } : ref,
        ) })
      }
      if (path.includes('/api/v1/repository/commits')) {
        if (failCommits) return Response.json({ message: 'Commits unavailable' }, { status: 503 })
        const url = new URL(path, 'http://test.local')
        if (url.searchParams.has('cursor')) return Response.json({ commits: [
          { sha: 'older1234567', message: 'Older page commit', author_name: 'Holark Test', authored_at: '2026-07-22T10:00:00Z' },
        ] })
        const response = await (await baseFetch(input, init)).json()
        return Response.json({ ...response, next_cursor: 'older-page' })
      }
      return baseFetch(input, init)
    }))
    renderRoute('/?view=changes')

    expect(await screen.findByRole('button', { name: /Initial commit/ })).toBeInTheDocument()
    const history = screen.getByLabelText('Commit history')
    Object.defineProperties(history, {
      clientHeight: { configurable: true, value: 400 },
      scrollHeight: { configurable: true, value: 1000 },
    })
    history.scrollTop = 500
    fireEvent.scroll(history)
    expect(await screen.findByRole('button', { name: 'Older page commit' })).toBeInTheDocument()
    failCommits = true
    await act(async () => {
      refs.resolve()
    })
    expect(screen.getByRole('combobox', { name: 'Branch' })).toHaveTextContent('main')
    expect(screen.getByRole('button', { name: /Initial commit/ })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Older page commit' })).toBeInTheDocument()
    expect(screen.getByLabelText('Commit history')).toBe(history)
    expect(history.scrollTop).toBe(500)
    expect(vi.mocked(fetch).mock.calls.filter(([input]) => {
      const url = new URL(String(input), 'http://test.local')
      return url.pathname === '/api/v1/repository/commits' && url.searchParams.get('ref') === 'main'
    })).toHaveLength(2)

    await user.click(screen.getByRole('link', { name: 'Issues' }))
    expect(await screen.findByRole('heading', { name: 'Issues' })).toBeInTheDocument()
    await user.click(screen.getByRole('link', { name: 'Repository' }))
    await user.click(screen.getByRole('button', { name: 'Changes' }))

    expect(await screen.findByText('Could not refresh commits. Showing the last loaded list.')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /Initial commit/ })).toBeInTheDocument()
  })

  it('renders issue labels across list and detail routes while keeping issue navigation active', async () => {
    const user = userEvent.setup()
    renderRoute('/')

    await user.click(await screen.findByRole('link', { name: 'Issues' }))
    expect(await screen.findByRole('heading', { name: 'Issues' })).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Issues' })).toHaveAttribute('aria-current', 'page')
    expect(screen.getByRole('link', { name: 'Repository' })).not.toHaveAttribute('aria-current')

    const labeledCardLink = await screen.findByRole('link', { name: /Fix issue navigation/ })
    const labeledCard = labeledCardLink.closest('article')
    expect(labeledCard).not.toBeNull()
    const cardLabels = within(labeledCard as HTMLElement).getByRole('list', { name: 'Labels' })
    expect(within(cardLabels).getAllByRole('listitem')).toHaveLength(4)
    expect(within(cardLabels).getByText('bug')).toHaveStyle({ backgroundColor: '#000000', color: '#ffffff' })
    const longCardLabel = within(cardLabels).getByText(longIssueLabelName)
    expect(longCardLabel).toHaveStyle({ backgroundColor: '#ffffff', color: '#000000' })
    expect(longCardLabel.closest('li')).toHaveAttribute('title', longIssueLabelName)
    expect(within(cardLabels).getByText('high priority')).toBeInTheDocument()
    expect(within(cardLabels).getByText('+1')).toBeInTheDocument()
    expect(within(cardLabels).queryByText('help wanted')).not.toBeInTheDocument()

    const unlabeledCard = screen.getByRole('link', { name: /Unlabeled issue/ }).closest('article')
    expect(unlabeledCard).not.toBeNull()
    expect(within(unlabeledCard as HTMLElement).queryByRole('list', { name: 'Labels' })).not.toBeInTheDocument()

    await user.click(labeledCardLink)
    expect(await screen.findByRole('heading', { name: 'Fix issue navigation' })).toBeInTheDocument()
    expect(await screen.findByRole('link', { name: 'Issues' })).toHaveAttribute('aria-current', 'page')
    expect(screen.getByRole('link', { name: 'Repository' })).not.toHaveAttribute('aria-current')
    const detailLabels = screen.getByRole('list', { name: 'Labels' })
    expect(within(detailLabels).getAllByRole('listitem')).toHaveLength(4)
    expect(within(detailLabels).getByText('bug')).toBeInTheDocument()
    expect(within(detailLabels).getByText(longIssueLabelName)).toBeInTheDocument()
    expect(within(detailLabels).getByText('high priority')).toBeInTheDocument()
    expect(within(detailLabels).getByText('help wanted')).toBeInTheDocument()
    expect(within(detailLabels).getByText('Something is broken')).toBeInTheDocument()
    expect(within(detailLabels).getByText('Needs prompt attention')).toBeInTheDocument()
    expect(within(detailLabels).getByText('Extra attention is needed')).toBeInTheDocument()
    expect(within(detailLabels).queryByText(/^\+/)).not.toBeInTheDocument()
  })

  it('opens the profile in local mode and switches between attention state and user stats', async () => {
    const user = userEvent.setup()
    renderRoute('/')

    await user.click(await screen.findByRole('link', { name: 'Open profile' }, { timeout: 10_000 }))

    expect(await screen.findByRole('heading', { name: 'Profile' }, { timeout: 10_000 })).toBeInTheDocument()
    expect(within(screen.getByRole('navigation', { name: 'Primary navigation' })).getByRole('link', { name: 'Profile' })).toHaveAttribute('aria-current', 'page')
    expect(screen.getByRole('heading', { name: 'Attention state' })).toBeInTheDocument()
    expect(screen.getByText('Coming soon')).toBeInTheDocument()

    const sections = within(screen.getByRole('navigation', { name: 'Profile sections' }))
    await user.click(sections.getByRole('link', { name: 'User stats' }))

    expect(screen.getByRole('heading', { name: 'User stats' })).toBeInTheDocument()
    expect(sections.getByRole('link', { name: 'User stats' })).toHaveAttribute('aria-current', 'page')
    expect(screen.getByText('Coming soon')).toBeInTheDocument()

    await user.click(sections.getByRole('link', { name: 'Attention state' }))
    expect(screen.getByRole('heading', { name: 'Attention state' })).toBeInTheDocument()
  }, 20_000)

  it('shows and refreshes the connected GitHub name and avatar', async () => {
    const user = userEvent.setup()
    const baseFetch = fetch as typeof globalThis.fetch
    let profile = { login: 'octocat', name: 'Mona Lisa', avatar_url: 'https://avatars.githubusercontent.com/u/1?v=1', profile_url: 'https://github.com/octocat' }
    vi.stubGlobal('fetch', vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      if (String(input).endsWith('/api/v1/github-profile')) return Response.json(profile)
      return baseFetch(input, init)
    }))
    renderRoute('/profile')

    expect(await screen.findByRole('heading', { name: 'Mona Lisa' }, { timeout: 10_000 })).toBeInTheDocument()
    expect(screen.getByRole('img', { name: 'GitHub avatar for Mona Lisa' })).toHaveAttribute('src', profile.avatar_url)
    expect(screen.getByRole('link', { name: '@octocat' })).toHaveAttribute('href', profile.profile_url)
    expect(screen.getByRole('link', { name: 'Open profile' }).querySelector('img')).toHaveAttribute('src', profile.avatar_url)

    profile = { ...profile, name: '', avatar_url: 'https://avatars.githubusercontent.com/u/1?v=2' }
    await user.click(screen.getByRole('button', { name: 'Sync GitHub profile' }))

    expect(await screen.findByRole('heading', { name: '@octocat' })).toBeInTheDocument()
    expect(screen.getByRole('img', { name: 'GitHub avatar for @octocat' })).toHaveAttribute('src', profile.avatar_url)
  }, 20_000)

  it('saves and resets prompt templates from settings', async () => {
    const user = userEvent.setup()
    const baseFetch = fetch as typeof globalThis.fetch
    const updates: string[] = []
    vi.stubGlobal('fetch', vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      const path = String(input)
      if (path.endsWith('/prompt-templates/issue_agent_plan') && init?.method === 'PUT') {
        const body = JSON.parse(String(init.body)) as { value: string }
        updates.push(body.value)
        const defaultValue = 'Default issue {{issue_title}}'
        return new Response(JSON.stringify({
          key: 'issue_agent_plan', name: 'Issue agent plan prompt', use: 'Issue holons', variables: [{ name: 'issue_title', description: 'Issue title.' }],
          value: body.value, default_value: defaultValue, overridden: body.value !== defaultValue,
        }), { status: 200, headers: { 'Content-Type': 'application/json' } })
      }
      return baseFetch(input, init)
    }))

    renderRoute('/settings')

    expect(await screen.findByRole('heading', { name: 'Settings' })).toBeInTheDocument()
    const editor = await screen.findByDisplayValue('Issue {{issue_title}}')
    await user.clear(editor)
    fireEvent.change(editor, { target: { value: 'Custom {{issue_title}}' } })
    await user.click(screen.getAllByRole('button', { name: 'Save' })[0])
    await waitFor(() => expect(updates).toContain('Custom {{issue_title}}'))

    await user.click(screen.getAllByRole('button', { name: 'Reset to default' })[0])
    await waitFor(() => expect(updates).toContain('Default issue {{issue_title}}'))
    expect(screen.getByRole('link', { name: 'Settings' })).toHaveAttribute('aria-current', 'page')
  })

  it('offers accessible keyboard-operated appearance controls and applies them immediately', async () => {
    const user = userEvent.setup()
    renderRoute('/settings')

    expect(await screen.findByRole('heading', { name: 'Appearance' })).toBeInTheDocument()
    const dark = screen.getByRole('radio', { name: /^Dark/ })
    const light = screen.getByRole('radio', { name: /^Light/ })

    expect(light).toBeChecked()
    expect(dark).not.toBeChecked()
    expect(screen.getByText(/saved in this browser/i)).toBeInTheDocument()
    expect(document.documentElement).toHaveAttribute('data-theme', 'light')

    dark.focus()
    await user.keyboard(' ')

    expect(dark).toBeChecked()
    expect(light).not.toBeChecked()
    expect(document.documentElement).toHaveAttribute('data-theme', 'dark')
    expect(document.documentElement.style.colorScheme).toBe('dark')
    expect(document.querySelector('meta[name="theme-color"]')).toHaveAttribute('content', '#181917')
    expect(window.localStorage.getItem(colorThemeStorageKey)).toBe('dark')
  })

  it('switches file highlighting between light and dark without remounting the route', async () => {
    window.localStorage.setItem(colorThemeStorageKey, 'light')
    renderRoute('/blob/src/main.tsx?ref=main')

    const editor = await screen.findByLabelText('Source code')
    expect(editor).toHaveAttribute('data-theme', 'holark-light')

    act(() => {
      window.dispatchEvent(new StorageEvent('storage', {
        key: colorThemeStorageKey,
        newValue: 'dark',
        storageArea: window.localStorage,
      }))
    })

    await waitFor(() => expect(editor).toHaveAttribute('data-theme', 'vs-dark'))
    expect(document.documentElement).toHaveAttribute('data-theme', 'dark')
  })

  it('uses spatial intent beside the header and expands immediately elsewhere', async () => {
    renderRoute('/')
    const panel = await screen.findByTestId('operations-panel')

    expect(panel).toHaveAttribute('data-expanded', 'false')
    fireEvent.mouseEnter(panel, { clientX: 0, clientY: 20 })
    expect(panel).toHaveAttribute('data-expanded', 'false')
    fireEvent.mouseMove(panel, { clientX: 23, clientY: 20 })
    expect(panel).toHaveAttribute('data-expanded', 'false')
    fireEvent.mouseMove(panel, { clientX: 24, clientY: 20 })
    expect(panel).toHaveAttribute('data-expanded', 'true')
    fireEvent.mouseLeave(panel)
    expect(panel).toHaveAttribute('data-expanded', 'false')
    fireEvent.mouseEnter(panel, { clientX: 0, clientY: 41 })
    expect(panel).toHaveAttribute('data-expanded', 'true')


  })

  it.each([
    { clientX: 1000, clientY: 11 },
    { clientX: 1000, clientY: 789 },
    { clientX: 699, clientY: 400 },
  ])('keeps the Holons panel open to its right until the pointer leaves its vertical bounds or moves left (%j)', async (outside) => {
    renderRoute('/')
    const panel = await screen.findByTestId('operations-panel')
    vi.spyOn(panel, 'getBoundingClientRect').mockReturnValue({
      x: 700, y: 12, left: 700, right: 988, top: 12, bottom: 788,
      width: 288, height: 776, toJSON: () => ({}),
    })

    fireEvent.mouseEnter(panel, { clientX: 800, clientY: 400 })
    expect(panel).toHaveAttribute('data-expanded', 'true')
    fireEvent.mouseLeave(panel, { clientX: 990, clientY: 400 })
    fireEvent.pointerMove(document.body, { clientX: 1000, clientY: 400 })
    expect(panel).toHaveAttribute('data-expanded', 'true')

    fireEvent.pointerMove(document.body, outside)
    expect(panel).toHaveAttribute('data-expanded', 'false')
  })

  it('opens the new-agent dialog from the holons panel', async () => {
    const user = userEvent.setup()
    renderRoute('/blob/src/main.tsx?ref=main')

    await user.click(await screen.findByRole('button', { name: 'Create new Holon from holons panel' }))

    expect(screen.getByRole('dialog', { name: 'New Holon' })).toBeInTheDocument()
    expect(screen.getByLabelText('Task')).toHaveFocus()
  })

  it('shows completed turns independently of running processes', async () => {
    const now = new Date().toISOString()
    const mixedHarnesses = [1, 2, 3, 4].map((index) => ({
      id: `harness-mixed-${index}`,
      holon_id: 'holon-mixed',
      agent_type: 'codex',
      title: index === 1 ? 'Mixed activity' : `Agent ${index}`,
      status: 'running',
      input_state: index === 4 ? 'none' : 'task_complete',
      activity: index === 4 ? 'working' : 'completed',
      created_at: now,
      updated_at: now,
    }))
    const baseFetch = fetch as typeof globalThis.fetch
    vi.stubGlobal('fetch', vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      const path = String(input)
      if (path.endsWith('/holons')) {
        return new Response(JSON.stringify([
          {
            id: 'holon-running',
            repository_id: 'holark',
            runtime_id: 'node-1',
            prompt: 'Running task',
            status: 'running',
            input_state: 'none', activity: 'working',
            created_at: new Date().toISOString(),
          },
          {
            id: 'holon-idle',
            repository_id: 'holark',
            runtime_id: 'node-1',
            prompt: 'Idle task',
            status: 'running',
            input_state: 'task_complete', activity: 'completed',
            created_at: new Date().toISOString(),
          },
          {
            id: 'holon-permission',
            repository_id: 'holark',
            runtime_id: 'node-1',
            prompt: 'Permission task',
            status: 'running',
            input_state: 'permission_required', activity: 'needs_input',
            created_at: new Date().toISOString(),
          },
          {
            id: 'holon-mixed',
            repository_id: 'holark',
            runtime_id: 'node-1',
            prompt: 'Mixed activity',
            agent_session: mixedHarnesses[0],
            agent_sessions: mixedHarnesses,
            status: 'running',
            input_state: 'none', activity: 'working',
            created_at: now,
          },
        ]), { status: 200, headers: { 'Content-Type': 'application/json' } })
      }
      return baseFetch(input, init)
    }))

    renderRoute('/')

    const agents = await screen.findByRole('region', { name: 'Holons' })
    const runningAgent = await within(agents).findByRole('link', { name: /Running task/ })
    const idleAgent = await within(agents).findByRole('link', { name: /Idle task/ })
    const permissionAgent = await within(agents).findByRole('link', { name: /Permission task/ })
    const mixedAgent = await within(agents).findByRole('link', { name: /Mixed activity/ })
    expect(runningAgent).toHaveTextContent('Running')
    expect(runningAgent).not.toHaveTextContent('Idle')
    expect(idleAgent).toHaveTextContent('Completed')
    expect(permissionAgent).toHaveTextContent('Permission required')
    expect(permissionAgent).not.toHaveTextContent('Idle')
    expect(mixedAgent).toHaveTextContent('Working')
    expect(mixedAgent).not.toHaveTextContent('Idle')
  })

  it('surfaces aggregate input attention from a secondary agent', async () => {
    const now = new Date().toISOString()
    const harnesses = [
      { id: 'harness-1', holon_id: 'holon-multi', agent_type: 'codex', title: 'Coordinate agents', status: 'running', input_state: 'none', activity: 'working', created_at: now, updated_at: now },
      { id: 'harness-2', holon_id: 'holon-multi', agent_type: 'codex', title: 'Agent 2', status: 'running', input_state: 'permission_required', activity: 'needs_input', created_at: now, updated_at: now },
    ]
    const baseFetch = fetch as typeof globalThis.fetch
    vi.stubGlobal('fetch', vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      if (String(input).endsWith('/holons')) {
        return new Response(JSON.stringify([{
          id: 'holon-multi',
          repository_id: 'holark',
          runtime_id: 'node-1',
          prompt: 'Coordinate agents',
          agent_session: harnesses[0],
          agent_sessions: harnesses,
          status: 'running',
          input_state: 'permission_required', activity: 'needs_input',
          created_at: now,
        }]), { status: 200, headers: { 'Content-Type': 'application/json' } })
      }
      return baseFetch(input, init)
    }))

    renderRoute('/')

    const agents = await screen.findByRole('region', { name: 'Holons' })
    const multiAgent = await within(agents).findByRole('link', { name: /Coordinate agents/ })
    expect(multiAgent).toHaveTextContent('Permission required')
    expect(multiAgent).not.toHaveTextContent('Running')
  })

  it('places the new-agent tile above existing agent tiles', async () => {
    const baseFetch = fetch as typeof globalThis.fetch
    vi.stubGlobal('fetch', vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      const path = String(input)
      if (path.endsWith('/holons')) {
        return new Response(JSON.stringify([{
          id: 'holon-1',
          repository_id: 'holark',
          runtime_id: 'node-1',
          prompt: 'Refactor panel controls',
          status: 'running',
          input_state: 'none', activity: 'working',
          created_at: new Date().toISOString(),
        }]), { status: 200, headers: { 'Content-Type': 'application/json' } })
      }
      return baseFetch(input, init)
    }))

    renderRoute('/')

    const agentTile = await screen.findByRole('link', { name: /Refactor panel controls/ })
    const newAgentTile = screen.getByRole('button', { name: 'Create new Holon from holons panel' })

    expect(newAgentTile.compareDocumentPosition(agentTile) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })

  it('orders active holons FIFO and ended holons newest-ended first', async () => {
    const user = userEvent.setup()
    const baseFetch = fetch as typeof globalThis.fetch
    vi.stubGlobal('fetch', vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      const path = String(input)
      if (path.endsWith('/holons')) {
        return new Response(JSON.stringify([
          { id: 'holon-old', repository_id: 'holark', runtime_id: 'node-1', prompt: 'Older running task', status: 'running', input_state: 'none', activity: 'working', created_at: '2026-01-01T10:00:00.000Z' },
          { id: 'holon-cancelled', repository_id: 'holark', runtime_id: 'node-1', prompt: 'Cancelled task', status: 'cancelled', input_state: 'none', activity: 'working', created_at: '2026-01-04T10:00:00.000Z', finished_at: '2026-01-07T10:00:00.000Z' },
          { id: 'holon-new', repository_id: 'holark', runtime_id: 'node-1', prompt: 'Newest running task', status: 'running', input_state: 'none', activity: 'working', created_at: '2026-01-03T10:00:00.000Z' },
          { id: 'holon-failed', repository_id: 'holark', runtime_id: 'node-1', prompt: 'Failed task', status: 'failed', input_state: 'none', activity: 'working', created_at: '2026-01-05T10:00:00.000Z', finished_at: '2026-01-10T10:00:00.000Z' },
          { id: 'holon-lost', repository_id: 'holark', runtime_id: 'node-1', prompt: 'Lost task', status: 'lost', input_state: 'none', activity: 'working', created_at: '2026-01-02T10:00:00.000Z', finished_at: '2026-01-08T10:00:00.000Z' },
          { id: 'holon-expired', repository_id: 'holark', runtime_id: 'node-1', prompt: 'Expired task', status: 'expired', input_state: 'none', activity: 'working', created_at: '2026-01-06T10:00:00.000Z' },
        ]), { status: 200, headers: { 'Content-Type': 'application/json' } })
      }
      return baseFetch(input, init)
    }))

    renderRoute('/')

    const agents = await screen.findByRole('region', { name: 'Holons' })
    await within(agents).findByRole('link', { name: /Newest running task/ })
    expect(within(agents).queryByRole('checkbox')).not.toBeInTheDocument()
    expect(within(agents).queryByRole('link', { name: /Cancelled task/ })).not.toBeInTheDocument()
    expect(within(agents).queryByRole('link', { name: /Failed task/ })).not.toBeInTheDocument()
    expect(within(agents).queryByRole('link', { name: /Lost task/ })).not.toBeInTheDocument()
    expect(within(agents).queryByRole('link', { name: /Expired task/ })).not.toBeInTheDocument()

    const newAgentTile = within(agents).getByRole('button', { name: 'Create new Holon from holons panel' })
    const sessionLinks = () => within(agents).getAllByRole('link').filter((link) => link.closest('article'))
    let links = sessionLinks()
    expect(links.map((link) => link.textContent)).toEqual([
      expect.stringContaining('Older running task'),
      expect.stringContaining('Newest running task'),
    ])
    expect(newAgentTile.compareDocumentPosition(links[0]) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()

    const showAll = within(agents).getByRole('button', { name: /Show ended holons/ })
    expect(showAll).toHaveTextContent('Show ended holons (4)')
    await user.click(showAll)

    await waitFor(() => expect(sessionLinks()).toHaveLength(6))
    const hideEnded = within(agents).getByRole('button', { name: /Hide ended holons/ })
    links = sessionLinks()
    expect(links.map((link) => link.textContent)).toEqual([
      expect.stringContaining('Older running task'),
      expect.stringContaining('Newest running task'),
      expect.stringContaining('Failed task'),
      expect.stringContaining('Lost task'),
      expect.stringContaining('Cancelled task'),
      expect.stringContaining('Expired task'),
    ])
    const agentText = agents.textContent ?? ''
    expect(agentText.indexOf('Older running task')).toBeLessThan(agentText.indexOf('Hide ended holons'))
    expect(agentText.indexOf('Hide ended holons')).toBeLessThan(agentText.indexOf('Failed task'))

    await user.click(hideEnded)

    await waitFor(() => expect(sessionLinks()).toHaveLength(2))
    expect(within(agents).queryByRole('link', { name: /Failed task/ })).not.toBeInTheDocument()
    expect(within(agents).getByRole('button', { name: /Show ended holons/ })).toBeInTheDocument()
  })

  it('reopens archived holons from the operations panel', async () => {
    vi.stubGlobal('WebSocket', class { static OPEN = 1; readyState = 0; send() {}; close() {} })
    const user = userEvent.setup()
    const baseFetch = fetch as typeof globalThis.fetch
    const fetchMock = vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      const path = String(input)
      if (path.endsWith('/agent-capabilities')) {
        return new Response(JSON.stringify({ capabilities: [{ type: 'codex', available: true }, { type: 'opencode', available: true }] }), { status: 200, headers: { 'Content-Type': 'application/json' } })
      }
      if (path.endsWith('/holons')) {
        return new Response(JSON.stringify([
          {
            id: 'holon-cancelled',
            repository_id: 'holark',
            runtime_id: '',
            prompt: 'Cancelled task',
            worktree_branch: 'holark/holon-cancelled',
            worktree_path: '/tmp/holon-cancelled',
            agent_session: { id: 'harness-holon-1', agent_type: 'codex', input_state: 'none', activity: 'working', created_at: new Date().toISOString(), updated_at: new Date().toISOString() },
            agent_sessions: [
              { id: 'harness-holon-1', agent_type: 'codex', input_state: 'none', activity: 'working', created_at: new Date().toISOString(), updated_at: new Date().toISOString() },
              { id: 'harness-holon-open', agent_type: 'opencode', input_state: 'none', activity: 'working', created_at: new Date().toISOString(), updated_at: new Date().toISOString() },
            ],
            status: 'cancelled',
            archived_at: '2026-01-02T11:00:00.000Z',
            input_state: 'none', activity: 'working',
            created_at: '2026-01-02T10:00:00.000Z',
          },
          {
            id: 'holon-expired-resumable',
            repository_id: 'holark',
            runtime_id: '',
            prompt: 'Expired task',
            worktree_branch: 'holark/holon-expired-resumable',
            worktree_path: '/tmp/holon-expired-resumable',
            agent_session: { id: 'harness-holon-2', agent_type: 'codex', resume_target: 'codex-holon-2', input_state: 'none', activity: 'working', created_at: new Date().toISOString(), updated_at: new Date().toISOString() },
            status: 'expired',
            archived_at: '2026-01-01T11:00:00.000Z',
            input_state: 'none', activity: 'working',
            created_at: '2026-01-01T10:00:00.000Z',
          },
        ]), { status: 200, headers: { 'Content-Type': 'application/json' } })
      }
      if (path.endsWith('/holons/holon-cancelled/reopen') || path.endsWith('/holons/holon-cancelled')) {
        if (path.endsWith('/reopen')) {
          expect(init?.method).toBe('POST')
          expect(init?.body).toBeUndefined()
        }
        return new Response(JSON.stringify({
          id: 'holon-cancelled',
          repository_id: 'holark',
          runtime_id: 'node-2',
          prompt: 'Cancelled task',
          worktree_branch: 'holark/holon-cancelled',
          worktree_path: '/tmp/holon-cancelled',
          agent_session: { id: 'harness-holon-1', agent_type: 'codex', input_state: 'none', activity: 'working', created_at: new Date().toISOString(), updated_at: new Date().toISOString() },
          status: 'queued',
          archived_at: undefined,
          input_state: 'none', activity: 'working',
          created_at: '2026-01-02T10:00:00.000Z',
        }), { status: 200, headers: { 'Content-Type': 'application/json' } })
      }
      if (new URL(path, 'http://test.local').pathname.endsWith('/holons/holon-cancelled/workspace')) {
        return new Response(JSON.stringify({ branch: 'holark/holon-cancelled', base_branch: 'main', base_commit: 'abc123', head_commit: 'abc123', has_changes: false, dirty: false, files: [], diff_truncated: false }), { status: 200, headers: { 'Content-Type': 'application/json' } })
      }
      return baseFetch(input, init)
    })
    vi.stubGlobal('fetch', fetchMock)

    renderRoute('/')
    const agents = await screen.findByRole('region', { name: 'Holons' })
    fireEvent.mouseEnter(await screen.findByTestId('operations-panel'))

    expect(within(agents).queryByText('Workspace archived.')).not.toBeInTheDocument()
    await user.click(await within(agents).findByRole('button', { name: /Show ended holons/ }))
    expect(await within(agents).findAllByRole('link', { name: /Workspace archived\./ })).toHaveLength(2)
    expect(await within(agents).findByRole('button', { name: 'Reopen Expired task' })).toBeInTheDocument()
    await user.click(await within(agents).findByRole('button', { name: 'Reopen Cancelled task' }))

    await waitFor(() => expect(fetchMock.mock.calls.some(([input]) => String(input).endsWith('/holons/holon-cancelled/reopen'))).toBe(true))
    expect(await screen.findByRole('tablist', { name: 'Holon terminals' })).toBeInTheDocument()
  })

  it('shows first prompt lines for issue-started agents in the operations panel', async () => {
    const baseFetch = fetch as typeof globalThis.fetch
    vi.stubGlobal('fetch', vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      const path = String(input)
      if (path.endsWith('/holons')) {
        return new Response(JSON.stringify([{
          id: 'holon-issue',
          repository_id: 'holark',
          runtime_id: 'node-1',
          prompt: 'Fix issue title\n\nIssue description:\nBody text',
          status: 'running',
          input_state: 'none', activity: 'working',
          created_at: new Date().toISOString(),
        }]), { status: 200, headers: { 'Content-Type': 'application/json' } })
      }
      return baseFetch(input, init)
    }))

    renderRoute('/')

    const agents = await screen.findByRole('region', { name: 'Holons' })
    expect(await within(agents).findByRole('link', { name: /Fix issue title/ })).toBeInTheDocument()
    expect(within(agents).queryByText('Issue description:')).not.toBeInTheDocument()
  })

  it('shows the repository missing-path state for an invalid repository URL', async () => {
    renderRoute('/blob/missing.ts?ref=main')
    expect(await screen.findByText('This path does not exist on main.')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Go to repository root' })).toHaveAttribute('href', '/tree/?ref=main')
  })

  it('does not show cached repository content when the selected branch is missing', async () => {
    const baseFetch = fetch as typeof globalThis.fetch
    vi.stubGlobal('fetch', vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      const path = String(input)
      if (path.includes('/tree') && new URL(path, 'http://test.local').searchParams.get('ref') === 'removed') {
        return new Response(JSON.stringify({ code: 'ref_not_found', message: 'Ref not found.' }), { status: 404, headers: { 'Content-Type': 'application/json' } })
      }
      return baseFetch(input, init)
    }))

    renderRoute('/tree/src?ref=removed')

    expect(await screen.findByText('Branch removed does not exist.')).toBeInTheDocument()
    expect(screen.queryByRole('region', { name: 'Directory contents' })).not.toBeInTheDocument()
  })
})
