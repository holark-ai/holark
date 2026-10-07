import { ArrowRight, Bot, Check, CheckCircle2, ChevronDown, ChevronRight, ChevronUp, Circle, Copy, ExternalLink, GitBranch, GitMerge, GitPullRequest, MessageSquare, MoreHorizontal, MoreVertical, Pencil, RefreshCw, Send, Trash2, X } from 'lucide-react'
import type { FormEvent, MouseEvent, ReactNode } from 'react'
import { useCallback, useEffect, useId, useReducer, useRef, useState } from 'react'
import { Link, useParams, useSearchParams } from 'react-router-dom'
import { Button } from '../../components/Button'
import { Dialog } from '../../components/Dialog'
import { DismissiblePopoverGroup, useDismissiblePopover, useOptionalDismissiblePopover } from '../../components/DismissiblePopover'
import { StatusBadge } from '../../components/StatusBadge'
import { TabSelectionIndicator } from '../../components/TabSelectionIndicator'
import { CommentComposer } from './CommentComposer'
import { MarkdownEditor } from '../attachments/MarkdownEditor'
import { PullRequestThreadContext, PullRequestThreadLocation } from './PullRequestThreadContext'
import { commentLocationKey, useCommentDrafts } from './useCommentDrafts'
import type { ScopedComments } from './useCommentDrafts'
import { api, type ApiError } from '../../data/api'
import type { PullRequest, PullRequestComment, PullRequestCommentLocation, PullRequestCommit, PullRequestParticipantSnapshot, PullRequestQueueItem, PullRequestRebase, PullRequestRebaseReadiness, PullRequestReview, PullRequestReviewMode, PullRequestTransitionStatus, PullRequestWorker, PullRequestWorkerMode, HarnessType, WorkspaceInspection } from '../../data/types'
import { useProject } from '../project/ProjectContext'
import { ProfileLink } from '../profile/ProfileLink'
import { HolonTile } from '../agents/HolonTile'
import { holonDisplayStatus } from '../agents/holonDisplay'
import { HolonComposer } from '../agents/HolonComposer'
import { NewAgentDialog } from '../agents/NewAgentDialog'
import { useOptionalHolonStore } from '../project/holonStoreContext'
import { PullRequestChanges } from './PullRequestChanges'
import { ResolutionAction } from './ResolutionAction'
import { PullRequestParticipants } from './PullRequestParticipants'
import { PullRequestMetadataCard, PullRequestMarkdown } from './PullRequestMetadataCard'
import { creationOption, defaultPullRequestCreationStatus, isActivePullRequestStatus, lifecycleCommands, mergeBlockedReasonLabel, pullRequestCreationOptions, type PullRequestCreationStatus } from './lifecycle'
import { MenuItem, MenuPopover } from '../../components/Menu'
import { SplitButtonMenu, SplitButtonMenuItem } from '../../components/SplitButtonMenu'
import { MemberIdentity } from '../members/MemberIdentity'
import { usePullRequestReview } from './usePullRequestReview'
import { ManualReviewActivity, ReviewSubmission, useManualReviews } from './ReviewSubmission'
import type { ManualReviewSubmission } from '../../data/types'
import { useDropdownMenu } from '../../components/useDropdownMenu'
import { initialPullRequestPageState, pullRequestPageReducer, pullRequestRevision, readinessAfterRebase, readinessMatchesRevision, recommendPullRequestAction, type ReadinessPhase } from './pullRequestPageState'
import styles from './PullRequests.module.css'
import detail from './PullRequestDetail.module.css'

export { PullRequestList } from '../workItems/WorkItems'

export function PullRequestDetail() {
  const { pullRequestId = '' } = useParams()
  return <PullRequestDetailPage key={pullRequestId} pullRequestId={pullRequestId} />
}

