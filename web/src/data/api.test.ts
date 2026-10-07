import { api, type ApiError } from './api'

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('api issue mutations', () => {
  it('rejects accepted issue projection-pending responses as API errors', async () => {
    const fetch = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      code: 'issue_projection_pending',
      message: 'GitHub accepted the change, but Holark has not stored it yet.',
      github_issue_url: 'https://github.com/org/repo/issues/42',
      github_issue_number: 42,
      reconciliation_required: true,
      safe_to_retry: false,
    }), {
      status: 202,
      headers: { 'Content-Type': 'application/json' },
    }))
    vi.stubGlobal('fetch', fetch)

    await expect(api.createIssue('holark', 'Fix issue list', 'Body text')).rejects.toMatchObject({
      code: 'issue_projection_pending',
      message: 'GitHub accepted the change, but Holark has not stored it yet.',
      status: 202,
    } satisfies Partial<ApiError>)
    expect(fetch).toHaveBeenCalledWith('/api/v1/issues', expect.objectContaining({ method: 'POST' }))
  })

  it('replaces issue assignees with Holark member IDs and returns the issue', async () => {
    const issue = { id: 'issue/1', assignee_holark_ids: ['member/a', 'member/b'] }
    const fetch = vi.fn().mockResolvedValue(new Response(JSON.stringify(issue), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    }))
    vi.stubGlobal('fetch', fetch)

    const returned = await api.replaceIssueAssignees('issue/1', ['member/a', 'member/b'])

    expect(returned).toEqual(issue)
    expect(fetch).toHaveBeenCalledWith('/api/v1/issues/issue%2F1/assignees', expect.objectContaining({
      method: 'PUT',
      headers: expect.objectContaining({ 'content-type': 'application/json' }),
      body: JSON.stringify({ assignee_holark_ids: ['member/a', 'member/b'] }),
    }))
  })

  it('builds label catalog, creation, and assignment requests', async () => {
    const label = { id: 'label-1', name: 'bug', color: 'd73a4a', description: 'Something is broken' }
    const issue = { id: 'issue-1', labels: [label] }
    const fetch = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify([label]), { status: 200 }))
      .mockResolvedValueOnce(new Response(JSON.stringify(label), { status: 201 }))
      .mockResolvedValueOnce(new Response(JSON.stringify(issue), { status: 200 }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ ...issue, labels: [] }), { status: 200 }))
    vi.stubGlobal('fetch', fetch)

    await api.issueLabels('my/project')
    await api.createIssueLabel('my/project', { name: 'bug', color: 'd73a4a', description: 'Something is broken' })
    await api.addIssueLabels('issue/1', ['bug'])
    await api.removeIssueLabels('issue/1', ['bug'])

    expect(fetch).toHaveBeenNthCalledWith(1, '/api/v1/labels', expect.any(Object))
    expect(fetch).toHaveBeenNthCalledWith(2, '/api/v1/labels', expect.objectContaining({
      method: 'POST', headers: expect.objectContaining({ 'content-type': 'application/json' }),
      body: JSON.stringify({ name: 'bug', color: 'd73a4a', description: 'Something is broken' }),
    }))
    expect(fetch).toHaveBeenNthCalledWith(3, '/api/v1/issues/issue%2F1/labels/add', expect.objectContaining({
      method: 'POST', headers: expect.objectContaining({ 'content-type': 'application/json' }), body: JSON.stringify({ labels: ['bug'] }),
    }))
    expect(fetch).toHaveBeenNthCalledWith(4, '/api/v1/issues/issue%2F1/labels/remove', expect.objectContaining({
      method: 'POST', headers: expect.objectContaining({ 'content-type': 'application/json' }), body: JSON.stringify({ labels: ['bug'] }),
    }))
  })

  it.each(['issue_projection_pending', 'label_projection_pending'])('rejects %s accepted responses', async (code) => {
    const fetch = vi.fn().mockResolvedValue(new Response(JSON.stringify({ code, message: 'Projection is pending.' }), {
      status: 202,
      headers: { 'Content-Type': 'application/json' },
    }))
    vi.stubGlobal('fetch', fetch)

    await expect(api.createIssueLabel('holark', { name: 'bug', color: 'd73a4a', description: '' })).rejects.toMatchObject({
      code, message: 'Projection is pending.', status: 202,
    } satisfies Partial<ApiError>)
  })
})

