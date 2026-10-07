import { useCallback, useContext, useEffect, useMemo, useRef, useState } from 'react'
import { api } from '../../data/api'
import { acceptPullRequest } from '../../data/pullRequestAcceptance'
import { OperationsContext } from '../operations/operationsContext'
import { isActivePullRequestStatus } from './lifecycle'
import { pullRequestRevision } from './pullRequestPageState'
import type { PullRequest, PullRequestDetail, PullRequestComment, PullRequestCommit, PullRequestParticipantSnapshot, PullRequestQueueItem, PullRequestRebase, PullRequestReview, PullRequestWorker, WorkspaceInspection } from '../../data/types'

type PullRequestReviewState = {
  pullRequest?: PullRequest
  commits: PullRequestCommit[]
  comments: PullRequestComment[]
  reviews: PullRequestReview[]
  queue: PullRequestQueueItem[]
  rebases: PullRequestRebase[]
  addRebase: (rebase: PullRequestRebase) => void
  workers: PullRequestWorker[]
  changes?: WorkspaceInspection
  loading: boolean
  commitsLoading: boolean
  changesLoading: boolean
  commitsStale: boolean
  changesStale: boolean
  error: string
  refresh: () => Promise<void>
  refreshRebaseState: () => Promise<void>
  refreshComments: () => Promise<boolean>
  checkComments: (force?: boolean) => Promise<boolean>
  commentsChecking: boolean
  commentsSyncing: boolean
  commentsRefreshError: string
  commentsConfirmed: boolean
  applyCommentMutation: (comment: PullRequestComment | string) => void
  refreshReviewState: () => Promise<void>
  addReview: (review: PullRequestReview) => void
  refreshWorkers: () => Promise<void>
  updatePullRequest: (pullRequest: PullRequest) => void
  updatePullRequestParticipants: (snapshot: PullRequestParticipantSnapshot) => void
}

type PageLifetime = { id: string, active: boolean, generation: number, reads: number }
type DetailMode = 'missing' | 'retry' | 'mismatch' | number
type DetailAttempt = { key: string, promise: Promise<boolean>, cancel: () => void }
const mismatchError = 'Pull request details changed while loading. Retrying shortly.'

// Each section owns its request and keeps its last successful content. Cancelling
// ownership also settles callers, even when the underlying HTTP read is still pending.
function useDetailSection<T>(page: PageLifetime, currentPullRequest: { current: PullRequest | undefined }, read: (id: string) => Promise<PullRequestDetail<T>>) {
  const [data, setData] = useState<T>()
  const [successfulKey, setSuccessfulKey] = useState('')
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(true)
  const bookkeeping = useRef({ attemptedKey: '', successfulKey: '', error: '', serial: 0, pending: undefined as DetailAttempt | undefined })

  const [displayedPage, setDisplayedPage] = useState(page)
  if (displayedPage !== page) {
    setDisplayedPage(page)
    setData(undefined)
    setSuccessfulKey('')
    setError('')
    setLoading(true)
  }
  useEffect(() => {
    bookkeeping.current = { attemptedKey: '', successfulKey: '', error: '', serial: 0, pending: undefined }
    return () => { bookkeeping.current.pending?.cancel() }
  }, [page])

  const ensure = useCallback((input: PullRequest | undefined, mode: DetailMode): Promise<boolean> => {
    const section = bookkeeping.current
    if (!input?.head_commit || !(input.diff_base_commit || input.base_commit)) {
      section.pending?.cancel()
      section.pending = undefined
      setLoading(false)
      return Promise.resolve(false)
    }
    const key = artifactRevision(input)
    if (section.pending?.key === key) return section.pending.promise
    const reload = typeof mode === 'number' ? mode === section.serial
      : mode === 'retry' ? Boolean(section.error) || section.successfulKey !== key
      : mode === 'mismatch' && section.error === mismatchError
    if (section.attemptedKey === key && !reload) return Promise.resolve(false)
    section.pending?.cancel()
    section.attemptedKey = key
    section.serial++
    setLoading(true)
    const generation = page.generation
    const owns = () => page.active && page.generation === generation && section.pending === attempt
    let cancel!: () => void
    const cancelled = new Promise<boolean>((resolve) => { cancel = () => resolve(false) })
    const attempt: DetailAttempt = { key, cancel, promise: Promise.resolve(false) }
    section.pending = attempt
    attempt.promise = Promise.race([cancelled, read(input.id).then((value) => {
      if (!owns() || key !== artifactRevision(currentPullRequest.current)) return false
      if (value.inputs.head_commit !== input.head_commit || value.inputs.diff_base_commit !== (input.diff_base_commit || input.base_commit)) {
        section.error = mismatchError
        setError(mismatchError)
        return true
      }
      section.successfulKey = key
      section.error = ''
      setData(value.data)
      setSuccessfulKey(key)
      setError('')
      return false
    }).catch((value) => {
      if (owns() && key === artifactRevision(currentPullRequest.current)) {
        section.error = requestError(value)
        setError(section.error)
      }
      return false
    })]).finally(() => {
      if (owns()) {
        section.pending = undefined
        setLoading(false)
      }
    })
    return attempt.promise
  }, [page, currentPullRequest, read])

  return { data, successfulKey, error, loading, ensure, bookkeeping }
}