function PullRequestDetailPage({ pullRequestId }: { pullRequestId: string }) {
  const project = useProject()
  const holonStore = useOptionalHolonStore()
  const transitionInFlight = useRef(false)
  const mergeInFlight = useRef(false)
  const rebaseInFlight = useRef(false)
  const canPoll = useCallback(() => !transitionInFlight.current && !mergeInFlight.current && !rebaseInFlight.current, [])
  const { pullRequest, commits, comments, reviews, queue, workers, rebases, addRebase, changes, loading, commitsLoading, changesLoading, commitsStale, changesStale, error, refresh: refreshReview, refreshRebaseState, refreshComments, checkComments, commentsChecking, commentsSyncing, commentsRefreshError: remoteCommentsError, applyCommentMutation, refreshReviewState, addReview, updatePullRequest, updatePullRequestParticipants } = usePullRequestReview(pullRequestId, canPoll)
  const [searchParams] = useSearchParams()
  const [activeTab, setActiveTab] = useState<ReviewTab>(searchParams.get('tab') === 'changes' ? 'changes' : 'overview')
  const manualReviews = useManualReviews(pullRequestId)
  const [reviewDialogOpen, setReviewDialogOpen] = useState(false)
  const [submissionHead, setSubmissionHead] = useState('')
  const reviewSubmissionAttempt = useRef<{
    input: ManualReviewSubmission
    drafts: PullRequestComment[]
    draftsAttempted: boolean
    draftsAccepted: boolean
    manualReviewAttempted: boolean
  } | undefined>(undefined)
  const [publishingCommentId, setPublishingCommentId] = useState('')
  const [publicationNotice, setPublicationNotice] = useState('')
  const [activeLocation, setActiveLocation] = useState<PullRequestCommentLocation>()
  const [createdComments, setCreatedComments] = useState<PullRequestComment[]>([])
  const unconfirmedComments = createdComments.filter((created) => !comments.some((comment) => comment.id === created.id))
  if (unconfirmedComments.length !== createdComments.length) {
    // Every accepted list retires confirmed fallbacks, including background refreshes.
    setCreatedComments(unconfirmedComments)
  }
  const [commentsRefreshError, setCommentsRefreshError] = useState('')
  const [refreshingComments, setRefreshingComments] = useState(false)
  const [viewCommentTarget, setViewCommentTarget] = useState<{ id: string }>()
  const [viewFilePath, setViewFilePath] = useState<string>()
  const focusedCommentTarget = useRef(viewCommentTarget)
  const commentsRefreshRequest = useRef(0)
  const refreshPostedComments = async () => {
    const request = ++commentsRefreshRequest.current
    setRefreshingComments(true)
    let applied = false
    try {
      applied = await refreshComments()
    } catch { /* A failed or superseded refresh keeps the saved comments below. */ }
    if (request !== commentsRefreshRequest.current) return
    setRefreshingComments(false)
    setCommentsRefreshError(applied ? '' : 'Comment saved. Could not refresh the comments list.')
  }

  const commentDrafts = useCommentDrafts(async (location, body) => {
    const comment = await api.createPullRequestComment(pullRequestId, { ...location, body, draft: true })
    applyCommentMutation(comment)
    setCreatedComments((current) => [...current, comment])
    void refreshPostedComments()
    return comment
  })
  const changeReviewTab = (tab: ReviewTab) => {
    if (tab !== 'overview') setViewCommentTarget(undefined)
    if (tab !== 'changes') setViewFilePath(undefined)
    setActiveTab(tab)
  }
  const viewFileInChanges = (path: string) => {
    setViewCommentTarget(undefined)
    setViewFilePath(path)
    setActiveTab('changes')
  }
  const viewComment = (id: string) => {
    setViewFilePath(undefined)
    setViewCommentTarget({ id })
    setActiveTab('overview')
  }
  useEffect(() => {
    if (activeTab !== 'overview' || !viewCommentTarget || focusedCommentTarget.current === viewCommentTarget) return
    const item = document.getElementById(`comment-${viewCommentTarget.id}`)
    if (item) {
      focusedCommentTarget.current = viewCommentTarget
      item.scrollIntoView?.({ block: 'center' })
      item.focus()
    }
  }, [activeTab, viewCommentTarget, comments, createdComments])
  const commentComposer = (location: PullRequestCommentLocation, scoped = false) => (
    <CommentComposer key={commentLocationKey(location)} location={location} drafts={commentDrafts} onViewComment={viewComment}
      autoFocus={scoped} onDiscard={scoped ? () => setActiveLocation(undefined) : undefined} />
  )
  const scopedComments: ScopedComments | undefined = pullRequest && isActivePullRequestStatus(pullRequest.status) ? {
    activeLocation, select: setActiveLocation, composer: (location) => commentComposer(location, true),
  } : undefined

  const [startingReview, setStartingReview] = useState(false)
  const [reviewError, setReviewError] = useState('')
  const [mergeDialogOpen, setMergeDialogOpen] = useState(false)
  const [closeDialogOpen, setCloseDialogOpen] = useState(false)
  const [transitioningTo, setTransitioningTo] = useState<PullRequestTransitionStatus>()
  const [merging, setMerging] = useState(false)
  const githubSyncGeneration = useRef(0)
  const [mergeError, setMergeError] = useState('')
  const [pageState, dispatchPage] = useReducer(pullRequestPageReducer, initialPullRequestPageState)
  const [rebaseReadinessRefresh, setRebaseReadinessRefresh] = useState(0)
  const refresh = useCallback(async () => {
    try {
      await refreshReview()
    } finally {
      setRebaseReadinessRefresh((value) => value + 1)
    }
  }, [refreshReview])
  const refreshRebase = useCallback(async () => {
    try {
      await refreshRebaseState()
    } finally {
      setRebaseReadinessRefresh((value) => value + 1)
    }
  }, [refreshRebaseState])
  const refreshMetadata = useCallback(async () => {
    updatePullRequest(await api.pullRequest(pullRequestId))
  }, [pullRequestId, updatePullRequest])
  const displayedPullRequest = useRef<PullRequest | undefined>(undefined)
  useEffect(() => {
    displayedPullRequest.current = pullRequest
  }, [pullRequest])
  const [startingWorkers, setStartingWorkers] = useState(false)
  const [addressConfirmation, setAddressConfirmation] = useState<{ message: string, hasFailure: boolean }>()
  const addressNotice = addressConfirmation?.message ?? ''
  useEffect(() => {
    if (!addressConfirmation || addressConfirmation.hasFailure) return
    const timer = window.setTimeout(() => setAddressConfirmation(undefined), 5000)
    return () => window.clearTimeout(timer)
  }, [addressConfirmation])
  const [continueDialogOpen, setContinueDialogOpen] = useState(false)
  const githubSyncRequest = useRef<Promise<void> | undefined>(undefined)
  const githubPageOwner = useRef<object | undefined>(undefined)
  useEffect(() => {
    githubPageOwner.current = {}
    return () => { githubPageOwner.current = undefined }
  }, [pullRequestId])
  const { open: overflowOpen, rootRef: overflowRef, close: closeOverflow, toggle: toggleOverflow } = useDropdownMenu(false, true)

  const linkedSessionId = pullRequest?.linked_session_ids?.[0]
  const github = pullRequest ? githubDetails(pullRequest) : {}
  const refreshGitHubStatus = async () => {
    // Eligibility can change while an older request is still running.
    if (githubSyncRequest.current) return githubSyncRequest.current
    const current = displayedPullRequest.current
    if (document.visibilityState === 'hidden' || !current || !githubStatusNeedsPolling(current) || transitionInFlight.current || mergeInFlight.current || rebaseInFlight.current) return
    const generation = githubSyncGeneration.current
    const owner = githubPageOwner.current
    const request = (async () => {
      try {
        const refreshed = current.sync_provider === 'github'
          ? await api.refreshPullRequestGitHubReadiness(current.id)
          : await api.syncPullRequest(current.id)
        if (owner && owner === githubPageOwner.current && displayedPullRequest.current?.id === current.id && generation === githubSyncGeneration.current && !transitionInFlight.current && !mergeInFlight.current && !rebaseInFlight.current) updatePullRequest(refreshed)
      } catch {
        // Keep accepted state on failure; the next readiness attempt retries.
      }
    })()
    githubSyncRequest.current = request
    try {
      await request
    } finally {
      githubSyncRequest.current = undefined
    }
  }
  const refreshGitHubStatusRef = useRef(refreshGitHubStatus)
  useEffect(() => { refreshGitHubStatusRef.current = refreshGitHubStatus })
  const visibleComments = [...comments, ...unconfirmedComments]
  const draftComments = visibleComments.filter((comment) => comment.publication_state === 'draft')
  const openReviewSubmission = () => {
    setSubmissionHead(reviewSubmissionAttempt.current?.input.head_commit || pullRequest?.head_commit || '')
    setReviewDialogOpen(true)
  }
  const publishComment = async (id: string) => {
    const comment = draftComments.find((item) => item.id === id)
    const current = displayedPullRequest.current
    if (!comment || !current || publishingCommentId) return
    if (current.comparison_state && current.comparison_state !== 'ready') throw new Error('The PR comparison is updating. Try again when it is ready.')
    if (comment.original_head_commit !== current.head_commit) throw new Error('This draft refers to an older revision. Remove it before publishing.')
    setPublishingCommentId(id)
    try {
      const updated = await api.publishPullRequestCommentDrafts(pullRequestId, current.head_commit, [id])
      updated.forEach(applyCommentMutation)
      setCreatedComments((existing) => existing.map((item) => updated.find((next) => next.id === item.id) || item))
    } finally {
      setPublishingCommentId('')
    }
  }
  const submitReview = async (input: ManualReviewSubmission) => {
    if (reviewSubmissionAttempt.current?.input.id !== input.id) {
      if (draftComments.some((comment) => comment.original_head_commit !== input.head_commit)) throw Object.assign(new Error('Some draft comments refer to an older revision. Remove those drafts before publishing.'), { status: 409 })
      if (draftComments.length > 250) throw Object.assign(new Error('Publish at most 250 draft comments at a time.'), { status: 400 })
      if (input.body.length > 4000) throw Object.assign(new Error('The review note exceeds the 4,000-character limit. Shorten it before publishing.'), { status: 400 })
      reviewSubmissionAttempt.current = { input, drafts: draftComments, draftsAttempted: false, draftsAccepted: false, manualReviewAttempted: false }
    }
    const attempt = reviewSubmissionAttempt.current
    const requireFreshHead = () => {
      const current = displayedPullRequest.current
      if (!current || (current.comparison_state && current.comparison_state !== 'ready') || attempt.input.head_commit !== current.head_commit) throw Object.assign(new Error('The PR changed. Review the latest changes before submitting.'), { status: 409 })
    }
    try {
      if (attempt.drafts.length && !attempt.draftsAccepted) {
        if (!attempt.draftsAttempted) requireFreshHead()
        attempt.draftsAttempted = true
        const updated = await api.publishPullRequestCommentDrafts(pullRequestId, attempt.input.head_commit, attempt.drafts.map((comment) => comment.id))
        attempt.draftsAccepted = true
        updated.forEach(applyCommentMutation)
        setCreatedComments((current) => current.map((comment) => updated.find((item) => item.id === comment.id) || comment))
      }
      if (attempt.input.event === 'approve' || attempt.input.body) {
        if (!attempt.manualReviewAttempted) requireFreshHead()
        attempt.manualReviewAttempted = true
        const submitted = await api.submitManualPullRequestReview(pullRequestId, attempt.input)
        manualReviews.accept(submitted)
        setPublicationNotice(submitted.state === 'failed' || submitted.state === 'pending' ? 'Review saved. GitHub publication needs retry.' : attempt.input.event === 'approve' ? 'Approval submitted.' : 'Review submitted.')
        setActiveTab('overview')
      } else setPublicationNotice(pullRequest?.sync_provider === 'github' ? 'Review submitted. Comments are publishing to GitHub.' : 'Review submitted.')
      reviewSubmissionAttempt.current = undefined
    } catch (value) {
      const accepted = attempt.draftsAccepted ? `${attempt.drafts.length} draft ${attempt.drafts.length === 1 ? 'comment' : 'comments'}` : ''
      const status = (value as ApiError)?.status
      const retainSubmission = Boolean(accepted) || !status || status >= 500
      if (!retainSubmission) reviewSubmissionAttempt.current = undefined
      const notice = accepted
        ? `Review partially submitted: ${accepted} accepted. ${attempt.input.event === 'approve' ? 'Approval is still unconfirmed. ' : attempt.input.body ? 'Review summary is still unconfirmed. ' : ''}${errorMessage(value)} Retry submission to continue.`
        : errorMessage(value)
      if (accepted) setPublicationNotice(notice)
      throw Object.assign(new Error(notice), { status, retainSubmission })
    } finally {
      void refreshReviewState().catch(() => undefined)
      void holonStore?.refresh()
    }
  }
  const unresolvedIds = visibleComments.filter((comment) => !comment.parent_comment_id && comment.status === 'unresolved' && comment.publication_state !== 'draft').map((comment) => comment.id)
  const unresolvedCount = unresolvedIds.length
  const activeReview = reviews.some((review) => ['queued', 'running', 'waiting_user', 'cancelling'].includes(review.status))
  const startReview = async (mode: PullRequestReviewMode) => {
    if (!pullRequest || startingReview || activeReview) return
    setReviewError('')
    setStartingReview(true)
    try {
      addReview(await api.createPullRequestReview(pullRequest.id, mode))
      void holonStore?.refresh()
    } catch (value) {
      setReviewError(errorMessage(value))
    } finally {
      setStartingReview(false)
    }
  }
  const mergePullRequest = async () => {
    if (!pullRequest || checkingBranch || mergeInFlight.current || transitionInFlight.current) return
    mergeInFlight.current = true
    githubSyncGeneration.current += 1
    setMerging(true)
    setMergeError('')
    try {
      const result = await api.mergePullRequest(pullRequest.id, pullRequest.merge_request_strategy || 'squash', unresolvedCount > 0)
      updatePullRequest(result.pull_request)
      setMergeDialogOpen(false)
    } catch (value) {
      setMergeError(errorMessage(value))
      return
    } finally {
      mergeInFlight.current = false
      setMerging(false)
    }
    // The confirmed merge is complete; refresh failures must not become merge errors.
    void refresh().catch(() => undefined)
  }
  const transitionPullRequest = async (status: PullRequestTransitionStatus) => {
    if (!pullRequest || transitionInFlight.current || mergeInFlight.current) return
    transitionInFlight.current = true
    githubSyncGeneration.current += 1
    setTransitioningTo(status)
    setReviewError('')
    try {
      updatePullRequest(await api.transitionPullRequest(pullRequest.id, status))
      if (status === 'closed') setCloseDialogOpen(false)
      await refresh()
    } catch (value) {
      setReviewError(errorMessage(value))
      if (status === 'closed') setCloseDialogOpen(false)
      await refreshMetadata().catch(() => undefined)
    } finally {
      transitionInFlight.current = false
      setTransitioningTo(undefined)
    }
  }
  const continueWork = async (prompt: string, title?: string, harnessType?: HarnessType, startupMode?: 'terminal') => {
    if (!pullRequest || pullRequest.comparison_state && pullRequest.comparison_state !== 'ready') throw new Error('The PR comparison is updating. Try again when it is ready.')
    const created = await api.createPullRequestWorkers(pullRequestId, [], 'continue', prompt, {
      title, agent_type: harnessType, startup_mode: startupMode ?? 'agent',
    })
    const worker = created.find((item) => item.mode === 'continue' && item.session_id)
    if (!worker?.session_id) throw new Error('The PR follow-up could not be created.')
    return api.holon(worker.session_id)
  }
  const latestRebase = rebases[rebases.length - 1]
  const rebaseReadiness = readinessAfterRebase(pageState, pullRequest, latestRebase)
  const rebaseActivity = pageState.rebaseActivity
  const displayedRevision = pullRequestRevision(pullRequest?.id, pullRequest?.base_commit, pullRequest?.head_commit)
  const hasConflicts = Boolean(pullRequest && rebaseReadiness.value?.rebase_conflict_state === 'conflicting')
  const comparisonCurrent = !pullRequest?.comparison_state || pullRequest.comparison_state === 'ready'
  const branchState = !comparisonCurrent ? 'unavailable' : pullRequestBranchState(rebaseReadiness.value, rebaseReadiness.phase, hasConflicts)
  const checkingBranch = branchState === 'checking' && latestRebase?.status === 'completed'
  const rebaseTarget = rebaseReadiness.value?.base_commit || ''
  const rebaseTargetCurrent = comparisonCurrent && readinessMatchesRevision(pageState, displayedRevision)
  const rebasing = Boolean(rebaseActivity)
  const latestReview = reviews[reviews.length - 1]
  const reviewCurrent = latestReview?.status === 'completed' && (latestReview.freshness
    ? !latestReview.freshness.outdated
    : latestReview.head_commit === pullRequest?.head_commit)
  const activeRebase = [...queue, ...rebases].find((item) => item.kind === 'rebase' && ['queued', 'running', 'waiting_user', 'cancelling'].includes(item.status))
  const settledRebase = latestRebase && !['queued', 'running', 'waiting_user', 'cancelling'].includes(latestRebase.status) ? latestRebase : undefined
  const finalizingRebase = rebases.find((item) => {
    const holon = item.session_id ? holonStore?.getHolon(item.session_id) : undefined
    return holon && holonDisplayStatus(holon) === 'finalizing'
  })
  const settledRebaseKey = settledRebase ? `${settledRebase.pull_request_id}:${settledRebase.id}:${settledRebase.status}` : ''
  const refreshedRebase = useRef('')
  useEffect(() => {
    if (!settledRebaseKey || refreshedRebase.current === settledRebaseKey) return
    refreshedRebase.current = settledRebaseKey
    void refreshRebase().catch(() => undefined)
  }, [settledRebaseKey, refreshRebase])
  const nextAction = recommendPullRequestAction({ ...pageState, readiness: rebaseReadiness }, {
    activeRebase: Boolean(activeRebase),
    latestReviewCompleted: Boolean(latestReview?.status === 'completed'),
    reviewCurrent,
    unresolvedCount,
  })
  const rebasePullRequest = async (kind: 'mechanical' | 'resolve') => {
    if (!pullRequest || rebaseInFlight.current || mergeInFlight.current || transitionInFlight.current || activeRebase || !rebaseTarget || !rebaseTargetCurrent) return
    const target = rebaseTarget
    let analyzing = false
    rebaseInFlight.current = true
    githubSyncGeneration.current += 1
    setReviewError('')
    dispatchPage({ type: 'rebase-started', activity: { kind: kind === 'mechanical' ? 'rebasing' : 'resolving', target } })
    try {
      const result = kind === 'mechanical'
        ? await api.attemptPullRequestRebase(pullRequest.id, target)
        : await api.resolvePullRequestRebase(pullRequest.id, target)
      if (!['queued', 'running', 'waiting_user', 'cancelling'].includes(result.status)) {
        refreshedRebase.current = `${result.pull_request_id}:${result.id}:${result.status}`
      }
      addRebase(result)
      if (result.status === 'failed' || result.status === 'stale') {
        analyzing = true
        dispatchPage({ type: 'rebase-started', activity: { kind: 'analyzing' } })
        dispatchPage({ type: 'readiness-started', revision: displayedRevision })
        await refreshRebase()
      } else if (result.session_id) {
        await Promise.all([refreshRebase(), holonStore?.refresh()])
      } else if (result.status === 'completed' && result.result_head_commit) {
        await refreshRebase()

      } else {
        analyzing = true
        dispatchPage({ type: 'rebase-started', activity: { kind: 'analyzing' } })
        dispatchPage({ type: 'readiness-started', revision: displayedRevision })
        await refreshRebase()
      }
    } catch (value) {
      if (['rebase_conflicts', 'rebase_target_changed', 'stale_head'].includes((value as ApiError)?.code || '')) {
        analyzing = true
        dispatchPage({ type: 'rebase-started', activity: { kind: 'analyzing' } })
        dispatchPage({ type: 'readiness-started', revision: displayedRevision })
        await refreshRebase()
      } else {
        setReviewError(errorMessage(value))
      }
    } finally {
      if (!analyzing) {
        rebaseInFlight.current = false
        dispatchPage({ type: 'rebase-finished' })
      }
    }
  }

  const githubPollingEligible = Boolean(pullRequest && githubStatusNeedsPolling(pullRequest))
  useEffect(() => {
    if (!githubPollingEligible) return undefined
    let cancelled = false
    let timer: number | undefined
    let running = false
    const isVisible = () => document.visibilityState !== 'hidden'
    const poll = async () => {
      if (cancelled || running || document.visibilityState === 'hidden') return
      running = true
      window.clearTimeout(timer)
      try { await refreshGitHubStatusRef.current() } finally {
        running = false
        if (!cancelled && isVisible()) timer = window.setTimeout(() => { void poll() }, 3000)
      }
    }
    const visibilityChanged = () => {
      window.clearTimeout(timer)
      if (document.visibilityState !== 'hidden') void poll()
    }
    timer = window.setTimeout(() => { void poll() }, 3000)
    document.addEventListener('visibilitychange', visibilityChanged)
    return () => {
      cancelled = true
      window.clearTimeout(timer)
      document.removeEventListener('visibilitychange', visibilityChanged)
    }
  }, [pullRequestId, githubPollingEligible])

  const displayedPullRequestID = pullRequest?.id
  const displayedBaseCommit = pullRequest?.base_commit
  const displayedHeadCommit = pullRequest?.head_commit
  const pullRequestActive = Boolean(pullRequest && isActivePullRequestStatus(pullRequest.status))
  useEffect(() => {
    if (!displayedPullRequestID || !pullRequestActive) {
      dispatchPage({ type: 'readiness-reset' })
      return undefined
    }
    if (merging) return undefined
    let cancelled = false
    const expected = { id: displayedPullRequestID, base: displayedBaseCommit, head: displayedHeadCommit }
    const revision = pullRequestRevision(expected.id, expected.base, expected.head)
    const stillDisplayed = () => {
      const current = displayedPullRequest.current
      return !cancelled && !mergeInFlight.current && current?.id === expected.id && current.base_commit === expected.base && current.head_commit === expected.head
    }
    const accept = (value: PullRequestRebaseReadiness) => {
      if (!stillDisplayed() || displayedPullRequest.current?.comparison_state && displayedPullRequest.current.comparison_state !== 'ready' || value.head_commit !== expected.head) return false
      dispatchPage({ type: 'readiness-succeeded', revision, value })
      return true
    }
    const check = async (retryStale: boolean) => {
      try {
        if (accept(await api.pullRequestRebaseReadiness(expected.id))) return
        if (!stillDisplayed()) return
        throw Object.assign(new Error('The pull request head changed.'), { code: 'stale_head' })
      } catch (value) {
        if (!stillDisplayed()) return
        if (retryStale && (value as ApiError)?.code === 'stale_head') {
          try {
            const refreshed = await api.pullRequest(expected.id)
            if (!stillDisplayed()) return
            updatePullRequest(refreshed)
            if (refreshed.head_commit === expected.head && isActivePullRequestStatus(refreshed.status)) await check(false)
            return
          } catch {
            // The status below remains deliberately independent of merge readiness.
          }
        }
        if (stillDisplayed()) dispatchPage({ type: 'readiness-failed', revision })
      }
    }
    dispatchPage({ type: 'readiness-started', revision })
    void check(true)
    return () => { cancelled = true }
  }, [displayedBaseCommit, displayedHeadCommit, displayedPullRequestID, pullRequestActive, rebaseReadinessRefresh, updatePullRequest, merging])

  useEffect(() => {
    if (rebaseActivity?.kind === 'analyzing' && (rebaseReadiness.phase === 'ready' || rebaseReadiness.phase === 'failed')) {
      rebaseInFlight.current = false
      dispatchPage({ type: 'rebase-finished' })
    }
  }, [rebaseActivity?.kind, rebaseReadiness.phase])

  const rebaseAction = rebaseActivity?.kind === 'analyzing' ? (
    <p role="status">Analyzing the latest {pullRequest?.base_branch} commit…</p>
  ) : rebaseActivity?.kind === 'rebasing' ? (
    <Button className={`${detail.wideButton} ${detail.mechanicalRebase}`} size="small" icon={<GitBranch />} disabled title={`Rebasing onto ${pullRequest?.base_branch} at ${shortCommit(rebaseActivity.target || '')}`}>
      {`Rebasing onto ${pullRequest?.base_branch}…`}
    </Button>
  ) : rebaseActivity?.kind === 'resolving' ? (
    <div className={`${detail.resolutionAction} ${detail.rebaseAction} ${nextAction === 'rebase' ? detail.primaryResolution : ''}`}>
      <Button size="small" icon={<Bot />} disabled>Resolving…</Button>
    </div>
  ) : activeRebase ? (
    activeRebase.session_id
      ? <HolonTile holonId={activeRebase.session_id} label="Rebase holon" />
      : <p role="status">{activeRebase.status === 'cancelling' ? 'Rebase cancelling…' : 'Rebase queued…'}</p>
  ) : pullRequest && isActivePullRequestStatus(pullRequest.status) && (branchState === 'behind' || hasConflicts) && (
    hasConflicts ? (
      <div className={`${detail.resolutionAction} ${detail.rebaseAction} ${nextAction === 'rebase' ? detail.primaryResolution : ''}`}>
        <Button size="small" icon={<Bot />} disabled={rebasing || !rebaseTargetCurrent} onClick={() => void rebasePullRequest('resolve')}>
          Rebase and resolve conflicts
        </Button>
      </div>
    ) : (
      <Button className={`${detail.wideButton} ${detail.mechanicalRebase}`} size="small" icon={<GitBranch />} disabled={rebasing || !rebaseTargetCurrent} title={`Rebase onto ${pullRequest.base_branch} at ${shortCommit(rebaseTarget)}`} onClick={() => void rebasePullRequest('mechanical')}>
        Rebase
      </Button>
    )
  )
  const createReply = async (parentCommentId: string, body: string) => {
    const comment = await api.createPullRequestComment(pullRequestId, { body, parent_comment_id: parentCommentId, draft: true })
    applyCommentMutation(comment)
    setCreatedComments((current) => [...current, comment])
    void refreshPostedComments()
  }

  const resolveComment = async (id: string) => {
    const updated = await api.resolvePullRequestComment(id)
    applyCommentMutation(updated)
    setCreatedComments((current) => current.map((comment) => comment.id === id ? updated : comment))
  }
  const reopenComment = async (id: string) => {
    const updated = await api.reopenPullRequestComment(id)
    applyCommentMutation(updated)
    setCreatedComments((current) => current.map((comment) => comment.id === id ? updated : comment))
  }
  const editComment = async (id: string, body: string) => {
    const updated = await api.updatePullRequestComment(id, body)
    applyCommentMutation(updated)
    setCreatedComments((current) => current.map((comment) => comment.id === id ? updated : comment))
    await refreshComments()
  }
  const deleteComment = async (id: string) => {
    await api.deletePullRequestComment(id)
    applyCommentMutation(id)
    setCreatedComments((current) => current.filter((comment) => comment.id !== id))
    await refreshComments()
  }
  const addressComments = async (ids: string[], mode: PullRequestWorkerMode) => {
    if (!pullRequest || !comparisonCurrent || startingWorkers || ids.length === 0) return
    setReviewError('')
    setAddressConfirmation(undefined)
    setStartingWorkers(true)
    try {
      const created = await api.createPullRequestWorkers(pullRequest.id, ids, mode)
      setAddressConfirmation({
        message: addressRequestNotice(created),
        hasFailure: created.some((item) => item.status === 'failed'),
      })
      await Promise.all([refreshReviewState(), holonStore?.refresh()])
    } catch (value) {
      setReviewError(errorMessage(value))
    } finally {
      setStartingWorkers(false)
    }
  }

  const lifecycle = pullRequest && lifecycleCommands(pullRequest.status, pullRequest.mergeable !== false && !hasConflicts, pullRequest.merge_blocked_reason)

  const primary = lifecycle?.primary
  const publicationBlocked = (pullRequest?.status === 'wip' || pullRequest?.status === 'draft') && pullRequest.publication_blocked
  const showMergeAction = pullRequest?.status === 'open' || Boolean(publicationBlocked)
  const lifecycleOperation = pullRequest?.operations?.find((operation) => ['pending', 'running', 'uncertain'].includes(operation.status) && ['transition', 'merge', 'create'].includes(operation.kind))
  const lifecycleDisabled = Boolean(primary?.merge && (!comparisonCurrent || checkingBranch)) || Boolean(lifecycleOperation) || Boolean(transitioningTo) || merging || (!primary?.transition && !primary?.merge) || Boolean(publicationBlocked)
  const mergeBlockedReason = pullRequest?.merge_blocked_reason || ''
  const checkingGitHubMergeability = pullRequest?.status === 'open' && pullRequest.sync_provider === 'github'
    && ['github_status_missing', 'github_status_stale', 'github_mergeability_unknown'].includes(mergeBlockedReason)
  const mergeStatus = !publicationBlocked && lifecycle?.tooltip && !hasConflicts && !['not_up_to_date', 'not_fast_forwardable'].includes(mergeBlockedReason)
    ? checkingGitHubMergeability ? '' : lifecycle.tooltip
    : ''
  const lifecycleAction = pullRequest?.status !== 'merged' && primary && (
    <div className={detail.mergeSection}>
      <Button
        className={showMergeAction ? detail.mergeButton : `${detail.wideButton} ${nextAction === 'lifecycle' ? detail.primaryAction : detail.mutedAction}`}
        variant="primary"
        icon={showMergeAction ? <GitMerge /> : <GitPullRequest />}
        disabled={lifecycleDisabled}
        onClick={() => primary.merge ? setMergeDialogOpen(true) : primary.transition && void transitionPullRequest(primary.transition)}
      >
        {merging ? 'Merging...' : transitioningTo || lifecycleOperation ? 'Updating...' : checkingGitHubMergeability ? 'Checking GitHub mergeability…' : showMergeAction ? 'Merge pull request' : primary.label}
      </Button>
      {mergeStatus && <p className={detail.warning}>{mergeStatus}</p>}
    </div>
  )

  return (
    <main className={`${styles.detailPage} ${detail.page}`} data-page-surface>
      <header aria-label="Pull request header" className={detail.header}>
        <div className={detail.topline}>
          <div className={detail.identity}>
            {pullRequest && <span className={`${detail.badge} ${detail[pullRequest.status]}`}><GitPullRequest />{pullRequest.status === 'wip' ? 'WIP' : pullRequest.status[0].toUpperCase() + pullRequest.status.slice(1)}</span>}
            {github.number && <span>#{github.number}</span>}
          </div>
          <div className={detail.utilities}>
            {pullRequest && isActivePullRequestStatus(pullRequest.status) && <Button size="small" icon={<MessageSquare />} onClick={openReviewSubmission}>Review{draftComments.length > 0 && <span className={detail.count}>{draftComments.length}</span>}</Button>}
            {github.url && <a href={github.url} rel="noreferrer" target="_blank"><ExternalLink /> GitHub</a>}
            <div className={detail.overflow} ref={overflowRef}>
              <Button variant="ghost" aria-label="Pull request actions" aria-haspopup="menu" aria-expanded={overflowOpen} icon={<MoreHorizontal />} onClick={() => toggleOverflow(true)} />
              {overflowOpen && <MenuPopover className={detail.overflowMenu}>
                {pullRequest && isActivePullRequestStatus(pullRequest.status) && <MenuItem disabled={!comparisonCurrent} onClick={() => { closeOverflow(); setContinueDialogOpen(true) }}><GitPullRequest />New holon from this PR</MenuItem>}
                <MenuItem onClick={() => { closeOverflow(); void refresh() }}><RefreshCw />Refresh</MenuItem>
                <MenuItem disabled={commentsChecking} onClick={() => { closeOverflow(); void checkComments(true) }}><RefreshCw />{commentsSyncing ? 'Syncing comments...' : 'Refresh comments'}</MenuItem>
                {lifecycle?.alternatives.map((command) => <MenuItem key={command.label} destructive={command.close} disabled={Boolean(lifecycleOperation) || Boolean(transitioningTo) || merging || Boolean(publicationBlocked && command.transition)} onClick={() => {
                  closeOverflow()
                  if (command.close) setCloseDialogOpen(true)
                  else if (command.transition) void transitionPullRequest(command.transition)
                }}>{command.close ? <X /> : <GitPullRequest />}{command.close ? 'Close pull request' : command.transition === 'draft' ? 'Convert to draft' : command.label}</MenuItem>)}
              </MenuPopover>}
            </div>
            <ProfileLink />
          </div>
        </div>
        <h1>{pullRequest?.title || 'Pull request'}</h1>
        {pullRequest && <div className={detail.metadata}>
          <span>Opened {formatDateTime(pullRequest.created_at)}</span><span aria-hidden="true">·</span>
          <GitBranch /><CopyableBranch key={`source:${pullRequest.head_branch}`} branch={pullRequest.head_branch} label="source" /><ArrowRight /><CopyableBranch key={`target:${pullRequest.base_branch}`} branch={pullRequest.base_branch} label="target" />
        </div>}
      </header>
      <ReviewTabs activeTab={activeTab} onChange={changeReviewTab} unresolvedCount={unresolvedCount} commitCount={commits.length} inspection={changes} />
      {draftComments.length > 0 && <div className={detail.reviewDraftBar} role="status"><span><Pencil />{draftComments.length} draft {draftComments.length === 1 ? 'comment' : 'comments'} <span className={detail.muted}>· Only visible to you</span></span>{pullRequest && isActivePullRequestStatus(pullRequest.status) && <Button size="small" onClick={openReviewSubmission}>Publish review</Button>}</div>}
      {publicationNotice && <p role="status" className={detail.reviewNotice}>{publicationNotice}</p>}
      {commentsRefreshError && <p role="alert" className={styles.formError}>{commentsRefreshError} <button className={detail.commentTextAction} type="button" disabled={refreshingComments} onClick={() => void refreshPostedComments()}>Retry refresh</button></p>}
      {activeTab === 'overview' && (
        <PullRequestOverview
          pullRequest={pullRequest}
          inspection={changes}
          loading={loading && !pullRequest}
          error={error}
          linkedSessionId={linkedSessionId}
          comments={visibleComments}
          commentsSyncing={commentsSyncing}
          commentsRefreshError={remoteCommentsError}
          reviews={reviews}
          queue={queue}
          addressNotice={addressNotice}
          onCancelWork={async (id) => {
            await api.cancelPullRequestWork(pullRequestId, id)
            await Promise.all([refreshReviewState(), holonStore?.refresh()])
          }}
          workers={workers}
          reviewError={reviewError}
          readOnly={!pullRequest || !isActivePullRequestStatus(pullRequest.status)}
          startingWorkers={startingWorkers || !comparisonCurrent}
          commentComposer={commentComposer({})}
          manualReviewActivity={<ManualReviewActivity reviews={manualReviews.reviews} historyError={manualReviews.error} historyLoading={manualReviews.loading} onReload={manualReviews.reload} onRetry={async (id) => manualReviews.accept(await api.retryManualPullRequestReview(pullRequestId, id))} />}
          onReplyComment={createReply}
          onResolveComment={resolveComment}
          onReopenComment={reopenComment}
          onEditComment={editComment}
          onDeleteComment={deleteComment} onPublishComment={publishComment}
          onAddressComments={addressComments}
          onViewFile={viewFileInChanges}
          revealCommentTarget={viewCommentTarget}
          onMetadataSaved={refreshMetadata}
          onParticipantSnapshot={updatePullRequestParticipants}
          reviewAction={activeReview ? null : <PrepareReviewMenu primary={nextAction === 'review'} pending={startingReview} disabled={!pullRequest || !isActivePullRequestStatus(pullRequest.status)} onSelect={(mode) => void startReview(mode)} />}
          resolutionAction={<ResolutionAction label="Resolve all automatically" primary={nextAction === 'resolve'} disabled={!comparisonCurrent || startingWorkers || unresolvedCount === 0 || !pullRequest || !isActivePullRequestStatus(pullRequest.status)} onResolve={(mode) => void addressComments(unresolvedIds, mode)} />}
          hasConflicts={hasConflicts}
          branchState={branchState}
          baseCommitsAhead={rebaseReadiness.value?.base_commits_ahead}
          latestRebase={settledRebase?.id === finalizingRebase?.id ? undefined : settledRebase}
          rebaseActive={rebasing || Boolean(activeRebase)}
          rebaseAction={<>
            {rebaseAction}
            {finalizingRebase?.session_id && finalizingRebase.id !== activeRebase?.id && <HolonTile holonId={finalizingRebase.session_id} label="Rebase holon" />}
            {!activeRebase && settledRebase?.status !== 'completed' && settledRebase?.session_id && <div role="status">
              <p>{settledRebase.status === 'stale' ? 'Rebase outdated' : `Rebase ${settledRebase.status}`} · <Link to={`/holons/${settledRebase.session_id}`}>View Holon</Link></p>
            </div>}
          </>}
          lifecycleAction={lifecycleAction}
        />
      )}
      {activeTab === 'commits' && (
        <div aria-label="Pull request content">
          {(commitsStale || commitsLoading && commits.length > 0) && <p role="status">Updating commits…</p>}
          <CommitsPanel pullRequest={pullRequest} commits={commits} inspection={changes} loading={commitsLoading && commits.length === 0} error={commits.length ? '' : error} />
        </div>
      )}
      {activeTab === 'changes' && (
        <div aria-label="Pull request content">
          {addressNotice && <p className={styles.addressNotice} role="status">{addressNotice}</p>}
          {reviewError && <p className={styles.formError} role="alert">{reviewError}</p>}
          {(changesStale || changesLoading && changes) && <p role="status">Updating changes…</p>}
          {changesLoading && !changes ? <p className={styles.message}>Loading changes...</p> : error && !changes ? <p className={styles.message}>{error}</p> : changes ? <PullRequestChanges key={`${changes.base_commit}:${changes.head_commit}`} pullRequestId={pullRequestId} inspection={changes} scopedComments={scopedComments} initialFilePath={viewFilePath}
            discussions={{ comments: visibleComments, render: (roots, showSavedContext = false) => roots.length > 0 && <CommentsPanel
              embedded showSavedContext={showSavedContext} comments={[...roots, ...visibleComments.filter((comment) => roots.some((root) => root.id === comment.parent_comment_id))]}
              queue={queue} workers={workers} addressNotice="" readOnly={!pullRequest || !isActivePullRequestStatus(pullRequest.status)}
              commentComposer={null} startingWorkers={startingWorkers || !comparisonCurrent} onReplyComment={createReply} onResolveComment={resolveComment}
              onReopenComment={reopenComment} onEditComment={editComment} onDeleteComment={deleteComment} onPublishComment={publishComment} onAddressComments={addressComments}
              onViewFile={viewFileInChanges} availableFilePaths={[]}
            /> }} /> : <p className={styles.message}>Changes unavailable.</p>}
        </div>
      )}
      {pullRequest && <HolonComposer contextLabel={github.number ? `PR #${github.number}` : 'Pull request'} contextPrompt={`Pull request: ${pullRequest.id} — ${pullRequest.title}\nBranch: ${pullRequest.head_branch} → ${pullRequest.base_branch}`} branch={pullRequest.head_branch} icon={<GitPullRequest />} onSubmit={continueWork} />}
      {pullRequest && <ReviewSubmission open={reviewDialogOpen} pullRequest={pullRequest} head={submissionHead} draftCount={draftComments.length} onClose={() => setReviewDialogOpen(false)} onViewDrafts={() => { setReviewDialogOpen(false); setActiveTab('overview') }} onSubmit={submitReview} />}
      {pullRequest && <NewAgentDialog
        open={continueDialogOpen}
        project={project}
        initialBranch={pullRequest.head_branch}
        draftScope={`${project.id}:pr:${pullRequest.id}`}
        title="New Holon from this PR"
        submitLabel="Create Holon"
        nameHint="Set a custom name. Otherwise, the name will be Follow Up N."
        allowTerminal
        harnessWorkflow="pull_request_feedback"
        onSubmit={continueWork}
        onClose={() => setContinueDialogOpen(false)}
      />}
      {pullRequest && (
        <MergePullRequestDialog
          open={mergeDialogOpen}
          pullRequest={pullRequest}
          unresolvedCount={unresolvedCount}
          merging={merging}
          checkingBranch={checkingBranch}
          error={mergeError}
          onClose={() => {
            if (!merging) setMergeDialogOpen(false)
          }}
          onMerge={() => void mergePullRequest()}
        />
      )}
      <Dialog
        open={closeDialogOpen}
        title="Close pull request"
        onClose={() => {
          if (!transitioningTo) setCloseDialogOpen(false)
        }}
        footer={(
          <>
            <Button disabled={Boolean(transitioningTo)} onClick={() => setCloseDialogOpen(false)}>Cancel</Button>
            <Button className={styles.dangerButton} disabled={Boolean(transitioningTo)} onClick={() => void transitionPullRequest('closed')}>Close</Button>
          </>
        )}
      >
        <p className={styles.closeWarning}>Holark cannot currently reopen this pull request.</p>
      </Dialog>
    </main>
  )
}

function CopyableBranch({ branch, label }: { branch: string, label: string }) {
  const [status, setStatus] = useState<'idle' | 'copied' | 'failed'>('idle')

  useEffect(() => {
    if (status !== 'copied') return
    const timer = window.setTimeout(() => setStatus('idle'), 2000)
    return () => window.clearTimeout(timer)
  }, [status])

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(branch)
      setStatus('copied')
    } catch {
      setStatus('failed')
    }
  }

  return (
    <span className={detail.copyableBranch}>
      <code>{branch}</code>
      <button
        type="button"
        className={detail.copyBranchButton}
        aria-label={`Copy ${label} branch name`}
        title={status === 'copied' ? 'Copied!' : `Copy ${label} branch name`}
        onClick={() => void copy()}
      >
        {status === 'copied' ? <Check /> : <Copy />}
      </button>
      <span role="status" className={status === 'failed' ? detail.copyError : detail.copyStatus}>
        {status === 'copied' ? `Copied ${label} branch name` : status === 'failed' ? 'Unable to copy. Try again.' : ''}
      </span>
    </span>
  )
}