describe('api project members', () => {
  it('builds search and resolve requests using Holark member IDs', async () => {
    const fetch = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ members: [] }), { status: 200 }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ members: [], missing_ids: ['member-stale'] }), { status: 200 }))
    vi.stubGlobal('fetch', fetch)

    await api.projectMemberSearch('my/project', 'Ali Ce', 30)
    await api.resolveProjectMembers('my/project', ['member-a', 'member-stale'])

    expect(fetch).toHaveBeenNthCalledWith(1, '/api/v1/github-members/search?q=Ali+Ce&limit=30', expect.any(Object))
    expect(fetch).toHaveBeenNthCalledWith(2, '/api/v1/github-members/resolve', expect.objectContaining({
      method: 'POST',
      headers: expect.objectContaining({ 'content-type': 'application/json' }),
      body: JSON.stringify({ ids: ['member-a', 'member-stale'] }),
    }))
  })
})

describe('api pull request participants', () => {
  it('uses encoded participant paths and Holark member request bodies', async () => {
    const snapshot = { pull_request_id: 'pr/id', assignee_holark_ids: [], requested_reviewer_holark_ids: [] }
    const fetch = vi.fn().mockImplementation(async () => new Response(JSON.stringify(snapshot), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    }))
    vi.stubGlobal('fetch', fetch)

    await api.replacePullRequestAssignees('pr/id', ['member/a', 'member/b'])
    await api.addPullRequestAssignee('pr/id', 'member/id')
    await api.removePullRequestAssignee('pr/id', 'member/id')
    await api.replacePullRequestRequestedReviewers('pr/id', ['member/a'])
    await api.addPullRequestRequestedReviewer('pr/id', 'member/id')
    await api.removePullRequestRequestedReviewer('pr/id', 'member/id')
    await api.syncPullRequestParticipants('pr/id')

    const memberBody = {
      method: 'POST',
      headers: expect.objectContaining({ 'content-type': 'application/json' }),
      body: JSON.stringify({ holark_id: 'member/id' }),
    }
    expect(fetch).toHaveBeenNthCalledWith(1, '/api/v1/pull-requests/pr%2Fid/assignees', expect.objectContaining({
      method: 'PUT', headers: expect.objectContaining({ 'content-type': 'application/json' }), body: JSON.stringify({ assignee_holark_ids: ['member/a', 'member/b'] }),
    }))
    expect(fetch).toHaveBeenNthCalledWith(2, '/api/v1/pull-requests/pr%2Fid/assignees', expect.objectContaining(memberBody))
    expect(fetch).toHaveBeenNthCalledWith(3, '/api/v1/pull-requests/pr%2Fid/assignees/member%2Fid', expect.objectContaining({ method: 'DELETE' }))
    expect(fetch).toHaveBeenNthCalledWith(4, '/api/v1/pull-requests/pr%2Fid/requested-reviewers', expect.objectContaining({
      method: 'PUT', headers: expect.objectContaining({ 'content-type': 'application/json' }), body: JSON.stringify({ requested_reviewer_holark_ids: ['member/a'] }),
    }))
    expect(fetch).toHaveBeenNthCalledWith(5, '/api/v1/pull-requests/pr%2Fid/requested-reviewers', expect.objectContaining(memberBody))
    expect(fetch).toHaveBeenNthCalledWith(6, '/api/v1/pull-requests/pr%2Fid/requested-reviewers/member%2Fid', expect.objectContaining({ method: 'DELETE' }))
    expect(fetch).toHaveBeenNthCalledWith(7, '/api/v1/pull-requests/pr%2Fid/participants/sync', expect.objectContaining({ method: 'POST' }))
  })
})