export function usePullRequestReview(pullRequestId: string, canPoll?: () => boolean): PullRequestReviewState {
  const markPullRequestMerged = useContext(OperationsContext)?.markPullRequestMerged
  const [pullRequest, setPullRequest] = useState<PullRequest>()
  const [comments, setComments] = useState<PullRequestComment[]>([])
  const [commentsChecking, setCommentsChecking] = useState(false)
  const [commentsSyncing, setCommentsSyncing] = useState(false)
  const [commentsRefreshError, setCommentsRefreshError] = useState('')
  const [commentsConfirmed, setCommentsConfirmed] = useState(false)
  const pendingCommentCheck = useRef<Promise<boolean> | undefined>(undefined)
  const stopCommentProgress = useRef<(() => void) | undefined>(undefined)
  const [reviews, setReviews] = useState<PullRequestReview[]>([])
  const [queue, setQueue] = useState<PullRequestQueueItem[]>([])
  const [rebases, setRebases] = useState<PullRequestRebase[]>([])
  const [workers, setWorkers] = useState<PullRequestWorker[]>([])
  const [readError, setReadError] = useState('')
  const [loading, setLoading] = useState(true)
  const page = useMemo<PageLifetime>(() => ({ id: pullRequestId, active: true, generation: 0, reads: 0 }), [pullRequestId])
  const currentPullRequest = useRef<PullRequest | undefined>(undefined)
  const hasLoaded = useRef(false)
  const fullRefreshes = useRef(0)
  const commentsRequest = useRef(0)
  const pendingCommentRead = useRef<{ page: PageLifetime, generation: number, request: number, promise: Promise<boolean> } | undefined>(undefined)
  const reviewsRequest = useRef(0)
  const readRequest = useRef(0)
  const workRequest = useRef(0)
  const workersRequest = useRef(0)
  const rebaseRequest = useRef(0)
  const recovery = useRef<Promise<void> | undefined>(undefined)
  const commits = useDetailSection(page, currentPullRequest, api.pullRequestCommits)
  const changes = useDetailSection(page, currentPullRequest, api.pullRequestChanges)
  const ensureCommits = commits.ensure
  const ensureChanges = changes.ensure
  const [displayedPage, setDisplayedPage] = useState(page)
  if (displayedPage !== page) {
    setDisplayedPage(page)
    setPullRequest(undefined)
    setComments([])
    setCommentsChecking(false)
    setCommentsSyncing(false)
    setCommentsRefreshError('')
    setCommentsConfirmed(false)
    setReviews([])
    setQueue([])
    setWorkers([])
    setRebases([])
    setLoading(true)
    setReadError('')
  }

  const applyPullRequest = useCallback((next: PullRequest) => {
    if (!page.active || next.id !== pullRequestId) return
    const accepted = acceptPullRequest(next)
    currentPullRequest.current = accepted
    setPullRequest(accepted)
    if (accepted.status === 'merged') markPullRequestMerged?.(accepted.id)
    return accepted
  }, [page, pullRequestId, markPullRequestMerged])

  const readPullRequest = useCallback(async () => {
    const generation = page.generation
    const request = ++readRequest.current
    page.reads++
    try {
      const next = await api.pullRequest(pullRequestId)
      if (!page.active || page.generation !== generation) return
      // PR acceptance is revision/mutation based, independent of detail ownership.
      applyPullRequest(next)
      if (request === readRequest.current) setReadError('')
    } catch (value) {
      if (page.active && page.generation === generation && request === readRequest.current) setReadError(requestError(value))
    } finally { page.reads-- }
  }, [page, pullRequestId, applyPullRequest])

  const ensureDetails = useCallback(async function ensure(mode: DetailMode = 'missing', changesMode = mode, retryMismatch = true): Promise<void> {
    if (!page.active) return
    const pendingRecovery = retryMismatch ? recovery.current : undefined
    if (pendingRecovery) {
      // A slow recovery reread must not hold back a newly accepted comparison.
      await ensure('missing', 'missing', false)
      await pendingRecovery
      return
    }
    const input = currentPullRequest.current
    const generation = page.generation
    const results = await Promise.all([ensureCommits(input, mode), ensureChanges(input, changesMode)])
    if (!page.active || page.generation !== generation) return
    if (artifactRevision(input) !== artifactRevision(currentPullRequest.current)) {
      await ensure('missing', 'missing', retryMismatch)
    } else if (retryMismatch && results.some(Boolean)) {
      // Both sections share one reread and one immediate retry. No state-driven
      // effect retries an error; persistent mismatches wait for the next poll.
      if (!recovery.current) {
        const pending = (async () => {
          await readPullRequest()
          if (page.active && page.generation === generation) await ensure('mismatch', 'mismatch', false)
        })()
        recovery.current = pending
        void pending.finally(() => { if (recovery.current === pending) recovery.current = undefined })
      }
      await recovery.current
    }
  }, [page, ensureCommits, ensureChanges, readPullRequest])

  const updatePullRequest = useCallback((next: PullRequest) => {
    applyPullRequest(next)
    if (page.active) void ensureDetails()
  }, [page, applyPullRequest, ensureDetails])

  const refreshComments = useCallback(function readComments(): Promise<boolean> {
    if (!page.active) return Promise.resolve(false)
    const pending = pendingCommentRead.current
    if (pending?.page === page && pending.generation === page.generation) {
      if (pending.request === commentsRequest.current) return pending.promise
      // A save invalidated the in-flight read. Follow it with a fresh read without overlap.
      return pending.promise.catch(() => false).then(readComments)
    }
    const generation = page.generation
    const request = ++commentsRequest.current
    const owns = () => page.active && page.generation === generation && request === commentsRequest.current
    const promise = api.pullRequestComments(pullRequestId).then((next) => {
      if (!owns()) return false
      // Accepted reads started after the latest mutation and include any deletions.
      setComments(next)
      return true
    }).catch((value) => {
      if (owns()) throw value
      return false
    }).finally(() => {
      if (pendingCommentRead.current?.promise === promise) pendingCommentRead.current = undefined
    })
    pendingCommentRead.current = { page, generation, request, promise }
    return promise
  }, [page, pullRequestId])

  const applyCommentMutation = useCallback((value: PullRequestComment | string) => {
    if (!page.active || (typeof value !== 'string' && value.pull_request_id !== pullRequestId)) return
    // Reads started before this mutation must not restore the old status.
    commentsRequest.current++
    setComments((current) => typeof value === 'string'
      ? current.filter((comment) => comment.id !== value && comment.parent_comment_id !== value)
      : current.some((comment) => comment.id === value.id)
        ? current.map((comment) => comment.id === value.id ? value : comment)
        : [...current, value])
  }, [page, pullRequestId])

  const checkComments = useCallback((force = false): Promise<boolean> => {
    if (!page.active) return Promise.resolve(false)
    if (pendingCommentCheck.current) return pendingCommentCheck.current
    const generation = page.generation
    const owns = () => page.active && page.generation === generation
    setCommentsChecking(true)
    // Observe the actual backend phase for both automatic and manual refreshes.
    // Checks, queued work, and cache reads never activate the sync indicator.
    const progress = new AbortController()
    let readingProgress = false
    const timer = window.setInterval(async () => {
      if (!owns() || progress.signal.aborted || readingProgress || document.visibilityState === 'hidden') return
      readingProgress = true
      try {
        const status = await api.pullRequestCommentRefreshStatus(pullRequestId, progress.signal)
        if (owns() && !progress.signal.aborted) setCommentsSyncing(status.syncing)
      } catch { /* Progress is optional; the refresh itself reports failures. */ }
      finally { readingProgress = false }
    }, 500)
    const stopProgress = () => {
      window.clearInterval(timer)
      progress.abort()
    }
    stopCommentProgress.current = stopProgress
    const pending = (async () => {
      try {
        const result = await api.refreshPullRequestComments(pullRequestId, force)
        stopProgress()
        if (!owns()) return false
        setCommentsSyncing(false)
        // A cache read begun before synchronization cannot confirm its result.
        if (pendingCommentRead.current) commentsRequest.current++
        if (!await refreshComments() || !owns()) return false
        setCommentsConfirmed(Boolean(result.synced_at) || result.sync_applicable === false)
        setCommentsRefreshError('')
        return true
      } catch (value) {
        if (owns()) setCommentsRefreshError(requestError(value))
        return false
      } finally {
        stopProgress()
        if (owns()) {
          stopCommentProgress.current = undefined
          setCommentsChecking(false)
          setCommentsSyncing(false)
          pendingCommentCheck.current = undefined
        }
      }
    })()
    pendingCommentCheck.current = pending
    return pending
  }, [page, pullRequestId, refreshComments])

  const refreshReviews = useCallback(async () => {
    const generation = page.generation
    const request = ++reviewsRequest.current
    const owns = () => page.active && page.generation === generation && request === reviewsRequest.current
    try {
      const next = await api.pullRequestReviews(pullRequestId)
      if (owns()) setReviews(next)
    } catch (value) { if (owns()) throw value }
  }, [page, pullRequestId])

  const refreshWorkers = useCallback(async () => {
    const generation = page.generation
    const request = ++workersRequest.current
    const next = await api.pullRequestWorkers(pullRequestId)
    if (page.active && page.generation === generation && request === workersRequest.current) setWorkers(next)
  }, [page, pullRequestId])

  const refreshRebases = useCallback(async () => {
    const generation = page.generation
    const request = ++rebaseRequest.current
    const next = await api.pullRequestRebases(pullRequestId)
    if (page.active && page.generation === generation && request === rebaseRequest.current) setRebases(next)
  }, [page, pullRequestId])

  const refreshWork = useCallback(async () => {
    const generation = page.generation
    const request = ++workRequest.current
    await Promise.allSettled([
      api.pullRequestQueue(pullRequestId).then((next) => {
        if (page.active && page.generation === generation && request === workRequest.current) setQueue(next)
      }),
      refreshWorkers(), refreshRebases(),
    ])
  }, [page, pullRequestId, refreshWorkers, refreshRebases])

  const refreshReviewState = useCallback(async () => {
    const generation = page.generation
    await Promise.all([
      refreshComments(), refreshReviews(), refreshWork(),
      readPullRequest().then(() => { if (page.active && page.generation === generation) return ensureDetails('retry') }),
    ])
  }, [page, refreshComments, refreshReviews, refreshWork, readPullRequest, ensureDetails])

  const reviewBase = pullRequest?.diff_base_commit || pullRequest?.base_commit
  const reviewHead = pullRequest?.head_commit
  useEffect(() => {
    if (reviewHead) void refreshReviews().catch(() => undefined)
  }, [refreshReviews, reviewBase, reviewHead])

  const addReview = useCallback((review: PullRequestReview) => {
    if (!page.active) return
    reviewsRequest.current++
    setReviews((current) => [...current.filter((item) => item.id !== review.id), review])
  }, [page])

  const addRebase = useCallback((rebase: PullRequestRebase) => {
    if (!page.active) return
    workRequest.current++
    rebaseRequest.current++
    setRebases((current) => [...current.filter((item) => item.id !== rebase.id), rebase])
    if (['queued', 'running', 'waiting_user', 'cancelling'].includes(rebase.status)) {
      setQueue((current) => [...current.filter((item) => item.id !== rebase.id), rebase])
    }
  }, [page])

  const updatePullRequestParticipants = useCallback((snapshot: PullRequestParticipantSnapshot) => {
    if (!page.active) return
    setPullRequest((current) => current?.id === snapshot.pull_request_id
      ? { ...current, assignee_holark_ids: snapshot.assignee_holark_ids, requested_reviewer_holark_ids: snapshot.requested_reviewer_holark_ids }
      : current)
  }, [page])

  const refresh = useCallback(async () => {
    if (!page.active) return
    const generation = page.generation
    const commitsAttempt = commits.bookkeeping.current.serial
    const changesAttempt = changes.bookkeeping.current.serial
    fullRefreshes.current++
    if (!hasLoaded.current) setLoading(true)
    void refreshComments().catch(() => undefined)
    void refreshReviews().catch(() => undefined)
    try {
      await Promise.all([
        refreshWork(),
        readPullRequest().then(() => {
          if (page.active && page.generation === generation) return ensureDetails(commitsAttempt, changesAttempt)
        }),
      ])
    } finally {
      if (page.active && page.generation === generation) {
        fullRefreshes.current--
        hasLoaded.current = true
        if (!fullRefreshes.current) setLoading(false)
      }
    }
  }, [page, commits.bookkeeping, changes.bookkeeping, refreshComments, refreshReviews, refreshWork, readPullRequest, ensureDetails])

  const refreshRebaseState = useCallback(async () => {
    const generation = page.generation
    await Promise.all([
      refreshRebases(),
      readPullRequest().then(() => { if (page.active && page.generation === generation) return ensureDetails() }),
    ])
  }, [page, refreshRebases, readPullRequest, ensureDetails])

  useEffect(() => {
    page.active = true
    page.generation++
    hasLoaded.current = false
    fullRefreshes.current = 0
    recovery.current = undefined
    pendingCommentCheck.current = undefined
    pendingCommentRead.current = undefined
    currentPullRequest.current = undefined
    return () => {
      stopCommentProgress.current?.()
      page.active = false
      page.generation++
    }
  }, [page])

  useEffect(() => {
    const timer = window.setTimeout(() => { void refresh() })
    return () => window.clearTimeout(timer)
  }, [refresh])

  // Cached lifecycle/topology reads must not wait for readiness or detail I/O.
  useEffect(() => {
    let running = false
    const poll = async () => {
      if (!page.active || page.reads > 0 || running || document.visibilityState === 'hidden' || canPoll?.() === false) return
      running = true
      try {
        await readPullRequest()
        if (page.active) void ensureDetails()
      } finally { running = false }
    }
    const refreshIfVisible = () => { void poll() }
    const timer = window.setInterval(refreshIfVisible, 3000)
    window.addEventListener('focus', refreshIfVisible)
    document.addEventListener('visibilitychange', refreshIfVisible)
    return () => {
      window.clearInterval(timer)
      window.removeEventListener('focus', refreshIfVisible)
      document.removeEventListener('visibilitychange', refreshIfVisible)
    }
  }, [page, canPoll, readPullRequest, ensureDetails])

  useEffect(() => {
    const checkIfVisible = () => {
      if (document.visibilityState !== 'hidden') void checkComments()
    }
    checkIfVisible()
    const timer = window.setInterval(checkIfVisible, 15_000)
    window.addEventListener('focus', checkIfVisible)
    document.addEventListener('visibilitychange', checkIfVisible)
    return () => {
      window.clearInterval(timer)
      window.removeEventListener('focus', checkIfVisible)
      document.removeEventListener('visibilitychange', checkIfVisible)
    }
  }, [checkComments])

  const hasActiveWork = [...reviews, ...queue, ...workers, ...rebases].some((work) =>
    ['queued', 'running', 'waiting_user', 'cancelling'].includes(work.status))
  const hasPendingComments = comments.some((comment) => comment.publication_state === 'pending')
  const lastWorkPoll = useRef(Date.now())
  useEffect(() => {
    const refreshIfVisible = (event?: Event) => {
      if (!page.active || document.visibilityState === 'hidden') return
      const workDue = hasActiveWork || (!hasPendingComments && Boolean(event)) || Date.now() - lastWorkPoll.current >= 60_000
      if (hasPendingComments || workDue) void refreshComments().catch(() => undefined)
      if (!workDue || (!hasActiveWork && fullRefreshes.current)) return
      lastWorkPoll.current = Date.now()
      void Promise.allSettled([refreshReviews(), refreshWork(), ensureDetails('retry')])
    }
    const timer = window.setInterval(refreshIfVisible, hasActiveWork || hasPendingComments ? 3000 : 60_000)
    window.addEventListener('focus', refreshIfVisible)
    document.addEventListener('visibilitychange', refreshIfVisible)
    return () => {
      window.clearInterval(timer)
      window.removeEventListener('focus', refreshIfVisible)
      document.removeEventListener('visibilitychange', refreshIfVisible)
    }
  }, [page, hasActiveWork, hasPendingComments, refreshComments, refreshReviews, refreshWork, ensureDetails])

  const revision = artifactRevision(pullRequest)
  const comparisonStale = Boolean(pullRequest && isActivePullRequestStatus(pullRequest.status)
    && pullRequest.comparison_state && pullRequest.comparison_state !== 'ready')
  return {
    pullRequest, commits: commits.data ?? [], comments, reviews, queue, workers, rebases, addRebase, changes: changes.data,
    loading, commitsLoading: commits.loading, changesLoading: changes.loading,
    commitsStale: Boolean(commits.successfulKey && (comparisonStale || commits.successfulKey !== revision)),
    changesStale: Boolean(changes.successfulKey && (comparisonStale || changes.successfulKey !== revision)),
    error: changes.error || commits.error || readError,
    checkComments, commentsChecking, commentsSyncing, commentsRefreshError, commentsConfirmed, applyCommentMutation,
    refresh, refreshRebaseState, refreshComments, refreshReviewState, addReview, refreshWorkers, updatePullRequest, updatePullRequestParticipants,
  }
}

function artifactRevision(pullRequest?: PullRequest) {
  return pullRequestRevision(pullRequest?.id, pullRequest?.diff_base_commit || pullRequest?.base_commit, pullRequest?.head_commit)
}

function requestError(value: unknown) {
  return (value as { message?: string })?.message || 'Request failed.'
}