function ComingSoonSplitButton({ accessibleLabel, icon, label, previewTitle, title }: {
  accessibleLabel?: string
  icon: ReactNode
  label: string
  previewTitle: string
  title: string
}) {
  const previewId = useId()
  const returnFocusRef = useRef<HTMLButtonElement>(null)
  const { open, rootRef, toggle } = useDismissiblePopover(previewId, returnFocusRef)
  const togglePreview = (event: MouseEvent<HTMLButtonElement>) => {
    returnFocusRef.current = event.currentTarget
    toggle()
  }

  return (
    <div ref={rootRef} className={detail.comingSoonSplit} title={title}>
      <Button
        aria-label={accessibleLabel ?? `${label} (coming soon)`}
        aria-expanded={open}
        aria-controls={previewId}
        className={detail.comingSoonSplitMain}
        icon={icon}
        onClick={togglePreview}
      >
        <span className={detail.comingSoonSplitLabel}>{label}</span>
        <span aria-hidden="true" className={detail.comingSoonBadge}>Soon</span>
      </Button>
      {open && (
        <div id={previewId} className={detail.comingSoonPreview} role="region" aria-label={previewTitle} title="">
          <span className={detail.comingSoonPreviewStatus}>Coming soon</span>
          <h3>{previewTitle}</h3>
        </div>
      )}
    </div>
  )
}

