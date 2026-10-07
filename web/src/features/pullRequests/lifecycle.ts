import type { PullRequest, PullRequestStatus, PullRequestTransitionStatus } from '../../data/types'

export type PullRequestCreationStatus = Extract<PullRequestStatus, 'wip' | 'draft' | 'open'>

export const defaultPullRequestCreationStatus: PullRequestCreationStatus = 'open'

export const pullRequestCreationOptions: Array<{
  status: PullRequestCreationStatus
  label: string
  pendingLabel: string
}> = [
  { status: 'wip', label: 'Create WIP', pendingLabel: 'Creating...' },
  { status: 'draft', label: 'Create draft PR', pendingLabel: 'Creating...' },
  { status: 'open', label: 'Open PR', pendingLabel: 'Creating...' },
]

export function creationOption(status: PullRequestCreationStatus) {
  return pullRequestCreationOptions.find((option) => option.status === status)
    ?? pullRequestCreationOptions.find((option) => option.status === defaultPullRequestCreationStatus)
    ?? pullRequestCreationOptions[0]
}

export function isActivePullRequestStatus(status: PullRequest['status']) {
  return status === 'wip' || status === 'draft' || status === 'open'
}

export function isTerminalPullRequestStatus(status: PullRequest['status']) {
  return status === 'merged' || status === 'closed'
}

export type LifecycleCommand = {
  label: string
  icon: 'pull-request' | 'retry' | 'merge' | 'close'
  transition?: PullRequestTransitionStatus
  merge?: boolean
  close?: boolean
}

export function lifecycleCommands(status: PullRequestStatus, mergeable: boolean, reason?: string): {
  primary: LifecycleCommand
  alternatives: LifecycleCommand[]
  tooltip?: string
} {
  const draft = { label: 'Move to draft', icon: 'pull-request' as const, transition: 'draft' as const }
  const wip = { label: 'Move to WIP', icon: 'retry' as const, transition: 'wip' as const }
  const open = { label: 'Open PR', icon: 'pull-request' as const, transition: 'open' as const }
  const close = { label: 'Close', icon: 'close' as const, close: true }

  switch (status) {
    case 'wip':
      return { primary: draft, alternatives: [open, close] }
    case 'draft':
      return { primary: open, alternatives: [wip, close] }
    case 'open':
      return {
        primary: { label: 'Merge', icon: 'merge', merge: mergeable || undefined },
        alternatives: [draft, close],
        tooltip: mergeable ? undefined : mergeBlockedTooltip(reason),
      }
    case 'closed':
      return { primary: { label: 'Reopen', icon: 'retry' }, alternatives: [], tooltip: 'Reopen unavailable' }
    case 'merged':
      return { primary: { label: 'Merged', icon: 'merge' }, alternatives: [] }
  }
}

const mergeBlockedReasons: Record<string, { label: string; tooltip: string }> = {
  comparison_stale: { label: 'Branch comparison needs refresh', tooltip: 'Refresh branch comparison' },
  merge_provider_unsupported: { label: 'Merge provider unsupported', tooltip: 'This pull request cannot be merged by Holark' },
  repository_unavailable: { label: 'Repository unavailable', tooltip: 'Repository unavailable' },
  base_not_found: { label: 'Base branch not found', tooltip: 'Branch not found' },
  head_not_found: { label: 'Head commit not found', tooltip: 'Branch not found' },
  stale_head: { label: 'Head changed', tooltip: 'Branch state changed' },
  github_head_changed: { label: 'Head changed', tooltip: 'Branch state changed' },
  not_up_to_date: { label: 'Head is not up to date with base', tooltip: 'Branch needs rebase' },
  not_fast_forwardable: { label: 'Head is not up to date with base', tooltip: 'Branch needs rebase' },
  github_unavailable: { label: 'GitHub merge unavailable', tooltip: 'GitHub unavailable' },
  github_merge_blocked: { label: 'GitHub blocked merge', tooltip: 'GitHub unavailable' },
  github_status_missing: { label: 'GitHub status unavailable', tooltip: 'Refresh GitHub status' },
  github_status_stale: { label: 'GitHub status is stale', tooltip: 'Refresh GitHub status' },
  github_checks_pending: { label: 'GitHub checks are pending', tooltip: 'GitHub checks pending' },
  github_checks_failed: { label: 'GitHub checks failed', tooltip: 'GitHub checks failed' },
  github_mergeability_unknown: { label: 'GitHub mergeability is unknown', tooltip: 'GitHub mergeability unknown' },
}

export function mergeBlockedTooltip(reason?: string) {
  return (reason && mergeBlockedReasons[reason]?.tooltip) || 'Merge unavailable'
}

export function mergeBlockedReasonLabel(reason?: string) {
  return (reason && mergeBlockedReasons[reason]?.label) || reason || 'Not ready'
}
