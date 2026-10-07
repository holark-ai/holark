import type { PullRequest } from './types'

// One acceptance boundary is shared by list, detail, and linked-Holon reads.
// Client barriers cover the interval before durable registration reaches a GET.
const views = new Map<string, PullRequest>()
const barriers = new Map<string, { revision: number, pending: number }>()
let sequence = 0

export function resetPullRequestAcceptance() {
  views.clear()
  barriers.clear()
  sequence = 0
}

export function pullRequestObservation() { return ++sequence }

export function beginPullRequestMutation(id: string) {
  const previous = barriers.get(id)
  barriers.set(id, { revision: ++sequence, pending: (previous?.pending ?? 0) + 1 })
  return () => {
    const current = barriers.get(id)
    barriers.set(id, { revision: ++sequence, pending: Math.max(0, (current?.pending ?? 1) - 1) })
  }
}

export function acceptPullRequest(next: PullRequest, observation = pullRequestObservation(), mutation = false): PullRequest {
  if (!Number.isSafeInteger(next.view_revision)) throw new Error('Invalid pull request view revision.')
  const current = views.get(next.id)
  const barrier = barriers.get(next.id)
  if (current && !mutation && barrier && (barrier.pending > 0 || observation < barrier.revision)) return current
  if (current && next.view_revision < current.view_revision) return current
  views.set(next.id, next)
  return next
}
