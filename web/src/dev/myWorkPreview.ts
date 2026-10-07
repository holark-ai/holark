import type { Issue, PullRequest, WorkItemSummary, WorkItemsResult } from '../data/types'
import { previewMembers } from './pullRequestListPreview'

const viewReasons: Record<string, string> = {
  review_requested: 'Review requested',
  assigned_issues: 'Assigned issue',
  assigned_pull_requests: 'Assigned PR',
}

export function previewMyWork(pullRequests: PullRequest[], issues: Issue[], params: URLSearchParams): WorkItemsResult {
  const identity = previewMembers.find((member) => member.is_me)!
  const rows: WorkItemSummary[] = pullRequests
    .filter((pr) => ['open', 'draft', 'wip'].includes(pr.status))
    .map((pr) => ({
      id: pr.id, kind: 'pull_request', title: pr.title, number: pr.sync_data.github?.number ?? 0,
      status: pr.status, assignee_ids: pr.assignee_holark_ids,
      reviewer_ids: pr.requested_reviewer_holark_ids, updated_at: pr.updated_at,
      reasons: [
        ...(pr.assignee_holark_ids.includes(identity.id) ? ['Assigned PR'] : []),
        ...(pr.status !== 'wip' && pr.requested_reviewer_holark_ids.includes(identity.id) ? ['Review requested'] : []),
      ],
    }))
  rows.push(...issues.filter((issue) => issue.status === 'open' && issue.assignee_holark_ids.includes(identity.id)).map((issue): WorkItemSummary => ({
    id: issue.id, kind: 'issue', title: issue.title, number: issue.sync_data.github?.number ?? 0,
    status: issue.status, author_id: issue.issuer_holark_id, assignee_ids: issue.assignee_holark_ids,
    reviewer_ids: [], updated_at: issue.updated_at, reasons: ['Assigned issue'],
  })))
  const queue = rows.filter((row) => row.reasons.length > 0)
    .sort((a, b) => b.updated_at.localeCompare(a.updated_at) || a.id.localeCompare(b.id))
  const counts: Record<string, number> = { all: queue.length }
  for (const [view, reason] of Object.entries(viewReasons)) counts[view] = queue.filter((row) => row.reasons.includes(reason)).length
  const view = params.get('view') || 'all'
  const matches = view === 'all' ? queue : queue.filter((row) => row.reasons.includes(viewReasons[view]))
  const page = Math.max(1, Math.floor(Number(params.get('page')) || 1))
  const perPage = 20
  return {
    rows: matches.slice((page - 1) * perPage, page * perPage),
    total: matches.length, page, per_page: perPage, counts, identity, sync: [],
  }
}
