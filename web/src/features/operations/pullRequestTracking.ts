import type { PullRequest, PullRequestSessionLink } from '../../data/types'

export type PullRequestTracking = {
  pullRequests: PullRequest[]
  pullRequestByHolon: ReadonlyMap<string, PullRequest>
}

export function buildPullRequestTracking(pullRequests: PullRequest[], links: PullRequestSessionLink[]): PullRequestTracking {
  const pullRequestById = new Map(pullRequests.map((pullRequest) => [pullRequest.id, pullRequest]))
  const pullRequestByHolon = new Map<string, PullRequest>()
  for (const link of links) {
    const pullRequest = pullRequestById.get(link.pull_request_id)
    if (pullRequest) pullRequestByHolon.set(link.session_id, pullRequest)
  }
  return { pullRequests, pullRequestByHolon }
}