describe('api pull request rebase readiness', () => {
	it('refreshes through the encoded explicit readiness path', async () => {
		const readiness = { base_commit: 'base', head_commit: 'head', base_commits_ahead: 0, branch_freshness: 'up_to_date', rebase_conflict_state: 'not_applicable' }
		const fetch = vi.fn().mockResolvedValue(new Response(JSON.stringify(readiness), { status: 200, headers: { 'Content-Type': 'application/json' } }))
		vi.stubGlobal('fetch', fetch)
		expect(await api.pullRequestRebaseReadiness('pr/id')).toEqual(readiness)
		expect(fetch).toHaveBeenCalledWith('/api/v1/pull-requests/pr%2Fid/rebase-readiness', expect.objectContaining({ method: 'POST' }))
	})

  it('keeps mechanical attempts and conflict resolution explicit', async () => {
    const work = { id: 'rebase', pull_request_id: 'pr/id', kind: 'rebase', status: 'completed', created_at: new Date().toISOString() }
    const fetch = vi.fn().mockImplementation(async () => new Response(JSON.stringify(work), { status: 201, headers: { 'Content-Type': 'application/json' } }))
    vi.stubGlobal('fetch', fetch)

    await api.attemptPullRequestRebase('pr/id', 'base-commit')
    await api.resolvePullRequestRebase('pr/id', 'base-commit')

    expect(fetch).toHaveBeenNthCalledWith(1, '/api/v1/pull-requests/pr%2Fid/rebase', expect.objectContaining({
      method: 'POST', headers: expect.objectContaining({ 'content-type': 'application/json', 'X-Request-ID': expect.stringMatching(/^[0-9a-f-]{36}$/) }),
      body: JSON.stringify({ mechanical_only: true, expected_base_commit: 'base-commit' }),
    }))
    expect(fetch).toHaveBeenNthCalledWith(2, '/api/v1/pull-requests/pr%2Fid/rebase', expect.objectContaining({
      method: 'POST', headers: expect.objectContaining({ 'content-type': 'application/json', 'X-Request-ID': expect.stringMatching(/^[0-9a-f-]{36}$/) }),
      body: JSON.stringify({ mechanical_only: false, expected_base_commit: 'base-commit' }),
    }))
  })
})

describe('api pull request holon links', () => {
  it('uses the project-scoped projection path', async () => {
    const fetch = vi.fn().mockResolvedValue(new Response('[]', { status: 200 }))
    vi.stubGlobal('fetch', fetch)

    await api.pullRequestSessionLinks('my/project')

    expect(fetch).toHaveBeenCalledWith('/api/v1/pull-request-holon-links', expect.any(Object))
  })
})

describe('singleton Holon contracts', () => {
  it('reads local agent capabilities directly', async () => {
    const capabilities = [{ type: 'codex', available: true, version: '0.152.1' }]
    const fetch = vi.fn().mockResolvedValue(new Response(JSON.stringify({ capabilities }), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    }))
    vi.stubGlobal('fetch', fetch)

    expect(await api.agentCapabilities()).toEqual(capabilities)
    expect(fetch).toHaveBeenCalledWith('/api/v1/agent-capabilities', expect.any(Object))
  })

  it('returns default harness metadata and saves a new default immediately', async () => {
    const fetch = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ capabilities: [{ type: 'codex', available: true }], default_harness: 'codex', default_harness_explicit: false }), { status: 200, headers: { 'Content-Type': 'application/json' } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ default_harness: 'opencode', default_harness_explicit: true }), { status: 200, headers: { 'Content-Type': 'application/json' } }))
    vi.stubGlobal('fetch', fetch)

    const capabilities = await api.agentCapabilities()
    expect(capabilities.default_harness).toBe('codex')
    expect(capabilities.default_harness_explicit).toBe(false)
    await expect(api.updateDefaultAgentHarness('opencode')).resolves.toEqual({ default_harness: 'opencode', default_harness_explicit: true })
    expect(fetch).toHaveBeenLastCalledWith('/api/v1/settings/default-agent-harness', expect.objectContaining({
      method: 'PUT', headers: expect.objectContaining({ 'content-type': 'application/json' }), body: JSON.stringify({ harness_type: 'opencode' }),
    }))
  })

  it('unwraps backend Holon and terminal collections', async () => {
    const fetch = vi.fn().mockImplementation(async (input: RequestInfo | URL) => new Response(JSON.stringify(String(input).endsWith('/terminals')
      ? { manual_terminals: [{ id: 'terminal-1' }] }
      : { holons: [{ id: 'holon-1' }] }), { status: 200, headers: { 'Content-Type': 'application/json' } }))
    vi.stubGlobal('fetch', fetch)

    expect(await api.holons()).toEqual([{ id: 'holon-1' }])
    expect(await api.manualTerminals('holon-1')).toEqual([{ id: 'terminal-1' }])
  })

  it('uses Holon lifecycle and nested agent-session routes', async () => {
    const holon = { id: 'h/id', agent_sessions: [{ id: 'agent-1' }] }
    const fetch = vi.fn().mockImplementation(async (input: RequestInfo | URL) => {
      const body = String(input).endsWith('/agent-sessions') ? { agent_sessions: holon.agent_sessions } : holon
      return new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })
    })
    vi.stubGlobal('fetch', fetch)

    await api.holon('h/id')
    await api.endHolon('h/id')
    await api.reopenHolon('h/id')
    expect(await api.agentSessions('h/id')).toEqual([{ id: 'agent-1' }])
    await api.forkAgentSession('h/id', 'agent/1')

    expect(fetch.mock.calls.map(([path]) => String(path))).toEqual([
      '/api/v1/holons/h%2Fid',
      '/api/v1/holons/h%2Fid/end',
      '/api/v1/holons/h%2Fid/reopen',
      '/api/v1/holons/h%2Fid/agent-sessions',
      '/api/v1/holons/h%2Fid/agent-sessions/agent%2F1/fork',
    ])
  })
})

