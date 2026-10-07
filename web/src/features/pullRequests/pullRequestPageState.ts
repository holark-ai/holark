import type { PullRequest, PullRequestRebase, PullRequestRebaseReadiness } from '../../data/types'

export type PullRequestRecommendation = 'rebase' | 'resolve' | 'review' | 'lifecycle'

export type RebaseActivity = {
  kind: 'rebasing' | 'resolving' | 'analyzing'
  target?: string
}

export type ReadinessPhase = 'idle' | 'loading' | 'refreshing' | 'ready' | 'failed'

type ReadinessState = {
  phase: ReadinessPhase
  revision?: string
  requestedRevision?: string
  value?: PullRequestRebaseReadiness
}

export type PullRequestPageState = {
  readiness: ReadinessState
  rebaseActivity?: RebaseActivity
}

export type PullRequestPageEvent =
  | { type: 'readiness-reset' }
  | { type: 'readiness-started', revision: string }
  | { type: 'readiness-succeeded', revision: string, value: PullRequestRebaseReadiness }
  | { type: 'readiness-failed', revision: string }
  | { type: 'rebase-started', activity: RebaseActivity }
  | { type: 'rebase-finished' }

export const initialPullRequestPageState: PullRequestPageState = {
  readiness: { phase: 'idle' },
}

export function pullRequestPageReducer(state: PullRequestPageState, event: PullRequestPageEvent): PullRequestPageState {
  switch (event.type) {
    case 'readiness-reset':
      return initialPullRequestPageState
    case 'readiness-started':
      return {
        ...state,
        readiness: {
          ...state.readiness,
          phase: state.readiness.value ? 'refreshing' : 'loading',
          requestedRevision: event.revision,
        },
      }
    case 'readiness-succeeded':
      if (state.readiness.requestedRevision !== event.revision) return state
      return {
        ...state,
        readiness: {
          phase: 'ready',
          revision: event.revision,
          value: event.value,
        },
      }
    case 'readiness-failed':
      if (state.readiness.requestedRevision !== event.revision) return state
      return {
        ...state,
        readiness: {
          ...state.readiness,
          phase: 'failed',
          requestedRevision: undefined,
        },
      }
    case 'rebase-started':
      return { ...state, rebaseActivity: event.activity }
    case 'rebase-finished':
      return { ...state, rebaseActivity: undefined }
  }
}

export function pullRequestRevision(id?: string, baseCommit?: string, headCommit?: string) {
  return id ? `${id}:${baseCommit || ''}:${headCommit || ''}` : ''
}

export function readinessMatchesRevision(state: PullRequestPageState, revision: string) {
  return Boolean(revision && state.readiness.revision === revision)
}

export function readinessAfterRebase(state: PullRequestPageState, pullRequest: PullRequest | undefined, rebase: PullRequestRebase | undefined): ReadinessState {
  // Once the head changes after a rebase, its old readiness is no longer
  // useful. Show the pending check until readiness for the new head arrives.
  if (!pullRequest || rebase?.status !== 'completed' || !rebase.result_head_commit
    || rebase.pull_request_id !== pullRequest.id
    || rebase.result_head_commit !== pullRequest.head_commit
    || rebase.target_base_commit !== pullRequest.base_commit
    || state.readiness.value?.head_commit === pullRequest.head_commit) return state.readiness
  return { phase: state.readiness.phase === 'failed' ? 'failed' : 'loading' }
}

export function recommendPullRequestAction(state: PullRequestPageState, input: {
  activeRebase: boolean
  latestReviewCompleted: boolean
  reviewCurrent: boolean
  unresolvedCount: number
}): PullRequestRecommendation | undefined {
  const readiness = state.readiness.value
  if (state.rebaseActivity || input.activeRebase) return 'rebase'
  if (!readiness && state.readiness.phase !== 'failed') return undefined
  if (readiness?.branch_freshness === 'not_up_to_date' || readiness?.rebase_conflict_state === 'conflicting') return 'rebase'
  if (input.latestReviewCompleted && input.unresolvedCount > 0) return 'resolve'
  if (!input.reviewCurrent) return 'review'
  return input.unresolvedCount > 0 ? 'resolve' : 'lifecycle'
}
