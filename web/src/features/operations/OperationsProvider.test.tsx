import { act, renderHook } from '@testing-library/react'
import { useContext, type ReactNode } from 'react'
import { api } from '../../data/api'
import type { PullRequest, PullRequestSessionLink } from '../../data/types'
import { HolonStoreContext } from '../project/holonStoreContext'
import { OperationsProvider } from './OperationsProvider'
import { OperationsContext } from './operationsContext'

beforeEach(() => vi.useFakeTimers())
afterEach(() => { vi.restoreAllMocks(); vi.useRealTimers() })

function Wrapper({ children }: { children: ReactNode }) {
  return (
    <HolonStoreContext.Provider value={{ holons: [], loading: false, error: undefined, getHolon: vi.fn(), updateHolon: vi.fn(), selectHolonTab: vi.fn(), activateHolon: vi.fn(), dismissSelectionError: vi.fn(), selectionErrors: {}, refresh: vi.fn() }}>
      <OperationsProvider projectId="preview">{children}</OperationsProvider>
    </HolonStoreContext.Provider>
  )
}

async function renderOperations() {
  const hook = renderHook(() => useContext(OperationsContext)!, { wrapper: Wrapper })
  await act(async () => { await vi.advanceTimersByTimeAsync(0) })
  return hook
}

it('loads two project-level projections and maps every linked holon', async () => {
  const pullRequest = previewPullRequest()
  vi.spyOn(api, 'pullRequests').mockResolvedValue([pullRequest])
  vi.spyOn(api, 'pullRequestSessionLinks').mockResolvedValue([
    { pull_request_id: pullRequest.id, session_id: 'feature-holon' },
    { pull_request_id: pullRequest.id, session_id: 'metadata-holon' },
    { pull_request_id: pullRequest.id, session_id: 'review-holon' },
    { pull_request_id: pullRequest.id, session_id: 'address-holon' },
    { pull_request_id: pullRequest.id, session_id: 'rebase-holon' },
    { pull_request_id: 'missing-pr', session_id: 'ignored-holon' },
  ])

  const { result } = await renderOperations()
  const pullRequestByHolon = result.current.pullRequestByHolon!

  expect(api.pullRequests).toHaveBeenCalledOnce()
  expect(api.pullRequests).toHaveBeenCalledWith('preview')
  expect(api.pullRequestSessionLinks).toHaveBeenCalledOnce()
  expect(api.pullRequestSessionLinks).toHaveBeenCalledWith('preview')
  expect([...pullRequestByHolon.keys()]).toEqual([
    'feature-holon', 'metadata-holon', 'review-holon', 'address-holon', 'rebase-holon',
  ])
  for (const pullRequestId of pullRequestByHolon.values()) expect(pullRequestId).toBe(pullRequest)
})

it.each(['pending', 'failed'])('publishes PRs while the first sidebar-link request is %s', async (state) => {
  const pullRequest = previewPullRequest()
  vi.spyOn(api, 'pullRequests').mockResolvedValue([pullRequest])
  let finishLinks!: (links: PullRequestSessionLink[]) => void
  vi.spyOn(api, 'pullRequestSessionLinks').mockImplementation(() => state === 'failed'
    ? Promise.reject(new Error('Links unavailable'))
    : new Promise((resolve) => { finishLinks = resolve }))

  const { result } = await renderOperations()

  expect(result.current.pullRequestList).toMatchObject({ data: [pullRequest], loading: false, error: undefined })
  if (state === 'pending') {
    await act(async () => { finishLinks([{ pull_request_id: pullRequest.id, session_id: 'review-holon' }]) })
    expect(result.current.pullRequestByHolon?.get('review-holon')).toEqual(pullRequest)
  }
})

it('refreshes PRs without waiting for links and retains previous links when their refresh fails', async () => {
  const pullRequest = previewPullRequest()
  const updated = { ...pullRequest, title: 'Updated after sync', view_revision: 2 }
  vi.spyOn(api, 'pullRequests').mockResolvedValueOnce([pullRequest]).mockResolvedValue([updated])
  let failLinks!: (error: Error) => void
  vi.spyOn(api, 'pullRequestSessionLinks')
    .mockResolvedValueOnce([{ pull_request_id: pullRequest.id, session_id: 'review-holon' }])
    .mockImplementation(() => new Promise((_resolve, reject) => { failLinks = reject }))
  const { result } = await renderOperations()
  expect(result.current.pullRequestByHolon?.get('review-holon')).toEqual(pullRequest)

  await act(async () => { await result.current.pullRequestList!.refresh() })
  expect(result.current.pullRequestList).toMatchObject({ data: [updated], loading: false, error: undefined })
  expect(result.current.pullRequestByHolon?.get('review-holon')).toEqual(updated)

  await act(async () => { failLinks(new Error('Links unavailable')) })
  expect(result.current.pullRequestList).toMatchObject({ data: [updated], loading: false, error: undefined })
  expect(result.current.pullRequestByHolon?.get('review-holon')).toEqual(updated)
})

function previewPullRequest(): PullRequest {
  return { view_revision: 1,
    id: 'pr-1', title: 'Keep PR activity together', summary: '',
    base_branch: 'main', base_commit: 'base', head_branch: 'feature/sidebar', head_commit: 'head', status: 'open',
    sync_data: {}, assignee_holark_ids: [], requested_reviewer_holark_ids: [], linked_session_ids: ['feature-holon'],
    created_at: '2026-08-26T10:00:00Z', updated_at: '2026-08-26T10:01:00Z',
  }
}