type ReviewTab = 'overview' | 'commits' | 'changes'

function ReviewTabs({ activeTab, onChange, unresolvedCount, commitCount, inspection }: {
  activeTab: ReviewTab
  onChange: (tab: ReviewTab) => void
  unresolvedCount: number
  commitCount?: number
  inspection?: WorkspaceInspection
}) {
  const stripRef = useRef<HTMLElement>(null)
  const additions = inspection?.files.reduce((total, file) => total + file.additions, 0) ?? 0
  const deletions = inspection?.files.reduce((total, file) => total + file.deletions, 0) ?? 0
  const tabs: Array<{ id: ReviewTab, label: string }> = [
    { id: 'overview', label: 'Overview' },
    { id: 'commits', label: 'Commits' },
    { id: 'changes', label: 'Changes' },
  ]

  return (
    <nav ref={stripRef} aria-label="Pull request sections" className={styles.tabs}>
      <TabSelectionIndicator stripRef={stripRef} selectedSelector="button[aria-current='page']" variant="underline" />
      {tabs.map((tab) => (
        <button
          aria-current={activeTab === tab.id ? 'page' : undefined}
          className={activeTab === tab.id ? styles.activeTab : undefined}
          key={tab.id}
          onClick={() => onChange(tab.id)}
          type="button"
        >
          {tab.label}
          {tab.id === 'overview' && <span className={detail.count} title="Unresolved comments">{unresolvedCount}</span>}
          {tab.id === 'commits' && commitCount !== undefined && <span className={detail.count}>{commitCount}</span>}
          {tab.id === 'changes' && inspection && <>
            <span className={detail.count}>{inspection.files.length}</span>
            <span className={detail.tabStats}>
              <span className={styles.additionText} aria-label={`${additions} additions`}>+{additions}</span>
              <span className={styles.deletionText} aria-label={`${deletions} deletions`}>−{deletions}</span>
            </span>
          </>}
        </button>
      ))}
    </nav>
  )
}

