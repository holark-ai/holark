import { act, renderHook } from '@testing-library/react'
import type { PullRequest, PullRequestComment } from '../../data/types'
import { api } from '../../data/api'
import { usePullRequestReview } from './usePullRequestReview'

// These tests exercise local section reads. Remote refresh behavior is covered below.
beforeEach(() => {
  vi.spyOn(api, 'refreshPullRequestComments').mockImplementation(() => new Promise(() => {}))
  vi.spyOn(api, 'pullRequestCommentRefreshStatus').mockResolvedValue({ syncing: false })
})

afterEach(() => {
  vi.restoreAllMocks()
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

function response(value: unknown) {
  return new Response(JSON.stringify(value), { status: 200, headers: { 'Content-Type': 'application/json' } })
}

function detailResponse(data: unknown, head_commit = 'head') {
  return response({ inputs: { head_commit, diff_base_commit: 'base' }, data })
}

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((next) => { resolve = next })
  return { promise, resolve }
}

const pullRequest = {
  view_revision: 1,
  head_commit: 'head',
  base_commit: 'base',
  id: 'pr-one',
  title: 'Fresh comments',
  status: 'open',
} as PullRequest

function comment(id: string, body: string) {
  return { id, pull_request_id: 'pr-one', body, status: 'unresolved' } as PullRequestComment
}

it('refreshes comments on detail load, focus, visibility, and every minute without applying stale responses', async () => {
  vi.useFakeTimers()
  const staleVisibility = deferred<Response>()
  const commentResponses: Array<Promise<Response> | Response> = [
    response([comment('initial', 'Initial')]),
    response([comment('focus', 'Focus')]),
    staleVisibility.promise,
    response([comment('visible', 'Visible')]),
    response([comment('timer', 'Timer')]),
  ]
  let commentRequest = 0
  const fetchMock = vi.fn((input: string | URL | Request) => {
    const path = typeof input === 'string' ? input : input instanceof URL ? input.pathname : new URL(input.url).pathname
    if (path.endsWith('/comments')) return commentResponses[commentRequest++]
    if (path === '/api/v1/pull-requests/pr-one') return Promise.resolve(response(pullRequest))
    if (path.endsWith('/changes')) return Promise.resolve(detailResponse({ files: [] }))
    if (path.endsWith('/commits')) return Promise.resolve(detailResponse([]))
    return Promise.resolve(response([]))
  })
  vi.stubGlobal('fetch', fetchMock)

  const { result } = renderHook(() => usePullRequestReview('pr-one'))
  await act(async () => { await vi.advanceTimersByTimeAsync(0) })
  expect(result.current.comments).toEqual([comment('initial', 'Initial')])

  await act(async () => { window.dispatchEvent(new Event('focus')) })
  expect(commentRequest).toBe(2)
  expect(result.current.comments).toEqual([comment('focus', 'Focus')])
  act(() => { document.dispatchEvent(new Event('visibilitychange')) })
  expect(commentRequest).toBe(3)
  await act(async () => { await result.current.refresh() })
  expect(commentRequest).toBe(3) // The in-flight cache read is shared.
  act(() => { result.current.applyCommentMutation(comment('visible', 'Visible')) })
  await act(async () => { staleVisibility.resolve(response([comment('stale', 'Stale')])) })
  await act(async () => { await result.current.refreshComments() })
  expect(result.current.comments).toEqual([comment('visible', 'Visible')])

  await act(async () => { await vi.advanceTimersByTimeAsync(60_000) })
  expect(result.current.comments).toEqual([comment('timer', 'Timer')])
  expect(commentRequest).toBe(5)
})

it('finishes a detail load that takes longer than the polling interval', async () => {
  vi.useFakeTimers()
  const delayedChanges = deferred<Response>()
  const changes = { files: [{ path: 'updated.ts', status: 'modified' }] }
  const fetchMock = vi.fn(async (input: string) => {
    if (input === '/api/v1/pull-requests/pr-one') return response(pullRequest)
    if (input.endsWith('/changes')) return (await delayedChanges.promise).clone()
    if (input.endsWith('/commits')) return detailResponse([])
    return response([])
  })
  vi.stubGlobal('fetch', fetchMock)
  const detailRequests = () => fetchMock.mock.calls.filter(([path]) => path === '/api/v1/pull-requests/pr-one').length
  const { result } = renderHook(() => usePullRequestReview('pr-one'))

  await act(async () => { await vi.advanceTimersByTimeAsync(0) })
  expect(result.current.loading).toBe(true)
  expect(result.current.error).toBe('')
  expect(detailRequests()).toBe(1)

  await act(async () => {
    await vi.advanceTimersByTimeAsync(90_000)
    window.dispatchEvent(new Event('focus'))
    document.dispatchEvent(new Event('visibilitychange'))
  })
  expect(detailRequests()).toBeGreaterThanOrEqual(31)
  expect(result.current.loading).toBe(true)

  await act(async () => { delayedChanges.resolve(detailResponse(changes)) })
  expect(result.current.pullRequest).toEqual(pullRequest)
  expect(result.current.changes).toEqual(changes)
  expect(result.current.loading).toBe(false)
  expect(result.current.error).toBe('')

  await act(async () => { await vi.advanceTimersByTimeAsync(30_000) })
  expect(detailRequests()).toBeGreaterThanOrEqual(41)
})

it.each(['commits', 'changes'])('retries failed /%s loads while work is active and resumes lightweight polling after recovery', async (endpoint) => {
  vi.useFakeTimers()
  const commits = [{ sha: 'abc123', message: 'Update file', authored_at: '2026-09-11T00:00:00Z' }]
  const changes = { files: [{ path: 'updated.ts', status: 'modified' }] }
  const error = `Unable to load ${endpoint}: fatal: bad revision`
  let failed = false
  const fetchMock = vi.fn(async (input: string) => {
    if (input.endsWith(`/${endpoint}`) && !failed) {
      failed = true
      return new Response(JSON.stringify({ message: error }), { status: 500 })
    }
    if (input === '/api/v1/pull-requests/pr-one') return response(pullRequest)
    if (input.endsWith('/reviews')) return response([{ id: 'review', status: 'running' }])
    if (input.endsWith('/commits')) return detailResponse(commits)
    if (input.endsWith('/changes')) return detailResponse(changes)
    return response([])
  })
  vi.stubGlobal('fetch', fetchMock)
  const requests = (resource: string) => fetchMock.mock.calls.filter(([path]) => path.endsWith(`/${resource}`)).length
  const { result } = renderHook(() => usePullRequestReview('pr-one'))

  await act(async () => { await vi.advanceTimersByTimeAsync(0) })
  expect(result.current.loading).toBe(false)
  expect(result.current.error).toBe(error)
  expect(result.current.commits).toEqual(endpoint === 'commits' ? [] : commits)
  expect(result.current.changes).toEqual(endpoint === 'changes' ? undefined : changes)
  expect(result.current.reviews[0].status).toBe('running')

  await act(async () => { await vi.advanceTimersByTimeAsync(2999) })
  expect(requests('commits')).toBe(1)
  expect(requests('changes')).toBe(1)
  expect(result.current.error).toBe(error)

  await act(async () => { await vi.advanceTimersByTimeAsync(1) })
  expect(requests('commits')).toBe(endpoint === 'commits' ? 2 : 1)
  expect(requests('changes')).toBe(endpoint === 'changes' ? 2 : 1)
  expect(result.current.pullRequest).toEqual(pullRequest)
  expect(result.current.commits).toEqual(commits)
  expect(result.current.changes).toEqual(changes)
  expect(result.current.error).toBe('')
  expect(result.current.loading).toBe(false)

  await act(async () => { await vi.advanceTimersByTimeAsync(6000) })
  expect(requests('commits')).toBe(endpoint === 'commits' ? 2 : 1)
  expect(requests('changes')).toBe(endpoint === 'changes' ? 2 : 1)
  for (const resource of ['pr-one', 'comments', 'reviews', 'queue', 'workers']) {
    expect(requests(resource)).toBe(resource === 'reviews' ? 5 : 4)
  }
  expect(result.current.error).toBe('')
})

it('keeps successful workers, commits, and changes visible when a later refresh fails', async () => {
  vi.useFakeTimers()
  const workers = [{ id: 'worker', mode: 'continue', status: 'completed' }]
  const commits = [{ sha: 'abc123', message: 'Update file', authored_at: '2026-09-11T00:00:00Z' }]
  const changes = { files: [{ path: 'updated.ts', status: 'modified' }] }
  let failSecondaryLoads = false
  const fetchMock = vi.fn(async (input: string) => {
    if (input === '/api/v1/pull-requests/pr-one') return response(pullRequest)
    if (input.endsWith('/workers')) {
      return failSecondaryLoads
        ? new Response(JSON.stringify({ message: 'Workers unavailable' }), { status: 500 })
        : response(workers)
    }
    if (input.endsWith('/commits')) {
      return failSecondaryLoads
        ? new Response(JSON.stringify({ message: 'Commits unavailable' }), { status: 500 })
        : detailResponse(commits)
    }
    if (input.endsWith('/changes')) {
      return failSecondaryLoads
        ? new Response(JSON.stringify({ message: 'Changes unavailable' }), { status: 500 })
        : detailResponse(changes)
    }
    return response([])
  })
  vi.stubGlobal('fetch', fetchMock)
  const { result } = renderHook(() => usePullRequestReview('pr-one'))

  await act(async () => { await vi.advanceTimersByTimeAsync(0) })
  expect(result.current.workers).toEqual(workers)
  expect(result.current.commits).toEqual(commits)
  expect(result.current.changes).toEqual(changes)

  failSecondaryLoads = true
  await act(async () => { await result.current.refresh() })
  expect(result.current.workers).toEqual(workers)
  expect(result.current.commits).toEqual(commits)
  expect(result.current.changes).toEqual(changes)
  expect(result.current.error).toBe('Changes unavailable')
})

it.each([
  { activeWork: 'reviews', failed: false },
  { activeWork: 'workers', failed: false },
  { activeWork: 'reviews', failed: true },
  { activeWork: 'workers', failed: true },
])('keeps active $activeWork polling during a delayed detail refresh (previous failure: $failed)', async ({ activeWork, failed }) => {
  vi.useFakeTimers()
  const delayedChanges = deferred<Response>()
  let changesRequests = 0
  let update = 0
  const fetchMock = vi.fn(async (input: string) => {
    if (input === '/api/v1/pull-requests/pr-one') return response(pullRequest)
    if (input.endsWith('/comments')) return response([comment('comment', `Update ${update}`)])
    if (input.endsWith('/reviews') || input.endsWith('/workers')) {
      return response([{
        id: 'work',
        status: input.endsWith(`/${activeWork}`) ? 'running' : update ? 'completed' : 'queued',
      }])
    }
    if (input.endsWith('/changes')) {
      changesRequests++
      if (changesRequests > 1) return delayedChanges.promise
      if (failed) return new Response(JSON.stringify({ message: 'Diff unavailable' }), { status: 500 })
      return detailResponse({ files: [] })
    }
    if (input.endsWith('/commits')) return detailResponse([])
    return response([])
  })
  vi.stubGlobal('fetch', fetchMock)
  const { result } = renderHook(() => usePullRequestReview('pr-one'))

  await act(async () => { await vi.advanceTimersByTimeAsync(0) })
  expect(result.current.loading).toBe(false)
  expect(Boolean(result.current.error)).toBe(failed)

  let refresh!: Promise<void>
  await act(async () => { refresh = result.current.refresh() })
  expect(changesRequests).toBe(2)

  update++
  await act(async () => { await vi.advanceTimersByTimeAsync(3000) })
  expect(result.current.comments[0].body).toBe('Update 1')
  expect(result.current.reviews[0].status).toBe(activeWork === 'reviews' ? 'running' : 'completed')
  expect(result.current.workers[0].status).toBe(activeWork === 'workers' ? 'running' : 'completed')

  update++
  await act(async () => { await vi.advanceTimersByTimeAsync(87_000) })
  expect(result.current.comments[0].body).toBe('Update 2')

  update++
  await act(async () => { window.dispatchEvent(new Event('focus')) })
  expect(result.current.comments[0].body).toBe('Update 3')

  update++
  await act(async () => { document.dispatchEvent(new Event('visibilitychange')) })
  expect(result.current.comments[0].body).toBe('Update 4')
  expect(changesRequests).toBe(2)

  const changes = { files: [{ path: 'updated.ts', status: 'modified' }] }
  await act(async () => {
    delayedChanges.resolve(detailResponse(changes))
    await refresh
  })
  expect(result.current.changes).toEqual(changes)
  expect(result.current.error).toBe('')
})

it.each(['review', 'address', 'continue'])('slows polling after %s work settles and refreshes on return to the page', async (kind) => {
  vi.useFakeTimers()
  let active = true
  let outdated = false
  let visible = true
  const visibility = vi.spyOn(document, 'visibilityState', 'get').mockImplementation(() => visible ? 'visible' : 'hidden')
  const fetchMock = vi.fn(async (input: string) => {
    if (input === '/api/v1/pull-requests/pr-one') return response(pullRequest)
    if (input.endsWith('/reviews')) return response([{
      id: 'review', status: kind === 'review' && active ? 'running' : 'completed',
      freshness: { generated: { head_commit: 'original' }, current: { head_commit: outdated ? 'advanced' : 'original' }, outdated },
    }])
    if (input.endsWith('/queue')) return response(kind === 'address' && active ? [{ id: 'address', status: 'waiting_user' }] : [])
    if (input.endsWith('/workers')) return response(kind === 'continue' ? [{ id: 'continue', mode: 'continue', status: active ? 'cancelling' : 'completed' }] : [])
    if (input.endsWith('/changes')) return detailResponse({ files: [] })
    if (input.endsWith('/commits')) return detailResponse([])
    return response([])
  })
  vi.stubGlobal('fetch', fetchMock)
  const requests = () => fetchMock.mock.calls.filter(([path]) => /\/(reviews|comments|queue|workers)$/.test(path)).length
  const { result, unmount } = renderHook(() => usePullRequestReview('pr-one'))
  try {
    await act(async () => { await vi.advanceTimersByTimeAsync(0) })
    const initial = requests()
    active = false
    await act(async () => { await vi.advanceTimersByTimeAsync(3000) })
    expect(requests()).toBe(initial + 4)
    expect(result.current.reviews[0].status).toBe('completed')

    outdated = true
    await act(async () => { await vi.advanceTimersByTimeAsync(12_000) })
    expect(requests()).toBe(initial + 4)
    await act(async () => { await vi.advanceTimersByTimeAsync(48_000) })
    expect(requests()).toBe(initial + 8)
    expect(result.current.reviews[0].freshness?.outdated).toBe(true)

    visible = false
    await act(async () => {
      document.dispatchEvent(new Event('visibilitychange'))
      window.dispatchEvent(new Event('focus'))
      await vi.advanceTimersByTimeAsync(60_000)
    })
    expect(requests()).toBe(initial + 8)
    visible = true
    outdated = false
    await act(async () => { document.dispatchEvent(new Event('visibilitychange')) })
    expect(requests()).toBe(initial + 12)
    expect(result.current.reviews[0].freshness?.outdated).toBe(false)
    await act(async () => { window.dispatchEvent(new Event('focus')) })
    expect(requests()).toBe(initial + 16)
  } finally {
    unmount()
    visibility.mockRestore()
  }
})

it('loads commits and changes while comments are still pending', async () => {
  vi.useFakeTimers()
  const pendingComments = deferred<Response>()
  const commits = [{ sha: 'head', message: 'Latest pushed commit' }]
  const changes = { files: [{ path: 'latest.ts', status: 'added' }] }
  vi.stubGlobal('fetch', vi.fn(async (input: string) => {
    if (input.endsWith('/comments')) return pendingComments.promise
    if (input === '/api/v1/pull-requests/pr-one') return response({ ...pullRequest, head_commit: 'head', base_commit: 'base' })
    if (input.endsWith('/commits')) return detailResponse(commits)
    if (input.endsWith('/changes')) return detailResponse(changes)
    return response([])
  }))
  const { result } = renderHook(() => usePullRequestReview('pr-one'))
  await act(async () => { await vi.advanceTimersByTimeAsync(0) })
  expect(result.current.commits).toEqual(commits)
  expect(result.current.changes).toEqual(changes)
  expect(result.current.loading).toBe(false)
  expect(result.current.comments).toEqual([])
  await act(async () => { pendingComments.resolve(response([comment('later', 'Arrived later')])) })
  expect(result.current.comments).toEqual([comment('later', 'Arrived later')])
})

it('refreshes pushed commits during active work and rejects details for the previous head', async () => {
  vi.useFakeTimers()
  let head = 'first'
  const staleChanges = deferred<Response>()
  let delayChanges = false
  const fetchMock = vi.fn(async (input: string) => {
    if (input === '/api/v1/pull-requests/pr-one') return response({ ...pullRequest, head_commit: head, base_commit: 'base' })
    if (input.endsWith('/reviews')) return response([{ id: 'active', status: 'running' }])
    if (input.endsWith('/commits')) return detailResponse([{ sha: head, message: head }], head)
    if (input.endsWith('/changes')) return delayChanges ? staleChanges.promise : detailResponse({ head_commit: head, files: [] }, head)
    return response([])
  })
  vi.stubGlobal('fetch', fetchMock)
  const { result } = renderHook(() => usePullRequestReview('pr-one'))
  await act(async () => { await vi.advanceTimersByTimeAsync(0) })
  expect(result.current.commits[0].sha).toBe('first')
  delayChanges = true
  let oldRefresh!: Promise<void>
  await act(async () => { oldRefresh = result.current.refresh() })
  head = 'second'
  delayChanges = false
  await act(async () => { await vi.advanceTimersByTimeAsync(3000) })
  expect(result.current.pullRequest?.head_commit).toBe('second')
  expect(result.current.commits[0].sha).toBe('second')
  expect(result.current.changes?.head_commit).toBe('second')
  await act(async () => {
    staleChanges.resolve(detailResponse({ head_commit: 'first', files: [] }, 'first'))
    await oldRefresh
  })
  expect(result.current.changes?.head_commit).toBe('second')
  expect(result.current.commits[0].sha).toBe('second')
  const commitRequests = fetchMock.mock.calls.filter(([path]) => path.endsWith('/commits')).length
  await act(async () => { await vi.advanceTimersByTimeAsync(3000) })
  expect(fetchMock.mock.calls.filter(([path]) => path.endsWith('/commits'))).toHaveLength(commitRequests)
})


it('loads the new head immediately when the first details are still pending', async () => {
  vi.useFakeTimers()
  const firstCommits = deferred<Response>()
  const firstChanges = deferred<Response>()
  const pendingWorkers = deferred<Response>()
  let head = 'first'
  vi.stubGlobal('fetch', vi.fn(async (input: string) => {
    if (input === '/api/v1/pull-requests/pr-one') return response({ ...pullRequest, head_commit: head })
    if (input.endsWith('/workers')) return pendingWorkers.promise
    if (input.endsWith('/commits')) return head === 'first' ? firstCommits.promise : detailResponse([{ sha: head }], head)
    if (input.endsWith('/changes')) return head === 'first' ? firstChanges.promise : detailResponse({ head_commit: head, files: [] }, head)
    return response([])
  }))
  const { result } = renderHook(() => usePullRequestReview('pr-one'))
  await act(async () => { await vi.advanceTimersByTimeAsync(0) })
  expect(result.current.commitsLoading).toBe(true)
  expect(result.current.changesLoading).toBe(true)

  head = 'second'
  await act(async () => { result.current.updatePullRequest({ ...pullRequest, view_revision: 2, head_commit: head }) })
  expect(result.current.commits).toEqual([{ sha: 'second' }])
  expect(result.current.changes?.head_commit).toBe('second')
  expect(result.current.commitsLoading).toBe(false)
  expect(result.current.changesLoading).toBe(false)

  await act(async () => {
    firstCommits.resolve(detailResponse([{ sha: 'first' }], 'first'))
    firstChanges.resolve(detailResponse({ head_commit: 'first', files: [] }, 'first'))
    pendingWorkers.resolve(response([]))
  })
  expect(result.current.commits).toEqual([{ sha: 'second' }])
  expect(result.current.changes?.head_commit).toBe('second')
})

it('reloads the PR and its first details when the server advances before the page sees the new head', async () => {
  vi.useFakeTimers()
  let head = 'first'
  vi.stubGlobal('fetch', vi.fn(async (input: string) => {
    if (input === '/api/v1/pull-requests/pr-one') return response({ ...pullRequest, head_commit: head })
    if (input.endsWith('/commits')) {
      head = 'second'
      return detailResponse([{ sha: head }], head)
    }
    if (input.endsWith('/changes')) return detailResponse({ head_commit: head, files: [] }, head)
    return response([])
  }))
  const { result } = renderHook(() => usePullRequestReview('pr-one'))
  await act(async () => { await vi.advanceTimersByTimeAsync(0) })
  expect(result.current.pullRequest?.head_commit).toBe('second')
  expect(result.current.commits).toEqual([{ sha: 'second' }])
  expect(result.current.changes?.head_commit).toBe('second')
  expect(result.current.error).toBe('')
})

it('backs off after one immediate retry if detail responses keep disagreeing with the PR', async () => {
  vi.useFakeTimers()
  let inconsistent = true
  const fetchMock = vi.fn(async (input: string) => {
    if (input === '/api/v1/pull-requests/pr-one') return response(pullRequest)
    const head = inconsistent ? 'different' : pullRequest.head_commit
    if (input.endsWith('/commits')) return detailResponse([{ sha: head }], head)
    if (input.endsWith('/changes')) return detailResponse({ head_commit: head, files: [] }, head)
    return response([])
  })
  vi.stubGlobal('fetch', fetchMock)
  const { result } = renderHook(() => usePullRequestReview('pr-one'))
  await act(async () => { await vi.advanceTimersByTimeAsync(0) })
  expect(result.current.commits).toEqual([])
  expect(result.current.changes).toBeUndefined()
  expect(result.current.error).toBe('Pull request details changed while loading. Retrying shortly.')
  const requests = () => fetchMock.mock.calls.filter(([path]) => path.endsWith('/changes')).length
  expect(requests()).toBe(2)
  await act(async () => { await vi.advanceTimersByTimeAsync(59_999) })
  expect(requests()).toBe(2)

  inconsistent = false
  await act(async () => { await vi.advanceTimersByTimeAsync(1) })
  expect(result.current.commits).toEqual([{ sha: 'head' }])
  expect(result.current.changes?.head_commit).toBe('head')
  expect(result.current.error).toBe('')
})

it('replaces a pending comparison-base load and keeps successful content stale until each new section arrives', async () => {
  vi.useFakeTimers()
  const oldCommits = deferred<Response>()
  const oldChanges = deferred<Response>()
  const newCommits = deferred<Response>()
  const newChanges = deferred<Response>()
  let base = 'base'
  let delayed = false
  vi.stubGlobal('fetch', vi.fn(async (input: string) => {
    if (input === '/api/v1/pull-requests/pr-one') return response({ ...pullRequest, comparison_state: 'ready', diff_base_commit: base })
    if (input.endsWith('/commits')) return delayed ? (base === 'base' ? oldCommits.promise : newCommits.promise) : detailResponse([{ sha: 'original' }])
    if (input.endsWith('/changes')) return delayed ? (base === 'base' ? oldChanges.promise : newChanges.promise) : detailResponse({ files: [], head_commit: 'original' })
    return response([])
  }))
  const { result } = renderHook(() => usePullRequestReview('pr-one'))
  await act(async () => { await vi.advanceTimersByTimeAsync(0) })
  await act(async () => { result.current.updatePullRequest({ ...pullRequest, view_revision: 2, comparison_state: 'stale' }) })
  expect(result.current.commits).toEqual([{ sha: 'original' }])
  expect(result.current.changes?.head_commit).toBe('original')
  expect(result.current.commitsStale).toBe(true)
  expect(result.current.changesStale).toBe(true)
  delayed = true
  let settled = false
  await act(async () => { void result.current.refresh().then(() => { settled = true }) })
  base = 'comparison-base'
  await act(async () => { result.current.updatePullRequest({ ...pullRequest, view_revision: 3, comparison_state: 'ready', diff_base_commit: base }) })
  expect(result.current.pullRequest?.base_commit).toBe('base')
  expect(result.current.commits).toEqual([{ sha: 'original' }])
  expect(result.current.changes?.head_commit).toBe('original')
  expect(result.current.commitsStale).toBe(true)
  expect(result.current.changesStale).toBe(true)
  expect(settled).toBe(false)
  await act(async () => { newCommits.resolve(response({ inputs: { head_commit: 'head', diff_base_commit: base }, data: [] })) })
  expect(result.current.commits).toEqual([])
  expect(result.current.commitsStale).toBe(false)
  expect(result.current.changesStale).toBe(true)
  await act(async () => { newChanges.resolve(response({ inputs: { head_commit: 'head', diff_base_commit: base }, data: { files: [] } })) })
  expect(result.current.changesStale).toBe(false)
  expect(settled).toBe(true)
  await act(async () => {
    oldCommits.resolve(detailResponse([{ sha: 'late' }]))
    oldChanges.resolve(detailResponse({ files: [], head_commit: 'late' }))
  })
  expect(result.current.commits).toEqual([])
  expect(result.current.changes).toEqual({ files: [] })
  expect(result.current.commitsLoading).toBe(false)
  expect(result.current.changesLoading).toBe(false)
})

it('keeps section failures independent with changes, commits, then PR-read error precedence', async () => {
  vi.useFakeTimers()
  const pendingCommits = deferred<Response>()
  const pendingChanges = deferred<Response>()
  let stage = 0
  const failure = (message: string) => new Response(JSON.stringify({ message }), { status: 500 })
  vi.stubGlobal('fetch', vi.fn(async (input: string) => {
    if (input === '/api/v1/pull-requests/pr-one') return stage ? failure('PR unavailable') : response(pullRequest)
    if (input.endsWith('/workers')) return response([{ id: 'worker', status: 'running' }])
    if (input.endsWith('/commits')) return stage === 0 ? detailResponse([{ sha: 'visible' }]) : stage === 1 ? failure('Commits unavailable') : pendingCommits.promise
    if (input.endsWith('/changes')) return stage === 0 ? detailResponse({ files: [] }) : stage === 1 ? failure('Changes unavailable') : pendingChanges.promise
    return response([])
  }))
  const { result } = renderHook(() => usePullRequestReview('pr-one'))
  await act(async () => { await vi.advanceTimersByTimeAsync(0) })
  stage = 1
  await act(async () => { await result.current.refresh() })
  expect(result.current.error).toBe('Changes unavailable')
  expect(result.current.commits).toEqual([{ sha: 'visible' }])
  expect(result.current.changes).toEqual({ files: [] })
  stage = 2
  await act(async () => { await vi.advanceTimersByTimeAsync(3000) })
  await act(async () => { pendingChanges.resolve(detailResponse({ files: [] })) })
  expect(result.current.error).toBe('Commits unavailable')
  expect(result.current.commits).toEqual([{ sha: 'visible' }])
  await act(async () => { pendingCommits.resolve(detailResponse([])) })
  expect(result.current.error).toBe('PR unavailable')
})

it('joins overlapping full refreshes, awaits work and details, and treats empty results as loaded during lightweight polling', async () => {
  vi.useFakeTimers()
  let delayed = false
  const pendingRead = deferred<Response>()
  const pendingChanges = deferred<Response>()
  const pendingWork = deferred<Response>()
  const pendingComments = deferred<Response>()
  const fetchMock = vi.fn(async (input: string) => {
    if (input === '/api/v1/pull-requests/pr-one') return delayed ? (await pendingRead.promise).clone() : response(pullRequest)
    if (input.endsWith('/changes')) return delayed ? (await pendingChanges.promise).clone() : detailResponse({ files: [] })
    if (input.endsWith('/commits')) return detailResponse([])
    if (input.endsWith('/comments')) return pendingComments.promise
    if (input.endsWith('/workers')) return delayed ? (await pendingWork.promise).clone() : response([{ id: 'worker', status: 'running' }])
    return response([])
  })
  vi.stubGlobal('fetch', fetchMock)
  const requests = (endpoint: string) => fetchMock.mock.calls.filter(([path]) => path.endsWith(`/${endpoint}`)).length
  const { result } = renderHook(() => usePullRequestReview('pr-one'))
  await act(async () => { await vi.advanceTimersByTimeAsync(0) })
  expect(result.current.loading).toBe(false)
  await act(async () => { await vi.advanceTimersByTimeAsync(6000) })
  expect(requests('commits')).toBe(1)
  expect(requests('changes')).toBe(1)
  delayed = true
  let settled = 0
  await act(async () => {
    void result.current.refresh().then(() => { settled++ })
    void result.current.refresh().then(() => { settled++ })
  })
  expect(settled).toBe(0)
  await act(async () => { pendingRead.resolve(response(pullRequest)) })
  expect(requests('commits')).toBe(2)
  expect(requests('changes')).toBe(2)
  await act(async () => { await vi.advanceTimersByTimeAsync(9000) })
  expect(requests('commits')).toBe(2)
  expect(requests('changes')).toBe(2)
  expect(settled).toBe(0)
  await act(async () => { pendingChanges.resolve(detailResponse({ files: [] })) })
  expect(settled).toBe(0)
  await act(async () => { pendingWork.resolve(response([])) })
  expect(settled).toBe(2)
  expect(result.current.comments).toEqual([])
  await act(async () => { pendingComments.resolve(response([])) })
})

it('awaits the coalesced mismatch reread and its bounded detail retry before finishing a full refresh', async () => {
  vi.useFakeTimers()
  let stage = 0
  let reads = 0
  const recoveryRead = deferred<Response>()
  const retryCommits = deferred<Response>()
  const retryChanges = deferred<Response>()
  let commitReads = 0
  let changeReads = 0
  vi.stubGlobal('fetch', vi.fn(async (input: string) => {
    if (input === '/api/v1/pull-requests/pr-one') {
      reads++
      return stage && reads === 3 ? recoveryRead.promise : response(pullRequest)
    }
    if (input.endsWith('/commits')) {
      commitReads++
      return stage === 0 ? detailResponse([]) : commitReads === 2 ? detailResponse([], 'other') : retryCommits.promise
    }
    if (input.endsWith('/changes')) {
      changeReads++
      return stage === 0 ? detailResponse({ files: [] }) : changeReads === 2 ? detailResponse({ files: [] }, 'other') : retryChanges.promise
    }
    return response([])
  }))
  const { result } = renderHook(() => usePullRequestReview('pr-one'))
  await act(async () => { await vi.advanceTimersByTimeAsync(0) })
  stage = 1
  let settled = false
  await act(async () => { void result.current.refresh().then(() => { settled = true }) })
  expect(reads).toBe(3)
  expect(settled).toBe(false)
  await act(async () => { recoveryRead.resolve(response(pullRequest)) })
  expect(commitReads).toBe(3)
  expect(changeReads).toBe(3)
  expect(settled).toBe(false)
  await act(async () => { retryChanges.resolve(detailResponse({ files: [] })) })
  expect(settled).toBe(false)
  await act(async () => { retryCommits.resolve(detailResponse([])) })
  expect(settled).toBe(true)
  expect(result.current.error).toBe('')
})

it('uses the accepted PR after a late older read and starts action-result details before the initial read settles', async () => {
  vi.useFakeTimers()
  const pendingRead = deferred<Response>()
  const fetchMock = vi.fn(async (input: string) => {
    if (input === '/api/v1/pull-requests/pr-one') return pendingRead.promise
    if (input.endsWith('/commits')) return detailResponse([{ sha: 'accepted' }], 'accepted')
    if (input.endsWith('/changes')) return detailResponse({ files: [], head_commit: 'accepted' }, 'accepted')
    return response([])
  })
  vi.stubGlobal('fetch', fetchMock)
  const { result } = renderHook(() => usePullRequestReview('pr-one'))
  await act(async () => { await vi.advanceTimersByTimeAsync(0) })
  await act(async () => { result.current.updatePullRequest({ ...pullRequest, view_revision: 2, head_commit: 'accepted' }) })
  expect(result.current.commits).toEqual([{ sha: 'accepted' }])
  await act(async () => { pendingRead.resolve(response(pullRequest)) })
  expect(result.current.pullRequest?.head_commit).toBe('accepted')
  expect(result.current.changes?.head_commit).toBe('accepted')
  expect(result.current.error).toBe('')
  expect(result.current.loading).toBe(false)
  expect(fetchMock.mock.calls.filter(([path]) => path.endsWith('/commits'))).toHaveLength(1)
})

it('bounds persistent mismatches during active polling without reloading a successful section', async () => {
  vi.useFakeTimers()
  const fetchMock = vi.fn(async (input: string) => {
    if (input === '/api/v1/pull-requests/pr-one') return response(pullRequest)
    if (input.endsWith('/workers')) return response([{ id: 'worker', status: 'running' }])
    if (input.endsWith('/commits')) return detailResponse([])
    if (input.endsWith('/changes')) return detailResponse({ files: [] }, 'different')
    return response([])
  })
  vi.stubGlobal('fetch', fetchMock)
  const requests = (endpoint: string) => fetchMock.mock.calls.filter(([path]) => path.endsWith(`/${endpoint}`)).length
  const { result } = renderHook(() => usePullRequestReview('pr-one'))
  await act(async () => { await vi.advanceTimersByTimeAsync(0) })
  expect(requests('pr-one')).toBe(2)
  expect(requests('commits')).toBe(1)
  expect(requests('changes')).toBe(2)
  await act(async () => { await vi.advanceTimersByTimeAsync(2999) })
  expect(requests('changes')).toBe(2)
  await act(async () => { await vi.advanceTimersByTimeAsync(1) })
  expect(requests('pr-one')).toBe(4)
  expect(requests('commits')).toBe(1)
  expect(requests('changes')).toBe(4)
  expect(result.current.error).toBe('Pull request details changed while loading. Retrying shortly.')
  expect(result.current.changesLoading).toBe(false)
})

it('invalidates pending detail, PR, and work callbacks on navigation and unmount', async () => {
  vi.useFakeTimers()
  const pending = new Map<string, ReturnType<typeof deferred<Response>>>()
  let delayOld = false
  const fetchMock = vi.fn(async (input: string) => {
    if (input.includes('/pr-one') && delayOld) {
      const value = pending.get(input) ?? deferred<Response>()
      pending.set(input, value)
      return (await value.promise).clone()
    }
    if (input === '/api/v1/pull-requests/pr-one' || input === '/api/v1/pull-requests/pr-two') return response({ ...pullRequest, id: input.split('/').pop() })
    if (input.endsWith('/commits')) return detailResponse([])
    if (input.endsWith('/changes')) return detailResponse({ files: [] })
    return response([])
  })
  vi.stubGlobal('fetch', fetchMock)
  const { result, rerender, unmount } = renderHook(({ id }) => usePullRequestReview(id), { initialProps: { id: 'pr-one' } })
  await act(async () => { await vi.advanceTimersByTimeAsync(0) })
  const oldUpdate = result.current.updatePullRequest
  delayOld = true
  await act(async () => {
    void result.current.refresh()
    void result.current.refreshWorkers()
    result.current.updatePullRequest({ ...pullRequest, view_revision: 2, head_commit: 'old-pending' })
  })
  rerender({ id: 'pr-two' })
  await act(async () => { await vi.advanceTimersByTimeAsync(0) })
  expect(result.current.pullRequest?.id).toBe('pr-two')
  await act(async () => {
    oldUpdate({ ...pullRequest, view_revision: 3, title: 'late action' })
    for (const [path, value] of pending) {
      value.resolve(path.endsWith('/pr-one') ? response({ ...pullRequest, view_revision: 3 })
        : path.endsWith('/changes') ? detailResponse({ files: [], head_commit: 'late' }, 'old-pending')
        : path.endsWith('/commits') ? detailResponse([{ sha: 'late' }], 'old-pending')
        : response([{ id: 'late', status: 'running' }]))
    }
  })
  expect(result.current.pullRequest?.id).toBe('pr-two')
  expect(result.current.commits).toEqual([])
  expect(result.current.changes).toEqual({ files: [] })
  expect(result.current.comments).toEqual([])
  expect(result.current.reviews).toEqual([])
  expect(result.current.workers).toEqual([])
  expect(result.current.rebases).toEqual([])
  expect(result.current.queue).toEqual([])
  expect(result.current.error).toBe('')
  expect(result.current.loading).toBe(false)
  expect(result.current.commitsStale).toBe(false)
  expect(result.current.changesStale).toBe(false)
  const pendingUnmount = deferred<Response>()
  fetchMock.mockImplementation(async (input: string) => input.endsWith('/pr-two') ? pendingUnmount.promise : response([]))
  await act(async () => { void result.current.refresh() })
  unmount()
  const calls = fetchMock.mock.calls.length
  await act(async () => { pendingUnmount.resolve(response({ ...pullRequest, id: 'pr-two', view_revision: 2, head_commit: 'after-unmount' })) })
  expect(fetchMock).toHaveBeenCalledTimes(calls)
})

it('loads an accepted action comparison while mismatch recovery is still rereading the PR', async () => {
  vi.useFakeTimers()
  const pendingRead = deferred<Response>()
  let reads = 0
  let head = 'mismatched'
  const fetchMock = vi.fn(async (input: string) => {
    if (input === '/api/v1/pull-requests/pr-one') return ++reads === 1 ? response(pullRequest) : pendingRead.promise
    if (input.endsWith('/commits')) return detailResponse([{ sha: head }], head)
    if (input.endsWith('/changes')) return detailResponse({ files: [], head_commit: head }, head)
    return response([])
  })
  vi.stubGlobal('fetch', fetchMock)
  const { result } = renderHook(() => usePullRequestReview('pr-one'))
  await act(async () => { await vi.advanceTimersByTimeAsync(0) })
  expect(reads).toBe(2)
  head = 'action-result'
  await act(async () => { result.current.updatePullRequest({ ...pullRequest, view_revision: 2, head_commit: head }) })
  expect(result.current.commits).toEqual([{ sha: head }])
  expect(result.current.changes?.head_commit).toBe(head)
  await act(async () => { pendingRead.resolve(response(pullRequest)) })
  expect(result.current.pullRequest?.head_commit).toBe(head)
  expect(result.current.error).toBe('')
  expect(result.current.loading).toBe(false)
  expect(fetchMock.mock.calls.filter(([path]) => path.endsWith('/changes'))).toHaveLength(2)
})

it.each(['closed', 'merged', 'open'] as const)('preserves detail compatibility for %s PRs', async (status) => {
  vi.useFakeTimers()
  vi.stubGlobal('fetch', vi.fn(async (input: string) => {
    if (input === '/api/v1/pull-requests/pr-one') return response({ ...pullRequest, status, comparison_state: status === 'open' ? undefined : 'stale' })
    if (input.endsWith('/commits')) return detailResponse([{ sha: 'head' }])
    if (input.endsWith('/changes')) return detailResponse({ files: [] })
    return response([])
  }))
  const { result } = renderHook(() => usePullRequestReview('pr-one'))
  await act(async () => { await vi.advanceTimersByTimeAsync(0) })
  expect(result.current.commitsStale).toBe(false)
  expect(result.current.changesStale).toBe(false)
})

it('keeps one recurring cached read pending across timer and visibility events', async () => {
  vi.useFakeTimers()
  const pending = deferred<Response>()
  const fetchMock = vi.fn(async (input: string) => {
    if (input === '/api/v1/pull-requests/pr-one') return (await pending.promise).clone()
    if (input.endsWith('/workers')) return response([{ id: 'active-worker', status: 'running' }])
    if (input.endsWith('/commits')) return detailResponse([])
    if (input.endsWith('/changes')) return detailResponse({ files: [] })
    return response([])
  })
  vi.stubGlobal('fetch', fetchMock)
  renderHook(() => usePullRequestReview('pr-one'))
  await act(async () => { await vi.advanceTimersByTimeAsync(0) })
  await act(async () => {
    await vi.advanceTimersByTimeAsync(9000)
    window.dispatchEvent(new Event('focus'))
    document.dispatchEvent(new Event('visibilitychange'))
  })
  const reads = () => fetchMock.mock.calls.filter(([path]) => path === '/api/v1/pull-requests/pr-one').length
  expect(reads()).toBe(1)
  await act(async () => { pending.resolve(response(pullRequest)) })
  await act(async () => { await vi.advanceTimersByTimeAsync(3000) })
  expect(reads()).toBe(2)
})

function stubCachedComments(readComments: () => Response | Promise<Response>) {
  vi.stubGlobal('fetch', vi.fn(async (path: string) => {
    if (path.endsWith('/comments')) return readComments()
    if (path === '/api/v1/pull-requests/pr-one') return response(pullRequest)
    if (path.endsWith('/commits')) return detailResponse([])
    if (path.endsWith('/changes')) return detailResponse({ files: [] })
    return response([])
  }))
}

it('shows cached comments before a slow GitHub refresh and preserves them on failure', async () => {
  vi.useFakeTimers()
  const remote = deferred<{ refreshed: boolean, synced_at: string | null, sync_applicable: boolean }>()
  vi.mocked(api.refreshPullRequestComments).mockReturnValue(remote.promise)
  stubCachedComments(() => response([comment('cached', 'Saved locally')]))
  const { result } = renderHook(() => usePullRequestReview('pr-one'))
  await act(async () => { await vi.advanceTimersByTimeAsync(0) })
  expect(result.current.comments[0].body).toBe('Saved locally')
  expect(result.current.commentsChecking).toBe(true)
  expect(result.current.commentsConfirmed).toBe(false)
  await act(async () => { remote.resolve({ refreshed: true, synced_at: '2026-01-01T00:00:00Z', sync_applicable: true }) })
  expect(result.current.commentsConfirmed).toBe(true)
  vi.mocked(api.refreshPullRequestComments).mockRejectedValue(new Error('GitHub offline'))
  await act(async () => { await vi.advanceTimersByTimeAsync(15_000) })
  expect(result.current.commentsRefreshError).toBe('GitHub offline')
  expect(result.current.comments[0].body).toBe('Saved locally')
  expect(result.current.commentsConfirmed).toBe(true)
})

it.each(['accepted', 'failed', 'superseded'])('confirms an initially empty list only after the refreshed cache read is accepted: %s', async (outcome) => {
  vi.useFakeTimers()
  const remote = deferred<{ refreshed: boolean, synced_at: string | null, sync_applicable: boolean }>()
  vi.mocked(api.refreshPullRequestComments).mockReturnValue(remote.promise)
  const refreshed = deferred<Response>()
  let reads = 0
  stubCachedComments(() => ++reads === 2 ? refreshed.promise : response([]))
  const { result } = renderHook(() => usePullRequestReview('pr-one'))
  await act(async () => { await vi.advanceTimersByTimeAsync(0) })
  await act(async () => { remote.resolve({ refreshed: true, synced_at: '2026-01-01T00:00:00Z', sync_applicable: true }) })
  expect(reads).toBe(2)
  expect(result.current.comments).toEqual([])
  expect(result.current.commentsChecking).toBe(true)
  expect(result.current.commentsConfirmed).toBe(false)

  if (outcome === 'superseded') {
    act(() => { result.current.applyCommentMutation('imported') })
  }
  await act(async () => {
    refreshed.resolve(outcome === 'failed'
      ? new Response(JSON.stringify({ message: 'Cache read failed' }), { status: 500 })
      : response([comment('imported', 'Imported from GitHub')]))
  })
  expect(result.current.commentsChecking).toBe(false)
  expect(result.current.commentsConfirmed).toBe(outcome === 'accepted')
  expect(result.current.comments).toEqual(outcome === 'accepted' ? [comment('imported', 'Imported from GitHub')] : [])
  expect(result.current.commentsRefreshError).toBe(outcome === 'failed' ? 'Cache read failed' : '')
})

it('checks every 15 seconds only while visible, checks on return, and forces manual refresh', async () => {
  vi.useFakeTimers()
  const remote = vi.mocked(api.refreshPullRequestComments).mockResolvedValue({ refreshed: false, synced_at: '2026-01-01T00:00:00Z', sync_applicable: true })
  stubCachedComments(() => response([]))
  let hidden = false
  vi.spyOn(document, 'visibilityState', 'get').mockImplementation(() => hidden ? 'hidden' : 'visible')
  const { result, unmount } = renderHook(() => usePullRequestReview('pr-one'))
  await act(async () => { await vi.advanceTimersByTimeAsync(0) })
  expect(remote).toHaveBeenCalledTimes(1)
  await act(async () => { await vi.advanceTimersByTimeAsync(30_000) })
  expect(remote).toHaveBeenCalledTimes(3)
  hidden = true
  await act(async () => {
    document.dispatchEvent(new Event('visibilitychange'))
    window.dispatchEvent(new Event('focus'))
    await vi.advanceTimersByTimeAsync(60_000)
  })
  expect(remote).toHaveBeenCalledTimes(3)
  hidden = false
  await act(async () => { document.dispatchEvent(new Event('visibilitychange')) })
  expect(remote).toHaveBeenCalledTimes(4)
  await act(async () => { await result.current.checkComments(true) })
  expect(remote).toHaveBeenLastCalledWith('pr-one', true)
  unmount()
  await act(async () => { await vi.advanceTimersByTimeAsync(30_000); window.dispatchEvent(new Event('focus')) })
  expect(remote).toHaveBeenCalledTimes(5)
})

it.each(['edit', 'delete'])('does not let an older cache read undo a successful %s', async (mutation) => {
  vi.useFakeTimers()
  const stale = deferred<Response>()
  let reads = 0
  stubCachedComments(() => ++reads === 1 ? response([comment('one', 'Original')]) : stale.promise)
  const { result } = renderHook(() => usePullRequestReview('pr-one'))
  await act(async () => { await vi.advanceTimersByTimeAsync(0) })
  let pending!: Promise<boolean>
  act(() => { pending = result.current.refreshComments() })
  act(() => { result.current.applyCommentMutation(mutation === 'edit' ? comment('one', 'Edited') : 'one') })
  await act(async () => { stale.resolve(response([comment('one', 'Original')])); await pending })
  expect(result.current.comments).toEqual(mutation === 'edit' ? [comment('one', 'Edited')] : [])
})

it.each(['resolved', 'unresolved'] as const)('removes a locally %s comment omitted by a later successful refresh', async (status) => {
  vi.useFakeTimers()
  vi.mocked(api.refreshPullRequestComments).mockResolvedValue({ refreshed: false, synced_at: null, sync_applicable: false })
  let cached: PullRequestComment[] = [{ ...comment('one', 'Original'), status: status === 'resolved' ? 'unresolved' : 'resolved' }]
  stubCachedComments(() => response(cached))
  const { result } = renderHook(() => usePullRequestReview('pr-one'))
  await act(async () => { await vi.advanceTimersByTimeAsync(0) })

  const stale = deferred<PullRequestComment[]>()
  vi.spyOn(api, 'pullRequestComments').mockReturnValueOnce(stale.promise)
  let pending!: Promise<boolean>
  act(() => { pending = result.current.refreshComments() })
  const saved = { ...cached[0], status }
  act(() => { result.current.applyCommentMutation(saved) })
  cached = [] // Another tab deletes the comment after the local mutation.
  await act(async () => { stale.resolve([]); await pending })
  expect(result.current.comments).toEqual([saved])

  vi.mocked(api.pullRequestComments).mockRejectedValueOnce(new Error('Cache read failed'))
  await act(async () => { expect(await result.current.checkComments(true)).toBe(false) })
  expect(result.current.comments).toEqual([saved])

  await act(async () => { expect(await result.current.checkComments(true)).toBe(true) })
  expect(result.current.comments).toEqual([])
})

it('ignores a remote refresh response from the previous PR page', async () => {
  vi.useFakeTimers()
  const old = deferred<{ refreshed: boolean, synced_at: string | null, sync_applicable: boolean }>()
  vi.mocked(api.refreshPullRequestComments).mockReturnValueOnce(old.promise).mockResolvedValue({ refreshed: false, synced_at: null, sync_applicable: false })
  stubCachedComments(() => response([]))
  const { result, rerender } = renderHook(({ id }) => usePullRequestReview(id), { initialProps: { id: 'pr-one' } })
  await act(async () => { await vi.advanceTimersByTimeAsync(0) })
  rerender({ id: 'pr-two' })
  await act(async () => { await vi.advanceTimersByTimeAsync(0) })
  await act(async () => { old.resolve({ refreshed: false, synced_at: null, sync_applicable: true }) })
  expect(result.current.commentsConfirmed).toBe(true)
  expect(result.current.commentsRefreshError).toBe('')
})

it.each(['published', 'failed'] as const)('polls only cached comments while pending and accepts unchanged-timestamp %s status', async (state) => {
  vi.useFakeTimers()
  let hidden = false
  vi.spyOn(document, 'visibilityState', 'get').mockImplementation(() => hidden ? 'hidden' : 'visible')
  const pending = { ...comment('saved', 'Saved'), publication_state: 'pending' as const, updated_at: '2026-01-01T00:00:00Z' }
  const delayed = deferred<Response>()
  let reads = 0
  stubCachedComments(() => ++reads === 1 ? response([]) : delayed.promise)
  const reviewReads = vi.spyOn(api, 'pullRequestReviews').mockResolvedValue([])
  const workerReads = vi.spyOn(api, 'pullRequestWorkers').mockResolvedValue([])
  const { result, unmount } = renderHook(() => usePullRequestReview('pr-one'))
  await act(async () => { await vi.advanceTimersByTimeAsync(0) })
  const reviewsBefore = reviewReads.mock.calls.length
  const workersBefore = workerReads.mock.calls.length
  act(() => { result.current.applyCommentMutation(pending) })
  expect(result.current.comments).toEqual([pending])
  await act(async () => { await vi.advanceTimersByTimeAsync(3000) })
  expect(reads).toBe(2)
  await act(async () => { await vi.advanceTimersByTimeAsync(6000) })
  expect(reads).toBe(2) // No overlapping cache reads.
  expect(reviewReads).toHaveBeenCalledTimes(reviewsBefore)
  expect(workerReads).toHaveBeenCalledTimes(workersBefore)
  hidden = true
  await act(async () => { delayed.resolve(response([pending])); await vi.advanceTimersByTimeAsync(6000) })
  expect(reads).toBe(2)
  hidden = false
  const settled = { ...pending, publication_state: state }
  vi.spyOn(api, 'pullRequestComments').mockResolvedValue([settled])
  await act(async () => { document.dispatchEvent(new Event('visibilitychange')) })
  expect(result.current.comments).toEqual([settled])
  expect(settled.updated_at).toBe(pending.updated_at)
  const cachedReads = vi.mocked(api.pullRequestComments).mock.calls.length
  await act(async () => { await vi.advanceTimersByTimeAsync(6000) })
  expect(api.pullRequestComments).toHaveBeenCalledTimes(cachedReads)
  unmount()
  await act(async () => { await vi.advanceTimersByTimeAsync(6000) })
  expect(api.pullRequestComments).toHaveBeenCalledTimes(cachedReads)
})

it('waits for a fresh serialized cache read after remote synchronization', async () => {
  vi.useFakeTimers()
  const oldCache = deferred<Response>()
  const remote = deferred<{ refreshed: boolean, synced_at: string, sync_applicable: boolean }>()
  vi.mocked(api.refreshPullRequestComments).mockReturnValue(remote.promise)
  let reads = 0
  stubCachedComments(() => ++reads === 1 ? oldCache.promise : response([comment('new', 'Imported')]))
  const { result } = renderHook(() => usePullRequestReview('pr-one'))
  await act(async () => { await vi.advanceTimersByTimeAsync(0) })
  await act(async () => { remote.resolve({ refreshed: true, synced_at: '2026-01-01T00:00:00Z', sync_applicable: true }) })
  expect(reads).toBe(1)
  expect(result.current.commentsConfirmed).toBe(false)
  await act(async () => { oldCache.resolve(response([])) })
  expect(reads).toBe(2)
  expect(result.current.comments).toEqual([comment('new', 'Imported')])
  expect(result.current.commentsConfirmed).toBe(true)
})