it.each([202, 502])('treats comment recovery status %s as an error with its GitHub reference', async (status) => {
  const code = status === 202 ? 'issue_comment_projection_pending' : 'issue_comment_outcome_uncertain'
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({ code, message: 'Sync before retrying', github_comment_url: 'https://github.com/comment', github_comment_id: '123' }), { status })))
  await expect(api.createIssueComment('issue', 'body')).rejects.toMatchObject({ code, status, github_comment_url: 'https://github.com/comment', github_comment_id: '123' })
})

describe('shared pull request response acceptance', () => {
  it('rejects overlapping observations before, during, and after an Open action across all views', async () => {
    let finishOpen!: (response: Response) => void
    let finishOld!: (response: Response) => void
    let finishDuring!: (response: Response) => void
    const opening = new Promise<Response>((resolve) => { finishOpen = resolve })
    const old = new Promise<Response>((resolve) => { finishOld = resolve })
    const during = new Promise<Response>((resolve) => { finishDuring = resolve })
    const draft = { id: 'pr-ordered', status: 'draft', view_revision: 1 }
    const opened = { ...draft, status: 'open', view_revision: 3 }
    const json = (value: unknown) => new Response(JSON.stringify(value), { status: 200 })
    let snapshot = draft
    vi.stubGlobal('fetch', vi.fn(async (input: string) => {
      if (input.endsWith('/transition')) return opening
      if (input.endsWith('/sync')) return old
      if (input.endsWith('/holons/holon-ordered/pull-request')) return during
      if (input === '/api/v1/pull-requests') return json([snapshot])
      return json(snapshot)
    }))
    expect(await api.pullRequest('pr-ordered')).toEqual(draft)
    const oldSync = api.syncPullRequest('pr-ordered')
    const action = api.transitionPullRequest('pr-ordered', 'open')
    const duringSync = api.sessionPullRequest('holon-ordered')
    snapshot = { ...draft, view_revision: 2 }
    finishOld(new Response(null, { status: 204 }))
    expect(await oldSync).toEqual(draft)
    finishOpen(json(opened))
    expect(await action).toEqual(opened)
    finishDuring(json({ pull_request: { ...draft, view_revision: 2 } }))
    expect((await duringSync).pull_request).toEqual(opened)
    expect(await api.pullRequests('repository')).toEqual([opened])
    snapshot = { ...draft, view_revision: 4 }
    expect(await api.pullRequest('pr-ordered')).toEqual(snapshot)
  })
})

it('recovers a successful Open after its response is lost', async () => {
  const initial = { id: 'recover-open', status: 'draft', view_revision: 1 }
  const opened = { ...initial, status: 'open', view_revision: 3 }
  let transitioned = false
  let requestID = ''
  const json = (value: unknown) => new Response(JSON.stringify(value), { status: 200 })
  vi.stubGlobal('fetch', vi.fn(async (input: string, init?: RequestInit) => {
    if (input.endsWith('/transition')) {
      const nextID = new Headers(init?.headers).get('X-Request-ID') || ''
      if (transitioned) { expect(nextID).toBe(requestID); return json(opened) }
      requestID = nextID
      transitioned = true
      throw new TypeError('Connection lost')
    }
    if (input.includes('/pull-request-operations/')) {
      expect(input).toBe(`/api/v1/pull-request-operations/${requestID}`)
      return json({ request_id: requestID, pull_request_id: initial.id, kind: 'transition', status: 'succeeded' })
    }
    return json(transitioned ? opened : initial)
  }))
  await api.pullRequest(initial.id)
  expect(await api.transitionPullRequest(initial.id, 'open')).toEqual(opened)
  expect(await api.pullRequest(initial.id)).toEqual(opened)
})