function PullRequestOverview({
  pullRequest,
  inspection,
  loading,
  error,
  linkedSessionId,
  comments,
  commentsSyncing,
  commentsRefreshError,
  reviews,
  queue,
  addressNotice,
  onCancelWork,
  workers,
  reviewError,
  readOnly,
  startingWorkers,
  commentComposer,
  manualReviewActivity,
  onReplyComment,
  onResolveComment,
  onReopenComment,
  onEditComment,
  onDeleteComment,
  onPublishComment,
  onAddressComments,
  onViewFile,
  revealCommentTarget,
  onMetadataSaved,
  onParticipantSnapshot,
  reviewAction,
  resolutionAction,
  rebaseAction,
  hasConflicts,
  branchState,
  baseCommitsAhead,
  latestRebase,
  rebaseActive,
  lifecycleAction,
}: {
  pullRequest?: PullRequest
  inspection?: WorkspaceInspection
  loading: boolean
  error: string
  linkedSessionId?: string
  comments: PullRequestComment[]
  commentsSyncing: boolean
  commentsRefreshError?: string
  reviews: PullRequestReview[]
  queue: PullRequestQueueItem[]
  addressNotice: string
  onCancelWork: (id: string) => Promise<void>
  workers: PullRequestWorker[]
  reviewError: string
  readOnly: boolean
  startingWorkers: boolean
  commentComposer: ReactNode
  manualReviewActivity: ReactNode
  onReplyComment: (parentCommentId: string, body: string) => Promise<void>
  onResolveComment: (id: string) => Promise<void>
  onReopenComment: (id: string) => Promise<void>
  onEditComment: (id: string, body: string) => Promise<void>
  onDeleteComment: (id: string) => Promise<void>
  onPublishComment: (id: string) => Promise<void>
  onAddressComments: (ids: string[], mode: PullRequestWorkerMode) => Promise<void>
  onViewFile: (path: string) => void
  revealCommentTarget?: { id: string }
  onMetadataSaved: () => Promise<void>
  onParticipantSnapshot: (snapshot: PullRequestParticipantSnapshot) => void
  reviewAction: ReactNode
  resolutionAction: ReactNode
  rebaseAction: ReactNode
  hasConflicts: boolean
  branchState: string
  baseCommitsAhead?: number
  latestRebase?: PullRequestRebase
  rebaseActive: boolean
  lifecycleAction: ReactNode
}) {
  if (loading) return <p className={styles.message}>Loading pull request...</p>
  if (error && !pullRequest) return <p className={styles.message}>{error}</p>
  if (!pullRequest) return <p className={styles.message}>Pull request unavailable.</p>

  const unresolvedCount = comments.filter((comment) => !comment.parent_comment_id && comment.status === 'unresolved' && comment.publication_state !== 'draft').length
  const draftCount = comments.filter((comment) => comment.publication_state === 'draft').length
  return (
    <div className={styles.overviewGrid}>
      <section className={styles.overviewMain}>
        <PullRequestMetadataCard
          key={pullRequest.id}
          pullRequest={pullRequest}
          onSaved={onMetadataSaved}
        />
        <div aria-label="Pull request content" className={`${styles.overviewContent} ${detail.conversations}`}>
          {manualReviewActivity}
          {error && !inspection && <p className={styles.message}>{error}</p>}
          <div className={styles.overviewBlock}>
            {reviewError && <p className={styles.formError}>{reviewError}</p>}
            <CommentsPanel
              comments={comments}
              commentsSyncing={commentsSyncing}
              refreshError={commentsRefreshError}
              queue={queue}
              workers={workers}
              addressNotice={addressNotice}
              readOnly={readOnly}
              commentComposer={commentComposer}
              onReplyComment={onReplyComment}
              onResolveComment={onResolveComment}
              onReopenComment={onReopenComment}
              onEditComment={onEditComment}
              onDeleteComment={onDeleteComment}
              onPublishComment={onPublishComment}
              onAddressComments={onAddressComments}
              onViewFile={onViewFile}
              availableFilePaths={inspection?.files.map((file) => file.path) ?? []}
              revealCommentTarget={revealCommentTarget}
              startingWorkers={startingWorkers}
            />
          </div>
        </div>
      </section>
      <aside aria-label="Pull request sidebar" className={styles.sidebar}>
        {pullRequest.status !== 'merged' && <div className={detail.actionsBox}>
          <h2 className={detail.actionsTitle}><GitPullRequest /> Review &amp; merge</h2>
          <ReviewStatusPanel reviews={reviews} action={reviewAction}>
            <div className={detail.resolutionSummary}>
              {unresolvedCount === 0 ? draftCount > 0 ? <span>{draftCount} draft {draftCount === 1 ? 'comment' : 'comments'} to review</span> : comments.length === 0 ? null : <><CheckCircle2 className={detail.good} /><span>All comments resolved</span></> : <span>{unresolvedCount} unresolved {unresolvedCount === 1 ? 'conversation' : 'conversations'}</span>}
            </div>
            {unresolvedCount > 0 && resolutionAction}
          </ReviewStatusPanel>
          <GitHubReadinessPanel pullRequest={pullRequest} hasConflicts={hasConflicts} rebaseAction={rebaseAction} rebaseActive={rebaseActive} branchState={branchState} baseCommitsAhead={baseCommitsAhead} latestRebase={latestRebase} />
          {lifecycleAction}
        </div>}
        <PullRequestParticipants pullRequest={pullRequest} onSnapshot={onParticipantSnapshot} />
        {(queue.length > 0 || workers.some((worker) => worker.mode === 'continue')) && <WorkerStatusPanel queue={queue} workers={workers} comments={comments} onCancelWork={onCancelWork} />}
        {linkedSessionId && (
          <section>
            <h2>Source holon</h2>
            <HolonTile holonId={linkedSessionId} label="Source holon" />
          </section>
        )}
      </aside>
    </div>
  )
}


function ReviewStatusPanel({ reviews, action, children }: { reviews: PullRequestReview[], action: ReactNode, children: ReactNode }) {
  const latest = reviews[reviews.length - 1]
  const store = useOptionalHolonStore()
  const holon = latest?.session_id ? store?.getHolon(latest.session_id) : undefined
  const finalizing = holon && holonDisplayStatus(holon) === 'finalizing'
  const reviewActive = latest && ['queued', 'running', 'waiting_user', 'cancelling'].includes(latest.status)
  const completedAt = latest?.completed_at || latest?.created_at
  const reviewCompleted = latest?.status === 'completed' && !latest.freshness?.outdated && !finalizing
  return (
    <section>
      <div className={detail.actionLine}>
        <strong>Agent review</strong>
        {latest && latest.status !== 'completed' && !reviewActive && !finalizing && <span>Failed</span>}
      </div>
      {latest && (
        <>
          {reviewCompleted && completedAt && (
            <Link className={detail.reviewCompletion} to={`/holons/${latest.session_id}`}>
              <CheckCircle2 />
              <span>Reviewed <time dateTime={completedAt}>{formatTimeOfDay(completedAt)}</time></span>
            </Link>
          )}
          {latest.freshness?.outdated && <p className={styles.metadataFreshness}>Review outdated</p>}
          {latest.error && <p>{latest.error}</p>}
          {(reviewActive || finalizing) && <HolonTile holonId={latest.session_id} label="Review holon" />}
        </>
      )}
      {action}
      <DismissiblePopoverGroup>
        <ComingSoonSplitButton
          accessibleLabel="Auto-split PR (coming soon)"
          icon={<GitPullRequest />}
          label="Auto-split for review"
          previewTitle="Review one flow at a time"
          title="Preview automatic review splitting — coming soon"
        />
        <ComingSoonSplitButton
          icon={<MessageSquare />}
          label="Ask the author Holon"
          previewTitle="Talk to the Holon behind this PR"
          title="Chat with the Holon that developed the linked issue, with its original context — coming soon"
        />
      </DismissiblePopoverGroup>
      {children}
    </section>
  )
}

