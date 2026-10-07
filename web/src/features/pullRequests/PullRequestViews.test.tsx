import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { Link, MemoryRouter, Route, Routes } from 'react-router-dom'
import { api } from '../../data/api'
import type { CreatePullRequestCommentRequest, PullRequestComment, PullRequestMetadata, PullRequestRebase, WorkspaceFileDiff } from '../../data/types'
import { ProjectMemberStoreProvider } from '../members/ProjectMemberStore'
import { ProjectContext } from '../project/ProjectContext'
import { PullRequestDetail } from './PullRequestViews'

// Exercise diff rendering without Monaco's browser clipboard and worker services.
vi.mock('monaco-editor', () => ({
  languages: { getLanguages: () => [] },
  editor: { colorize: async () => '', tokenize: () => [] },
}))

beforeEach(() => {
  vi.spyOn(api, 'refreshPullRequestComments').mockImplementation(() => new Promise(() => {}))
  vi.spyOn(api, 'pullRequestCommentRefreshStatus').mockResolvedValue({ syncing: false })
})

afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

type DetailStatus = 'wip' | 'draft' | 'open' | 'closed' | 'merged'

function stubPullRequestDetail(status: DetailStatus, options: {
  comparisonState?: 'ready' | 'stale' | 'unavailable'
  syncData?: Record<string, unknown>
  refreshedSyncData?: Record<string, unknown>
  refreshedStatus?: DetailStatus
  syncProvider?: string
  mergeable?: boolean
  mergeBlockedReason?: string
  transitionFailure?: string
  transitionDelay?: Promise<void>
  publicationBlocked?: boolean
  metadataAgentCompletion?: { title: string, description: string, error?: string }
  initialAgentSession?: { id: string, runtime_id: string, status: string, input_state: string, error: string }
  freshness?: PullRequestMetadata['freshness']
  preparationTarget?: 'wip' | 'draft' | 'open'
  assigneeHolarkIDs?: string[] | null
  requestedReviewerHolarkIDs?: string[] | null
  projectMembers?: Record<string, unknown>[]
  participantMutation?: (path: string, init?: RequestInit) => Promise<Response>
  participantSync?: () => Promise<Response>
  comments?: Record<string, unknown>[]
  workers?: Record<string, unknown>[]
  queue?: Record<string, unknown>[]
  reviews?: Record<string, unknown>[]
  rebaseReadiness?: () => Response | Promise<Response>
  rebaseMutation?: (init?: RequestInit) => Response | Promise<Response>
  commentMutation?: (body: CreatePullRequestCommentRequest) => Promise<Response>
  commentList?: () => Response | Promise<Response>
  files?: WorkspaceFileDiff[]
} = {}) {
  const now = new Date().toISOString()
  let currentStatus = status
  let currentTitle = 'Lifecycle work'
  let currentSummary = 'Summary'
  let metadataAgentStage = 0
  let preparationTarget = options.preparationTarget
  let freshness = options.freshness
  let currentSyncData = options.syncData ?? {}
  const comments = options.comments ?? [{
    id: 'comment-1', pull_request_id: 'pr-1', body: 'Needs docs', scope: 'pull_request',
    original_head_commit: 'def45678', status: 'unresolved', author_type: 'user', created_at: now, updated_at: now,
  }]
  let queue = options.queue ?? []
  const fetchMock = vi.fn<(input: RequestInfo | URL, init?: RequestInit) => Promise<Response>>(async (input, init) => {
    const path = String(input)
    if (path.endsWith('/github-members/resolve')) {
      const ids = JSON.parse(String(init?.body)).ids as string[]
      const members = (options.projectMembers ?? []).filter((member) => ids.includes(String(member.id)))
      return new Response(JSON.stringify({ members, missing_ids: ids.filter((id) => !members.some((member) => member.id === id)) }), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    if (path.includes('/github-members/search?')) {
      return new Response(JSON.stringify({ members: options.projectMembers ?? [] }), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    if (path.endsWith('/pull-requests/pr-1/participants/sync') && init?.method === 'POST') {
      return options.participantSync?.() ?? new Response(JSON.stringify({ code: 'sync_failed', message: 'Participant sync failed.' }), { status: 502, headers: { 'Content-Type': 'application/json' } })
    }
    if ((path.includes('/pull-requests/pr-1/assignees') || path.includes('/pull-requests/pr-1/requested-reviewers')) && (init?.method === 'POST' || init?.method === 'PUT' || init?.method === 'DELETE')) {
      return options.participantMutation?.(path, init) ?? new Response(JSON.stringify({ code: 'mutation_failed', message: 'Participant mutation failed.' }), { status: 502, headers: { 'Content-Type': 'application/json' } })
    }
    if (path.endsWith('/pull-requests/pr-1/transition')) {
      await options.transitionDelay
      if (options.transitionFailure) {
        return new Response(JSON.stringify({ code: 'transition_failed', message: options.transitionFailure }), { status: 409, headers: { 'Content-Type': 'application/json' } })
      }
      currentStatus = JSON.parse(String(init?.body)).status as DetailStatus
      preparationTarget = undefined
      return new Response(JSON.stringify({ id: 'pr-1', status: currentStatus }), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
		if (path.endsWith('/pull-requests/pr-1/rebase-readiness')) {
			if (options.rebaseReadiness) return options.rebaseReadiness()
			return new Response(JSON.stringify({
				base_commit: 'abc12345', head_commit: 'def45678', base_commits_ahead: 3, branch_freshness: 'not_up_to_date', rebase_conflict_state: 'clean',
			}), { status: 200, headers: { 'Content-Type': 'application/json' } })
		}
    if (path.endsWith('/pull-requests/pr-1/rebase') && init?.method === 'POST' && options.rebaseMutation) {
      return options.rebaseMutation(init)
    }
    if (path.endsWith('/pull-requests/pr-1/sync')) {
      currentSyncData = options.refreshedSyncData ?? currentSyncData
      currentStatus = options.refreshedStatus ?? currentStatus
      return new Response(null, { status: 204 })
    }
    if (path.endsWith('/pull-requests/pr-1/metadata/agent')) {
      metadataAgentStage = 1
      return new Response(JSON.stringify({ view_revision: 1,
        pull_request_id: 'pr-1', title: currentTitle, description: currentSummary, updated_at: now,
        preparation_target: preparationTarget, freshness,
        agent_session: { id: 'metadata-holon-1', runtime_id: 'node-1', status: 'running', input_state: 'none', error: '' },
      }), { status: 202, headers: { 'Content-Type': 'application/json' } })
    }
    if (path.endsWith('/pull-requests/pr-1/metadata') && (!init?.method || init.method === 'GET')) {
      let agentHolon = options.initialAgentSession
      if (metadataAgentStage > 0 && options.metadataAgentCompletion) {
        agentHolon = {
          id: 'metadata-holon-1', runtime_id: 'node-1', status: metadataAgentStage === 1 ? 'running' : 'completed', input_state: metadataAgentStage === 1 ? 'task_complete' : 'none',
          error: options.metadataAgentCompletion.error ?? '',
        }
        if (agentHolon.status === 'completed') {
          currentTitle = options.metadataAgentCompletion.title
          currentSummary = options.metadataAgentCompletion.description
        }
        metadataAgentStage = 2
        if (agentHolon.status === 'completed' && preparationTarget === 'open') {
          currentStatus = preparationTarget
          preparationTarget = undefined
        }
      } else if (metadataAgentStage > 0) {
        agentHolon = { id: 'metadata-holon-1', runtime_id: 'node-1', status: 'running', input_state: 'none', error: '' }
      }
      return new Response(JSON.stringify({ view_revision: 1, pull_request_id: 'pr-1', title: currentTitle, description: currentSummary, updated_at: now,
        freshness, preparation_target: (currentStatus === 'wip' || currentStatus === 'draft') ? preparationTarget : undefined, agent_session: agentHolon }), {
        status: 200, headers: { 'Content-Type': 'application/json' },
      })
    }
    if (path.endsWith('/pull-requests/pr-1/metadata')) {
      const body = JSON.parse(String(init?.body)) as { title?: string, description?: string }
      if (body.title !== undefined) currentTitle = body.title.trim()
      if (body.description !== undefined) { currentSummary = body.description.trim(); freshness = undefined }
      return new Response(JSON.stringify({ view_revision: 1, pull_request_id: 'pr-1', title: currentTitle, description: currentSummary, updated_at: now,
        preparation_target: (currentStatus === 'wip' || currentStatus === 'draft') ? preparationTarget : undefined,
        agent_session: metadataAgentStage > 0 ? { id: 'metadata-holon-1', runtime_id: 'node-1', status: 'running', input_state: 'none', error: '' } : options.initialAgentSession }), {
        status: 200, headers: { 'Content-Type': 'application/json' },
      })
    }
    if (path.endsWith('/pull-requests/pr-1') || path.endsWith('/pull-requests/pr-1/github-readiness')) {
      return new Response(JSON.stringify({ view_revision: 1,
        id: 'pr-1', repository_id: 'holark', title: currentTitle, summary: currentSummary, base_branch: 'main', base_commit: 'abc12345', head_branch: 'feature/work', head_commit: 'def45678', status: currentStatus, comparison_state: options.comparisonState,
        sync_provider: options.syncProvider, sync_data: currentSyncData, mergeable: options.mergeable ?? currentStatus === 'open', merge_blocked_reason: options.mergeBlockedReason,
        publication_blocked: (currentStatus === 'wip' || currentStatus === 'draft') && (options.publicationBlocked ?? preparationTarget === 'open'),
        assignee_holark_ids: options.assigneeHolarkIDs, requested_reviewer_holark_ids: options.requestedReviewerHolarkIDs,
        linked_session_ids: ['holon-1'], created_at: now, updated_at: now,
      }), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    if (path.endsWith('/pull-requests/pr-1/comments') && init?.method === 'POST' && options.commentMutation) {
      return options.commentMutation(JSON.parse(String(init.body)) as CreatePullRequestCommentRequest)
    }
    if (path.endsWith('/pull-requests/pr-1/comments')) {
      return options.commentList?.() ?? new Response(JSON.stringify(comments), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    if (path.endsWith('/pull-requests/pr-1/reviews')) {
      return new Response(JSON.stringify(options.reviews ?? []), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    if (path.endsWith('/pull-requests/pr-1/rebases')) return jsonResponse([])
    if (path.endsWith('/pull-requests/pr-1/workers')) {
      return new Response(JSON.stringify(options.workers ?? []), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    const cancelMatch = path.match(/\/pull-requests\/pr-1\/queue\/([^/]+)\/cancel$/)
    if (cancelMatch && init?.method === 'POST') {
      const cancelled = queue.find((item) => item.id === cancelMatch[1])
      queue = queue.filter((item) => item.id !== cancelMatch[1])
      return new Response(JSON.stringify({ ...cancelled, status: 'cancelled' }), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    if (path.endsWith('/pull-requests/pr-1/manual-reviews') && (!init?.method || init.method === 'GET')) {
      return Response.json([])
    }
    if (path.endsWith('/pull-requests/pr-1/queue')) {
      return new Response(JSON.stringify(queue), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    if (path.endsWith('/pull-requests/pr-1/commits')) {
      return new Response(JSON.stringify({ inputs: { head_commit: 'def45678', diff_base_commit: 'abc12345' }, data: [] }), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    if (path.endsWith('/pull-requests/pr-1/changes')) {
      return new Response(JSON.stringify({ inputs: { head_commit: 'def45678', diff_base_commit: 'abc12345' }, data: { branch: 'feature/work', base_branch: 'main', base_commit: 'abc12345', head_commit: 'def45678', has_changes: true, dirty: false, diff_truncated: false, files: options.files ?? [] } }), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    return new Response(JSON.stringify({ code: 'not_found', message: 'Not found.' }), { status: 404, headers: { 'Content-Type': 'application/json' } })
  })
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

function renderPullRequestDetail(path = '/pulls/pr-1') {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <ProjectContext.Provider value={{ id: 'holark', name: 'Holark', default_branch: 'main' }}>
        <ProjectMemberStoreProvider projectId="holark">
          <button type="button">Global navigation</button>
          <Routes><Route path="/pulls/:pullRequestId" element={<PullRequestDetail />} /><Route path="/holons/:holonId" element={<div>Agent page</div>} /></Routes>
          <button type="button">Operations panel</button>
        </ProjectMemberStoreProvider>
      </ProjectContext.Provider>
    </MemoryRouter>,
  )
}

it.each(['initial', 'rebase'])('loads startup details when historical rebase work refreshes before the initial PR (%s response first)', async (first) => {
  const user = userEvent.setup()
  stubPullRequestDetail('open', { workers: [{
    id: 'worker-1', pull_request_id: 'pr-1', mode: 'continue', status: 'running', created_at: new Date().toISOString(),
  }] })
  const snapshot = await api.pullRequest('pr-1')
  let finishInitial!: (value: typeof snapshot) => void
  let finishRebase!: (value: typeof snapshot) => void
  const initial = new Promise<typeof snapshot>((resolve) => { finishInitial = resolve })
  const rebase = new Promise<typeof snapshot>((resolve) => { finishRebase = resolve })
  const reads = vi.spyOn(api, 'pullRequest').mockReturnValueOnce(initial).mockReturnValueOnce(rebase).mockResolvedValue(snapshot)
  vi.spyOn(api, 'pullRequestRebases').mockResolvedValue([{
    id: 'historical-rebase', pull_request_id: 'pr-1', status: 'completed', created_at: '2026-09-01T00:00:00Z',
  } as PullRequestRebase])
  const commits = vi.spyOn(api, 'pullRequestCommits').mockResolvedValue({
    inputs: { head_commit: snapshot.head_commit, diff_base_commit: snapshot.base_commit },
    data: [{ sha: snapshot.head_commit, message: 'Startup commit', authored_at: '2026-09-01T00:00:00Z' }],
  } as Awaited<ReturnType<typeof api.pullRequestCommits>>)
  const changes = vi.spyOn(api, 'pullRequestChanges')
  renderPullRequestDetail()
  await waitFor(() => expect(reads).toHaveBeenCalledTimes(2))
  await act(async () => { (first === 'initial' ? finishInitial : finishRebase)(snapshot) })
  await act(async () => { (first === 'initial' ? finishRebase : finishInitial)(snapshot) })
  await waitFor(() => {
    expect(commits).toHaveBeenCalledTimes(1)
    expect(changes).toHaveBeenCalledTimes(1)
  })
  await user.click(screen.getByRole('button', { name: /^Commits/ }))
  expect(await screen.findByText('Startup commit')).toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: /^Changes/ }))
  expect(screen.queryByText('Loading changes...')).not.toBeInTheDocument()
  expect(screen.queryByText('Changes unavailable.')).not.toBeInTheDocument()
})

it('stops a click-time target change and analyzes conflicts before offering a Holon', async () => {
  const user = userEvent.setup()
  let finishRebase!: (response: Response) => void
  let finishAnalysis!: (response: Response) => void
  let baseMoved = false
  const rebaseResponse = new Promise<Response>((resolve) => { finishRebase = resolve })
  const analysisResponse = new Promise<Response>((resolve) => { finishAnalysis = resolve })
  const fetchMock = stubPullRequestDetail('open', {
    rebaseMutation: () => rebaseResponse,
    rebaseReadiness: () => baseMoved
      ? analysisResponse
      : Response.json({ base_commit: 'abc12345', head_commit: 'def45678', base_commits_ahead: 3, branch_freshness: 'not_up_to_date', rebase_conflict_state: 'clean' }),
  })
  renderPullRequestDetail()

  expect(await screen.findByText('main is 3 commits ahead · No conflicts')).toBeInTheDocument()
  const rebase = await screen.findByRole('button', { name: 'Rebase' }, { timeout: 4000 })
  expect(rebase).toHaveAttribute('title', 'Rebase onto main at abc12345')
  await user.click(rebase)
  expect(screen.getByRole('button', { name: 'Rebasing onto main…' })).toHaveAttribute('title', 'Rebasing onto main at abc12345')

  const rebaseCall = fetchMock.mock.calls.find(([input, init]) => String(input).endsWith('/pull-requests/pr-1/rebase') && init?.method === 'POST')
  expect(JSON.parse(String(rebaseCall?.[1]?.body))).toEqual({ mechanical_only: true, expected_base_commit: 'abc12345' })

  baseMoved = true
  await act(async () => finishRebase(Response.json({
    id: 'rebase-1', pull_request_id: 'pr-1', kind: 'rebase', mechanical_only: true, status: 'failed',
    target_base_commit: 'abc12345', error: 'rebase target changed', created_at: new Date().toISOString(),
  }, { status: 201 })))
  expect(await screen.findByText('Analyzing the latest main commit…')).toBeInTheDocument()
  const readiness = within(screen.getByRole('region', { name: 'Merge readiness' }))
  expect(readiness.queryByRole('button', { name: 'Rebase and resolve conflicts' })).not.toBeInTheDocument()

  await act(async () => finishAnalysis(Response.json({
    base_commit: 'fedcba98', head_commit: 'def45678', base_commits_ahead: 3, branch_freshness: 'not_up_to_date', rebase_conflict_state: 'conflicting',
  })))
  await waitFor(() => expect(readiness.getByRole('button', { name: 'Rebase and resolve conflicts' })).toBeEnabled())
  expect(fetchMock.mock.calls.filter(([input, init]) => String(input).endsWith('/pull-requests/pr-1/rebase') && init?.method === 'POST')).toHaveLength(1)
})

it('shows Address work contextually and keeps Continue work separate', async () => {
  const user = userEvent.setup()
  const now = new Date().toISOString()
  const comments = [
    {
      id: 'comment-1', pull_request_id: 'pr-1', body: 'Update the documentation with a very long explanation', scope: 'pull_request',
      original_head_commit: 'def45678', status: 'unresolved', author_type: 'user', created_at: now, updated_at: now,
    },
    {
      id: 'comment-2', pull_request_id: 'pr-1', body: 'Handle the empty state', scope: 'pull_request',
      original_head_commit: 'def45678', status: 'unresolved', author_type: 'user', created_at: now, updated_at: now,
    },
  ]
  const addressWorkers = [
    { id: 'address-running', pull_request_id: 'pr-1', comment_id: 'comment-1', session_id: 'holon-address', kind: 'worker', mode: 'auto', status: 'running', created_at: now },
    { id: 'address-queued', pull_request_id: 'pr-1', comment_id: 'comment-2', kind: 'worker', mode: 'assisted', status: 'queued', created_at: now },
  ]
  const fetchMock = stubPullRequestDetail('open', {
    comments,
    queue: addressWorkers,
    workers: [
      ...addressWorkers,
      { id: 'continue-work', pull_request_id: 'pr-1', session_id: 'holon-continue', kind: 'worker', mode: 'continue', status: 'running', created_at: now },
    ],
  })
  renderPullRequestDetail()

  const firstThread = (await screen.findByText('Update the documentation with a very long explanation', { selector: 'p' })).closest('article')!
  expect(within(firstThread).getByText('Address running')).toBeInTheDocument()
  expect(within(firstThread).getByRole('button', { name: 'Address in progress' })).toBeDisabled()

  const sidebar = screen.getByRole('complementary', { name: 'Pull request sidebar' })
  expect(within(sidebar).getByText('1 running · 1 queued')).toBeInTheDocument()
  expect(within(sidebar).getByRole('heading', { name: 'Continue work' })).toBeInTheDocument()
  expect(within(sidebar).queryByRole('heading', { name: 'Workers' })).not.toBeInTheDocument()

  await user.click(within(sidebar).getByText('View active requests'))
  expect(within(sidebar).getByRole('link', { name: 'Update the documentation with a very long explanation' })).toHaveAttribute('href', '#comment-comment-1')
  await user.click(within(sidebar).getAllByRole('button', { name: 'Cancel' })[1])
  await waitFor(() => expect(fetchMock.mock.calls.some(([input, init]) => (
    String(input).endsWith('/pull-requests/pr-1/queue/address-queued/cancel') && init?.method === 'POST'
  ))).toBe(true))
  await waitFor(() => expect(within(sidebar).getByText('1 running')).toBeInTheDocument())
})

it('shows resolved participants for an open pull request', async () => {
  const assignee = { id: 'member-assignee', login: 'alice', avatar_url: 'https://example.com/alice.png', profile_url: 'https://github.com/alice', permission: 'write', is_me: false }
  const reviewer = { id: 'member-reviewer', login: 'bob', avatar_url: 'https://example.com/bob.png', profile_url: 'https://github.com/bob', permission: 'read', is_me: false }
  stubPullRequestDetail('open', {
    assigneeHolarkIDs: [assignee.id],
    requestedReviewerHolarkIDs: [reviewer.id],
    projectMembers: [assignee, reviewer],
  })
  renderPullRequestDetail()

  const sidebar = await screen.findByRole('complementary', { name: 'Pull request sidebar' })
  expect(within(sidebar).getByRole('heading', { name: 'Assignees' })).toBeInTheDocument()
  expect(await within(sidebar).findByText('alice')).toBeInTheDocument()
  expect(within(sidebar).getByRole('heading', { name: 'Requested reviewers' })).toBeInTheDocument()
  expect(within(sidebar).getByText('bob')).toBeInTheDocument()
})

it.each(['draft', 'open', 'merged'] as const)('shows participant sections for %s pull requests', async (status) => {
  stubPullRequestDetail(status)
  renderPullRequestDetail()

  const sidebar = await screen.findByRole('complementary', { name: 'Pull request sidebar' })
  expect(within(sidebar).getByRole('heading', { name: 'Assignees' })).toBeInTheDocument()
  expect(within(sidebar).getByRole('heading', { name: 'Requested reviewers' })).toBeInTheDocument()
  if (status === 'merged') {
    expect(within(sidebar).queryByRole('button', { name: /Edit assignees/i })).not.toBeInTheDocument()
    expect(within(sidebar).queryByRole('button', { name: /Edit requested reviewers/i })).not.toBeInTheDocument()
  }
})

it.each(['wip', 'closed'] as const)('hides participant sections for %s pull requests', async (status) => {
  stubPullRequestDetail(status)
  renderPullRequestDetail()

  const sidebar = await screen.findByRole('complementary', { name: 'Pull request sidebar' })
  expect(within(sidebar).queryByRole('heading', { name: 'Assignees' })).not.toBeInTheDocument()
  expect(within(sidebar).queryByRole('heading', { name: 'Requested reviewers' })).not.toBeInTheDocument()
})

it('keeps active non-GitHub participant sections display-only', async () => {
  stubPullRequestDetail('open', { syncProvider: 'gitlab' })
  renderPullRequestDetail()

  const sidebar = await screen.findByRole('complementary', { name: 'Pull request sidebar' })
  expect(within(sidebar).getByRole('heading', { name: 'Assignees' })).toBeInTheDocument()
  expect(within(sidebar).getByRole('heading', { name: 'Requested reviewers' })).toBeInTheDocument()
  expect(within(sidebar).queryByRole('button', { name: /Edit assignees/i })).not.toBeInTheDocument()
  expect(within(sidebar).queryByRole('button', { name: /Edit requested reviewers/i })).not.toBeInTheDocument()
})

it('saves the final participant collection once when Done closes the picker', async () => {
  const user = userEvent.setup()
  const alice = { id: 'member-alice', login: 'alice', permission: 'write', is_me: false }
  const bob = { id: 'member-bob', login: 'bob', permission: 'write', is_me: false }
  const pending = new Map<string, (response: Response) => void>()
  const fetchMock = stubPullRequestDetail('open', {
    syncProvider: 'github', assigneeHolarkIDs: [], requestedReviewerHolarkIDs: [], projectMembers: [alice, bob],
    participantMutation: (path, init) => new Promise((resolve) => { pending.set(`${init?.method}:${path}`, resolve) }),
  })
  renderPullRequestDetail()

  const sidebar = await screen.findByRole('complementary', { name: 'Pull request sidebar' })
  await user.click(within(sidebar).getByRole('button', { name: 'Edit assignees' }))
  expect(screen.getByRole('searchbox', { name: 'Search project members' })).toHaveFocus()
  await user.click(await screen.findByRole('option', { name: /alice/ }))
  await user.click(screen.getByRole('option', { name: /bob/ }))
  expect(within(sidebar).getByText('None assigned.')).toBeInTheDocument()
  expect(screen.getByRole('option', { name: /alice/ })).toHaveAttribute('aria-selected', 'true')
  expect(screen.getByRole('option', { name: /bob/ })).toHaveAttribute('aria-selected', 'true')
  await user.click(screen.getByRole('button', { name: 'Done' }))

  expect(screen.queryByRole('searchbox', { name: 'Search project members' })).not.toBeInTheDocument()
  expect(within(sidebar).getByText('alice')).toBeInTheDocument()
  expect(within(sidebar).getByText('bob')).toBeInTheDocument()
  expect(within(sidebar).getByRole('button', { name: 'Edit assignees' })).toHaveAttribute('aria-disabled', 'true')
  expect(within(sidebar).getByRole('button', { name: 'Edit assignees' })).toHaveFocus()
  expect(fetchMock).toHaveBeenCalledWith('/api/v1/pull-requests/pr-1/assignees', expect.objectContaining({
    method: 'PUT', headers: expect.objectContaining({ 'content-type': 'application/json' }), body: JSON.stringify({ assignee_holark_ids: [alice.id, bob.id] }),
  }))
  expect(fetchMock.mock.calls.filter(([input, init]) => String(input).endsWith('/assignees') && init?.method === 'PUT')).toHaveLength(1)

  pending.get('PUT:/api/v1/pull-requests/pr-1/assignees')?.(jsonResponse({ pull_request_id: 'pr-1', assignee_holark_ids: [alice.id, bob.id], requested_reviewer_holark_ids: [] }))
  await waitFor(() => expect(within(sidebar).getByRole('button', { name: 'Edit assignees' })).not.toHaveAttribute('aria-disabled'))

  await user.click(within(sidebar).getByRole('button', { name: 'Edit requested reviewers' }))
  await user.click(await screen.findByRole('option', { name: /alice/ }))
  await user.click(within(sidebar).getByRole('button', { name: 'Edit requested reviewers' }))
  expect(within(sidebar).getAllByText('alice')).toHaveLength(2)
  pending.get('PUT:/api/v1/pull-requests/pr-1/requested-reviewers')?.(jsonResponse({ pull_request_id: 'pr-1', assignee_holark_ids: [alice.id, bob.id], requested_reviewer_holark_ids: [alice.id] }))
  await waitFor(() => expect(within(sidebar).getAllByText('alice')).toHaveLength(2))
  expect(fetchMock).toHaveBeenCalledWith('/api/v1/pull-requests/pr-1/requested-reviewers', expect.objectContaining({
    method: 'PUT', headers: expect.objectContaining({ 'content-type': 'application/json' }), body: JSON.stringify({ requested_reviewer_holark_ids: [alice.id] }),
  }))
})

it('commits participant changes on an outside click and preserves the outside focus target', async () => {
  const user = userEvent.setup()
  const alice = { id: 'member-alice', login: 'alice', permission: 'write', is_me: false }
  const pending = new Promise<Response>(() => {})
  const fetchMock = stubPullRequestDetail('open', {
    syncProvider: 'github', assigneeHolarkIDs: [], requestedReviewerHolarkIDs: [], projectMembers: [alice],
    participantMutation: async () => pending,
  })
  renderPullRequestDetail()

  const sidebar = await screen.findByRole('complementary', { name: 'Pull request sidebar' })
  await user.click(within(sidebar).getByRole('button', { name: 'Edit assignees' }))
  await user.click(await screen.findByRole('option', { name: /alice/ }))
  await user.click(screen.getByRole('button', { name: 'Global navigation' }))

  expect(screen.getByRole('button', { name: 'Global navigation' })).toHaveFocus()
  expect(screen.queryByRole('searchbox', { name: 'Search project members' })).not.toBeInTheDocument()
  expect(within(sidebar).getByText('alice')).toBeInTheDocument()
  expect(fetchMock).toHaveBeenCalledWith('/api/v1/pull-requests/pr-1/assignees', expect.objectContaining({
    method: 'PUT', headers: expect.objectContaining({ 'content-type': 'application/json' }), body: JSON.stringify({ assignee_holark_ids: [alice.id] }),
  }))
})

it('commits the active participant draft before keyboard opens the other editor', async () => {
  const user = userEvent.setup()
  const alice = { id: 'member-alice', login: 'alice', permission: 'write', is_me: false }
  const fetchMock = stubPullRequestDetail('open', {
    syncProvider: 'github', assigneeHolarkIDs: [], requestedReviewerHolarkIDs: [], projectMembers: [alice],
    participantMutation: async () => jsonResponse({
      pull_request_id: 'pr-1', assignee_holark_ids: [alice.id], requested_reviewer_holark_ids: [],
    }),
  })
  renderPullRequestDetail()

  const sidebar = await screen.findByRole('complementary', { name: 'Pull request sidebar' })
  await user.click(within(sidebar).getByRole('button', { name: 'Edit assignees' }))
  await user.click(await screen.findByRole('option', { name: /alice/ }))
  await user.tab()
  await user.tab()
  expect(within(sidebar).getByRole('button', { name: 'Edit requested reviewers' })).toHaveFocus()
  await user.keyboard('{Enter}')

  await waitFor(() => expect(fetchMock).toHaveBeenCalledWith('/api/v1/pull-requests/pr-1/assignees', expect.objectContaining({
    method: 'PUT', headers: expect.objectContaining({ 'content-type': 'application/json' }), body: JSON.stringify({ assignee_holark_ids: [alice.id] }),
  })))
  const assignees = within(sidebar).getByRole('heading', { name: 'Assignees' }).closest('section') as HTMLElement
  expect(within(assignees).getByText('alice')).toBeInTheDocument()
  expect(within(sidebar).getByRole('button', { name: 'Edit requested reviewers' })).toHaveAttribute('aria-expanded', 'true')
  expect(screen.getByRole('searchbox', { name: 'Search project members' })).toHaveFocus()
})

it('discards participant drafts on Escape and skips unchanged saves', async () => {
  const user = userEvent.setup()
  const alice = { id: 'member-alice', login: 'alice', permission: 'write', is_me: false }
  const bob = { id: 'member-bob', login: 'bob', permission: 'write', is_me: false }
  const fetchMock = stubPullRequestDetail('open', {
    syncProvider: 'github', assigneeHolarkIDs: [alice.id], requestedReviewerHolarkIDs: [], projectMembers: [alice, bob],
  })
  renderPullRequestDetail()

  const sidebar = await screen.findByRole('complementary', { name: 'Pull request sidebar' })
  const editAssignees = within(sidebar).getByRole('button', { name: 'Edit assignees' })
  await user.click(editAssignees)
  await user.click(await screen.findByRole('option', { name: /bob/ }))
  await user.keyboard('{Escape}')

  expect(screen.queryByRole('searchbox', { name: 'Search project members' })).not.toBeInTheDocument()
  expect(within(sidebar).getByText('alice')).toBeInTheDocument()
  expect(within(sidebar).queryByText('bob')).not.toBeInTheDocument()
  expect(editAssignees).toHaveFocus()
  expect(fetchMock.mock.calls.filter(([, init]) => init?.method === 'PUT')).toHaveLength(0)

  await user.click(editAssignees)
  await user.click(screen.getByRole('button', { name: 'Done' }))
  expect(fetchMock.mock.calls.filter(([, init]) => init?.method === 'PUT')).toHaveLength(0)
})

it('reconciles participants from GitHub after a failed mutation', async () => {
  const user = userEvent.setup()
  const alice = { id: 'member-alice', login: 'alice', permission: 'write', is_me: false }
  const bob = { id: 'member-bob', login: 'bob', permission: 'write', is_me: false }
  const fetchMock = stubPullRequestDetail('open', {
    syncProvider: 'github', assigneeHolarkIDs: [alice.id], requestedReviewerHolarkIDs: [], projectMembers: [alice, bob],
    participantMutation: async () => jsonResponse({ code: 'github_failed', message: 'GitHub rejected the assignee.' }, 502),
    participantSync: async () => jsonResponse({ pull_request_id: 'pr-1', assignee_holark_ids: [alice.id], requested_reviewer_holark_ids: [] }),
  })
  renderPullRequestDetail()

  const sidebar = await screen.findByRole('complementary', { name: 'Pull request sidebar' })
  await user.click(within(sidebar).getByRole('button', { name: 'Edit assignees' }))
  await user.click(await screen.findByRole('option', { name: /bob/ }))
  await user.click(screen.getByRole('button', { name: 'Done' }))

  expect(await screen.findByRole('alert')).toHaveTextContent('Couldn’t update assignees. GitHub’s selection was restored.')
  expect(screen.getAllByRole('alert')).toHaveLength(1)
  expect(within(sidebar).getByText('alice')).toBeInTheDocument()
  expect(within(sidebar).queryByText('bob')).not.toBeInTheDocument()
  expect(fetchMock).toHaveBeenCalledWith('/api/v1/pull-requests/pr-1/participants/sync', expect.objectContaining({ method: 'POST' }))

  await user.click(within(sidebar).getByRole('button', { name: 'Edit assignees' }))
  expect(screen.queryByRole('alert')).not.toBeInTheDocument()
})

it('retains the prior participant snapshot when mutation and reconciliation both fail', async () => {
  const user = userEvent.setup()
  const alice = { id: 'member-alice', login: 'alice', permission: 'write', is_me: false }
  const bob = { id: 'member-bob', login: 'bob', permission: 'write', is_me: false }
  stubPullRequestDetail('open', {
    syncProvider: 'github', assigneeHolarkIDs: [alice.id], requestedReviewerHolarkIDs: [], projectMembers: [alice, bob],
    participantMutation: async () => jsonResponse({ code: 'github_failed', message: 'GitHub rejected the assignee.' }, 502),
    participantSync: async () => jsonResponse({ code: 'sync_failed', message: 'GitHub participant refresh failed.' }, 502),
  })
  renderPullRequestDetail()

  const sidebar = await screen.findByRole('complementary', { name: 'Pull request sidebar' })
  await user.click(within(sidebar).getByRole('button', { name: 'Edit assignees' }))
  await user.click(await screen.findByRole('option', { name: /bob/ }))
  await user.click(screen.getByRole('button', { name: 'Done' }))

  expect(await screen.findByRole('alert')).toHaveTextContent('Couldn’t update assignees. Refresh to check GitHub.')
  expect(screen.getAllByRole('alert')).toHaveLength(1)
  expect(within(sidebar).getByText('alice')).toBeInTheDocument()
  expect(within(sidebar).queryByText('bob')).not.toBeInTheDocument()
})

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

it('reads external lifecycle changes while readiness fails and stops readiness after merge', async () => {
  vi.useFakeTimers()
  const fetchMock = stubPullRequestDetail('open', { syncProvider: 'github' })
  const initial = await api.pullRequest('pr-1')
  let current = initial
  vi.spyOn(api, 'pullRequest').mockImplementation(async () => current)
  const readiness = vi.spyOn(api, 'refreshPullRequestGitHubReadiness').mockRejectedValue(new Error('offline'))
  renderPullRequestDetail()
  await act(async () => { await vi.advanceTimersByTimeAsync(0) })
  await act(async () => { await vi.advanceTimersByTimeAsync(6000) })
  expect(readiness).toHaveBeenCalledTimes(2)
  current = { ...initial, view_revision: 2, status: 'merged', title: 'Externally merged' }
  await act(async () => { await vi.advanceTimersByTimeAsync(3000) })
  expect(screen.getByText('Externally merged')).toBeInTheDocument()
  const attempts = readiness.mock.calls.length
  await act(async () => { await vi.advanceTimersByTimeAsync(6000) })
  expect(readiness).toHaveBeenCalledTimes(attempts)
  expect(githubSyncRequests(fetchMock)).toHaveLength(0)
})

it('keeps the readiness deadline through frequent cached updates and waits after a slow request', async () => {
  vi.useFakeTimers()
  const fetchMock = stubPullRequestDetail('open', { syncProvider: 'github' })
  const sync = vi.spyOn(api, 'refreshPullRequestGitHubReadiness')
  let finish!: (value: Awaited<ReturnType<typeof api.pullRequest>>) => void
  sync.mockImplementationOnce(() => new Promise((resolve) => { finish = resolve }))
  renderPullRequestDetail()
  await act(async () => { await vi.advanceTimersByTimeAsync(0) })
  const cached = await api.pullRequest('pr-1')
  for (let i = 0; i < 3; i++) {
    await act(async () => {
      await vi.advanceTimersByTimeAsync(900)
      window.dispatchEvent(new Event('focus'))
    })
  }
  expect(sync).not.toHaveBeenCalled()
  await act(async () => { await vi.advanceTimersByTimeAsync(300) })
  expect(sync).toHaveBeenCalledTimes(1)
  await act(async () => { await vi.advanceTimersByTimeAsync(10_000) })
  expect(sync).toHaveBeenCalledTimes(1)
  await act(async () => { finish(cached) })
  await act(async () => { await vi.advanceTimersByTimeAsync(2999) })
  expect(sync).toHaveBeenCalledTimes(1)
  await act(async () => { await vi.advanceTimersByTimeAsync(1) })
  expect(sync).toHaveBeenCalledTimes(2)
  expect(fetchMock.mock.calls.filter(([path]) => String(path).endsWith('/pull-requests/pr-1')).length).toBeGreaterThan(3)
})

it('keeps one request through polling eligibility changes', async () => {
  vi.useFakeTimers()
  stubPullRequestDetail('open', { syncProvider: 'github' })
  const snapshot = await api.pullRequest('pr-1')
  let current = snapshot
  vi.spyOn(api, 'pullRequest').mockImplementation(async () => current)
  let finish!: (value: typeof snapshot) => void
  const sync = vi.spyOn(api, 'refreshPullRequestGitHubReadiness').mockImplementationOnce(() => new Promise((resolve) => { finish = resolve }))
  renderPullRequestDetail()
  await act(async () => { await vi.advanceTimersByTimeAsync(0) })
  await act(async () => { await vi.advanceTimersByTimeAsync(3000) })
  expect(sync).toHaveBeenCalledTimes(1)
  await act(async () => {
    current = { ...snapshot, view_revision: 2, status: 'closed' }
    window.dispatchEvent(new Event('focus'))
  })
  await act(async () => {
    current = { ...snapshot, view_revision: 3 }
    window.dispatchEvent(new Event('focus'))
  })
  await act(async () => { await vi.advanceTimersByTimeAsync(4000) })
  expect(sync).toHaveBeenCalledTimes(1)
  await act(async () => { finish(current) })
  await act(async () => { await vi.advanceTimersByTimeAsync(2999) })
  expect(sync).toHaveBeenCalledTimes(1)
  await act(async () => { await vi.advanceTimersByTimeAsync(1) })
  expect(sync).toHaveBeenCalledTimes(2)
})

it('skips readiness during Rebase and rejects the earlier mutation generation', async () => {
  vi.useFakeTimers()
  let finishRebase!: (response: Response) => void
  stubPullRequestDetail('open', {
    syncProvider: 'github',
    rebaseMutation: () => new Promise((resolve) => { finishRebase = resolve }),
  })
  const snapshot = await api.pullRequest('pr-1')
  let finishSync!: (value: typeof snapshot) => void
  const sync = vi.spyOn(api, 'refreshPullRequestGitHubReadiness').mockImplementationOnce(() => new Promise((resolve) => { finishSync = resolve }))
  renderPullRequestDetail()
  await act(async () => { await vi.advanceTimersByTimeAsync(0) })
  await act(async () => { await vi.advanceTimersByTimeAsync(3000) })
  expect(sync).toHaveBeenCalledTimes(1)
  fireEvent.click(screen.getByRole('button', { name: 'Rebase' }))
  await act(async () => { finishSync({ ...snapshot, view_revision: 100, title: 'Obsolete sync result' }) })
  expect(screen.queryByText('Obsolete sync result')).not.toBeInTheDocument()
  await act(async () => { await vi.advanceTimersByTimeAsync(6000) })
  expect(sync).toHaveBeenCalledTimes(1)
  await act(async () => {
    finishRebase(Response.json({ id: 'rebase-1', pull_request_id: 'pr-1', kind: 'rebase', status: 'completed', result_head_commit: 'new-head' }))
  })
  await act(async () => { await vi.advanceTimersByTimeAsync(3000) })
  expect(sync).toHaveBeenCalledTimes(2)
})

it('skips hidden attempts and discards a pending response after navigation', async () => {
  vi.useFakeTimers()
  stubPullRequestDetail('open', { syncProvider: 'github' })
  const sync = vi.spyOn(api, 'refreshPullRequestGitHubReadiness')
  const visibility = vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('hidden')
  const view = renderPullRequestDetail()
  await act(async () => { await vi.advanceTimersByTimeAsync(6000) })
  expect(sync).not.toHaveBeenCalled()
  visibility.mockReturnValue('visible')
  const cached = await api.pullRequest('pr-1')
  let finish!: (value: typeof cached) => void
  sync.mockImplementationOnce(() => new Promise((resolve) => { finish = resolve }))
  await act(async () => { await vi.advanceTimersByTimeAsync(3000) })
  expect(sync).toHaveBeenCalledTimes(1)
  view.unmount()
  await act(async () => {
    finish({ ...cached, title: 'Obsolete response' })
    await vi.advanceTimersByTimeAsync(6000)
  })
  expect(sync).toHaveBeenCalledTimes(1)
  expect(screen.queryByText('Obsolete response')).not.toBeInTheDocument()
})

it.each(['slow', 'failing'])('accepts cached head, title, and repaired comparison with %s readiness', async (mode) => {
  vi.useFakeTimers()
  const fetchMock = stubPullRequestDetail('open', { syncProvider: 'github', comparisonState: 'stale' })
  const initial = await api.pullRequest('pr-1')
  let current = initial
  vi.spyOn(api, 'pullRequest').mockImplementation(async () => current)
  const readiness = vi.spyOn(api, 'refreshPullRequestGitHubReadiness').mockImplementation(() =>
    mode === 'slow' ? new Promise(() => {}) : Promise.reject(new Error('offline')))
  const commits = vi.spyOn(api, 'pullRequestCommits').mockImplementation(async () => ({
    inputs: { head_commit: current.head_commit, diff_base_commit: current.diff_base_commit || current.base_commit }, data: [],
  }))
  vi.spyOn(api, 'pullRequestChanges').mockImplementation(async () => ({
    inputs: { head_commit: current.head_commit, diff_base_commit: current.diff_base_commit || current.base_commit }, data: { branch: 'feature', base_branch: 'main', base_commit: current.base_commit, head_commit: current.head_commit, has_changes: true, dirty: false, diff_truncated: false, files: [] },
  }))
  renderPullRequestDetail()
  await act(async () => { await vi.advanceTimersByTimeAsync(0) })
  await act(async () => { await vi.advanceTimersByTimeAsync(3000) })
  current = { ...initial, view_revision: 2, title: 'External head update', head_commit: 'new-head', comparison_state: 'ready' }
  await act(async () => { await vi.advanceTimersByTimeAsync(3000) })
  expect(screen.getByRole('heading', { name: 'External head update' })).toBeInTheDocument()
  expect(commits).toHaveBeenCalledTimes(2)
  expect(readiness).toHaveBeenCalledTimes(mode === 'slow' ? 1 : 2)
  expect(githubSyncRequests(fetchMock)).toHaveLength(0)
})

it('retains exceptional full synchronization for a stale local comparison', async () => {
  vi.useFakeTimers()
  stubPullRequestDetail('open', { comparisonState: 'stale' })
  const initial = await api.pullRequest('pr-1')
  const sync = vi.spyOn(api, 'syncPullRequest').mockResolvedValue({ ...initial, view_revision: 2, comparison_state: 'ready' })
  const readiness = vi.spyOn(api, 'refreshPullRequestGitHubReadiness')
  renderPullRequestDetail()
  await act(async () => { await vi.advanceTimersByTimeAsync(0) })
  await act(async () => { await vi.advanceTimersByTimeAsync(3000) })
  expect(sync).toHaveBeenCalledTimes(1)
  await act(async () => { await vi.advanceTimersByTimeAsync(6000) })
  expect(sync).toHaveBeenCalledTimes(1)
  expect(readiness).not.toHaveBeenCalled()
})

it('resumes readiness on visibility restoration without overlapping a pending request', async () => {
  vi.useFakeTimers()
  stubPullRequestDetail('open', { syncProvider: 'github' })
  const initial = await api.pullRequest('pr-1')
  const visibility = vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible')
  let finish!: (value: typeof initial) => void
  const readiness = vi.spyOn(api, 'refreshPullRequestGitHubReadiness').mockImplementationOnce(() => new Promise((resolve) => { finish = resolve }))
  renderPullRequestDetail()
  await act(async () => { await vi.advanceTimersByTimeAsync(0) })
  await act(async () => { await vi.advanceTimersByTimeAsync(3000) })
  expect(readiness).toHaveBeenCalledTimes(1)
  await act(async () => {
    visibility.mockReturnValue('hidden')
    document.dispatchEvent(new Event('visibilitychange'))
    await vi.advanceTimersByTimeAsync(9000)
  })
  await act(async () => {
    visibility.mockReturnValue('visible')
    document.dispatchEvent(new Event('visibilitychange'))
    await vi.advanceTimersByTimeAsync(9000)
  })
  expect(readiness).toHaveBeenCalledTimes(1)
  await act(async () => { finish(initial) })
  await act(async () => { await vi.advanceTimersByTimeAsync(3000) })
  expect(readiness).toHaveBeenCalledTimes(2)
  await act(async () => {
    visibility.mockReturnValue('hidden')
    document.dispatchEvent(new Event('visibilitychange'))
    await vi.advanceTimersByTimeAsync(9000)
  })
  expect(readiness).toHaveBeenCalledTimes(2)
  await act(async () => {
    visibility.mockReturnValue('visible')
    document.dispatchEvent(new Event('visibilitychange'))
  })
  expect(readiness).toHaveBeenCalledTimes(3)
})

function githubSyncRequests(fetchMock: ReturnType<typeof vi.fn>) {
  return fetchMock.mock.calls.filter(([input]) => String(input).endsWith('/pull-requests/pr-1/sync'))
}

it('shows the metadata Holon while keeping the description, commits and changes browsable', async () => {
  const user = userEvent.setup()
  stubPullRequestDetail('wip', {
    preparationTarget: 'open',
    initialAgentSession: { id: 'metadata-holon-1', runtime_id: 'node-1', status: 'running', input_state: 'none', error: '' },
  })
  renderPullRequestDetail()

  expect(await screen.findByRole('link', { name: /Metadata holon/ })).toHaveAttribute('href', '/holons/metadata-holon-1')
  expect(screen.getByText('Summary')).toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Merge pull request' })).toBeDisabled()
  await user.click(screen.getByRole('button', { name: 'Pull request actions' }))
  expect(screen.getByRole('menuitem', { name: 'Open PR' })).toBeDisabled()
  expect(screen.getByRole('menuitem', { name: 'Close pull request' })).toBeEnabled()
  await user.keyboard('{Escape}')
  expect(screen.getByRole('navigation', { name: 'Pull request sections' })).not.toHaveAttribute('inert')
  await user.click(screen.getByRole('button', { name: /^Commits/ }))
  expect(screen.getByText('No commits available.')).toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: /^Changes/ }))
  expect(screen.getByRole('region', { name: 'Pull request file diffs' })).toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: /^Overview/ }))
  expect(screen.getByRole('button', { name: 'Merge pull request' })).toBeDisabled()
})

it('waits for publication and keeps generated metadata visible across refreshes and overview remounts', async () => {
  const user = userEvent.setup()
  let readinessRequest = 0
  let finishReadinessRefresh!: (response: Response) => void
  const readinessRefresh = new Promise<Response>((resolve) => { finishReadinessRefresh = resolve })
  stubPullRequestDetail('wip', { rebaseReadiness: () => {
    readinessRequest++
    return readinessRequest === 1 ? jsonResponse({ base_commit: 'abc12345', head_commit: 'def45678', base_commits_ahead: 0, branch_freshness: 'up_to_date', rebase_conflict_state: 'clean' }) : readinessRefresh
  } })
  const initial = { ...await api.pullRequest('pr-1'), summary: '', publication_blocked: true }
  const pullRequest = vi.spyOn(api, 'pullRequest').mockResolvedValue(initial)
  const completed: PullRequestMetadata = { view_revision: 1,
    pull_request_id: initial.id, title: 'Generated title', description: 'Generated description',
    updated_at: '2026-09-05T12:00:00Z',
    agent_session: { id: 'metadata-holon-1', runtime_id: 'node-1', status: 'completed', input_state: 'none', error: '' },
  }
  const metadata = vi.spyOn(api, 'pullRequestMetadata').mockResolvedValue({
    ...completed, title: initial.title, description: '', preparation_target: 'open',
    agent_session: { ...completed.agent_session!, status: 'running' },
  })
  renderPullRequestDetail()
  expect(await screen.findByRole('link', { name: /Metadata holon/ })).toBeInTheDocument()

  // The agent has finished, but its artifact has not yet been imported.
  // Wait for the next response rather than coupling the test to a poll count.
  let observeCompletion!: () => void
  const completionObserved = new Promise<void>((resolve) => { observeCompletion = resolve })
  metadata.mockImplementation(async () => {
    observeCompletion()
    return { ...completed, title: initial.title, description: '', preparation_target: 'open' }
  })
  await act(async () => { await completionObserved })
  expect(screen.getByRole('link', { name: /Metadata holon/ })).toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Edit description' })).toBeDisabled()
  expect(screen.queryByText('No description provided.')).not.toBeInTheDocument()
  expect(screen.getByRole('heading', { name: initial.title, level: 1 })).toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Merge pull request' })).toBeDisabled()

  const published = { ...initial, title: completed.title, summary: completed.description, status: 'open' as const, updated_at: completed.updated_at, publication_blocked: false }
  pullRequest.mockResolvedValue(published)
  let finishRefresh!: (value: PullRequestMetadata) => void
  const refreshPending = new Promise<PullRequestMetadata>((resolve) => { finishRefresh = resolve })
  metadata.mockResolvedValueOnce(completed).mockReturnValue(refreshPending)

  expect(await screen.findByRole('heading', { name: completed.title, level: 1 }, { timeout: 4000 })).toBeInTheDocument()
  const summary = screen.getByRole('region', { name: 'Pull request metadata' })
  expect(await within(summary).findByText(completed.description)).toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Edit description' })).toBeEnabled()
  expect(screen.queryByRole('region', { name: 'Pull request metadata generation' })).not.toBeInTheDocument()
  expect(screen.getByText('Branch up to date')).toBeInTheDocument()
  expect(screen.queryByText('Checking whether rebase is needed…')).not.toBeInTheDocument()

  pullRequest.mockResolvedValue({ ...published, updated_at: '2026-09-05T12:00:01Z', mergeable: true })
  await user.click(screen.getByRole('button', { name: 'Pull request actions' }))
  await user.click(screen.getByRole('menuitem', { name: 'Refresh' }))
  expect(within(screen.getByRole('region', { name: 'Pull request metadata' })).getByText(completed.description)).toBeInTheDocument()
  expect(screen.queryByRole('region', { name: 'Pull request metadata generation' })).not.toBeInTheDocument()
  expect(screen.getByText('Branch up to date')).toBeInTheDocument()
  expect(screen.queryByText('Checking whether rebase is needed…')).not.toBeInTheDocument()
  await act(async () => { finishReadinessRefresh(jsonResponse({ base_commit: 'abc12345', head_commit: 'def45678', base_commits_ahead: 0, branch_freshness: 'up_to_date', rebase_conflict_state: 'clean' })) })
  expect(await screen.findByText('Branch up to date')).toBeInTheDocument()

  // Returning to Overview mounts a fresh card with a populated PR snapshot.
  await user.click(screen.getByRole('button', { name: /^Commits/ }))
  await user.click(screen.getByRole('button', { name: /^Overview/ }))
  expect(screen.getByText(completed.description)).toBeInTheDocument()
  expect(screen.queryByRole('region', { name: 'Pull request metadata generation' })).not.toBeInTheDocument()
  await act(async () => { finishRefresh(completed) })
  expect(screen.getByText(completed.description)).toBeInTheDocument()
})

it('keeps a conflicting rebase recommendation stable while published metadata changes refs', async () => {
  let readinessRequest = 0
  let finishReadinessRefresh!: (response: Response) => void
  const readinessRefresh = new Promise<Response>((resolve) => { finishReadinessRefresh = resolve })
  stubPullRequestDetail('wip', { rebaseReadiness: () => {
    readinessRequest++
    return readinessRequest === 1
      ? jsonResponse({ base_commit: 'abc12345', head_commit: 'def45678', base_commits_ahead: 3, branch_freshness: 'not_up_to_date', rebase_conflict_state: 'conflicting' })
      : readinessRefresh
  } })
  const initial = { ...await api.pullRequest('pr-1'), summary: '', publication_blocked: true }
  const pullRequest = vi.spyOn(api, 'pullRequest').mockResolvedValue(initial)
  const completed: PullRequestMetadata = { view_revision: 1,
    pull_request_id: initial.id,
    title: 'Generated title',
    description: 'Generated description',
    updated_at: '2026-09-05T12:00:00Z',
    agent_session: { id: 'metadata-holon-1', runtime_id: 'node-1', status: 'completed', input_state: 'none', error: '' },
  }
  const metadata = vi.spyOn(api, 'pullRequestMetadata').mockResolvedValue({
    ...completed,
    title: initial.title,
    description: '',
    preparation_target: 'open',
    agent_session: { ...completed.agent_session!, status: 'running' },
  })
  renderPullRequestDetail()

  const readiness = within(await screen.findByRole('region', { name: 'Merge readiness' }))
  expect(await readiness.findByText('main is 3 commits ahead · Conflicts')).toBeInTheDocument()
  const conflictAction = readiness.getByRole('button', { name: 'Rebase and resolve conflicts' })
  expect(conflictAction).toBeEnabled()
  const conflictClass = conflictAction.parentElement?.className
  const reviewClass = screen.getByRole('button', { name: 'Run review' }).className

  pullRequest.mockResolvedValue({
    ...initial,
    title: completed.title,
    summary: completed.description,
    status: 'open',
    base_commit: 'fedcba98',
    publication_blocked: false,
  })
  metadata.mockResolvedValue(completed)

  expect(await screen.findByRole('heading', { name: completed.title, level: 1 }, { timeout: 4000 })).toBeInTheDocument()
  const staleConflictAction = readiness.getByRole('button', { name: 'Rebase and resolve conflicts' })
  expect(staleConflictAction).toBeDisabled()
  expect(staleConflictAction.parentElement?.className).toBe(conflictClass)
  expect(screen.getByRole('button', { name: 'Run review' }).className).toBe(reviewClass)
  expect(screen.queryByText('Checking whether rebase is needed…')).not.toBeInTheDocument()

  await act(async () => { finishReadinessRefresh(jsonResponse({
    base_commit: 'fedcba98',
    head_commit: 'def45678',
    base_commits_ahead: 3,
    branch_freshness: 'not_up_to_date',
    rebase_conflict_state: 'conflicting',
  })) })
  expect(await readiness.findByRole('button', { name: 'Rebase and resolve conflicts' })).toBeEnabled()
  expect(screen.getByRole('button', { name: 'Run review' }).className).toBe(reviewClass)
})

it('keeps Run review muted while an automatic rebase is reconciled', async () => {
  const user = userEvent.setup()
  let readinessRequest = 0
  let finishReadinessRefresh!: (response: Response) => void
  let finishRebase!: (response: Response) => void
  let rebaseStarted = false
  const readinessRefresh = new Promise<Response>((resolve) => { finishReadinessRefresh = resolve })
  const rebaseResponse = new Promise<Response>((resolve) => { finishRebase = resolve })
  const activeRebase = {
    id: 'rebase-1', pull_request_id: 'pr-1', kind: 'rebase', status: 'running', session_id: 'rebase-holon',
    head_commit: 'def45678', target_base_commit: 'abc12345', mechanical_only: false, created_at: new Date().toISOString(),
  } satisfies PullRequestRebase
  stubPullRequestDetail('open', {
    rebaseReadiness: () => ++readinessRequest === 1
      ? jsonResponse({ base_commit: 'abc12345', head_commit: 'def45678', base_commits_ahead: 3, branch_freshness: 'not_up_to_date', rebase_conflict_state: 'conflicting' })
      : readinessRefresh,
    rebaseMutation: () => {
      rebaseStarted = true
      return rebaseResponse
    },
  })
  vi.spyOn(api, 'pullRequestRebases').mockImplementation(async () => rebaseStarted ? [activeRebase] : [])
  renderPullRequestDetail()

  const readiness = within(await screen.findByRole('region', { name: 'Merge readiness' }))
  const resolve = await readiness.findByRole('button', { name: 'Rebase and resolve conflicts' })
  expect(readiness.getByText('main is 3 commits ahead · Conflicts')).toBeInTheDocument()
  const reviewClass = screen.getByRole('button', { name: 'Run review' }).className
  await user.click(resolve)
  expect(readiness.getByRole('button', { name: 'Resolving…' })).toBeDisabled()
  expect(screen.getByRole('button', { name: 'Run review' }).className).toBe(reviewClass)

  await act(async () => { finishRebase(jsonResponse(activeRebase, 201)) })
  expect(await screen.findByRole('link', { name: /Rebase holon/ })).toHaveAttribute('href', '/holons/rebase-holon')
  expect(readiness.queryByText('main is 3 commits ahead · Conflicts')).not.toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Run review' }).className).toBe(reviewClass)

  await act(async () => { finishReadinessRefresh(jsonResponse({
    base_commit: 'abc12345',
    head_commit: 'def45678',
    base_commits_ahead: 3,
    branch_freshness: 'not_up_to_date',
    rebase_conflict_state: 'conflicting',
  })) })
  expect(screen.getByRole('button', { name: 'Run review' }).className).toBe(reviewClass)
})

it('retries publication using the saved description without displaying an agent failure', async () => {
  const user = userEvent.setup()
  stubPullRequestDetail('wip', { publicationBlocked: false })
  const initial = await api.pullRequest('pr-1')
  const pullRequest = vi.spyOn(api, 'pullRequest').mockResolvedValue(initial)
  const saved: PullRequestMetadata = { view_revision: 1,
    pull_request_id: initial.id, title: initial.title, description: initial.summary, updated_at: initial.updated_at,
    generation_complete: true, preparation_target: 'open',
    agent_session: { id: 'metadata-holon-1', runtime_id: '', status: 'completed', input_state: 'none', error: '' },
  }
  const metadata = vi.spyOn(api, 'pullRequestMetadata').mockResolvedValue({ ...saved, application_error: { operation: 'publication', message: 'Publication failed.' } })
  const regenerate = vi.spyOn(api, 'startPullRequestMetadataAgent')
  const retry = vi.spyOn(api, 'retryPullRequestMetadata').mockImplementation(async () => {
    pullRequest.mockResolvedValue({ ...initial, status: 'open' })
    metadata.mockResolvedValue(saved)
    return saved
  })
  renderPullRequestDetail()
  expect(await screen.findByRole('alert')).toHaveTextContent('Publication failed.')
  expect(screen.getByText('Summary')).toBeInTheDocument()
  expect(screen.queryByRole('link', { name: /Metadata holon/ })).not.toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'Retry publication' }))
  expect(retry).toHaveBeenCalledWith('pr-1')
  expect(regenerate).not.toHaveBeenCalled()
  expect(await screen.findByRole('button', { name: /merge/i })).toBeInTheDocument()
  expect(screen.queryByText('Publication failed.')).not.toBeInTheDocument()
})

it('replaces a recoverable metadata failure with generated metadata', async () => {
  const user = userEvent.setup()
  stubPullRequestDetail('wip', {
    preparationTarget: 'wip',
    initialAgentSession: { id: 'metadata-holon-1', runtime_id: 'node-1', status: 'failed', input_state: 'none', error: 'Metadata output was not produced.' },
    metadataAgentCompletion: { title: 'Generated title', description: 'Generated description' },
  })
  renderPullRequestDetail()

  expect(await screen.findByText('Metadata output was not produced.')).toBeInTheDocument()
  expect(screen.getByText('Summary')).toBeInTheDocument()
  expect(screen.getByRole('link', { name: /Metadata holon/ })).toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'Retry' }))
  expect(await screen.findByRole('link', { name: /Metadata holon/ })).toBeInTheDocument()
  expect(screen.getByText('Summary')).toBeInTheDocument()
  expect(await screen.findByRole('heading', { name: 'Generated title', level: 1 }, { timeout: 6000 })).toBeInTheDocument()
  expect(screen.getByText('Generated description')).toBeInTheDocument()
  expect(screen.queryByRole('link', { name: /Metadata holon/ })).not.toBeInTheDocument()
})

it('edits the description in place and preserves the saved Markdown across navigation', async () => {
  const user = userEvent.setup()
  stubPullRequestDetail('open', { freshness: outdatedDescription })
  renderPullRequestDetail()

  expect(await screen.findByText(/Description may be outdated/)).toBeInTheDocument()
  await user.click(await screen.findByRole('button', { name: 'Edit description' }))
  const editor = screen.getByRole('textbox', { name: 'Description' })
  expect(editor).toHaveValue('Summary')
  expect(editor).toHaveFocus()
  await user.clear(editor)
  await user.type(editor, 'Updated **description**')
  await user.click(screen.getByRole('button', { name: 'Save' }))
  await waitFor(() => expect(screen.queryByRole('textbox', { name: 'Description' })).not.toBeInTheDocument())
  expect(screen.queryByText(/Description may be outdated/)).not.toBeInTheDocument()
  expect(within(screen.getByRole('region', { name: 'Pull request metadata' })).getByText('description').tagName).toBe('STRONG')

  await user.click(screen.getByRole('button', { name: /^Commits/ }))
  await user.click(screen.getByRole('button', { name: /^Overview/ }))
  await user.click(await screen.findByRole('button', { name: 'Edit description' }))
  expect(screen.getByRole('textbox', { name: 'Description' })).toHaveValue('Updated **description**')
  await user.clear(screen.getByRole('textbox', { name: 'Description' }))
  await user.type(screen.getByRole('textbox', { name: 'Description' }), 'Discard this edit')
  await user.click(screen.getByRole('button', { name: 'Cancel' }))
  await user.click(screen.getByRole('button', { name: 'Edit description' }))
  expect(screen.getByRole('textbox', { name: 'Description' })).toHaveValue('Updated **description**')
})

it('retains an unsaved description after a failed save and a parent refresh', async () => {
  const user = userEvent.setup()
  stubPullRequestDetail('open')
  const update = vi.spyOn(api, 'updatePullRequestMetadata').mockRejectedValueOnce(new Error('GitHub metadata update failed.'))
  renderPullRequestDetail()

  await user.click(await screen.findByRole('button', { name: 'Edit description' }))
  await user.clear(screen.getByRole('textbox', { name: 'Description' }))
  await user.type(screen.getByRole('textbox', { name: 'Description' }), 'Keep this draft')
  await user.click(screen.getByRole('button', { name: 'Save' }))
  expect(await screen.findByRole('alert')).toHaveTextContent('GitHub metadata update failed.')
  await user.click(screen.getByRole('button', { name: 'Pull request actions' }))
  await user.click(screen.getByRole('menuitem', { name: 'Refresh' }))
  expect(screen.getByRole('textbox', { name: 'Description' })).toHaveValue('Keep this draft')

  update.mockRestore()
  await user.click(screen.getByRole('button', { name: 'Save' }))
  expect(await screen.findByText('Keep this draft')).toBeInTheDocument()
  await waitFor(() => expect(screen.queryByRole('textbox', { name: 'Description' })).not.toBeInTheDocument())
})

it.each(['closed', 'merged'] as const)('keeps %s pull request comments read-only', async (status) => {
  const user = userEvent.setup()
  stubPullRequestDetail(status)
  renderPullRequestDetail()

  expect(await screen.findByText('Lifecycle work')).toBeInTheDocument()
  expect(within(screen.getByRole('banner', { name: 'Pull request header' })).getByText(new RegExp(status, 'i'))).toBeInTheDocument()
  expect(screen.getByText('Needs docs')).toBeInTheDocument()
  expect(screen.queryByLabelText('Comment')).not.toBeInTheDocument()
  expect(screen.queryByRole('button', { name: 'Comment actions' })).not.toBeInTheDocument()
  expect(screen.queryByRole('button', { name: 'Mark resolved' })).not.toBeInTheDocument()
  expect(screen.queryByRole('button', { name: 'Resolve automatically' })).not.toBeInTheDocument()
  if (status === 'closed') {
    expect(screen.getByRole('button', { name: 'Resolve all automatically' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Run review' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Reopen' })).toBeDisabled()
  } else {
    expect(screen.queryByText('Review & merge')).not.toBeInTheDocument()
  }
  await user.click(screen.getByRole('button', { name: 'Pull request actions' }))
  expect(screen.getAllByRole('menuitem')).toHaveLength(2)
  expect(screen.queryByRole('menuitem', { name: /Holons panel/ })).not.toBeInTheDocument()
  expect(screen.getByRole('menuitem', { name: 'Refresh' })).toBeEnabled()
  await user.keyboard('{Escape}')
  expect(screen.queryByLabelText('Title')).not.toBeInTheDocument()
  expect(screen.queryByLabelText('Description')).not.toBeInTheDocument()
  expect(screen.queryByRole('button', { name: 'Save changes' })).not.toBeInTheDocument()
  if (status === 'closed') {
    expect(screen.getByText('Reopen unavailable')).toBeInTheDocument()
  }
})

it('confirms unresolved comments in the merge dialog', async () => {
  const user = userEvent.setup()
  stubPullRequestDetail('open')
  const pullRequest = await api.pullRequest('pr-1')
  const merge = vi.spyOn(api, 'mergePullRequest').mockResolvedValue({ pull_request: { ...pullRequest, status: 'merged' }, merged_commit: 'merged-head' })
  renderPullRequestDetail()

  await screen.findByText('Lifecycle work')
  await user.click(screen.getByRole('button', { name: /merge/i }))
  const dialog = await screen.findByRole('dialog', { name: /merge/i })
  expect(within(dialog).getByText('Unresolved comments').parentElement).toHaveTextContent('1')
  expect(merge).not.toHaveBeenCalled()
  await user.click(within(dialog).getByRole('button', { name: /merge/i }))
  await waitFor(() => expect(merge).toHaveBeenCalledWith('pr-1', 'squash', true))
})

it.each(['succeeds', 'fails'])('completes merging while the post-merge refresh is delayed and then %s', async (outcome) => {
  const user = userEvent.setup()
  stubPullRequestDetail('open')
  const pullRequest = await api.pullRequest('pr-1')
  const merge = vi.spyOn(api, 'mergePullRequest').mockResolvedValue({
    pull_request: { ...pullRequest, view_revision: 2, status: 'merged' }, merged_commit: 'merged-head',
  })
  renderPullRequestDetail()
  await user.click(await screen.findByRole('button', { name: /merge/i }))
  const dialog = await screen.findByRole('dialog', { name: /merge/i })

  let finish!: (value: Awaited<ReturnType<typeof api.pullRequestWorkers>>) => void
  let fail!: (error: Error) => void
  const pending = new Promise<Awaited<ReturnType<typeof api.pullRequestWorkers>>>((resolve, reject) => {
    finish = resolve
    fail = reject
  })
  const workers = vi.spyOn(api, 'pullRequestWorkers').mockReturnValueOnce(pending).mockResolvedValue([])
  // An older cached response must not undo the accepted merge either.
  const read = vi.spyOn(api, 'pullRequest').mockResolvedValue(pullRequest)
  await user.click(within(dialog).getByRole('button', { name: /merge/i }))
  await waitFor(() => expect(workers).toHaveBeenCalled())
  expect(merge).toHaveBeenCalledTimes(1)
  expect(within(screen.getByRole('banner', { name: 'Pull request header' })).getByText('Merged')).toBeInTheDocument()
  expect(screen.queryByRole('dialog', { name: /merge/i })).not.toBeInTheDocument()
  expect(screen.queryByText('Merging...')).not.toBeInTheDocument()

  // Clearing the merge guard restores lifecycle polling before refresh settles.
  const readsBeforeFocus = read.mock.calls.length
  fireEvent.focus(window)
  await waitFor(() => expect(read.mock.calls.length).toBeGreaterThan(readsBeforeFocus))
  await act(async () => {
    if (outcome === 'fails') fail(new Error('Post-merge refresh failed'))
    else finish([])
  })
  expect(within(screen.getByRole('banner', { name: 'Pull request header' })).getByText('Merged')).toBeInTheDocument()
  expect(screen.queryByRole('dialog', { name: /merge/i })).not.toBeInTheDocument()
  expect(screen.queryByText('Post-merge refresh failed')).not.toBeInTheDocument()
})

it.each([
  { status: 'wip' as const, action: 'Move to draft', target: 'draft' },
  { status: 'draft' as const, action: /open/i, target: 'open' },
])('uses the $status primary lifecycle action and refreshes the complete pull request', async ({ status, action, target }) => {
  const user = userEvent.setup()
  const fetchMock = stubPullRequestDetail(status)
  renderPullRequestDetail()

  await user.click(await screen.findByRole('button', { name: action }))

  await waitFor(() => expect(fetchMock).toHaveBeenCalledWith('/api/v1/pull-requests/pr-1/transition', expect.objectContaining({
    method: 'POST',
    body: JSON.stringify({ status: target }),
  })))
  expect(await within(screen.getByRole('banner', { name: 'Pull request header' })).findByText(target === 'wip' ? 'WIP' : target[0].toUpperCase() + target.slice(1))).toBeInTheDocument()
  expect(fetchMock.mock.calls.filter(([input]) => String(input).endsWith('/pull-requests/pr-1'))).toHaveLength(2)
})

it.each(['wip', 'draft', 'open'] as const)('confirms Close from the %s lifecycle menu', async (status) => {
  const user = userEvent.setup()
  const fetchMock = stubPullRequestDetail(status)
  renderPullRequestDetail()

  await user.click(await screen.findByRole('button', { name: 'Pull request actions' }))
  await user.click(screen.getByRole('menuitem', { name: 'Close pull request' }))

  expect(screen.getByRole('dialog', { name: 'Close pull request' })).toHaveTextContent('Holark cannot currently reopen this pull request.')
  await user.click(screen.getByRole('button', { name: 'Close' }))

  await waitFor(() => expect(fetchMock).toHaveBeenCalledWith('/api/v1/pull-requests/pr-1/transition', expect.objectContaining({
    body: JSON.stringify({ status: 'closed' }),
  })))
  expect(await screen.findByText('Closed')).toBeInTheDocument()
})

it.each([
  { status: 'wip' as const, action: /open/i, target: 'open' },
  { status: 'draft' as const, action: /wip/i, target: 'wip' },
  { status: 'open' as const, action: /draft/i, target: 'draft' },
])('transitions from $status to $target through the actions menu', async ({ status, action, target }) => {
  const user = userEvent.setup()
  const fetchMock = stubPullRequestDetail(status)
  renderPullRequestDetail()

  await user.click(await screen.findByRole('button', { name: 'Pull request actions' }))
  await user.click(screen.getByRole('menuitem', { name: action }))

  await waitFor(() => expect(fetchMock).toHaveBeenCalledWith('/api/v1/pull-requests/pr-1/transition', expect.objectContaining({
    body: JSON.stringify({ status: target }),
  })))
  expect(await within(screen.getByRole('banner', { name: 'Pull request header' })).findByText(target === 'wip' ? 'WIP' : target[0].toUpperCase() + target.slice(1))).toBeInTheDocument()
})

it('prevents duplicate lifecycle transitions while a request is pending', async () => {
  const user = userEvent.setup()
  let finishTransition: () => void = () => undefined
  const transitionDelay = new Promise<void>((resolve) => { finishTransition = resolve })
  const fetchMock = stubPullRequestDetail('wip', { transitionDelay })
  renderPullRequestDetail()

  const primary = await screen.findByRole('button', { name: 'Move to draft' })
  await user.click(primary)
  expect(primary).toBeDisabled()
  await user.click(screen.getByRole('button', { name: 'Pull request actions' }))
  expect(screen.getByRole('menuitem', { name: 'Open PR' })).toBeDisabled()
  expect(screen.getByRole('menuitem', { name: 'Close pull request' })).toBeDisabled()
  await user.click(screen.getByRole('menuitem', { name: 'Open PR' }))
  await user.keyboard('{Escape}')
  await user.click(primary)
  expect(fetchMock.mock.calls.filter(([input]) => String(input).endsWith('/transition'))).toHaveLength(1)

  finishTransition()
  expect(await screen.findByText('Draft')).toBeInTheDocument()
})

it('keeps lifecycle state unchanged and reports transition failures', async () => {
  const user = userEvent.setup()
  stubPullRequestDetail('wip', { transitionFailure: 'Lifecycle transition failed.' })
  renderPullRequestDetail()

  await user.click(await screen.findByRole('button', { name: 'Move to draft' }))

  expect(await screen.findByText('Lifecycle transition failed.')).toBeInTheDocument()
  expect(screen.getByText('WIP')).toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Move to draft' })).toBeEnabled()
})

it('renders GitHub pull request commits and patches from clean detail endpoints', async () => {
  const user = userEvent.setup()
  const now = new Date().toISOString()
  const calls: string[] = []
  const fetchMock = vi.fn<(input: RequestInfo | URL, init?: RequestInit) => Promise<Response>>(async (input) => {
    const path = String(input)
    calls.push(path)
    if (path.endsWith('/pull-requests/pr-gh')) {
      return new Response(JSON.stringify({ view_revision: 1,
        id: 'pr-gh', repository_id: 'holark', title: 'From GitHub', summary: 'Imported', base_branch: 'main', base_commit: 'abc12345', head_branch: 'feature', head_commit: 'def45678', status: 'open',
        sync_provider: 'github', sync_external_id: 'github:pmaviro/holark#42', sync_data: { github: { url: 'https://github.com/pmaviro/holark/pull/42', number: 42 } },
        linked_session_ids: [], created_at: now, updated_at: now,
      }), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    if (path.endsWith('/pull-requests/pr-gh/comments')) {
      return new Response(JSON.stringify([]), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    if (path.endsWith('/pull-requests/pr-gh/workers')) {
      return new Response(JSON.stringify([]), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    if (path.endsWith('/pull-requests/pr-gh/reviews')) {
      return new Response(JSON.stringify([]), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    if (path.endsWith('/pull-requests/pr-gh/commits')) {
      return new Response(JSON.stringify({ inputs: { head_commit: 'def45678', diff_base_commit: 'abc12345' }, data: [{ sha: 'def45678', message: 'Ship real diff\n\nBody', author_name: 'Mona', authored_at: now, external_url: 'https://github.com/pmaviro/holark/commit/def45678' }] }), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    if (path.endsWith('/pull-requests/pr-gh/changes?path=src%2Flate.ts')) {
      return new Response(JSON.stringify({ inputs: { head_commit: 'def45678', diff_base_commit: 'abc12345' }, data: {
        branch: 'feature', base_branch: 'main', base_commit: 'abc12345', head_commit: 'def45678', has_changes: true, dirty: false, diff_truncated: false,
        files: [{ path: 'src/late.ts', status: 'added', additions: 1, deletions: 0, binary: false, diff_truncated: false, diff: 'diff --git a/src/late.ts b/src/late.ts\n--- /dev/null\n+++ b/src/late.ts\n@@ -0,0 +1 @@\n+late only' }],
      } }), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    if (path.endsWith('/pull-requests/pr-gh/changes')) {
      return new Response(JSON.stringify({ inputs: { head_commit: 'def45678', diff_base_commit: 'abc12345' }, data: {
        branch: 'feature', base_branch: 'main', base_commit: 'abc12345', head_commit: 'def45678', has_changes: true, dirty: false, diff_truncated: true,
        files: [
          { path: 'README.md', status: 'modified', additions: 1, deletions: 1, binary: false, diff_truncated: false, diff: 'diff --git a/README.md b/README.md\n--- a/README.md\n+++ b/README.md\n@@ -1 +1 @@\n-old\n+new' },
          { path: 'src/late.ts', status: 'added', additions: 1, deletions: 0, binary: false, diff_truncated: true, diff: '' },
        ],
      } }), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    return new Response(JSON.stringify({ code: 'not_found', message: 'Not found.' }), { status: 404, headers: { 'Content-Type': 'application/json' } })
  })
  vi.stubGlobal('fetch', fetchMock)

  render(
    <MemoryRouter initialEntries={['/pulls/pr-gh']}>
      <ProjectContext.Provider value={{ id: 'holark', name: 'Holark', default_branch: 'main' }}>
        <Routes><Route path="/pulls/:pullRequestId" element={<PullRequestDetail />} /></Routes>
      </ProjectContext.Provider>
    </MemoryRouter>,
  )

  expect(await screen.findByText('From GitHub')).toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: /^Commits/ }))
  expect(await screen.findByText('Ship real diff')).toBeInTheDocument()
  expect(screen.getByText(/Mona ·/)).toBeInTheDocument()
  expect(screen.queryByRole('button', { name: /^Comment on / })).not.toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: /^Changes/ }))
  await waitFor(() => expect(screen.getAllByText('README.md')).toHaveLength(2))
  expect(within(screen.getByRole('region', { name: 'Pull request file diffs' })).getByText('new')).toBeInTheDocument()
  expect(await screen.findByText('late only')).toBeInTheDocument()
  const search = screen.getByRole('searchbox', { name: 'Search paths and changed lines' })
  await user.type(search, 'late only')
  expect(within(screen.getByLabelText('Changed files')).getByRole('status')).toHaveTextContent('1 matching file')
  expect(within(screen.getByRole('region', { name: 'Pull request file diffs' })).getAllByRole('article')).toHaveLength(2)
  expect(calls).toContain('/api/v1/pull-requests/pr-gh/changes?path=src%2Flate.ts')
  expect(calls).toEqual(expect.arrayContaining(['/api/v1/pull-requests/pr-gh', '/api/v1/pull-requests/pr-gh/comments', '/api/v1/pull-requests/pr-gh/commits', '/api/v1/pull-requests/pr-gh/changes']))
})

it('keeps the pull request overview available when diff refs are missing', async () => {
  const now = new Date().toISOString()
  const fetchMock = vi.fn<(input: RequestInfo | URL) => Promise<Response>>(async (input) => {
    const path = String(input)
    if (path.endsWith('/pull-requests/pr-gh')) {
      return new Response(JSON.stringify({ view_revision: 1,
        id: 'pr-gh', repository_id: 'holark', title: 'Prompt settings page', summary: '**Imported**\n\n- Keeps overview focused', base_branch: 'main', base_commit: 'abc12345', head_branch: 'feature', head_commit: 'def45678', status: 'open',
        sync_provider: 'github', sync_external_id: 'github:pmaviro/holark#42', sync_data: { github: { url: 'https://github.com/pmaviro/holark/pull/42', number: 42 } },
        linked_session_ids: [], created_at: now, updated_at: now,
      }), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    if (path.endsWith('/pull-requests/pr-gh/comments') || path.endsWith('/pull-requests/pr-gh/reviews')) {
      return new Response(JSON.stringify([]), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    if (path.endsWith('/pull-requests/pr-gh/commits') || path.endsWith('/pull-requests/pr-gh/changes')) {
      return new Response(JSON.stringify({ code: 'ref_not_found', message: 'Reference not found.' }), { status: 404, headers: { 'Content-Type': 'application/json' } })
    }
    return new Response(JSON.stringify({ code: 'not_found', message: 'Not found.' }), { status: 404, headers: { 'Content-Type': 'application/json' } })
  })
  vi.stubGlobal('fetch', fetchMock)

  render(
    <MemoryRouter initialEntries={['/pulls/pr-gh']}>
      <ProjectContext.Provider value={{ id: 'holark', name: 'Holark', default_branch: 'main' }}>
        <Routes><Route path="/pulls/:pullRequestId" element={<PullRequestDetail />} /></Routes>
      </ProjectContext.Provider>
    </MemoryRouter>,
  )

  expect(await screen.findByText('Prompt settings page')).toBeInTheDocument()
  expect(await screen.findByText('Imported')).toBeInTheDocument()
  expect(screen.getByText('#42')).toBeInTheDocument()
  expect(screen.getByText('Reference not found.')).toBeInTheDocument()
})

it.each(['pull_request', 'file', 'line'])('adds, replies, edits, deletes and resolves %s comments', async (scope) => {
  const user = userEvent.setup()
  const now = new Date().toISOString()
  let comments = [{
    id: 'comment-1', pull_request_id: 'pr-1', body: 'Check the edge case', scope, path: scope !== 'pull_request' ? 'README.md' : undefined, side: scope === 'line' ? 'RIGHT' : undefined, line: scope === 'line' ? 2 : undefined,
    original_head_commit: 'def45678', publication_state: 'published', author_type: 'user', status: 'unresolved', created_at: now, updated_at: now,
  }]
  const commentRequests: CreatePullRequestCommentRequest[] = []
  const fetchMock = vi.fn<(input: RequestInfo | URL, init?: RequestInit) => Promise<Response>>(async (input, init) => {
    const path = String(input)
    if (path.endsWith('/pull-requests/pr-1') || path.endsWith('/pull-requests/pr-1/github-readiness')) {
      return new Response(JSON.stringify({ view_revision: 1,
        id: 'pr-1', repository_id: 'holark', title: 'Local PR', summary: 'Summary', base_branch: 'main', base_commit: 'abc12345', head_branch: 'feature', head_commit: 'def45678', status: 'open',
        sync_data: {}, linked_session_ids: [], created_at: now, updated_at: now,
      }), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    if (path.endsWith('/pull-requests/pr-1/reviews') || path.endsWith('/pull-requests/pr-1/manual-reviews')) {
      return new Response(JSON.stringify([]), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    if (path.endsWith('/pull-requests/pr-1/comments') && init?.method === 'POST') {
      const body = JSON.parse(String(init.body)) as CreatePullRequestCommentRequest
      commentRequests.push(body)
      const created = {
        id: body.parent_comment_id ? 'reply-1' : 'comment-2', pull_request_id: 'pr-1', body: body.body, scope, path: scope !== 'pull_request' ? 'README.md' : undefined, side: scope === 'line' ? 'RIGHT' : undefined, line: scope === 'line' ? 2 : undefined,
        original_head_commit: 'def45678', publication_state: body.draft ? 'draft' : 'published', author_type: 'user', status: body.parent_comment_id ? 'resolved' : 'unresolved', created_at: now, updated_at: now,
        ...(body.parent_comment_id ? { parent_comment_id: body.parent_comment_id } : {}),
      }
      comments = [...comments, created]
      return new Response(JSON.stringify(created), { status: 201, headers: { 'Content-Type': 'application/json' } })
    }
    if (path.endsWith('/pull-requests/pr-1/comments')) {
      return new Response(JSON.stringify(comments), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    if (path.endsWith('/pull-requests/pr-1/workers')) {
      return new Response(JSON.stringify([]), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    if (path.endsWith('/pull-request-comments/comment-1/resolve')) {
      comments = comments.map((comment) => comment.id === 'comment-1' ? { ...comment, status: 'resolved', resolved_at: now, updated_at: now } : comment)
      return new Response(JSON.stringify(comments[0]), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    if (path.endsWith('/pull-request-comments/comment-2') && init?.method === 'PATCH') {
      const body = JSON.parse(String(init.body)) as { body: string }
      comments = comments.map((comment) => comment.id === 'comment-2' ? { ...comment, body: body.body, updated_at: now } : comment)
      return new Response(JSON.stringify(comments.find((comment) => comment.id === 'comment-2')), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    if (path.endsWith('/pull-request-comments/comment-2') && init?.method === 'DELETE') {
      comments = comments.filter((comment) => comment.id !== 'comment-2')
      return new Response(JSON.stringify({ deleted: true }), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    if (path.endsWith('/pull-requests/pr-1/commits')) {
      return new Response(JSON.stringify({ inputs: { head_commit: 'def45678', diff_base_commit: 'abc12345' }, data: [] }), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    if (path.endsWith('/pull-requests/pr-1/changes')) {
      return new Response(JSON.stringify({ inputs: { head_commit: 'def45678', diff_base_commit: 'abc12345' }, data: {
        branch: 'feature', base_branch: 'main', base_commit: 'abc12345', head_commit: 'def45678', has_changes: true, dirty: false, diff_truncated: false,
        files: [{ path: 'README.md', status: 'modified', additions: 2, deletions: 1, binary: false, diff_truncated: false, diff: 'diff --git a/README.md b/README.md' }],
      } }), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    return new Response(JSON.stringify({ code: 'not_found', message: 'Not found.' }), { status: 404, headers: { 'Content-Type': 'application/json' } })
  })
  vi.stubGlobal('fetch', fetchMock)

  render(
    <MemoryRouter initialEntries={['/pulls/pr-1']}>
      <ProjectContext.Provider value={{ id: 'holark', name: 'Holark', default_branch: 'main' }}>
        <Routes><Route path="/pulls/:pullRequestId" element={<PullRequestDetail />} /></Routes>
      </ProjectContext.Provider>
    </MemoryRouter>,
  )

  expect(await screen.findByText('Check the edge case')).toBeInTheDocument()
  expect(screen.getByText('1 unresolved conversation')).toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Reply' })).toBeDisabled()
  await user.type(screen.getByLabelText('Reply to comment: Check the edge case'), 'Handled in the latest change')
  await user.click(screen.getByRole('button', { name: 'Reply' }))
  expect(await screen.findByText('Handled in the latest change')).toBeInTheDocument()
  expect(commentRequests[0]).toEqual({ body: 'Handled in the latest change', parent_comment_id: 'comment-1', draft: true })
  expect(screen.getByText('1 unresolved conversation')).toBeInTheDocument()
  await user.type(screen.getByLabelText('Comment'), 'Add docs too')
  await user.click(screen.getByRole('button', { name: 'Save draft' }))
  expect(await screen.findByText('Add docs too')).toBeInTheDocument()
  const originalThread = within(screen.getByText('Check the edge case').closest('article')!)
  await user.click(originalThread.getByRole('button', { name: /^Mark resolved$/i }))
  expect(await screen.findByText('Resolved')).toBeInTheDocument()
  expect(originalThread.getByRole('button', { name: /reopen/i })).toBeEnabled()

  const newThread = within(screen.getByText('Add docs too').closest('article')!)
  await user.click(newThread.getByRole('button', { name: 'Comment actions' }))
  await user.click(screen.getByRole('menuitem', { name: 'Edit' }))
  const editInput = screen.getByLabelText('Edit comment')
  await user.clear(editInput)
  await user.type(editInput, 'Updated docs note')
  await user.click(screen.getByRole('button', { name: 'Save' }))
  expect(await screen.findByText('Updated docs note')).toBeInTheDocument()

  vi.stubGlobal('confirm', vi.fn(() => true))
  await user.click(newThread.getByRole('button', { name: 'Comment actions' }))
  await user.click(screen.getByRole('menuitem', { name: 'Delete' }))
  await waitFor(() => expect(screen.queryByText('Updated docs note')).not.toBeInTheDocument())
})

it('preserves a new reply draft when an earlier reply finishes', async () => {
  const user = userEvent.setup()
  const now = new Date().toISOString()
  let finishReply!: (response: Response) => void
  const pendingReply = new Promise<Response>((resolve) => { finishReply = resolve })
  const comments = [
    {
      id: 'comment-1', pull_request_id: 'pr-1', body: 'First comment', scope: 'pull_request',
      original_head_commit: 'def45678', status: 'unresolved', author_type: 'user', created_at: now, updated_at: now,
    },
    {
      id: 'comment-2', pull_request_id: 'pr-1', body: 'Second comment', scope: 'pull_request',
      original_head_commit: 'def45678', status: 'unresolved', author_type: 'user', created_at: now, updated_at: now,
    },
  ]
  stubPullRequestDetail('open', {
    comments,
    commentMutation: async () => pendingReply,
  })
  renderPullRequestDetail()

  expect(await screen.findByText('First comment')).toBeInTheDocument()
  await user.type(screen.getByLabelText('Reply to comment: First comment'), 'Reply A')
  await user.click(within(screen.getByText('First comment').closest('article')!).getByRole('button', { name: 'Reply' }))
  const secondReply = screen.getByLabelText('Reply to comment: Second comment')
  await user.type(secondReply, 'Draft B')

  finishReply(jsonResponse({
    id: 'reply-1', pull_request_id: 'pr-1', parent_comment_id: 'comment-1', body: 'Reply A', scope: 'pull_request',
    original_head_commit: 'def45678', status: 'resolved', author_type: 'user', created_at: now, updated_at: now,
  }, 201))

  await waitFor(() => expect(within(secondReply.closest('article')!).getByRole('button', { name: 'Reply' })).toBeEnabled())
  expect(secondReply).toHaveValue('Draft B')
})

it.each(['pull_request', 'file', 'line'])('resolves individual and all unresolved %s conversations', async (scope) => {
  const user = userEvent.setup()
  const now = new Date().toISOString()
  const workerRequests: Array<{ comment_ids: string[], mode: string }> = []
  const comments = [
    {
      id: 'comment-1', pull_request_id: 'pr-1', body: 'First comment', scope, path: scope !== 'pull_request' ? 'README.md' : undefined, side: scope === 'line' ? 'RIGHT' : undefined, line: scope === 'line' ? 2 : undefined,
      original_head_commit: 'def45678', status: 'unresolved', author_type: 'user', created_at: now, updated_at: now,
    },
    {
      id: 'comment-2', pull_request_id: 'pr-1', body: 'Second comment', scope, path: scope !== 'pull_request' ? 'README.md' : undefined, side: scope === 'line' ? 'RIGHT' : undefined, line: scope === 'line' ? 2 : undefined,
      original_head_commit: 'def45678', status: 'unresolved', author_type: 'agent', created_at: now, updated_at: now,
    },
    {
      id: 'comment-3', pull_request_id: 'pr-1', body: 'Already done', scope, path: scope !== 'pull_request' ? 'README.md' : undefined, side: scope === 'line' ? 'RIGHT' : undefined, line: scope === 'line' ? 2 : undefined,
      original_head_commit: 'def45678', status: 'resolved', author_type: 'user', created_at: now, updated_at: now, resolved_at: now,
    },
  ]
  const fetchMock = vi.fn<(input: RequestInfo | URL, init?: RequestInit) => Promise<Response>>(async (input, init) => {
    const path = String(input)
    if (path.endsWith('/pull-requests/pr-1') || path.endsWith('/pull-requests/pr-1/github-readiness')) {
      return new Response(JSON.stringify({ view_revision: 1,
        id: 'pr-1', repository_id: 'holark', title: 'Local PR', summary: 'Summary', base_branch: 'main', base_commit: 'abc12345', head_branch: 'feature', head_commit: 'def45678', status: 'open',
        sync_data: {}, linked_session_ids: [], created_at: now, updated_at: now,
      }), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    if (path.endsWith('/pull-requests/pr-1/comments')) {
      return new Response(JSON.stringify(comments), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    if (path.endsWith('/pull-requests/pr-1/workers') && init?.method === 'POST') {
      const body = JSON.parse(String(init.body)) as { comment_ids: string[], mode: string }
      workerRequests.push(body)
      return new Response(JSON.stringify(body.comment_ids.map((commentId) => ({
        id: `worker-${commentId}`, pull_request_id: 'pr-1', comment_id: commentId, session_id: `holon-${commentId}`, mode: body.mode, status: 'queued', base_head_commit: 'def45678', created_at: now,
      }))), { status: 201, headers: { 'Content-Type': 'application/json' } })
    }
    if (path.endsWith('/pull-requests/pr-1/workers')) {
      return new Response(JSON.stringify([]), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    if (path.endsWith('/pull-requests/pr-1/reviews')) {
      return new Response(JSON.stringify([]), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    if (path.endsWith('/pull-requests/pr-1/commits')) {
      return new Response(JSON.stringify({ inputs: { head_commit: 'def45678', diff_base_commit: 'abc12345' }, data: [] }), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    if (path.endsWith('/pull-requests/pr-1/changes')) {
      return new Response(JSON.stringify({ inputs: { head_commit: 'def45678', diff_base_commit: 'abc12345' }, data: { branch: 'feature', base_branch: 'main', base_commit: 'abc12345', head_commit: 'def45678', has_changes: true, dirty: false, diff_truncated: false, files: [] } }), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    return new Response(JSON.stringify({ code: 'not_found', message: 'Not found.' }), { status: 404, headers: { 'Content-Type': 'application/json' } })
  })
  vi.stubGlobal('fetch', fetchMock)

  render(
    <MemoryRouter initialEntries={['/pulls/pr-1']}>
      <ProjectContext.Provider value={{ id: 'holark', name: 'Holark', default_branch: 'main' }}>
        <Routes><Route path="/pulls/:pullRequestId" element={<PullRequestDetail />} /></Routes>
      </ProjectContext.Provider>
    </MemoryRouter>,
  )

  expect(await screen.findByText('First comment')).toBeInTheDocument()
  const firstThread = within(screen.getByText('First comment').closest('article')!)
  await user.click(firstThread.getByRole('button', { name: /^Resolve automatically$/i }))
  await waitFor(() => expect(workerRequests[0]).toMatchObject({ comment_ids: ['comment-1'], mode: 'auto' }))

  await user.click(screen.getByRole('button', { name: /resolve all.*options/i }))
  await user.click(screen.getByRole('menuitem', { name: /assisted/i }))
  await waitFor(() => expect(workerRequests[1]).toMatchObject({ comment_ids: ['comment-1', 'comment-2'], mode: 'assisted' }))
  expect(within(screen.getByLabelText('Pull request content')).getByText('2 Address requests: 2 queued.')).toHaveAttribute('role', 'status')
})

it.each([
  { startupMode: 'agent', prompt: 'Address the remaining feedback' },
  { startupMode: 'agent', prompt: '' },
  { startupMode: 'agent', prompt: '   ' },
  { startupMode: 'terminal', prompt: '' },
] as const)('submits the PR dialog with $startupMode startup and task "$prompt" and opens the worker holon', async ({ startupMode, prompt }) => {
  const user = userEvent.setup()
  const now = new Date().toISOString()
  const workerRequests: unknown[] = []
  const fetchMock = vi.fn<(input: RequestInfo | URL, init?: RequestInit) => Promise<Response>>(async (input, init) => {
    const path = String(input)
    if (path.endsWith('/agent-capabilities')) {
      return jsonResponse({ capabilities: [{ type: 'codex', available: true }] })
    }
    if (path.endsWith('/holons/holon-continue')) {
      return jsonResponse({ id: 'holon-continue', repository_id: 'holark', prompt, status: 'running', created_at: now })
    }
    if (path.endsWith('/pull-requests/pr-1') || path.endsWith('/pull-requests/pr-1/github-readiness')) {
      return new Response(JSON.stringify({ view_revision: 1,
        id: 'pr-1', repository_id: 'holark', title: 'Local PR', summary: 'Summary', base_branch: 'main', base_commit: 'abc12345', head_branch: 'feature', head_commit: 'def45678', status: 'open',
        sync_data: {}, linked_session_ids: [], created_at: now, updated_at: now,
      }), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    if (path.endsWith('/pull-requests/pr-1/workers') && init?.method === 'POST') {
      workerRequests.push(JSON.parse(String(init.body)))
      return new Response(JSON.stringify([{ id: 'worker-continue', pull_request_id: 'pr-1', session_id: 'holon-continue', mode: 'continue', status: 'running', base_head_commit: 'def45678', created_at: now }]), { status: 201, headers: { 'Content-Type': 'application/json' } })
    }
    if (path.endsWith('/pull-requests/pr-1/commits')) return jsonResponse({ inputs: { head_commit: 'def45678', diff_base_commit: 'abc12345' }, data: [] })
    if (path.endsWith('/pull-requests/pr-1/comments') || path.endsWith('/pull-requests/pr-1/workers') || path.endsWith('/pull-requests/pr-1/reviews')) {
      return new Response(JSON.stringify([]), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    if (path.endsWith('/pull-requests/pr-1/changes')) {
      return new Response(JSON.stringify({ inputs: { head_commit: 'def45678', diff_base_commit: 'abc12345' }, data: { branch: 'feature', base_branch: 'main', base_commit: 'abc12345', head_commit: 'def45678', has_changes: true, dirty: false, diff_truncated: false, files: [] } }), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    return new Response(JSON.stringify({ code: 'not_found', message: 'Not found.' }), { status: 404, headers: { 'Content-Type': 'application/json' } })
  })
  vi.stubGlobal('fetch', fetchMock)

  render(
    <MemoryRouter initialEntries={['/pulls/pr-1']}>
      <ProjectContext.Provider value={{ id: 'holark', name: 'Holark', default_branch: 'main' }}>
        <Routes>
          <Route path="/pulls/:pullRequestId" element={<PullRequestDetail />} />
          <Route path="/holons/holon-continue" element={<div>Worker terminal</div>} />
        </Routes>
      </ProjectContext.Provider>
    </MemoryRouter>,
  )

  await screen.findByText('Local PR')
  await user.click(screen.getByRole('button', { name: 'Pull request actions' }))
  await user.click(screen.getByRole('menuitem', { name: /new holon/i }))

  const dialog = within(await screen.findByRole('dialog', { name: 'New Holon from this PR' }))
  expect(workerRequests).toHaveLength(0)
  expect(dialog.getByText('feature')).toBeInTheDocument()
  if (startupMode === 'terminal') {
    await user.click(dialog.getByRole('combobox', { name: 'Start with' }))
    await user.click(screen.getByRole('option', { name: 'Terminal' }))
    expect(dialog.getByLabelText('Task (optional)')).toHaveValue('')
  } else if (prompt) {
    await user.type(dialog.getByLabelText('Task'), prompt)
  }
  const submit = dialog.getByRole('button', { name: 'Create Holon' })
  await waitFor(() => expect(submit).toBeEnabled())
  await user.click(submit)

  expect(await screen.findByText('Worker terminal')).toBeInTheDocument()
  expect(workerRequests).toEqual([{
    comment_ids: [], mode: 'continue', prompt: prompt.trim(),
    startup: startupMode === 'terminal' ? { startup_mode: 'terminal' } : { agent_type: 'codex', startup_mode: 'agent' },
  }])
})

it.each(['assisted', 'auto'])('starts a %s review without opening the agent window', async (mode) => {
  const user = userEvent.setup()
  const now = new Date().toISOString()
  let reviews: unknown[] = []
  const fetchMock = vi.fn<(input: RequestInfo | URL, init?: RequestInit) => Promise<Response>>(async (input, init) => {
    const path = String(input)
    if (path.endsWith('/pull-requests/pr-1') || path.endsWith('/pull-requests/pr-1/github-readiness')) {
      return new Response(JSON.stringify({ view_revision: 1,
        id: 'pr-1', repository_id: 'holark', title: 'Local PR', summary: 'Summary', base_branch: 'main', base_commit: 'abc12345', head_branch: 'feature', head_commit: 'def45678', status: 'open',
        sync_data: {}, linked_session_ids: [], created_at: now, updated_at: now,
      }), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    if (path.endsWith('/pull-requests/pr-1/reviews') && init?.method === 'POST') {
      reviews = [{ id: 'review-1', pull_request_id: 'pr-1', session_id: 'holon-review', status: 'running', base_branch: 'main', base_commit: 'abc12345', head_branch: 'feature', head_commit: 'def45678', created_at: now }]
      return new Response(JSON.stringify(reviews[0]), { status: 201, headers: { 'Content-Type': 'application/json' } })
    }
    if (path.endsWith('/pull-requests/pr-1/reviews')) {
      return new Response(JSON.stringify(reviews), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    if (path.endsWith('/pull-requests/pr-1/comments')) {
      return new Response(JSON.stringify([]), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    if (path.endsWith('/pull-requests/pr-1/workers')) {
      return new Response(JSON.stringify([]), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    if (path.endsWith('/pull-requests/pr-1/commits')) {
      return new Response(JSON.stringify({ inputs: { head_commit: 'def45678', diff_base_commit: 'abc12345' }, data: [] }), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    if (path.endsWith('/pull-requests/pr-1/changes')) {
      return new Response(JSON.stringify({ inputs: { head_commit: 'def45678', diff_base_commit: 'abc12345' }, data: { branch: 'feature', base_branch: 'main', base_commit: 'abc12345', head_commit: 'def45678', has_changes: true, dirty: false, diff_truncated: false, files: [] } }), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    return new Response(JSON.stringify({ code: 'not_found', message: 'Not found.' }), { status: 404, headers: { 'Content-Type': 'application/json' } })
  })
  vi.stubGlobal('fetch', fetchMock)

  render(
    <MemoryRouter initialEntries={['/pulls/pr-1']}>
      <ProjectContext.Provider value={{ id: 'holark', name: 'Holark', default_branch: 'main' }}>
        <Routes>
          <Route path="/pulls/:pullRequestId" element={<PullRequestDetail />} />
          <Route path="/holons/:holonId" element={<div>Agent page</div>} />
        </Routes>
      </ProjectContext.Provider>
    </MemoryRouter>,
  )

  expect(await screen.findByText('Local PR')).toBeInTheDocument()
  const changeRequestsBeforeReview = fetchMock.mock.calls.filter(([input]) => String(input).endsWith('/pull-requests/pr-1/changes')).length
  const commitRequestsBeforeReview = fetchMock.mock.calls.filter(([input]) => String(input).endsWith('/pull-requests/pr-1/commits')).length
  if (mode === 'auto') {
    await user.click(screen.getByRole('button', { name: 'Review options' }))
    await user.click(screen.getByRole('menuitem', { name: 'Run review and publish automatically' }))
  } else {
    await user.click(screen.getByRole('button', { name: 'Run review' }))
  }
  expect(screen.queryByText('Loading pull request...')).not.toBeInTheDocument()

  await waitFor(() => expect(fetchMock).toHaveBeenCalledWith('/api/v1/pull-requests/pr-1/reviews', expect.objectContaining({ method: 'POST', body: JSON.stringify({ mode }) })))
  expect(fetchMock.mock.calls.filter(([input]) => String(input).endsWith('/pull-requests/pr-1/changes'))).toHaveLength(changeRequestsBeforeReview)
  expect(fetchMock.mock.calls.filter(([input]) => String(input).endsWith('/pull-requests/pr-1/commits'))).toHaveLength(commitRequestsBeforeReview)
  expect(screen.queryByText('Agent page')).not.toBeInTheDocument()
  expect(await screen.findByText('Local PR')).toBeInTheDocument()
})

it.each([
  { changed: 'head', includeDraft: false },
  { changed: 'comparison', includeDraft: true },
] as const)('recovers an uncertain approval with its original ID after the PR $changed changes', async ({ changed, includeDraft }) => {
  const user = userEvent.setup()
  const now = new Date().toISOString()
  const draft: PullRequestComment = {
    id: 'manual-draft', pull_request_id: 'pr-1', body: 'Reviewed finding', scope: 'pull_request',
    original_head_commit: 'def45678', status: 'unresolved', author_type: 'user', publication_state: 'draft', created_at: now, updated_at: now,
  }
  stubPullRequestDetail('open', { syncProvider: 'github', comments: includeDraft ? [draft] : [] })
  let current = await api.pullRequest('pr-1')
  vi.spyOn(api, 'pullRequest').mockImplementation(async () => current)
  vi.spyOn(api, 'refreshPullRequestGitHubReadiness').mockImplementation(async () => current)
  vi.spyOn(api, 'manualPullRequestReviews').mockResolvedValue([])
  const publishDrafts = vi.spyOn(api, 'publishPullRequestCommentDrafts').mockImplementation(async () => {
    draft.publication_state = 'pending'
    return [draft]
  })
  const submit = vi.spyOn(api, 'submitManualPullRequestReview')
    .mockImplementationOnce(async () => {
      current = { ...current, view_revision: 2, title: 'PR changed after submission',
        ...(changed === 'head' ? { head_commit: 'new-head' } : { comparison_state: 'stale' as const }) }
      throw new TypeError('Submission response lost')
    })
    .mockImplementation(async (_id, input) => ({ ...input, pull_request_id: 'pr-1', state: 'published', created_at: now }))
  renderPullRequestDetail()
  await user.click(await screen.findByRole('button', { name: includeDraft ? 'Publish review' : 'Review' }))
  const dialog = within(await screen.findByRole('dialog', { name: 'Submit review' }))
  await user.click(dialog.getByRole('radio', { name: 'Approve' }))
  await user.type(dialog.getByRole('textbox', { name: /Approval note/ }), 'Reviewed this revision')
  await user.click(dialog.getByRole('button', { name: includeDraft ? 'Publish 1 & approve' : 'Approve pull request' }))
  expect(await dialog.findByText(/Submission response lost/)).toBeInTheDocument()
  await screen.findByText('PR changed after submission')
  expect(dialog.getByRole('button', { name: 'Retry submission' })).toBeEnabled()

  await user.click(dialog.getByRole('button', { name: 'Cancel' }))
  await user.click(screen.getByRole('button', { name: 'Review' }))
  const reopened = within(await screen.findByRole('dialog', { name: 'Submit review' }))
  expect(reopened.getByRole('textbox', { name: /Approval note/ })).toBeDisabled()
  expect(reopened.getByText('def45678')).toBeInTheDocument()
  await user.click(reopened.getByRole('button', { name: 'Retry submission' }))
  await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Submit review' })).not.toBeInTheDocument())
  expect(submit).toHaveBeenCalledTimes(2)
  expect(submit.mock.calls[1]).toEqual(submit.mock.calls[0])
  expect(submit.mock.calls[1][1]).toMatchObject({ event: 'approve', body: 'Reviewed this revision', head_commit: 'def45678' })
  expect(publishDrafts).toHaveBeenCalledTimes(includeDraft ? 1 : 0)
  expect(await screen.findByText('You approved')).toBeInTheDocument()

  await user.click(screen.getByRole('button', { name: 'Review' }))
  const next = within(await screen.findByRole('dialog', { name: 'Submit review' }))
  expect(next.getByRole('textbox', { name: /Approval note/ })).toBeEnabled()
  expect(next.getByRole('textbox', { name: /Approval note/ })).toHaveValue('')
  if (changed === 'comparison') expect(next.getByRole('button', { name: 'Approve pull request' })).toBeDisabled()
})

it('keeps a comment-only submission retryable after its drafts are accepted and unlocks the confirmed form', async () => {
  const user = userEvent.setup()
  const now = new Date().toISOString()
  const draft: PullRequestComment = {
    id: 'manual-draft', pull_request_id: 'pr-1', body: 'Reviewed finding', scope: 'pull_request',
    original_head_commit: 'def45678', status: 'unresolved', author_type: 'user', publication_state: 'draft', created_at: now, updated_at: now,
  }
  stubPullRequestDetail('open', { comments: [draft] })
  let currentComments = [draft]
  vi.spyOn(api, 'pullRequestComments').mockImplementation(async () => currentComments)
  vi.spyOn(api, 'manualPullRequestReviews').mockResolvedValue([])
  const publishDrafts = vi.spyOn(api, 'publishPullRequestCommentDrafts')
    .mockImplementationOnce(async () => {
      currentComments = [{ ...draft, publication_state: 'local' }]
      throw new TypeError('Publication response lost')
    })
    .mockImplementation(async () => currentComments)
  const submit = vi.spyOn(api, 'submitManualPullRequestReview')
    .mockImplementation(async (_id, input) => ({ ...input, pull_request_id: 'pr-1', state: 'local', created_at: now }))
  renderPullRequestDetail()
  await user.click(await screen.findByRole('button', { name: 'Publish review' }))
  const dialog = within(await screen.findByRole('dialog', { name: 'Submit review' }))
  await user.click(dialog.getByRole('button', { name: 'Publish 1 comment' }))
  expect(await dialog.findByText('Publication response lost')).toBeInTheDocument()
  await waitFor(() => expect(dialog.queryByRole('button', { name: /Review drafts/ })).not.toBeInTheDocument())
  expect(dialog.getByRole('button', { name: 'Retry submission' })).toBeEnabled()
  expect(dialog.getByRole('textbox', { name: /Review summary/ })).toBeDisabled()
  expect(dialog.getByRole('radio', { name: 'Approve' })).toBeDisabled()

  await user.click(dialog.getByRole('button', { name: 'Cancel' }))
  await user.click(screen.getByRole('button', { name: 'Review' }))
  const reopened = within(await screen.findByRole('dialog', { name: 'Submit review' }))
  expect(reopened.getByRole('button', { name: 'Retry submission' })).toBeEnabled()
  await user.click(reopened.getByRole('button', { name: 'Retry submission' }))
  await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Submit review' })).not.toBeInTheDocument())
  expect(publishDrafts).toHaveBeenCalledTimes(2)
  expect(publishDrafts.mock.calls[1]).toEqual(publishDrafts.mock.calls[0])
  expect(publishDrafts).toHaveBeenLastCalledWith('pr-1', 'def45678', ['manual-draft'])
  expect(submit).not.toHaveBeenCalled()

  await user.click(screen.getByRole('button', { name: 'Review' }))
  const next = within(await screen.findByRole('dialog', { name: 'Submit review' }))
  expect(next.queryByRole('button', { name: 'Retry submission' })).not.toBeInTheDocument()
  expect(next.getByRole('textbox', { name: /Review summary/ })).toBeEnabled()
  expect(next.getByRole('textbox', { name: /Review summary/ })).toHaveValue('')
  expect(next.getByRole('button', { name: 'Publish review' })).toBeDisabled()
  expect(next.queryByText('Publication response lost')).not.toBeInTheDocument()
  await user.click(next.getByRole('radio', { name: 'Approve' }))
  await user.click(next.getByRole('button', { name: 'Approve pull request' }))
  await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Submit review' })).not.toBeInTheDocument())
  expect(submit).toHaveBeenCalledTimes(1)
  expect(submit).toHaveBeenCalledWith('pr-1', expect.objectContaining({ event: 'approve', body: '', head_commit: 'def45678' }))
})

it.each(['ready', 'stale', 'unavailable'] as const)('requires a ready comparison to publish a single agent draft: %s', async (comparisonState) => {
  const user = userEvent.setup()
  const now = new Date().toISOString()
  const draft = (id: string): PullRequestComment => ({
    id, pull_request_id: 'pr-1', body: `Finding ${id}`, scope: 'pull_request', original_head_commit: 'def45678',
    status: 'unresolved', author_type: 'agent', publication_state: 'draft', created_at: now, updated_at: now,
  })
  stubPullRequestDetail('open', { comparisonState, comments: [draft('finding-1'), draft('finding-2')] })
  vi.spyOn(api, 'manualPullRequestReviews').mockResolvedValue([])
  const publishDrafts = vi.spyOn(api, 'publishPullRequestCommentDrafts').mockResolvedValue([{ ...draft('finding-1'), publication_state: 'local' }, draft('finding-2')])
  renderPullRequestDetail()
  await screen.findByText('Finding finding-1')
  await user.click(screen.getAllByRole('button', { name: 'Comment actions' })[0])
  await user.click(screen.getByRole('menuitem', { name: 'Publish' }))
  if (comparisonState === 'ready') {
    await waitFor(() => expect(publishDrafts).toHaveBeenCalledWith('pr-1', 'def45678', ['finding-1']))
  } else {
    expect(await screen.findByText('The PR comparison is updating. Try again when it is ready.')).toBeInTheDocument()
    expect(publishDrafts).not.toHaveBeenCalled()
  }
})

it('disables merge for an unsupported local pull request', async () => {
  const user = userEvent.setup()
  const fetchMock = stubPullRequestDetail('open', {
    mergeable: false,
    mergeBlockedReason: 'merge_provider_unsupported',
  })
  renderPullRequestDetail()

  expect(await screen.findByText('Lifecycle work')).toBeInTheDocument()
  const merge = screen.getByRole('button', { name: /merge/i })
  expect(merge).toBeDisabled()
  expect(screen.getByText('This pull request cannot be merged by Holark')).toBeInTheDocument()
  await user.click(merge)
  expect(fetchMock.mock.calls.some(([input, init]) => String(input).endsWith('/pull-requests/pr-1/merge') && init?.method === 'POST')).toBe(false)
})

it.each(['github_status_missing', 'github_status_stale', 'github_mergeability_unknown'])('shows GitHub mergeability polling in the merge button for %s', async (mergeBlockedReason) => {
  stubPullRequestDetail('open', {
    syncProvider: 'github',
    mergeable: false,
    mergeBlockedReason,
  })
  renderPullRequestDetail()

  expect(await screen.findByRole('button', { name: 'Checking GitHub mergeability…' })).toBeDisabled()
  expect(screen.getAllByText('Checking GitHub mergeability…')).toHaveLength(1)
  expect(screen.queryByText('GitHub mergeability unknown')).not.toBeInTheDocument()
})

it('merges a GitHub-backed pull request from the detail page', async () => {
  const user = userEvent.setup()
  const now = new Date().toISOString()
  let finishReadiness!: (response: Response) => void
  const readinessResponse = new Promise<Response>((resolve) => { finishReadiness = resolve })
  let finishMerge!: (response: Response) => void
  const mergeResponse = new Promise<Response>((resolve) => { finishMerge = resolve })
  let finishPostMergeRefresh!: (response: Response) => void
  const postMergeRefresh = new Promise<Response>((resolve) => { finishPostMergeRefresh = resolve })
  let pullRequest: Record<string, unknown> = { view_revision: 1,
    id: 'pr-gh', repository_id: 'holark', title: 'GitHub PR', summary: 'Summary', base_branch: 'main', base_commit: 'abc12345', head_branch: 'feature', head_commit: 'def45678', status: 'open',
    sync_provider: 'github', sync_external_id: 'github:pmaviro/holark#42', sync_data: { github: { url: 'https://github.com/pmaviro/holark/pull/42', number: 42 } },
    mergeable: true, merge_provider: 'github', merge_request_strategy: 'squash', unresolved_comment_count: 0, linked_session_ids: [], created_at: now, updated_at: now,
  }
  const fetchMock = vi.fn<(input: RequestInfo | URL, init?: RequestInit) => Promise<Response>>(async (input, init) => {
    const path = String(input)
    if (path.endsWith('/pull-requests/pr-gh/merge')) {
      expect(init?.method).toBe('POST')
      expect(JSON.parse(String(init?.body))).toEqual({ strategy: 'squash', bypass_unresolved_comments: false })
      return mergeResponse
    }
    if (path.endsWith('/pull-requests/pr-gh/rebase-readiness')) {
      return readinessResponse
    }
    if (path.endsWith('/pull-requests/pr-gh')) {
      if (pullRequest.status === 'merged') return postMergeRefresh
      return new Response(JSON.stringify(pullRequest), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    if (path.endsWith('/pull-requests/pr-gh/commits')) return jsonResponse({ inputs: { head_commit: 'def45678', diff_base_commit: 'abc12345' }, data: [] })
    if (path.endsWith('/pull-requests/pr-gh/comments') || path.endsWith('/pull-requests/pr-gh/reviews')) {
      return new Response(JSON.stringify([]), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    if (path.endsWith('/pull-requests/pr-gh/changes')) {
      return new Response(JSON.stringify({ inputs: { head_commit: 'def45678', diff_base_commit: 'abc12345' }, data: { branch: 'feature', base_branch: 'main', base_commit: 'abc12345', head_commit: 'def45678', has_changes: true, dirty: false, diff_truncated: false, files: [] } }), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    return new Response(JSON.stringify({ code: 'not_found', message: 'Not found.' }), { status: 404, headers: { 'Content-Type': 'application/json' } })
  })
  vi.stubGlobal('fetch', fetchMock)

  render(
    <MemoryRouter initialEntries={['/pulls/pr-gh']}>
      <ProjectContext.Provider value={{ id: 'holark', name: 'Holark', default_branch: 'main' }}>
        <Routes><Route path="/pulls/:pullRequestId" element={<PullRequestDetail />} /></Routes>
      </ProjectContext.Provider>
    </MemoryRouter>,
  )

  expect(await screen.findByText('GitHub PR')).toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: /merge/i }))
  expect(await screen.findByRole('dialog', { name: /merge/i })).toBeInTheDocument()
  expect(screen.queryByText('Provider')).not.toBeInTheDocument()
  await user.click(within(screen.getByRole('dialog', { name: /merge/i })).getByRole('button', { name: /merge/i }))

  await waitFor(() => expect(fetchMock).toHaveBeenCalledWith('/api/v1/pull-requests/pr-gh/merge', expect.objectContaining({ method: 'POST' })))
  await act(async () => { finishReadiness(jsonResponse({
    base_commit: 'merged-main', head_commit: 'def45678', base_commits_ahead: 1, branch_freshness: 'not_up_to_date', rebase_conflict_state: 'conflicting',
  })) })
  expect(screen.queryByText(/Conflicts/)).not.toBeInTheDocument()
  expect(screen.queryByRole('button', { name: 'Rebase and resolve conflicts' })).not.toBeInTheDocument()

  pullRequest = { ...pullRequest, status: 'merged', mergeable: false, merged_commit: 'fed789ab', merge_strategy: 'squash', merged_at: now }
  await act(async () => { finishMerge(jsonResponse({ pull_request: pullRequest, merged_commit: 'fed789ab' })) })
  expect(await within(screen.getByRole('banner', { name: 'Pull request header' })).findByText('Merged')).toBeInTheDocument()
  expect(screen.queryByText('Review & merge')).not.toBeInTheDocument()
  expect(screen.queryByText('Branch status unavailable')).not.toBeInTheDocument()
  expect(screen.queryByText(/Conflicts/)).not.toBeInTheDocument()

  await act(async () => { finishPostMergeRefresh(jsonResponse(pullRequest)) })
})

it('links a fresh completed review and places the latest addressing holon in each comment thread', async () => {
  const user = userEvent.setup()
  const now = new Date().toISOString()
  const comment = { pull_request_id: 'pr-1', scope: 'pull_request', original_head_commit: 'head', author_type: 'user', created_at: now, updated_at: now }
  const worker = { pull_request_id: 'pr-1', mode: 'auto', status: 'completed', base_head_commit: 'head', created_at: now }
  stubPullRequestDetail('open', {
    comments: [
      { ...comment, id: 'docs', body: 'Add documentation', status: 'unresolved' },
      { ...comment, id: 'tests', body: 'Cover the edge cases', status: 'resolved' },
    ],
    reviews: [{
      id: 'review', pull_request_id: 'pr-1', session_id: 'review-holon', status: 'completed', summary: 'Two comments to address', head_commit: 'head', created_at: now,
      provenance: { diff_base_commit: 'ab0197e0', head_commit: 'fe5e6760' },
    }],
    workers: [
      { ...worker, id: 'old-docs', comment_id: 'docs', session_id: 'old-docs-holon' },
      { ...worker, id: 'tests', comment_id: 'tests', session_id: 'tests-holon' },
      { ...worker, id: 'new-docs', comment_id: 'docs', session_id: 'docs-holon' },
    ],
  })
  renderPullRequestDetail()
  const docs = (await screen.findByText('Add documentation')).closest('article')!
  const tests = screen.getByText('Cover the edge cases').closest('article')!
  expect(within(docs).getByRole('link', { name: /Comment holon/ })).toHaveAttribute('href', '/holons/docs-holon')
  expect(within(tests).getByRole('link', { name: /Comment holon/ })).toHaveAttribute('href', '/holons/tests-holon')
  const review = (await screen.findByText('Agent review')).closest('section')!
  const completion = within(review).getByRole('link', { name: /Reviewed/ })
  expect(completion).toHaveAttribute('href', '/holons/review-holon')
  expect(within(review).queryByText('Two comments to address')).not.toBeInTheDocument()
  expect(within(review).queryByRole('link', { name: /Review holon/ })).not.toBeInTheDocument()
  expect(document.querySelector('a[href="/holons/old-docs-holon"]')).toBeNull()
  expect(within(review).queryByText('Review outdated')).not.toBeInTheDocument()
  await user.click(completion)
  expect(await screen.findByText('Agent page')).toBeInTheDocument()
})

it.each(['queued', 'running', 'waiting_user', 'cancelling'])('hides the review action for an active %s review', async (status) => {
  stubPullRequestDetail('open', { reviews: [{
    id: 'review', status, session_id: 'review-holon', created_at: new Date().toISOString(),
    provenance: { diff_base_commit: '14bfe9f0', head_commit: '9821e430' },
  }] })
  renderPullRequestDetail()
  const review = (await screen.findByText('Agent review')).closest('section')!
  expect(within(review).queryByText('Running')).not.toBeInTheDocument()
  expect(within(review).queryByText(/Reviewed/)).not.toBeInTheDocument()
  expect(within(review).queryByRole('button', { name: 'Run review' })).not.toBeInTheDocument()
})

const outdatedDescription: NonNullable<PullRequestMetadata['freshness']> = {
  generated: { diff_base_commit: '1234567890', head_commit: 'abcdef1234' },
  current: { diff_base_commit: '2345678901', head_commit: 'bcdef12345' },
  outdated: true,
}

it('shows clickable outdated ranges and keeps the text and warning while regenerating', async () => {
  const user = userEvent.setup()
  stubPullRequestDetail('open', { freshness: outdatedDescription })
  const initial = await api.pullRequestMetadata('pr-1')
  const metadata = vi.spyOn(api, 'pullRequestMetadata').mockResolvedValue(initial)
  const running: PullRequestMetadata = {
    ...initial, generation_complete: false,
    agent_session: { id: 'metadata-holon-1', runtime_id: '', status: 'running', input_state: 'none', error: '' },
  }
  let finishStart!: (metadata: PullRequestMetadata) => void
  const startPending = new Promise<PullRequestMetadata>((resolve) => { finishStart = resolve })
  const start = vi.spyOn(api, 'startPullRequestMetadataAgent').mockImplementation(async () => {
    metadata.mockResolvedValue(running)
    return startPending
  })
  renderPullRequestDetail()
  const warning = await screen.findByText(/Description may be outdated/)
  expect(warning).toHaveTextContent('Generated for 1234567 → abcdef1; current PR changes are 2345678 → bcdef12.')
  for (const hash of ['1234567890', 'abcdef1234', '2345678901', 'bcdef12345']) {
    expect(within(warning).getByRole('link', { name: hash.slice(0, 7) })).toHaveAttribute('href', `/?view=commits&ref=${hash}`)
  }
  await user.click(screen.getByRole('button', { name: 'Regenerate' }))
  expect(start).toHaveBeenCalledWith('pr-1', 'regenerate', '')
  expect(screen.getByText('Summary')).toBeInTheDocument()
  expect(screen.getByText(/Description may be outdated/)).toBeInTheDocument()
  expect(screen.queryByText('Generating description…')).not.toBeInTheDocument()
  expect(screen.queryByRole('region', { name: 'Pull request metadata generation' })).not.toBeInTheDocument()
  await act(async () => { finishStart(running) })
  expect(await screen.findByRole('link', { name: /Metadata holon/ })).toBeInTheDocument()
  // Success can itself be outdated; it still removes progress and offers Regenerate.
  metadata.mockResolvedValue({ ...running, generation_complete: true, agent_session: { ...running.agent_session!, status: 'completed' } })
  await waitFor(() => expect(screen.queryByRole('link', { name: /Metadata holon/ })).not.toBeInTheDocument(), { timeout: 4000 })
  expect(screen.getByText(/Description may be outdated/)).toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Regenerate' })).toBeEnabled()
  expect(screen.queryByRole('button', { name: 'Retry' })).not.toBeInTheDocument()
  expect(screen.queryByRole('link', { name: /Metadata holon/ })).not.toBeInTheDocument()
})

it('reevaluates description freshness when PR changes are refreshed', async () => {
  const user = userEvent.setup()
  stubPullRequestDetail('open')
  const initial = await api.pullRequestMetadata('pr-1')
  const metadata = vi.spyOn(api, 'pullRequestMetadata').mockResolvedValue(initial)
  renderPullRequestDetail()
  await screen.findByRole('button', { name: 'Regenerate' })
  expect(screen.queryByText(/Description may be outdated/)).not.toBeInTheDocument()
  metadata.mockResolvedValue({ ...initial, freshness: outdatedDescription })
  await user.click(screen.getByRole('button', { name: 'Pull request actions' }))
  await user.click(screen.getByRole('menuitem', { name: 'Refresh' }))
  expect(await screen.findByText(/Description may be outdated/)).toBeInTheDocument()
})

it('keeps the outdated description warning when a metadata refresh fails', async () => {
  const user = userEvent.setup()
  stubPullRequestDetail('open', { freshness: outdatedDescription })
  const initial = await api.pullRequestMetadata('pr-1')
  const metadata = vi.spyOn(api, 'pullRequestMetadata').mockResolvedValue(initial)
  renderPullRequestDetail()

  expect(await screen.findByText(/Description may be outdated/)).toBeInTheDocument()
  metadata.mockRejectedValueOnce(new Error('Metadata temporarily unavailable.'))
  await user.click(screen.getByRole('button', { name: 'Pull request actions' }))
  await user.click(screen.getByRole('menuitem', { name: 'Refresh' }))
  await waitFor(() => expect(metadata).toHaveBeenCalledTimes(2))
  expect(screen.getByText(/Description may be outdated/)).toBeInTheDocument()
})

it('previews and saves a comment draft, then keeps the next draft editable', async () => {
  const user = userEvent.setup()
  const mutation = vi.fn(async (body: CreatePullRequestCommentRequest) => Response.json({
    id: 'saved', pull_request_id: 'pr-1', publication_state: 'draft', original_head_commit: 'def45678', scope: 'pull_request', status: 'unresolved', author_type: 'user', created_at: new Date().toISOString(), ...body,
  }, { status: 201 }))
  stubPullRequestDetail('open', { comments: [], commentMutation: mutation })
  renderPullRequestDetail()
  const draft = '**Ready** to review\n\n- One detail'
  await user.type(await screen.findByRole('textbox', { name: 'Comment' }), draft)
  await user.click(screen.getByRole('button', { name: 'Preview comment' }))
  const preview = screen.getByRole('region', { name: 'Comment preview' })
  expect(within(preview).getByText('Ready').tagName).toBe('STRONG')
  expect(within(preview).getByRole('listitem')).toHaveTextContent('One detail')
  await user.click(screen.getByRole('button', { name: 'Edit comment draft' }))
  expect(screen.getByRole('textbox', { name: 'Comment' })).toHaveValue(draft)
  await user.click(screen.getByRole('button', { name: 'Preview comment' }))
  await user.click(screen.getByRole('button', { name: 'Save draft' }))
  await waitFor(() => expect(mutation).toHaveBeenCalledWith({ body: draft, draft: true }))
  expect(await screen.findByRole('textbox', { name: 'Comment' })).toHaveValue('')
  await user.type(screen.getByRole('textbox', { name: 'Comment' }), 'Next draft')
  expect(screen.getByRole('textbox', { name: 'Comment' })).toHaveValue('Next draft')
  expect(screen.queryByRole('region', { name: 'Comment preview' })).not.toBeInTheDocument()
})

it('toggles unresolved activity and hides bulk resolution when feedback is resolved', async () => {
  const user = userEvent.setup()
  const now = new Date().toISOString()
  stubPullRequestDetail('open', { comments: [{
    id: 'resolved', pull_request_id: 'pr-1', body: 'Already addressed', status: 'resolved', author_type: 'user', created_at: now, updated_at: now,
  }] })
  renderPullRequestDetail()
  expect(await screen.findByText('Already addressed')).toBeInTheDocument()
  expect(screen.getByText('All comments resolved')).toBeInTheDocument()
  expect(screen.queryByRole('button', { name: 'Resolve all automatically' })).not.toBeInTheDocument()
  expect(screen.queryByText('Not run')).not.toBeInTheDocument()
  expect(screen.queryByText('Get an independent review of these changes.')).not.toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'Unresolved only' }))
  expect(screen.queryByText('Already addressed')).not.toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'Show all activity' }))
  expect(screen.getByText('Already addressed')).toBeInTheDocument()
})


it.each(['wip', 'draft'] as const)('disables Merge during %s preparation across page remounts, then enables it after publication', async (status) => {
  const user = userEvent.setup()
  stubPullRequestDetail(status, {
    publicationBlocked: true,
    preparationTarget: 'open',
    initialAgentSession: { id: 'metadata-holon-1', runtime_id: 'node-1', status: 'running', input_state: 'none', error: '' },
  })
  const pending = await api.pullRequest('pr-1')
  const pullRequest = vi.spyOn(api, 'pullRequest').mockResolvedValue(pending)
  const page = renderPullRequestDetail()
  expect(await screen.findByRole('button', { name: 'Merge pull request' })).toBeDisabled()
  expect(screen.queryByRole('button', { name: 'Move to draft' })).not.toBeInTheDocument()
  page.unmount()
  renderPullRequestDetail()
  expect(await screen.findByRole('button', { name: 'Merge pull request' })).toBeDisabled()
  expect(screen.queryByRole('button', { name: 'Move to draft' })).not.toBeInTheDocument()
  const readiness = within(screen.getByRole('region', { name: 'Merge readiness' }))
  expect(readiness.getByText('Awaiting publication')).toBeInTheDocument()
  expect(screen.queryByText('Published')).not.toBeInTheDocument()
  pullRequest.mockResolvedValue({ ...pending, status: 'open', publication_blocked: false, mergeable: true })
  await user.click(screen.getByRole('button', { name: 'Pull request actions' }))
  await user.click(screen.getByRole('menuitem', { name: 'Refresh' }))
  await waitFor(() => expect(screen.getByRole('button', { name: /merge/i })).toBeEnabled())
  expect(screen.queryByText('Awaiting publication')).not.toBeInTheDocument()
  expect(readiness.getByText('Published')).toBeInTheDocument()
})

const scopedPatch = '@@ -2,2 +2,2 @@\n-old\n+new\n context\n'
const scopedFiles: WorkspaceFileDiff[] = [
  { path: 'new.ts', old_path: 'old.ts', status: 'renamed', additions: 1, deletions: 1, binary: false, diff_truncated: false, diff: scopedPatch },
  { path: 'deleted.ts', status: 'deleted', additions: 0, deletions: 1, binary: false, diff_truncated: false, diff: '@@ -1 +0,0 @@\n-deleted\n' },
  { path: 'image.png', status: 'added', additions: 0, deletions: 0, binary: true, diff_truncated: false, diff: '' },
  { path: 'missing.ts', status: 'modified', additions: 1, deletions: 1, binary: false, diff_truncated: true, diff: '' },
  { path: 'partial.ts', status: 'modified', additions: 2, deletions: 0, binary: false, diff_truncated: true, diff: '@@ -0,0 +1,2 @@\n+complete\n+fragment' },
]

it('places keyboard-accessible composers at files and rows and restores independent drafts across tabs', async () => {
  const user = userEvent.setup()
  stubPullRequestDetail('open', { files: scopedFiles })
  renderPullRequestDetail()
  await user.type(await screen.findByLabelText('Comment'), 'general draft')
  await user.click(screen.getByRole('button', { name: /^Changes/ }))
  const fileAction = screen.getByRole('button', { name: 'Comment on file new.ts' })
  fileAction.focus()
  await user.keyboard('{Enter}')
  const fileInput = screen.getByRole('textbox', { name: /^Comment on / })
  expect(fileInput).toHaveFocus()
  expect(fileAction.closest('header')?.nextElementSibling).toContainElement(fileInput)
  await user.type(fileInput, 'file draft')
  const rowAction = screen.getByRole('button', { name: 'Comment on new.ts LEFT line 2' })
  rowAction.focus()
  await user.keyboard(' ')
  const lineInput = screen.getByRole('textbox', { name: /^Comment on / })
  expect(lineInput).toHaveFocus()
  expect(lineInput.closest('[data-diff-side=LEFT]')).toHaveAttribute('data-diff-line', '2')
  await user.type(lineInput, 'line draft')
  await user.click(screen.getByRole('button', { name: /^Overview/ }))
  expect(screen.getByLabelText('Comment')).toHaveValue('general draft')
  await user.click(screen.getByRole('button', { name: /^Changes/ }))
  expect(screen.getByRole('textbox', { name: /^Comment on / })).toHaveValue('line draft')
  await user.click(screen.getByRole('button', { name: 'Comment on file new.ts' }))
  expect(screen.getByRole('textbox', { name: /^Comment on / })).toHaveValue('file draft')
  await user.click(screen.getByRole('button', { name: 'Discard' }))
  expect(screen.queryByRole('textbox', { name: /^Comment on / })).not.toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'Comment on file new.ts' }))
  expect(screen.getByRole('textbox', { name: /^Comment on / })).toHaveValue('')
  expect(screen.getAllByRole('button', { name: /^Comment on file / })).toHaveLength(1)
  expect(screen.getAllByRole('button', { name: / line / })).toHaveLength(3)
  await user.click(screen.getByRole('button', { name: 'Show inline diff' }))
  expect(screen.getAllByRole('button', { name: / line / })).toHaveLength(3)
  await user.click(screen.getByRole('button', { name: 'Comment on new.ts RIGHT line 3' }))
  await user.type(screen.getByRole('textbox', { name: /^Comment on / }), 'inline draft')
  await user.click(screen.getByRole('button', { name: 'Show side-by-side diff' }))
  expect(screen.getByRole('textbox', { name: /^Comment on / })).toHaveValue('inline draft')
  await user.click(screen.getByRole('button', { name: /deleted.ts/ }))
  expect(screen.getByRole('button', { name: 'Comment on deleted.ts LEFT line 1' })).toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: /partial.ts/ }))
  expect(screen.getByRole('button', { name: 'Comment on partial.ts RIGHT line 1' })).toBeInTheDocument()
  expect(screen.queryByRole('button', { name: 'Comment on partial.ts RIGHT line 2' })).not.toBeInTheDocument()
})

it.each(['tab', 'file'])('keeps completed image uploads in the draft when switching PR %s during a batch', async (navigation) => {
  const user = userEvent.setup()
  stubPullRequestDetail('open', { files: scopedFiles })
  const firstURL = 'https://github.com/user-attachments/assets/11111111-1111-1111-1111-111111111111'
  const secondURL = 'https://github.com/user-attachments/assets/22222222-2222-2222-2222-222222222222'
  let finishUpload!: (asset: { url: string }) => void
  const upload = vi.spyOn(api, 'uploadGitHubImage')
    .mockResolvedValueOnce({ url: firstURL })
    .mockResolvedValueOnce({ url: secondURL })
    .mockImplementationOnce(() => new Promise((resolve) => { finishUpload = resolve }))
  renderPullRequestDetail()
  await screen.findByLabelText('Comment')
  await user.click(screen.getByRole('button', { name: /^Changes/ }))
  await user.click(screen.getByRole('button', { name: 'Comment on file new.ts' }))
  const input = screen.getByRole('textbox', { name: /^Comment on / }) as HTMLTextAreaElement
  fireEvent.change(input, { target: { value: 'before selected after' } })
  input.setSelectionRange(7, 15)
  await user.upload(screen.getByLabelText('Choose images'), ['one', 'two', 'three', 'four'].map((name) => new File(['image'], `${name}.png`, { type: 'image/png' })))
  await waitFor(() => expect(upload).toHaveBeenCalledTimes(3))
  const expected = `before \n\n![one.png](${firstURL})\n\n![two.png](${secondURL})\n\n after`
  expect(input).toHaveValue(expected)
  expect(input).toBeDisabled()
  if (navigation === 'tab') {
    await user.click(screen.getByRole('button', { name: /^Overview/ }))
  } else {
    await user.click(screen.getByRole('button', { name: /deleted.ts/ }))
    await user.click(screen.getByRole('button', { name: 'Comment on file deleted.ts' }))
    expect(screen.getByRole('textbox', { name: /^Comment on / })).toHaveValue('')
  }
  expect(upload.mock.calls[2][1]?.aborted).toBe(true)
  if (navigation === 'tab') await user.click(screen.getByRole('button', { name: /^Changes/ }))
  else {
    await user.click(screen.getByRole('button', { name: /^new.ts/ }))
    await user.click(screen.getByRole('button', { name: 'Comment on file new.ts' }))
  }
  const restored = screen.getByRole('textbox', { name: /^Comment on / })
  expect(restored).toHaveValue(expected)
  expect(restored).toBeEnabled()
  await act(async () => { finishUpload({ url: 'https://github.com/user-attachments/assets/33333333-3333-3333-3333-333333333333' }) })
  expect(restored).toHaveValue(expected)
  expect(upload).toHaveBeenCalledTimes(3)
})

it('preserves a failed draft and never reposts on refresh retry', async () => {
  const user = userEvent.setup()
  let failPost = true
  let failList = false
  let finishPost: (() => void) | undefined
  const comments: Record<string, unknown>[] = []
  const mutation = vi.fn(async (body: CreatePullRequestCommentRequest) => {
    if (failPost) return Response.json({ message: 'Post failed' }, { status: 500 })
    await new Promise<void>((resolve) => { finishPost = resolve })
    const comment = { id: 'saved', pull_request_id: 'pr-1', publication_state: 'draft', original_head_commit: 'def45678', scope: 'pull_request', status: 'unresolved', author_type: 'user', created_at: new Date().toISOString(), ...body }
    comments.push(comment)
    failList = true
    return Response.json(comment, { status: 201 })
  })
  stubPullRequestDetail('open', { files: scopedFiles, commentMutation: mutation, commentList: () => failList ? Response.json({ message: 'List failed' }, { status: 500 }) : Response.json(comments) })
  renderPullRequestDetail()
  await screen.findByLabelText('Comment')
  await user.click(screen.getByRole('button', { name: /^Changes/ }))
  await user.click(screen.getByRole('button', { name: 'Comment on new.ts LEFT line 2' }))
  const input = screen.getByRole('textbox', { name: /^Comment on / })
  await user.type(input, 'keep this draft')
  await user.click(screen.getByRole('button', { name: 'Save draft' }))
  expect(await screen.findByText('Post failed')).toBeInTheDocument()
  expect(input).toHaveValue('keep this draft')
  failPost = false
  await user.dblClick(screen.getByRole('button', { name: 'Save draft' }))
  expect(mutation).toHaveBeenCalledTimes(2)
  expect(input).toBeDisabled()
  finishPost!()
  expect(await screen.findByText('Comment saved. Could not refresh the comments list.')).toBeInTheDocument()
  expect(input).toHaveValue('')
  await user.click(screen.getByRole('button', { name: 'View comment' }))
  expect(screen.getByText('keep this draft')).toBeInTheDocument()
  failList = false
  await user.click(screen.getByRole('button', { name: 'Retry refresh' }))
  await waitFor(() => expect(screen.queryByRole('button', { name: 'Retry refresh' })).not.toBeInTheDocument())
  expect(mutation).toHaveBeenCalledTimes(2)
})

it.each([200, 500])('retains saved comments across a shared post refresh returning %s and a failed background read', async (postStatus) => {
  const user = userEvent.setup()
  const comments: Record<string, unknown>[] = []
  let finishPostRefresh!: (response: Response) => void
  let finishBackgroundRefresh!: (response: Response) => void
  const postRefresh = new Promise<Response>((resolve) => { finishPostRefresh = resolve })
  const backgroundRefresh = new Promise<Response>((resolve) => { finishBackgroundRefresh = resolve })
  const commentList = vi.fn()
    .mockImplementationOnce(() => Response.json([]))
    .mockReturnValueOnce(postRefresh)
    .mockReturnValueOnce(backgroundRefresh)
    .mockImplementation(() => Response.json(comments))
  const mutation = vi.fn(async (body: CreatePullRequestCommentRequest) => {
    const comment = { id: 'saved', pull_request_id: 'pr-1', publication_state: 'draft', original_head_commit: 'def45678', status: 'unresolved', author_type: 'user', created_at: new Date().toISOString(), ...body }
    comments.push(comment)
    return Response.json(comment, { status: 201 })
  })
  stubPullRequestDetail('open', {
    commentList, commentMutation: mutation,
    workers: [{ id: 'active-worker', mode: 'continue', status: 'running' }],
  })
  renderPullRequestDetail()
  await user.type(await screen.findByLabelText('Comment'), 'keep the saved comment visible')
  await user.click(screen.getByRole('button', { name: 'Save draft' }))
  expect(commentList).toHaveBeenCalledTimes(2)
  expect(screen.getByText('keep the saved comment visible')).toBeInTheDocument()

  act(() => { window.dispatchEvent(new Event('focus')) })
  expect(commentList).toHaveBeenCalledTimes(2)
  await act(async () => {
    finishPostRefresh(Response.json(postStatus === 200 ? comments : { message: 'List failed' }, { status: postStatus }))
  })
  expect(screen.getByText('keep the saved comment visible')).toBeInTheDocument()
  await act(async () => {
    window.dispatchEvent(new Event('focus'))
    expect(commentList).toHaveBeenCalledTimes(3)
    finishBackgroundRefresh(Response.json({ message: 'Background list failed' }, { status: 500 }))
  })
  expect(screen.getByText('keep the saved comment visible')).toBeInTheDocument()
  if (postStatus === 500) {
    expect(screen.getByText('Comment saved. Could not refresh the comments list.')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Retry refresh' }))
  } else {
    await act(async () => { window.dispatchEvent(new Event('focus')) })
  }
  await waitFor(() => expect(screen.queryByRole('button', { name: 'Retry refresh' })).not.toBeInTheDocument())
  expect(screen.getAllByText('keep the saved comment visible')).toHaveLength(1)
  expect(mutation).toHaveBeenCalledTimes(1)
})

it.each(['failed', 'delayed'])('retires saved comment fallbacks after background reconciliation of a %s post refresh', async (postRefreshResult) => {
  const user = userEvent.setup()
  let finishCommentCheck!: (result: Awaited<ReturnType<typeof api.refreshPullRequestComments>>) => void
  vi.mocked(api.refreshPullRequestComments).mockReturnValue(new Promise((resolve) => { finishCommentCheck = resolve }))
  const comments: Record<string, unknown>[] = []
  let finishPostRefresh!: (response: Response) => void
  const postRefresh = new Promise<Response>((resolve) => { finishPostRefresh = resolve })
  const commentList = vi.fn()
    .mockImplementationOnce(() => Response.json([]))
    .mockReturnValueOnce(postRefresh)
    .mockImplementation(() => Response.json(comments))
  stubPullRequestDetail('open', {
    commentList,
    commentMutation: async (body) => {
      const comment = { id: 'saved', pull_request_id: 'pr-1', publication_state: 'draft', original_head_commit: 'def45678', status: 'unresolved', author_type: 'user', created_at: new Date().toISOString(), ...body }
      comments.push(comment)
      return Response.json(comment, { status: 201 })
    },
  })
  renderPullRequestDetail()
  await user.type(await screen.findByLabelText('Comment'), 'externally deleted comment')
  await user.click(screen.getByRole('button', { name: 'Save draft' }))
  expect(commentList).toHaveBeenCalledTimes(2)
  expect(screen.getByText('externally deleted comment')).toBeInTheDocument()

  await act(async () => {
    finishPostRefresh(postRefreshResult === 'failed' ? Response.json({ message: 'List failed' }, { status: 500 }) : Response.json(comments))
  })
  await act(async () => { window.dispatchEvent(new Event('focus')) })
  expect(commentList).toHaveBeenCalledTimes(3)
  expect(screen.getAllByText('externally deleted comment')).toHaveLength(1)
  expect(screen.getByText('1 draft comment to review')).toBeInTheDocument()

  comments.length = 0
  await user.click(screen.getByRole('button', { name: 'Pull request actions' }))
  await user.click(screen.getByRole('menuitem', { name: 'Refresh' }))
  await waitFor(() => expect(screen.queryByText('externally deleted comment')).not.toBeInTheDocument())
  await act(async () => { finishCommentCheck({ refreshed: false, synced_at: null, sync_applicable: false }) })
  expect(screen.queryByText('All comments resolved')).not.toBeInTheDocument()
})

it('clears drafts when leaving the PR, including navigation to another PR', async () => {
  const user = userEvent.setup()
  stubPullRequestDetail('open', { files: scopedFiles })
  render(
    <MemoryRouter initialEntries={['/pulls/pr-1']}>
      <ProjectContext.Provider value={{ id: 'holark', name: 'Holark', default_branch: 'main' }}>
        <Link to="/pulls/pr-1">First PR</Link><Link to="/pulls/pr-2">Second PR</Link>
        <Routes><Route path="/pulls/:pullRequestId" element={<PullRequestDetail />} /></Routes>
      </ProjectContext.Provider>
    </MemoryRouter>,
  )
  await user.type(await screen.findByLabelText('Comment'), 'general draft')
  await user.click(screen.getByRole('button', { name: /^Changes/ }))
  await user.click(screen.getByRole('button', { name: 'Comment on file new.ts' }))
  await user.type(screen.getByRole('textbox', { name: /^Comment on / }), 'file draft')
  await user.click(screen.getByRole('link', { name: 'Second PR' }))
  await user.click(screen.getByRole('link', { name: 'First PR' }))
  expect(await screen.findByLabelText('Comment')).toHaveValue('')
  await user.click(screen.getByRole('button', { name: /^Changes/ }))
  expect(screen.queryByRole('textbox', { name: /^Comment on / })).not.toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'Comment on file new.ts' }))
  expect(screen.getByRole('textbox', { name: /^Comment on / })).toHaveValue('')
})

it('keeps saved draft editing and deletion working when the list refresh fails', async () => {
  const user = userEvent.setup()
  vi.stubGlobal('confirm', vi.fn(() => true))
  let saved = false
  let comment: Record<string, unknown>
  const fetchMock = stubPullRequestDetail('open', {
    commentList: () => saved ? Response.json({ message: 'List failed' }, { status: 500 }) : Response.json([]),
    commentMutation: async (request) => {
      saved = true
      comment = { id: 'new-comment', pull_request_id: 'pr-1', publication_state: 'draft', original_head_commit: 'def45678', author_type: 'user', scope: 'file', path: 'new.ts', status: 'unresolved', created_at: new Date().toISOString(), ...request }
      return Response.json(comment, { status: 201 })
    },
  })
  const originalFetch = fetchMock.getMockImplementation()!
  fetchMock.mockImplementation(async (input, init) => {
    if (String(input).includes('/pull-request-comments/new-comment')) {
      if (init?.method === 'PATCH') comment.body = JSON.parse(String(init.body)).body
      return Response.json(comment)
    }
    return originalFetch(input, init)
  })
  renderPullRequestDetail()
  await user.type(await screen.findByLabelText('Comment'), 'cached note')
  await user.click(screen.getByRole('button', { name: 'Save draft' }))
  await screen.findByText('Comment saved. Could not refresh the comments list.')
  expect(screen.queryByRole('button', { name: 'Mark resolved' })).not.toBeInTheDocument()
  expect(screen.queryByRole('button', { name: 'Reopen' })).not.toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'Comment actions' }))
  await user.click(screen.getByRole('menuitem', { name: 'Edit' }))
  await user.clear(screen.getByLabelText('Edit comment'))
  await user.type(screen.getByLabelText('Edit comment'), 'cached edit')
  await user.click(screen.getByRole('button', { name: 'Save' }))
  // A failed refresh keeps the existing edit form open, but the saved cache changes.
  await user.click(screen.getByRole('button', { name: 'Cancel' }))
  expect(screen.getByText('cached edit')).toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'Comment actions' }))
  await user.click(screen.getByRole('menuitem', { name: 'Delete' }))
  await waitFor(() => expect(screen.queryByText('cached edit')).not.toBeInTheDocument())
})

it('preserves active reply and edit forms when resolving a diff thread', async () => {
  const user = userEvent.setup()
  const now = new Date().toISOString()
  let comments = [{
    id: 'thread-1', pull_request_id: 'pr-1', body: 'Thread first\nThread details', scope: 'line', path: 'new.ts', side: 'RIGHT', line: 2, diff_hunk: scopedPatch,
    original_head_commit: 'def45678', status: 'unresolved', author_type: 'user', created_at: now, updated_at: now,
  }]
  const fetchMock = stubPullRequestDetail('open', { comments, commentList: () => Response.json(comments), files: scopedFiles })
  const originalFetch = fetchMock.getMockImplementation()!
  fetchMock.mockImplementation(async (input, init) => {
    const path = String(input)
    if (path.endsWith('/pull-request-comments/thread-1/resolve') || path.endsWith('/pull-request-comments/thread-1/reopen')) {
      const status = path.endsWith('/resolve') ? 'resolved' : 'unresolved'
      comments = comments.map((comment) => ({ ...comment, status }))
      return Response.json(comments[0])
    }
    return originalFetch(input, init)
  })
  renderPullRequestDetail()

  await screen.findByText(/Thread details/)
  await user.click(screen.getByRole('button', { name: /^Changes/ }))
  const article = (await screen.findByText(/Thread details/)).closest('article')!
  const reply = within(article).getByLabelText('Reply to comment: Thread first')
  await user.type(reply, 'Keep this reply')
  await user.click(within(article).getByRole('button', { name: 'Mark resolved' }))
  expect(await within(article).findByText('Resolved')).toBeInTheDocument()
  expect(within(article).getByRole('button', { name: 'Collapse thread' })).toBeDisabled()
  expect(reply).toHaveValue('Keep this reply')

  await user.clear(reply)
  const collapse = within(article).getByRole('button', { name: 'Collapse thread' })
  expect(collapse).toBeEnabled()
  await user.click(collapse)
  expect(within(article).getByRole('button', { name: 'Expand thread' })).toHaveAttribute('aria-expanded', 'false')
  expect(within(article).queryByText(/Thread details/)).not.toBeInTheDocument()

  await user.click(within(article).getByRole('button', { name: 'Reopen' }))
  expect(await within(article).findByText(/Thread details/)).toBeInTheDocument()
  expect(within(article).queryByRole('button', { name: /thread$/i })).not.toBeInTheDocument()

  await user.click(within(article).getByRole('button', { name: 'Mark resolved' }))
  expect(await within(article).findByRole('button', { name: 'Expand thread' })).toBeInTheDocument()
  await user.click(within(article).getByRole('button', { name: 'Comment actions' }))
  await user.click(screen.getByRole('menuitem', { name: 'Edit' }))
  expect(within(article).getByLabelText('Edit comment')).toHaveValue('Thread first\nThread details')
  expect(within(article).getByRole('button', { name: 'Collapse thread' })).toBeDisabled()
})

it('navigates exact saved paths to a focused file header without changing creation drafts', async () => {
  const user = userEvent.setup()
  const now = new Date().toISOString()
  const comments = [
    { id: 'available', pull_request_id: 'pr-1', body: 'Available file', scope: 'file', path: 'image.png', original_head_commit: 'def45678', status: 'unresolved', author_type: 'user', created_at: now, updated_at: now },
    { id: 'renamed', pull_request_id: 'pr-1', body: 'Old saved path', scope: 'file', path: 'old.ts', original_head_commit: 'def45678', status: 'unresolved', author_type: 'user', created_at: now, updated_at: now },
  ]
  stubPullRequestDetail('open', { comments, files: scopedFiles })
  renderPullRequestDetail()
  await screen.findByText('Available file')

  await user.click(screen.getByRole('button', { name: /^Changes/ }))
  await user.click(screen.getByRole('button', { name: /image.png/ }))
  await user.click(screen.getByRole('button', { name: 'Comment on file image.png' }))
  await user.type(screen.getByRole('textbox', { name: /^Comment on / }), 'preserved file draft')
  await user.click(screen.getByRole('button', { name: /^Overview/ }))

  const available = screen.getByText('Available file').closest('article')!
  const unavailable = screen.getByText('Old saved path').closest('article')!
  expect(within(unavailable).queryByRole('button', { name: /old\.ts/ })).not.toBeInTheDocument()
  const view = within(available).getByRole('button', { name: /image\.png/ })
  view.focus()
  await user.keyboard('{Enter}')

  expect(screen.getByRole('button', { name: /^Changes/ })).toHaveAttribute('aria-current', 'page')
  const fileHeading = screen.getByRole('heading', { name: 'image.png' })
  expect(fileHeading.closest('header')).toHaveFocus()
  expect(screen.getByRole('textbox', { name: /^Comment on / })).toHaveValue('preserved file draft')
})

it('reveals and focuses a saved draft thread through View comment', async () => {
  const user = userEvent.setup()
  const comments: Record<string, unknown>[] = []
  const mutation = vi.fn(async (request: CreatePullRequestCommentRequest) => {
    const comment = { id: 'saved-draft', pull_request_id: 'pr-1', status: 'unresolved', publication_state: 'draft', author_type: 'user', original_head_commit: 'def45678', created_at: new Date().toISOString(), updated_at: new Date().toISOString(), ...request }
    comments.push(comment)
    return Response.json(comment, { status: 201 })
  })
  stubPullRequestDetail('open', { comments, commentMutation: mutation, files: scopedFiles })
  renderPullRequestDetail()
  await screen.findByLabelText('Comment')
  await user.click(screen.getByRole('button', { name: /^Changes/ }))
  await user.click(screen.getByRole('button', { name: 'Comment on new.ts RIGHT line 2' }))
  await user.type(screen.getByRole('textbox', { name: /^Comment on / }), 'Posted first{enter}Posted details')
  await user.click(screen.getByRole('button', { name: 'Save draft' }))
  await user.click(await screen.findByRole('button', { name: 'View comment' }))

  const article = screen.getByText(/Posted details/).closest('article')!
  expect(article).toHaveFocus()
  expect(within(article).getByText(/Posted details/)).toBeVisible()
  expect(within(article).getByLabelText('Saved diff excerpt for new.ts')).toBeInTheDocument()
})

it('shows saved context in Overview and places threads beside the matching diff lines', async () => {
  const user = userEvent.setup()
  const now = new Date().toISOString()
  const base = { pull_request_id: 'pr-1', status: 'unresolved', author_type: 'user', created_at: now, updated_at: now, original_head_commit: 'def45678' }
  const line = { ...base, scope: 'line', path: 'new.ts', side: 'RIGHT', line: 2, diff_hunk: scopedPatch }
  const comments = [
    { ...base, id: 'general', scope: 'pull_request', body: 'General discussion' },
    { ...base, id: 'file', scope: 'file', path: 'new.ts', body: 'File discussion' },
    { ...line, id: 'left', side: 'LEFT', body: 'Old line discussion' },
    { ...line, id: 'right', body: 'New line discussion' },
    { ...line, id: 'context', side: 'LEFT', line: 3, body: 'Old context discussion' },
    { ...line, id: 'reply', parent_comment_id: 'right', body: 'Existing reply' },
    { ...line, id: 'malformed', diff_hunk: '@@ malformed @@', body: 'No excerpt' },
    { ...line, id: 'resolved', status: 'resolved', body: 'Resolved first\nResolved details' },
    { ...line, id: 'resolved-reply', parent_comment_id: 'resolved', body: 'Resolved reply' },
    { ...base, id: 'binary', scope: 'file', path: 'image.png', body: 'Binary file discussion' },
  ]
  const fetchMock = stubPullRequestDetail('open', { comments, files: scopedFiles })
  const originalFetch = fetchMock.getMockImplementation()!
  fetchMock.mockImplementation(async (input, init) => String(input).endsWith('/workers') && init?.method === 'POST'
    ? Response.json({ message: 'Could not start resolution' }, { status: 503 })
    : originalFetch(input, init))
  renderPullRequestDetail()
  const general = (await screen.findByText('General discussion')).closest('article')!
  expect(within(general).queryByLabelText(/Comment location:/)).not.toBeInTheDocument()
  const file = screen.getByText('File discussion').closest('article')!
  expect(within(file).getByRole('button', { name: 'new.ts' })).toBeInTheDocument()
  for (const body of ['Old line discussion', 'New line discussion', 'No excerpt', 'Resolved first']) {
    const location = within(screen.getByText(body).closest('article')!).getByRole('button', { name: 'new.ts' })
    expect(location).toBeVisible()
    expect(location).toHaveTextContent('new.ts')
  }
  expect(within(file).queryByLabelText(/Saved diff excerpt/)).not.toBeInTheDocument()
  const excerpt = within(screen.getByText('New line discussion').closest('article')!).getByLabelText('Saved diff excerpt for new.ts')
  expect(excerpt).toHaveTextContent('-old')
  expect(excerpt.querySelector('[data-commented="true"]')).toHaveTextContent(/Commented line\..*\+new/)
  expect(within(screen.getByText('No excerpt').closest('article')!).queryByLabelText(/Saved diff excerpt/)).not.toBeInTheDocument()
  const resolved = within(screen.getByText('Resolved first').closest('article')!)
  expect(resolved.getByText('1 reply')).toBeInTheDocument()
  expect(resolved.queryByText(/Resolved details|Resolved reply/)).not.toBeInTheDocument()
  expect(resolved.queryByLabelText(/Saved diff excerpt/)).not.toBeInTheDocument()
  const disclosure = resolved.getByRole('button', { name: 'Expand thread' })
  expect(disclosure).toHaveAttribute('aria-expanded', 'false')
  expect(disclosure).toHaveAttribute('aria-controls', 'comment-content-resolved')
  disclosure.focus()
  await user.keyboard('{Enter}')
  expect(resolved.getByRole('button', { name: 'Collapse thread' })).toHaveAttribute('aria-expanded', 'true')
  expect(resolved.getByText(/Resolved details/)).toBeInTheDocument()
  expect(resolved.getByText('Resolved reply')).toBeInTheDocument()
  expect(resolved.getByLabelText('Saved diff excerpt for new.ts')).toBeInTheDocument()

  await user.click(screen.getByRole('button', { name: /^Changes/ }))
  expect(within(screen.getByLabelText('File comments for new.ts')).getByText('File discussion')).toBeInTheDocument()
  for (const [body, side, number] of [['Old line discussion', 'LEFT', '2'], ['New line discussion', 'RIGHT', '2'], ['Old context discussion', 'LEFT', '3']]) {
    const anchor = screen.getByText(body).closest('[data-diff-line]')!
    expect(anchor).toHaveAttribute('data-diff-side', side)
    expect(anchor).toHaveAttribute('data-diff-line', number)
  }
  expect(screen.getAllByText('Existing reply')).toHaveLength(1)
  expect(screen.queryByText('General discussion')).not.toBeInTheDocument()
  expect(screen.queryByLabelText('Saved diff excerpt for new.ts')).not.toBeInTheDocument()
  const thread = within(screen.getByText('New line discussion').closest('article')!)
  await user.type(thread.getByRole('textbox'), 'Unfinished reply')
  for (const layout of ['Show inline diff', 'Show side-by-side diff']) {
    await user.click(screen.getByRole('button', { name: layout }))
    expect(thread.getByRole('textbox')).toHaveValue('Unfinished reply')
    expect(screen.getAllByText('Old context discussion')).toHaveLength(1)
  }
  await user.click(screen.getByRole('button', { name: /image.png/ }))
  const binary = within(screen.getByLabelText('File comments for image.png'))
  expect(binary.getByText('Binary file discussion')).toBeInTheDocument()
  await user.click(binary.getByRole('button', { name: 'Resolve automatically' }))
  expect(await screen.findByRole('alert')).toHaveTextContent('Could not start resolution')
})

it.each(['closed', 'merged'] as const)('keeps context and navigation available without write actions on %s PRs', async (status) => {
  const user = userEvent.setup()
  const now = new Date().toISOString()
  const base = { pull_request_id: 'pr-1', scope: 'line', path: 'new.ts', side: 'RIGHT', line: 2, diff_hunk: scopedPatch, status: 'unresolved', author_type: 'user', created_at: now, updated_at: now, original_head_commit: 'def45678' }
  const comments = [
    { ...base, id: 'current', body: 'Current discussion\nCurrent details', status: 'resolved' },
    { ...base, id: 'stale', original_head_commit: 'previous-head', body: 'Earlier revision' },
    { ...base, id: 'outside', line: 90, body: 'Missing line' },
    { ...base, id: 'mismatch', diff_hunk: scopedPatch.replace('+new', '+different'), body: 'Changed source' },
    { ...base, id: 'missing', path: 'gone.ts', body: 'Removed file' },
    { ...base, id: 'fragment', path: 'partial.ts', diff_hunk: '@@ -0,0 +1,2 @@\n+complete\n+fragment', body: 'Truncated line' },
  ]
  stubPullRequestDetail(status, { comments, files: scopedFiles })
  renderPullRequestDetail()
  const overview = (await screen.findByText('Current discussion')).closest('article')!
  expect(screen.queryByRole('textbox', { name: /^Comment on / })).not.toBeInTheDocument()
  expect(within(overview).queryByRole('button', { name: 'Comment actions' })).not.toBeInTheDocument()
  expect(within(overview).queryByRole('button', { name: 'Reply' })).not.toBeInTheDocument()
  await user.click(within(overview).getByRole('button', { name: 'Expand thread' }))
  expect(within(overview).getByText(/Current details/)).toBeInTheDocument()
  await user.click(within(overview).getByRole('button', { name: /new\.ts/ }))
  expect(screen.getByRole('heading', { name: 'old.ts → new.ts' }).closest('header')).toHaveFocus()
  const fallback = screen.getByRole('region', { name: 'Comments outside the current diff' })
  for (const body of ['Earlier revision', 'Missing line', 'Changed source']) expect(within(fallback).getByText(body)).toBeInTheDocument()
  expect(within(fallback).getAllByLabelText('Saved diff excerpt for new.ts')).toHaveLength(2)
  expect(within(screen.getByRole('region', { name: 'Comments on other files' })).getByText('Removed file')).toBeInTheDocument()
  const current = screen.getByText('Current discussion').closest('article')!
  expect(current.closest('[data-diff-line]')).toHaveAttribute('data-diff-line', '2')
  await user.click(within(current).getByRole('button', { name: 'Expand thread' }))
  expect(within(current).getByRole('button', { name: 'Collapse thread' })).toBeInTheDocument()
  expect(screen.queryByRole('button', { name: 'Reply' })).not.toBeInTheDocument()
  expect(screen.queryByRole('button', { name: /^Comment on / })).not.toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: /partial.ts/ }))
  expect(within(screen.getByRole('region', { name: 'Comments outside the current diff' })).getByText('Truncated line')).toBeInTheDocument()
})

it.each(['wip', 'draft'] as const)('allows Open PR with an empty description from %s', async (status) => {
  const user = userEvent.setup()
  stubPullRequestDetail(status)
  const initial = { ...await api.pullRequest('pr-1'), summary: '', publication_blocked: false }
  vi.spyOn(api, 'pullRequest').mockResolvedValue(initial)
  vi.spyOn(api, 'pullRequestMetadata').mockResolvedValue({ view_revision: 1, pull_request_id: initial.id, title: initial.title, description: '', updated_at: initial.updated_at })
  const transition = vi.spyOn(api, 'transitionPullRequest').mockResolvedValue(initial)
  const generate = vi.spyOn(api, 'startPullRequestMetadataAgent')
  renderPullRequestDetail()
  if (status === 'wip') {
    await user.click(await screen.findByRole('button', { name: 'Pull request actions' }))
    await user.click(screen.getByRole('menuitem', { name: 'Open PR' }))
  } else {
    await user.click(await screen.findByRole('button', { name: 'Open PR' }))
  }
  expect(transition).toHaveBeenCalledWith('pr-1', 'open')
  expect(generate).not.toHaveBeenCalled()
})

it.each(['wip', 'draft'] as const)('refreshes %s after synchronous preparation without a metadata session', async (status) => {
  stubPullRequestDetail(status)
  const initial = { ...await api.pullRequest('pr-1'), summary: '', publication_blocked: true }
  vi.spyOn(api, 'pullRequest').mockResolvedValueOnce(initial).mockResolvedValue({ ...initial, status: 'open', publication_blocked: false, mergeable: true })
  vi.spyOn(api, 'pullRequestMetadata').mockResolvedValue({ view_revision: 1, pull_request_id: initial.id, title: initial.title, description: '', updated_at: initial.updated_at, generation_complete: true })
  renderPullRequestDetail()
  await waitFor(() => expect(screen.getByRole('button', { name: 'Merge pull request' })).toBeEnabled())
  expect(screen.queryByRole('button', { name: 'Opening PR…' })).not.toBeInTheDocument()
})

it.each([
  { operation: 'generation', action: 'Retry' },
  { operation: 'save', action: 'Retry saving' },
  { operation: 'publication', action: 'Retry publication' },
] as const)('shows recovery after opening fails during $operation', async ({ operation, action }) => {
  stubPullRequestDetail('draft', { publicationBlocked: true })
  const initial = await api.pullRequest('pr-1')
  vi.spyOn(api, 'pullRequestMetadata').mockResolvedValue({ view_revision: 1,
    pull_request_id: initial.id, title: initial.title, description: initial.summary, updated_at: initial.updated_at,
    preparation_target: 'open', generation_complete: operation === 'publication',
    ...(operation === 'generation' ? { generation_error: 'Generation failed.' } : { application_error: { operation, message: 'Application failed.' } }),
  })
  renderPullRequestDetail()
  expect(await screen.findByRole('button', { name: action })).toBeEnabled()
  expect(await screen.findByRole('button', { name: 'Merge pull request' })).toBeDisabled()
  expect(screen.queryByRole('button', { name: 'Opening PR…' })).not.toBeInTheDocument()
})

it('keeps Open pending and then Open when overlapping stale snapshots finish', async () => {
  const user = userEvent.setup()
  let finishTransition!: () => void
  const transitionDelay = new Promise<void>((resolve) => { finishTransition = resolve })
  const fetchMock = stubPullRequestDetail('draft', { transitionDelay })
  const initial = { ...await api.pullRequest('pr-1'), view_revision: 1 }
  const originalFetch = fetchMock.getMockImplementation()!
  let finishOld!: (response: Response) => void
  let finishDuring!: (response: Response) => void
  const old = new Promise<Response>((resolve) => { finishOld = resolve })
  const during = new Promise<Response>((resolve) => { finishDuring = resolve })
  let nextDetail: Promise<Response> | undefined
  let opened = false
  fetchMock.mockImplementation(async (input, init) => {
    const path = String(input)
    if (path.endsWith('/pull-requests/pr-1/sync')) return old
    if (path.endsWith('/transition')) {
      await transitionDelay
      opened = true
      return jsonResponse({ ...initial, status: 'open', view_revision: 3 })
    }
    if (path === '/api/v1/pull-requests/pr-1') {
      if (nextDetail) { const result = nextDetail; nextDetail = undefined; return result }
      return jsonResponse(opened ? { ...initial, status: 'open', view_revision: 3 } : initial)
    }
    return originalFetch(input, init)
  })
  renderPullRequestDetail()
  const open = await screen.findByRole('button', { name: 'Open PR' })
  const staleSync = api.syncPullRequest('pr-1')
  nextDetail = during
  await act(async () => { window.dispatchEvent(new Event('focus')) })
  await user.click(open)
  expect(screen.getByRole('button', { name: 'Updating...' })).toBeDisabled()
  await act(async () => { finishOld(new Response(null, { status: 204 })); await staleSync })
  expect(screen.getByRole('button', { name: 'Updating...' })).toBeDisabled()
  expect(screen.queryByRole('button', { name: 'Open PR' })).not.toBeInTheDocument()
  await act(async () => { finishTransition() })
  expect(await within(screen.getByRole('banner', { name: 'Pull request header' })).findByText('Open')).toBeInTheDocument()
  await act(async () => { finishDuring(jsonResponse({ ...initial, view_revision: 2 })) })
  expect(within(screen.getByRole('banner', { name: 'Pull request header' })).getByText('Open')).toBeInTheDocument()
  expect(screen.queryByRole('button', { name: 'Open PR' })).not.toBeInTheDocument()
})

it('preserves the last description warning when its comparison becomes unknown', async () => {
  stubPullRequestDetail('open', { freshness: outdatedDescription })
  const initial = await api.pullRequestMetadata('pr-1')
  const metadata = vi.spyOn(api, 'pullRequestMetadata').mockResolvedValue(initial)
  renderPullRequestDetail()
  expect(await screen.findByText(/Description may be outdated/)).toBeInTheDocument()
  metadata.mockResolvedValue({ ...initial, freshness: undefined })
  await act(async () => { window.dispatchEvent(new Event('focus')) })
  expect(screen.getByText(/Description may be outdated/)).toBeInTheDocument()
})


it('retains stale comparison display without enabling merge, Continue, or Address', async () => {
  const user = userEvent.setup()
  stubPullRequestDetail('open', { comparisonState: 'stale', mergeable: true })
  renderPullRequestDetail()
  expect(await screen.findByRole('heading', { name: 'Lifecycle work', level: 1 })).toBeInTheDocument()
  expect(await screen.findByText('Summary')).toBeInTheDocument()
  expect(await screen.findByText('Needs docs')).toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: /^Commits/ }))
  expect(await screen.findByText('Updating commits…')).toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: /^Changes/ }))
  expect(await screen.findByText('Updating changes…')).toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: /^Overview/ }))
  expect(await screen.findByRole('button', { name: 'Merge pull request' })).toBeDisabled()
  expect(screen.getByRole('button', { name: 'Resolve all automatically' })).toBeDisabled()
  await user.click(screen.getByRole('button', { name: 'Pull request actions' }))
  expect(screen.getByRole('menuitem', { name: 'New holon from this PR' })).toBeDisabled()
})

it('keeps cached comments visible while checking and allows a forced retry after failure', async () => {
  const user = userEvent.setup()
  let reject!: (error: Error) => void
  vi.mocked(api.refreshPullRequestComments).mockReturnValueOnce(new Promise((_, fail) => { reject = fail }))
  stubPullRequestDetail('open', { comments: [], syncProvider: 'github' })
  renderPullRequestDetail()
  expect(await screen.findByText('No comments yet.')).toBeInTheDocument()
  await act(async () => { reject(new Error('GitHub unavailable')) })
  expect(screen.getByRole('alert')).toHaveTextContent('GitHub unavailable')
  vi.mocked(api.refreshPullRequestComments).mockResolvedValue({ refreshed: true, synced_at: '2026-01-01T00:00:00Z', sync_applicable: true })
  await user.click(screen.getByRole('button', { name: 'Pull request actions' }))
  await user.click(screen.getByRole('menuitem', { name: 'Refresh comments' }))
  expect(api.refreshPullRequestComments).toHaveBeenLastCalledWith('pr-1', true)
  expect(await screen.findByText('No comments yet.')).toBeInTheDocument()
  expect(screen.queryByText('GitHub unavailable')).not.toBeInTheDocument()
})

it.each([false, true])('shows sync progress in the overflow menu (hidden resolved threads: %s)', async (hiddenResolvedThreads) => {
  const user = userEvent.setup()
  const synced = { refreshed: true, synced_at: '2026-01-01T00:00:00Z', sync_applicable: true }
  vi.mocked(api.refreshPullRequestComments).mockResolvedValueOnce(synced)
  const now = new Date().toISOString()
  stubPullRequestDetail('open', { syncProvider: 'github', comments: hiddenResolvedThreads ? [{
    id: 'resolved', pull_request_id: 'pr-1', body: 'Already addressed', status: 'resolved', author_type: 'user', created_at: now, updated_at: now,
  }] : [] })
  renderPullRequestDetail()
  if (hiddenResolvedThreads) await user.click(await screen.findByRole('button', { name: 'Unresolved only' }))
  const emptyMessage = hiddenResolvedThreads ? 'No unresolved conversations.' : 'No comments yet.'
  expect(await screen.findByText(emptyMessage)).toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'Pull request actions' }))
  await waitFor(() => expect(screen.getByRole('menuitem', { name: 'Refresh comments' })).toBeEnabled())

  let finishSync!: (result: typeof synced) => void
  vi.mocked(api.refreshPullRequestComments).mockReturnValueOnce(new Promise((resolve) => { finishSync = resolve }))
  await user.click(screen.getByRole('menuitem', { name: 'Refresh comments' }))
  expect(screen.queryByRole('menu')).not.toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'Pull request actions' }))
  vi.mocked(api.pullRequestCommentRefreshStatus).mockResolvedValue({ syncing: true })
  expect(await screen.findByRole('menuitem', { name: 'Syncing comments...' })).toBeDisabled()
  expect(screen.queryByText('Syncing comments from GitHub…')).not.toBeInTheDocument()
  expect(screen.queryByText('No comments yet.')).not.toBeInTheDocument()
  expect(screen.queryByText('No unresolved conversations.')).not.toBeInTheDocument()

  await act(async () => { finishSync(synced) })
  expect(await screen.findByText(emptyMessage)).toBeInTheDocument()
  expect(screen.getByRole('menuitem', { name: 'Refresh comments' })).toBeEnabled()
})

it.each(['pending', 'failed'] as const)('shows %s publication without disabling Address', async (state) => {
  const now = '2026-01-01T00:00:00Z'
  stubPullRequestDetail('open', { comments: [{
    id: 'publication', pull_request_id: 'pr-1', body: 'Address this saved comment', scope: 'pull_request',
    original_head_commit: 'def45678', status: 'unresolved', author_type: 'user', created_at: now, updated_at: now,
    publication_state: state,
  }] })
  renderPullRequestDetail()
  expect(await screen.findByText(state === 'pending' ? 'Pending GitHub publication' : 'Saved locally; GitHub publication failed')).toBeInTheDocument()
  const article = screen.getByText('Address this saved comment').closest('article')!
  expect(within(article).getByRole('button', { name: 'Resolve automatically' })).toBeEnabled()
})