it('reuses a durable request ID when an uncertain action is retried', async () => {
  const requestIDs: string[] = []
  const fetch = vi.fn(async (input: string, init?: RequestInit) => {
    if (input.endsWith('/transition')) {
      requestIDs.push(new Headers(init?.headers).get('X-Request-ID') || '')
      throw new TypeError('Connection lost')
    }
    return new Response(JSON.stringify({ status: 'uncertain' }), { status: 200 })
  })
  vi.stubGlobal('fetch', fetch)
  await expect(api.transitionPullRequest('uncertain-open', 'open')).rejects.toMatchObject({ request_id: expect.stringMatching(/^[0-9a-f-]{36}$/) })
  await expect(api.transitionPullRequest('uncertain-open', 'open')).rejects.toMatchObject({ request_id: requestIDs[0] })
  expect(requestIDs).toHaveLength(2)
  expect(requestIDs[0]).toBe(requestIDs[1])
})

it('reads cached PR comments without triggering remote work', async () => {
  const comments = [{ id: 'comment-1', pull_request_id: 'pr/one', body: 'Current discussion', status: 'unresolved' }]
  const fetch = vi.fn(async () => new Response(JSON.stringify(comments), { status: 200 }))
  vi.stubGlobal('fetch', fetch)
  expect(await api.pullRequestComments('pr/one')).toEqual(comments)
  expect(fetch).toHaveBeenCalledExactlyOnceWith('/api/v1/pull-requests/pr%2Fone/comments', expect.objectContaining({ headers: expect.any(Object), signal: expect.any(AbortSignal) }))
})

it('rejects a pull request response without its required revision', async () => {
  vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ id: 'unversioned', status: 'draft' }), { status: 200 })))
  await expect(api.pullRequest('unversioned')).rejects.toThrow('Invalid pull request view revision.')
})

it.each(['merge', 'publish', 'rebase'] as const)('recovers the complete typed %s result by replaying its durable request', async (action) => {
  const pull_request = { id: 'recovered-result', status: 'open', view_revision: 2 }
  const result = action === 'merge' ? { pull_request, merged_commit: 'merged' }
    : action === 'publish' ? { pull_request, head_commit: 'published', worker: { id: 'continue-worker', status: 'completed' } }
    : { id: 'rebase-work', pull_request_id: pull_request.id, status: 'running', session_id: 'resolution-session' }
  const ids: string[] = []
  vi.stubGlobal('fetch', vi.fn(async (path: string, init?: RequestInit) => {
    if (path.includes('/pull-request-operations/')) {
      expect(action).not.toBe('rebase')
      return new Response(JSON.stringify({ status: 'succeeded' }), { status: 200 })
    }
    expect(path).toBe(`/api/v1/pull-requests/${pull_request.id}/${action}`)
    ids.push(new Headers(init?.headers).get('X-Request-ID') || '')
    if (ids.length === 1) throw new TypeError('Response lost')
    return new Response(JSON.stringify(result), { status: 200 })
  }))
  const recovered = action === 'merge' ? await api.mergePullRequest(pull_request.id, 'squash', false)
    : action === 'publish' ? await api.publishPullRequest(pull_request.id)
    : await api.resolvePullRequestRebase(pull_request.id, 'base')
  expect(recovered).toEqual(result)
  expect(ids).toHaveLength(2)
  expect(ids[0]).toMatch(/^[0-9a-f-]{36}$/)
  expect(ids[1]).toBe(ids[0])
})