function WorkerStatusPanel({ queue, workers, comments, onCancelWork }: { queue: PullRequestQueueItem[], workers: PullRequestWorker[], comments: PullRequestComment[], onCancelWork: (id: string) => Promise<void> }) {
  const [cancelling, setCancelling] = useState<string>()
  const [error, setError] = useState('')
  const continueWorkers = workers.filter((worker) => worker.mode === 'continue')
  const latestContinue = continueWorkers[continueWorkers.length - 1]
  const cancel = async (id: string) => {
    setCancelling(id)
    setError('')
    try { await onCancelWork(id) } catch (value) { setError(errorMessage(value)) } finally { setCancelling(undefined) }
  }
  return (
    <>
      {queue.length > 0 && (
        <section className={styles.addressQueuePanel}>
          <div className={detail.actionLine}><h2>Work queue</h2><span>{addressQueueSummary(queue)}</span></div>
          {error && <p className={styles.formError}>{error}</p>}
          <details className={styles.addressQueueDetails}>
            <summary>View active requests</summary>
            <ol className={styles.addressQueueList}>
              {queue.map((item) => {
                const comment = comments.find((candidate) => candidate.id === item.comment_id)
                const isCancelling = item.status === 'cancelling' || cancelling === item.id
                return (
                  <li className={styles.addressQueueItem} key={item.id}>
                    <div className={styles.addressQueueItemHeader}>
                      {comment ? <a href={`#comment-${comment.id}`}>{firstLine(comment.body)}</a> : <span>{item.kind === 'rebase' ? 'Rebase request' : 'Address request'}</span>}
                      <StatusBadge status={item.status} />
                    </div>
                    <div className={styles.addressQueueItemMeta}>
                      {item.kind === 'worker' && <span>{item.mode === 'assisted' ? 'Assisted' : 'Automatic'}</span>}
                      {item.session_id && <Link to={`/holons/${item.session_id}`}>Open holon</Link>}
                      <Button variant="ghost" size="small" disabled={isCancelling} onClick={() => void cancel(item.id)}>{isCancelling ? 'Cancelling…' : 'Cancel'}</Button>
                    </div>
                    {item.error && <p>{item.error}</p>}
                  </li>
                )
              })}
            </ol>
          </details>
        </section>
      )}
      {latestContinue && (
        <section>
          <div className={detail.actionLine}><h2>Continue work</h2><StatusBadge status={latestContinue.status} /></div>
          {latestContinue.error && <p>{latestContinue.error}</p>}
          {latestContinue.reply_body && <p>{latestContinue.reply_body}</p>}
          {latestContinue.result_head_commit && <p>Published {shortCommit(latestContinue.result_head_commit)}</p>}
          {latestContinue.session_id && <HolonTile holonId={latestContinue.session_id} label="Continue holon" />}
        </section>
      )}
    </>
  )
}

function CommentsPanel({
  comments,
  commentsSyncing = false,
  refreshError,
  queue,
  workers,
  addressNotice,
  readOnly,
  commentComposer,
  onReplyComment,
  onResolveComment,
  onReopenComment,
  onEditComment,
  onDeleteComment,
  onPublishComment,
  onAddressComments,
  onViewFile,
  availableFilePaths,
  revealCommentTarget,
  startingWorkers,
  embedded = false,
  showSavedContext = true,
}: {
  refreshError?: string
  embedded?: boolean
  showSavedContext?: boolean
  comments: PullRequestComment[]
  commentsSyncing?: boolean
  queue: PullRequestQueueItem[]
  workers: PullRequestWorker[]
  addressNotice: string
  readOnly: boolean
  commentComposer: ReactNode
  onReplyComment: (parentCommentId: string, body: string) => Promise<void>
  onResolveComment: (id: string) => Promise<void>
  onReopenComment: (id: string) => Promise<void>
  onEditComment: (id: string, body: string) => Promise<void>
  onDeleteComment: (id: string) => Promise<void>
  onPublishComment: (id: string) => Promise<void>
  onAddressComments: (ids: string[], mode: PullRequestWorkerMode) => Promise<void>
  onViewFile: (path: string) => void
  availableFilePaths: string[]
  revealCommentTarget?: { id: string }
  startingWorkers: boolean
}) {
  const [error, setError] = useState('')
  const [updatingId, setUpdatingId] = useState('')
  const [resolutionActions, setResolutionActions] = useState<Record<string, string>>({})
  const [editingId, setEditingId] = useState('')
  const [editBody, setEditBody] = useState('')
  const [uploadingEdit, setUploadingEdit] = useState(false)
  const [replyDrafts, setReplyDrafts] = useState<Record<string, string>>({})
  const [replyVersions, setReplyVersions] = useState<Record<string, number>>({})
  const [uploadingReplies, setUploadingReplies] = useState<Record<string, boolean>>({})
  const [savingReplyId, setSavingReplyId] = useState('')
	const { openValue: openMenuId, rootRef: commentMenuRef, close: closeCommentMenu, toggle: toggleCommentMenu } = useDropdownMenu('')
  const [unresolvedOnly, setUnresolvedOnly] = useState(false)
  const [expandedCommentIds, setExpandedCommentIds] = useState<Record<string, boolean>>({})
  const [dismissedRevealTarget, setDismissedRevealTarget] = useState<{ id: string }>()
  const setCommentExpanded = (id: string, expanded: boolean) => {
    setExpandedCommentIds((current) => ({ ...current, [id]: expanded }))
  }
  const toggleCommentExpanded = (id: string, expanded: boolean) => {
    if (expanded) setDismissedRevealTarget(undefined)
    else if (revealCommentTarget?.id === id) setDismissedRevealTarget(revealCommentTarget)
    setCommentExpanded(id, expanded)
  }
  const holonByComment = new Map(workers.filter((worker) => worker.comment_id && worker.session_id).map((worker) => [worker.comment_id, worker.session_id]))
  const addressWorkByComment = new Map(queue.filter((item) => item.comment_id).map((item) => [item.comment_id, item]))
  const topLevelComments = comments.filter((comment) => !comment.parent_comment_id)
  const replies = comments.filter((comment) => comment.parent_comment_id)
  const visibleComments = topLevelComments.filter((comment) => !unresolvedOnly || comment.status === 'unresolved' || comment.id === revealCommentTarget?.id)

  const submitReply = async (event: FormEvent, parentCommentId: string) => {
    event.preventDefault()
    const draft = replyDrafts[parentCommentId] ?? ''
    const nextBody = draft.trim()
    if (nextBody === '' || savingReplyId || uploadingReplies[parentCommentId]) return
    setSavingReplyId(parentCommentId)
    setError('')
    try {
      await onReplyComment(parentCommentId, nextBody)
      setReplyVersions((current) => ({ ...current, [parentCommentId]: (current[parentCommentId] ?? 0) + 1 }))
      setReplyDrafts((current) => current[parentCommentId] === draft
        ? { ...current, [parentCommentId]: '' }
        : current)
    } catch (value) {
      setError(errorMessage(value))
    } finally {
      setSavingReplyId('')
    }
  }

  const updateComment = async (comment: PullRequestComment) => {
    if (resolutionActions[comment.id]) return
    setResolutionActions((current) => ({ ...current, [comment.id]: comment.status === 'resolved' ? 'Reopening...' : 'Resolving...' }))
    setError('')
    try {
      if (comment.status === 'resolved') {
        await onReopenComment(comment.id)
      } else {
        await onResolveComment(comment.id)
      }
		closeCommentMenu()
    } catch (value) {
      setError(errorMessage(value))
    } finally {
      setResolutionActions((current) => {
        const next = { ...current }
        delete next[comment.id]
        return next
      })
    }
  }

  const startEdit = (comment: PullRequestComment) => {
    setCommentExpanded(comment.parent_comment_id || comment.id, true)
    setEditingId(comment.id)
    setEditBody(comment.body)
	closeCommentMenu()
    setError('')
  }

  const cancelEdit = () => {
    setEditingId('')
    setEditBody('')
  }

  const saveEdit = async (event: FormEvent, comment: PullRequestComment) => {
    event.preventDefault()
    const nextBody = editBody.trim()
    if (nextBody === '' || uploadingEdit || updatingId === comment.id) return
    setUpdatingId(comment.id)
    setError('')
    try {
      await onEditComment(comment.id, nextBody)
      setEditingId('')
      setEditBody('')
    } catch (value) {
      setError(errorMessage(value))
    } finally {
      setUpdatingId('')
    }
  }

  const deleteComment = async (comment: PullRequestComment) => {
    if (!window.confirm('Delete this comment?')) return
    setUpdatingId(comment.id)
    setError('')
    try {
      await onDeleteComment(comment.id)
		closeCommentMenu()
    } catch (value) {
      setError(errorMessage(value))
    } finally {
      setUpdatingId('')
    }
  }

  const publishComment = async (comment: PullRequestComment) => {
    setUpdatingId(comment.id)
    setError('')
    closeCommentMenu()
    try {
      await onPublishComment(comment.id)
    } catch (value) {
      setError(errorMessage(value))
    } finally {
      setUpdatingId('')
    }
  }

  const commentActions = (comment: PullRequestComment) => !readOnly && editingId !== comment.id && (
    <div className={styles.commentMenu} ref={openMenuId === comment.id ? commentMenuRef : undefined}>
      <Button variant="ghost" size="small"
        aria-expanded={openMenuId === comment.id}
        aria-haspopup="menu"
        aria-label={comment.parent_comment_id ? 'Reply actions' : 'Comment actions'}
        className={detail.threadIconButton}
        onClick={() => toggleCommentMenu(comment.id)}
        type="button"
      >
        <MoreVertical />
      </Button>
      {openMenuId === comment.id && (
        <MenuPopover className={styles.commentMenuPopover}>
          {comment.publication_state === 'draft' && (
            <MenuItem disabled={updatingId === comment.id} onClick={() => void publishComment(comment)}>
              <Send /> Publish
            </MenuItem>
          )}
          <MenuItem disabled={updatingId === comment.id || Boolean(resolutionActions[comment.id])} onClick={() => startEdit(comment)}>
            <Pencil /> Edit
          </MenuItem>
          <MenuItem disabled={updatingId === comment.id || Boolean(resolutionActions[comment.id])} onClick={() => void deleteComment(comment)}>
            <Trash2 /> Delete
          </MenuItem>
        </MenuPopover>
      )}
    </div>
  )

  const commentBody = (comment: PullRequestComment) => editingId === comment.id ? (
    <form className={styles.commentEditForm} onSubmit={(event) => void saveEdit(event, comment)}>
      <MarkdownEditor key={comment.id} aria-label={comment.parent_comment_id ? 'Edit reply' : 'Edit comment'} value={editBody} onChange={setEditBody} onUploadingChange={setUploadingEdit} disabled={updatingId === comment.id} maxLength={4000} rows={3} />
      <div>
        <Button icon={<X />} size="small" disabled={uploadingEdit || updatingId === comment.id} onClick={cancelEdit}>Cancel</Button>
        <Button variant="primary" size="small" disabled={uploadingEdit || updatingId === comment.id || Boolean(resolutionActions[comment.id]) || editBody.trim() === ''} type="submit">Save</Button>
      </div>
    </form>
  ) : <PullRequestMarkdown value={comment.body} />

  return (
    <section className={embedded ? detail.diffThreads : styles.commentsPanel}>
      {!embedded && <header className={styles.commentsHeader}>
        <h2>Activity</h2>
        <Button variant="ghost" size="small" aria-pressed={unresolvedOnly} onClick={() => setUnresolvedOnly(!unresolvedOnly)}>
          {unresolvedOnly ? 'Show all activity' : 'Unresolved only'}
        </Button>
      </header>}
      {refreshError && <p role="alert" className={styles.formError}>{refreshError}</p>}
      {addressNotice && <p className={styles.addressNotice} role="status">{addressNotice}</p>}
      {error && <p className={styles.formError}>{error}</p>}
      <div className={styles.commentList} role="list" aria-label="Comment threads">
        {visibleComments.map((comment) => {
          const addressWork = addressWorkByComment.get(comment.id)
          const threadReplies = replies.filter((reply) => reply.parent_comment_id === comment.id)
          const replyBody = replyDrafts[comment.id] ?? ''
          const activeForm = editingId === comment.id || threadReplies.some((reply) => reply.id === editingId) || replyBody.length > 0
          const revealed = revealCommentTarget?.id === comment.id && dismissedRevealTarget !== revealCommentTarget
          const expanded = comment.status === 'unresolved' || expandedCommentIds[comment.id] === true || activeForm || revealed
          const contentId = `comment-content-${comment.id}`
          const canViewFile = Boolean(comment.path) && availableFilePaths.includes(comment.path ?? '')
          return (
          <div className={embedded ? detail.diffThread : undefined} role="listitem" key={comment.id}>
          <article className={`${styles.commentItem} ${comment.status === 'resolved' ? styles.resolvedComment : ''}`} id={`comment-${comment.id}`} tabIndex={-1}>
            <header className={styles.commentItemHeader}>
              <div className={styles.commentHeaderStart}>
                <CommentAuthor comment={comment} onViewInChanges={canViewFile ? () => onViewFile(comment.path!) : undefined} />
              </div>
              <div className={detail.threadHeaderActions}>
                {comment.status === 'resolved' && (
                  <span className={detail.threadResolution}>
                    <span>Resolved</span>
                    {!expanded && <><span aria-hidden="true">·</span><span>{threadReplies.length} {threadReplies.length === 1 ? 'reply' : 'replies'}</span></>}
                  </span>
                )}
                {commentActions(comment)}
                {comment.status === 'resolved' && (
                  <Button variant="ghost" size="small" className={detail.threadIconButton}
                    icon={expanded ? <ChevronUp /> : <ChevronDown />} aria-controls={contentId}
                    aria-expanded={expanded} aria-label={expanded ? 'Collapse thread' : 'Expand thread'}
                    disabled={activeForm} onClick={() => toggleCommentExpanded(comment.id, !expanded)} />
                )}
              </div>
            </header>
            <div id={contentId} className={detail.threadContent}>
              <PullRequestThreadContext comment={comment} showExcerpt={expanded && showSavedContext} />
              {!expanded ? (
                <div className={detail.collapsedThreadSummary}>
                  <p>{firstLine(comment.body)}</p>
                </div>
              ) : (
                <div className={styles.commentBody}>
                  {commentBody(comment)}
                  {threadReplies.map((reply) => (
                    <div className={styles.commentReply} key={reply.id}>
                      <div className={styles.commentReplyHeader}>
                        <CommentAuthor comment={reply} />
                        {reply.publication_state === 'draft' && commentActions(reply)}
                      </div>
                      <div className={styles.commentReplyBody}>{commentBody(reply)}</div>
                    </div>
                  ))}
                </div>
              )}
            </div>
            {addressWork && (
              <div className={styles.commentWorkState}>
                <Bot aria-hidden="true" />
                <StatusBadge status={addressWork.status} label={`Address ${addressWork.status.replace(/_/g, ' ')}`} />
                <span>{addressWork.mode === 'assisted' ? 'Assisted' : 'Automatic'}</span>
              </div>
            )}
            {holonByComment.has(comment.id) && (
              <div className={styles.commentHolons}>
                <HolonTile holonId={holonByComment.get(comment.id)!} label="Comment holon" compact />
              </div>
            )}
            {!readOnly && (
              <form className={styles.commentReplyActions} onSubmit={(event) => void submitReply(event, comment.id)}>
                <div className={styles.commentReplyEditor}><MarkdownEditor
                  className={styles.commentReplyInput}
                  aria-label={`Reply to comment: ${firstLine(comment.body)}`}
                  value={replyBody}
                  previewResetKey={String(replyVersions[comment.id] ?? 0)}
                  onChange={(body) => {
                    setReplyDrafts((current) => ({ ...current, [comment.id]: body }))
                    if (body) setCommentExpanded(comment.id, true)
                  }}
                  onUploadingChange={(uploading) => setUploadingReplies((current) => ({ ...current, [comment.id]: uploading }))}
                  disabled={savingReplyId === comment.id}
                  autoResize rows={1}
                  revealToolbar
                  header={<div className={styles.commentAuthor}><strong>You</strong></div>}
                  maxLength={4000}
                  placeholder="Write a reply..."
                  actions={<Button className={styles.commentReplySubmit} size="small" disabled={Boolean(savingReplyId) || uploadingReplies[comment.id] || replyBody.trim() === ''} type="submit">
                    {savingReplyId === comment.id ? 'Replying...' : 'Reply'}
                  </Button>}
                /></div>
                <span className={styles.commentReplyButtons}>
                  {comment.publication_state !== 'draft' && <Button size="small" disabled={uploadingReplies[comment.id] || updatingId === comment.id || Boolean(resolutionActions[comment.id])} aria-busy={Boolean(resolutionActions[comment.id])} onClick={() => void updateComment(comment)}>{resolutionActions[comment.id] || (comment.status === 'resolved' ? 'Reopen' : 'Mark resolved')}</Button>}
                  {comment.publication_state !== 'draft' && comment.status === 'unresolved' && <ResolutionAction label={addressWork ? 'Address in progress' : undefined} compact disabled={startingWorkers || Boolean(addressWork) || Boolean(resolutionActions[comment.id])} onResolve={(mode) => void onAddressComments([comment.id], mode)} />}
                </span>
              </form>
            )}
          </article>
          </div>
          )
        })}
        {!commentsSyncing && visibleComments.length === 0 && <p className={detail.emptyActivity}>{unresolvedOnly ? 'No unresolved conversations.' : 'No comments yet.'}</p>}
      </div>
      {!readOnly && commentComposer}
    </section>
  )
}

function formatCommentTime(value: string) {
  const minutes = Math.max(0, Math.floor((Date.now() - new Date(value).getTime()) / 60000))
  if (minutes < 1) return 'Just now'
  if (minutes < 60) return `${minutes} min ago`
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `${hours} ${hours === 1 ? 'hour' : 'hours'} ago`
  const days = Math.floor(hours / 24)
  if (days < 7) return `${days} ${days === 1 ? 'day' : 'days'} ago`
  return new Date(value).toLocaleDateString(undefined, { month: 'short', day: 'numeric' })
}

function CommentAuthor({ comment, onViewInChanges }: { comment: PullRequestComment, onViewInChanges?: () => void }) {
  return (
    <div className={styles.commentAuthor}>
      {comment.author_type === 'user' && comment.author_github_user_id &&
        <MemberIdentity memberId={comment.author_github_user_id} linkProfile showAvatar={false} className={styles.commentMember} />}
      {comment.author_type === 'agent'
        ? <strong>Agent</strong>
        : !comment.author_github_user_id && <strong>You</strong>}
      {(comment.scope === 'file' || comment.scope === 'line') && <>
        <span className={styles.commentSeparator} aria-hidden="true">·</span>
        <PullRequestThreadLocation comment={comment} onViewInChanges={onViewInChanges} />
      </>}
      <span className={styles.commentSeparator} aria-hidden="true">·</span>
      <time dateTime={comment.created_at} title={formatDateTime(comment.created_at)}>{formatCommentTime(comment.created_at)}</time>
      {comment.publication_state === 'draft' && <span className={detail.draftBadge}>Draft</span>}
      {comment.publication_state === 'pending' && <span>Pending GitHub publication</span>}
      {comment.publication_state === 'failed' && <span>Saved locally; GitHub publication failed</span>}
    </div>
  )
}

function CommitsPanel({ pullRequest, commits, inspection, loading, error }: {
  pullRequest?: PullRequest
  commits: PullRequestCommit[]
  inspection?: WorkspaceInspection
  loading: boolean
  error: string
}) {
  const [expanded, setExpanded] = useState<Set<string>>(() => new Set())
  if (loading) return <p className={styles.message}>Loading commits...</p>
  if (error) return <p className={styles.message}>{error}</p>
  if (!pullRequest) return <p className={styles.message}>Commits unavailable.</p>

  const expandableCommits = commits.filter((commit) => commit.message.split('\n').slice(1).join('\n').trim())
  const allExpanded = expandableCommits.length > 0 && expandableCommits.every((commit) => expanded.has(commit.sha))
  const commonAncestor = inspection?.common_ancestor_commit
  const ancestorDetails = inspection?.common_ancestor

  return (
    <section className={styles.commitPanel}>
      <div className={detail.commitContext}>
        <span>
          Merge base with {pullRequest.base_branch}: <code>{commonAncestor ? shortCommit(commonAncestor) : 'Unavailable'}</code>
          {ancestorDetails && <> - {firstLine(ancestorDetails.message)} · {ancestorDetails.author_name || 'Unknown author'}{ancestorDetails.authored_at ? ` · ${formatDateTime(ancestorDetails.authored_at)}` : ''}</>}
        </span>
        {expandableCommits.length > 0 && (
          <Button variant="ghost" size="small" aria-pressed={allExpanded} onClick={() => setExpanded(allExpanded ? new Set() : new Set(expandableCommits.map((commit) => commit.sha)))}>
            {allExpanded ? 'Collapse all' : 'Expand all'}
          </Button>
        )}
      </div>
      <div className={styles.commitList}>
        {commits.length > 0 ? commits.map((commit) => {
          const messageBody = commit.message.split('\n').slice(1).join('\n').trim()
          const row = (
            <>
              {messageBody ? <ChevronRight aria-hidden="true" /> : <span aria-hidden="true" />}
              <div>
                <strong>{firstLine(commit.message)}</strong>
                <span>{commit.author_name || 'Unknown author'}{commit.authored_at ? ` · ${formatDateTime(commit.authored_at)}` : ''}</span>
              </div>
              {commit.external_url ? (
                <a href={commit.external_url} rel="noreferrer" target="_blank" aria-label={`View commit ${shortCommit(commit.sha)} on GitHub`}>
                  <code>{shortCommit(commit.sha)}</code>
                </a>
              ) : <code>{shortCommit(commit.sha)}</code>}
            </>
          )
          return messageBody ? (
            <details className={detail.commitEntry} key={commit.sha} open={expanded.has(commit.sha)} onClick={(event) => {
              if ((event.target as Element).closest('a')) return
              event.preventDefault()
              setExpanded((current) => {
                const next = new Set(current)
                if (next.has(commit.sha)) next.delete(commit.sha)
                else next.add(commit.sha)
                return next
              })
            }}>
              <summary className={styles.commitRow}>{row}</summary>
              <div className={detail.commitMessage}><p>{messageBody}</p></div>
            </details>
          ) : (
            <div className={detail.commitEntry} key={commit.sha}>
              <div className={styles.commitRow}>{row}</div>
            </div>
          )
        }) : (
          <p className={detail.emptyActivity}>No commits available.</p>
        )}
      </div>
    </section>
  )
}