it('waits for a successful no-content synchronization before reading cached detail', async () => {
  let complete!: (response: Response) => void
  const synchronized = new Promise<Response>((resolve) => { complete = resolve })
  const cached = { id: 'success-only-sync', status: 'wip', view_revision: 1, head_commit: 'published-head' }
  const fetcher = vi.fn(async (path: string) => path.endsWith('/sync') ? synchronized : new Response(JSON.stringify(cached)))
  vi.stubGlobal('fetch', fetcher)
  const result = api.syncPullRequest(cached.id)
  expect(fetcher).toHaveBeenCalledExactlyOnceWith('/api/v1/pull-requests/success-only-sync/sync', expect.objectContaining({ method: 'POST' }))
  complete(new Response(null, { status: 204 }))
  expect(await result).toEqual(cached)
  expect(fetcher.mock.calls.map(([path]) => path)).toEqual([
    '/api/v1/pull-requests/success-only-sync/sync', '/api/v1/pull-requests/success-only-sync',
  ])
  fetcher.mockClear()
  fetcher.mockResolvedValue(new Response(JSON.stringify({ code: 'pull_request_sync_stale', message: 'Retry synchronization' }), { status: 409 }))
  await expect(api.syncPullRequest(cached.id)).rejects.toMatchObject({ code: 'pull_request_sync_stale' })
  expect(fetcher.mock.calls.map(([path]) => path)).toEqual([
    '/api/v1/pull-requests/success-only-sync/sync',
  ])
})

it('keeps the synchronization barrier when its cached GET finishes after an action', async () => {
  const initial = { id: 'sync-then-get-barrier', status: 'draft', view_revision: 1 }
  const opened = { ...initial, status: 'open', view_revision: 3 }
  let finishDetail!: (response: Response) => void
  const detail = new Promise<Response>((resolve) => { finishDetail = resolve })
  let delayDetail = false
  const fetcher = vi.fn(async (path: string) => {
    if (path.endsWith('/sync')) return new Response(null, { status: 204 })
    if (path.endsWith('/transition')) return new Response(JSON.stringify(opened))
    return delayDetail ? detail : new Response(JSON.stringify(initial))
  })
  vi.stubGlobal('fetch', fetcher)
  await api.pullRequest(initial.id)
  delayDetail = true
  const refresh = api.syncPullRequest(initial.id)
  await vi.waitFor(() => expect(fetcher).toHaveBeenCalledTimes(3))
  expect(await api.transitionPullRequest(initial.id, 'open')).toEqual(opened)
  finishDetail(new Response(JSON.stringify({ ...initial, view_revision: 2 })))
  expect(await refresh).toEqual(opened)
})

it('accepts readiness through the observation barrier without a full sync or extra read', async () => {
  const initial = { id: 'readiness-barrier', status: 'draft', view_revision: 1 }
  const opened = { ...initial, status: 'open', view_revision: 3 }
  let finish!: (response: Response) => void
  const pending = new Promise<Response>((resolve) => { finish = resolve })
  const fetcher = vi.fn(async (path: string, _init?: RequestInit) => {
    if (path.endsWith('/github-readiness')) return pending
    return Response.json(path.endsWith('/transition') ? opened : initial)
  })
  vi.stubGlobal('fetch', fetcher)
  await api.pullRequest(initial.id)
  const readiness = api.refreshPullRequestGitHubReadiness(initial.id)
  expect(fetcher).toHaveBeenLastCalledWith('/api/v1/pull-requests/readiness-barrier/github-readiness', expect.objectContaining({ method: 'POST' }))
  expect(await api.transitionPullRequest(initial.id, 'open')).toEqual(opened)
  finish(Response.json({ ...initial, view_revision: 2 }))
  expect(await readiness).toEqual(opened)
  expect(fetcher).toHaveBeenCalledTimes(3)
})

it('requests automatic and forced comments refresh independently of cached reads', async () => {
  const result = { refreshed: true, synced_at: '2026-01-01T00:00:00Z', sync_applicable: true }
  const fetch = vi.fn(async () => new Response(JSON.stringify(result), { status: 200 }))
  vi.stubGlobal('fetch', fetch)
  expect(await api.refreshPullRequestComments('pr/one')).toEqual(result)
  expect(fetch).toHaveBeenLastCalledWith('/api/v1/pull-requests/pr%2Fone/comments/refresh', expect.objectContaining({ method: 'POST', body: '{"force":false}' }))
  await api.refreshPullRequestComments('pr/one', true)
  expect(fetch).toHaveBeenLastCalledWith('/api/v1/pull-requests/pr%2Fone/comments/refresh', expect.objectContaining({ method: 'POST', body: '{"force":true}' }))
})