function MergePullRequestDialog({ open, pullRequest, unresolvedCount, merging, checkingBranch, error, onClose, onMerge }: {
  open: boolean
  pullRequest: PullRequest
  unresolvedCount: number
  merging: boolean
  checkingBranch: boolean
  error: string
  onClose: () => void
  onMerge: () => void
}) {
  const mergeLabel = unresolvedCount > 0 ? 'Merge despite unresolved comments' : 'Merge'
  const readiness = checkingBranch ? 'Checking branch…' : pullRequest.mergeable === false ? mergeBlockedReasonLabel(pullRequest.merge_blocked_reason) : 'Ready'
  const githubReadiness = githubDetails(pullRequest).readiness
  return (
    <Dialog
      open={open}
      title="Merge pull request"
      onClose={onClose}
      footer={<><Button disabled={merging} onClick={onClose}>Cancel</Button><Button variant="primary" icon={<GitMerge />} disabled={merging || checkingBranch || pullRequest.mergeable === false} onClick={onMerge}>{merging ? 'Merging...' : mergeLabel}</Button></>}
    >
      <div className={styles.mergeDialog}>
        <dl>
          <div><dt>Strategy</dt><dd>{mergeStrategyLabel(pullRequest.merge_request_strategy || 'squash')}</dd></div>
          <div><dt>Base branch</dt><dd>{pullRequest.base_branch}</dd></div>
          <div><dt>Head commit</dt><dd>{shortCommit(pullRequest.head_commit)}</dd></div>
          <div><dt>Unresolved comments</dt><dd>{unresolvedCount}</dd></div>
          <div><dt>Readiness</dt><dd>{readiness}</dd></div>
          {pullRequest.sync_provider === 'github' && <div><dt>GitHub checks</dt><dd>{githubReadiness?.checks_state ?? 'unavailable'}</dd></div>}
          {pullRequest.sync_provider === 'github' && <div><dt>GitHub mergeability</dt><dd>{githubReadiness?.mergeability_state ?? 'unavailable'}</dd></div>}
        </dl>
        {error && <p className={styles.formError}>{error}</p>}
      </div>
    </Dialog>
  )
}

export function CreatePullRequestMenu({ disabled = false, pendingStatus, sessionHeaderMenu = false, branch, baseBranch, onCompare, onSelect }: {
  disabled?: boolean
  pendingStatus?: PullRequestCreationStatus
  sessionHeaderMenu?: boolean
  branch?: string
  baseBranch?: string
  onCompare?: () => void
  onSelect: (status: PullRequestCreationStatus) => void
}) {
  const dropdown = useDropdownMenu(false)
  const sharedPopover = useOptionalDismissiblePopover('pull-request-creation-options')
  const { open, rootRef, close } = sharedPopover ?? dropdown
  const triggerRef = sharedPopover?.triggerRef
  const toggle = sharedPopover?.toggle ?? (() => dropdown.toggle(true))
  const pending = Boolean(pendingStatus)
  const primaryOption = creationOption(pendingStatus ?? defaultPullRequestCreationStatus)
  const select = (status: PullRequestCreationStatus) => {
    if (disabled || pending) return
    close()
    onSelect(status)
  }

  return (
    <div className={`${styles.createMenu} ${sessionHeaderMenu ? styles.sessionCreateMenu : ''}`} ref={rootRef}>
      <Button className={styles.createMenuPrimary} variant="primary" icon={<GitPullRequest />} disabled={disabled || pending} onClick={() => select(defaultPullRequestCreationStatus)}>
        {pending ? primaryOption.pendingLabel : primaryOption.label}
      </Button>
      <button
        ref={triggerRef}
        aria-expanded={open}
        aria-haspopup="menu"
        aria-label="Pull request creation options"
        className={styles.createMenuButton}
        data-variant={sessionHeaderMenu ? 'primary' : undefined}
        disabled={disabled || pending}
        onClick={toggle}
        type="button"
      >
        <ChevronDown />
      </button>
      {open && !pending && (
        <MenuPopover className={styles.createMenuPopover} data-holon-header-popover={sessionHeaderMenu ? '' : undefined}>
          {sessionHeaderMenu && branch && <div className={styles.createMenuBranches}><span title={branch}>{branch}</span><span>→</span><span>{baseBranch}</span></div>}
          {pullRequestCreationOptions.map((option) => (
            <MenuItem key={option.status} onClick={() => select(option.status)}>
              <GitPullRequest /> {option.label}
            </MenuItem>
          ))}
          {onCompare && <MenuItem onClick={() => { close(); onCompare() }}>Compare changes</MenuItem>}
        </MenuPopover>
      )}
    </div>
  )
}

function addressRequestNotice(work: PullRequestWorker[]) {
  if (work.length === 0) return 'No Address work was created.'
  const counts = new Map<string, number>()
  for (const item of work) counts.set(item.status, (counts.get(item.status) ?? 0) + 1)
  if (work.length === 1) {
    const status = work[0].status
    if (status === 'skipped') return 'Comment already resolved; Address work skipped.'
    if (status === 'failed') return 'Address work could not start.'
    if (status === 'queued') return 'Address request queued.'
    return 'Address work started.'
  }
  const order: PullRequestWorker['status'][] = ['running', 'waiting_user', 'queued', 'cancelling', 'completed', 'skipped', 'failed', 'cancelled', 'stale']
  const summary = order
    .filter((status) => counts.has(status))
    .map((status) => `${counts.get(status)} ${status.replace(/_/g, ' ')}`)
    .join(', ')
  return `${work.length} Address requests: ${summary}.`
}

function addressQueueSummary(queue: PullRequestQueueItem[]) {
  const running = queue.filter((item) => item.status === 'running' || item.status === 'waiting_user').length
  const queued = queue.filter((item) => item.status === 'queued').length
  const cancelling = queue.filter((item) => item.status === 'cancelling').length
  return [
    running > 0 ? `${running} running` : '',
    queued > 0 ? `${queued} queued` : '',
    cancelling > 0 ? `${cancelling} cancelling` : '',
  ].filter(Boolean).join(' · ')
}

function firstLine(value: string) {
  return value.split('\n')[0] || 'Commit'
}

function shortCommit(value: string) {
  return value.slice(0, 8)
}

function githubDetails(pullRequest: PullRequest) {
  return pullRequest.sync_data?.github ?? {}
}

function githubStatusNeedsPolling(pullRequest: PullRequest) {
  if (isActivePullRequestStatus(pullRequest.status) && pullRequest.comparison_state && pullRequest.comparison_state !== 'ready') return true
  return pullRequest.sync_provider === 'github' && isActivePullRequestStatus(pullRequest.status)
}

function pullRequestBranchState(readiness: PullRequestRebaseReadiness | undefined, phase: ReadinessPhase, hasConflicts: boolean) {
  if (hasConflicts) return 'conflicting'
  if (!readiness) return phase === 'loading' || phase === 'refreshing' ? 'checking' : 'unknown'
  return readiness.branch_freshness === 'not_up_to_date' ? 'behind' : 'up_to_date'
}

function GitHubReadinessPanel({ pullRequest, hasConflicts, rebaseAction, rebaseActive, branchState, baseCommitsAhead = 0, latestRebase }: { pullRequest: PullRequest, hasConflicts: boolean, rebaseAction: ReactNode, rebaseActive: boolean, branchState: string, baseCommitsAhead?: number, latestRebase?: PullRequestRebase }) {
  const publicationBlocked = (pullRequest.status === 'wip' || pullRequest.status === 'draft') && pullRequest.publication_blocked
  const showPublication = publicationBlocked || pullRequest.status === 'open' || pullRequest.status === 'merged'
  const behind = branchState === 'behind'
  const upToDate = branchState === 'up_to_date'
  const aheadLabel = `${pullRequest.base_branch} is ${baseCommitsAhead} ${baseCommitsAhead === 1 ? 'commit' : 'commits'} ahead`
  const branchLabel = branchState === 'checking'
    ? 'Checking branch…'
    : behind || hasConflicts
      ? `${aheadLabel} · ${hasConflicts ? 'Conflicts' : 'No conflicts'}`
      : upToDate ? 'Branch up to date' : 'Branch status unavailable'
  const rebasedAt = latestRebase?.completed_at || latestRebase?.created_at
  const linkedRebaseCompleted = upToDate && latestRebase?.status === 'completed' && latestRebase.session_id && rebasedAt
  return (
    <section aria-label="Merge readiness" className={detail.mergeReadiness}>
      {showPublication && (
        <div className={detail.publicationSummary}>
          {publicationBlocked ? <Circle /> : <CheckCircle2 className={detail.good} />}
          <span>{publicationBlocked ? 'Awaiting publication' : 'Published'}</span>
        </div>
      )}
      {pullRequest.status !== 'merged' && (
        <div className={detail.branchStatus}>
          {rebaseActive ? null : linkedRebaseCompleted ? (
            <Link className={`${detail.branchSummary} ${detail.branchCompletion} ${detail.good}`} to={`/holons/${latestRebase.session_id}`}>
              <CheckCircle2 />
              <span>Branch up to date · Rebased <time dateTime={rebasedAt}>{formatTimeOfDay(rebasedAt)}</time></span>
            </Link>
          ) : (
            <div className={`${detail.branchSummary} ${hasConflicts || behind ? detail.warning : upToDate ? detail.good : detail.muted}`}>
              {upToDate && !hasConflicts && !behind ? <CheckCircle2 /> : <GitBranch />}
              <span>{branchLabel}</span>
            </div>
          )}
          {rebaseAction}
        </div>
      )}
    </section>
  )
}

function mergeStrategyLabel(strategy?: string) {
  if (strategy === 'squash') return 'Squash'
  return strategy || 'Squash'
}

function formatDateTime(value: string) {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' }).format(date)
}

function formatTimeOfDay(value: string) {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return `${String(date.getHours()).padStart(2, '0')}:${String(date.getMinutes()).padStart(2, '0')}`
}

function errorMessage(value: unknown) {
  const error = value as ApiError
  return error?.message || 'Request failed.'
}

function PrepareReviewMenu({ primary, pending, disabled, onSelect }: {
  primary: boolean
  pending: boolean
  disabled: boolean
  onSelect: (mode: PullRequestReviewMode) => void
}) {
  const { open, rootRef, close, toggle } = useDropdownMenu(false)
  const select = (mode: PullRequestReviewMode) => { close(); onSelect(mode) }
  return <div className={`${detail.resolutionAction} ${primary ? detail.primaryResolution : ''}`} ref={rootRef}>
    <Button className={`${detail.wideButton} ${primary ? detail.primaryAction : detail.mutedAction}`} icon={<Bot />} disabled={disabled || pending} onClick={() => select('assisted')}>{pending ? 'Starting...' : 'Run review'}</Button>
    <button type="button" aria-label="Review options" aria-haspopup="menu" aria-expanded={open} className={detail.splitChevron} disabled={disabled || pending} onClick={() => toggle(true)}><ChevronDown /></button>
    {open && !pending && <SplitButtonMenu className={detail.resolutionMenu}>
      <SplitButtonMenuItem onClick={() => select('auto')}><Bot />Run review and publish automatically</SplitButtonMenuItem>
    </SplitButtonMenu>}
  </div>
}
