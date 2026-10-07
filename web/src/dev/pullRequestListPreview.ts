import type { GitHubMember, PullRequest, WorkItemsResult } from '../data/types'

export const previewMembers: GitHubMember[] = [
  { id: 'member-riley', login: 'riley', permission: 'admin', is_me: true },
  { id: 'member-ada', login: 'ada-reviewer', permission: 'write', is_me: false },
  { id: 'member-jonas', login: 'jonas', permission: 'write', is_me: false },
]

const previewTopics = [
  'repository search', 'terminal session recovery', 'keyboard navigation', 'pull request reviews',
  'workspace configuration', 'branch switching', 'file previews', 'commit history',
  'member search', 'GitHub synchronization', 'diff rendering', 'agent permissions',
  'notification settings', 'worktree cleanup', 'issue linking', 'dark theme contrast',
]
const previewChanges = [
  (topic: string) => `Improve ${topic}`,
  (topic: string) => `Fix edge cases in ${topic}`,
  (topic: string) => `Simplify ${topic} for new contributors`,
  (topic: string) => `Speed up ${topic} in large repositories`,
  (topic: string) => `Preserve ${topic} preferences across restarts and shared workspaces`,
]
const previewStates: PullRequest['status'][] = ['open', 'draft', 'wip', 'merged', 'closed']

// Cross topics with changes, varying people and dates independently of status.
// Keep these separate from the small shared-tree fixtures and their linked Holons.
export const additionalPreviewPullRequests: PullRequest[] = previewTopics.flatMap((topic, topicIndex) =>
  previewChanges.map((title, changeIndex) => {
    const index = topicIndex * previewChanges.length + changeIndex
    const number = 160 + index
    const status = previewStates[(topicIndex + changeIndex) % previewStates.length]
    const local = status === 'wip'
    const created = Date.UTC(2026, 8, 1) + index * 6 * 60 * 60 * 1000
    const updated = created + (index * 13 % 72) * 60 * 60 * 1000
    return {
      id: `pr-${number}`, view_revision: 1, comparison_state: 'ready',
      title: title(topic), summary: `Refine ${topic} with clearer behavior and more reliable feedback.`,
      base_branch: index % 6 === 0 ? 'release/next' : 'main', base_commit: '15ad9fe1',
      head_branch: `feature/pr-${number}`, head_commit: '7e9d8b1', status,
      sync_provider: local ? undefined : 'github',
      sync_data: local ? {} : { github: { number, draft: status === 'draft', url: `https://github.com/example/acme-dashboard/pull/${number}` } },
      assignee_holark_ids: index % 4 === 0 ? [] : index % 4 === 1
        ? ['member-riley', 'member-jonas'] : [previewMembers[index % previewMembers.length].id],
      requested_reviewer_holark_ids: local || index % 4 === 0 ? [] : index % 4 === 2
        ? ['member-ada', 'member-riley'] : [previewMembers[(index + 1) % previewMembers.length].id],
      linked_session_ids: [], created_at: new Date(created).toISOString(), updated_at: new Date(updated).toISOString(),
      closed_at: status === 'closed' ? new Date(updated).toISOString() : undefined,
      merged_at: status === 'merged' ? new Date(updated).toISOString() : undefined,
    }
  }),
)

const lifecycleStates: Record<string, string[]> = {
  active: ['wip', 'draft', 'open'], open: ['draft', 'open'], closed: ['closed', 'merged'],
  merged: ['merged'], wip: ['wip'], unmerged: ['wip', 'draft', 'open', 'closed'],
  all: ['wip', 'draft', 'open', 'closed', 'merged'],
}

const authorId = (pr: PullRequest) => previewMembers[(pr.sync_data.github?.number ?? 0) % previewMembers.length].id

// Supply interactive sample results to the real list without a running backend.
export function previewPullRequestSearch(pullRequests: PullRequest[], params: URLSearchParams): WorkItemsResult {
  const query = params.get('q')?.trim() || 'is:active sort:created-desc'
  const tokens: string[] = query.match(/"(?:\\.|[^"\\])*"|\S+/g) ?? []
  if (!tokens.some((token) => /^(is|state):/.test(token))) tokens.push('is:active')
  const sort = tokens.find((token) => token.startsWith('sort:'))?.slice(5) || 'created-desc'
  const page = Math.max(1, Number(params.get('page')) || 1)
  const perPage = 20
  const matches = pullRequests.filter((pr) => matchesTitle(pr.title, params.get('title') ?? '') && tokens.every((token) => {
    if (token.startsWith('"')) return pr.title.toLowerCase().includes(JSON.parse(token).toLowerCase())
    const separator = token.indexOf(':')
    if (separator < 0) {
      if (/^#\d+$/.test(token) || (tokens.filter((value) => !value.startsWith('is:')).length === 1 && /^\d+$/.test(token))) {
        return pr.sync_data.github?.number === Number(token.replace('#', ''))
      }
      return pr.title.toLowerCase().includes(token.toLowerCase())
    }
    const name = token.slice(0, separator)
    const value = token.slice(separator + 1)
    if (name === 'is') return lifecycleStates[value]?.includes(pr.status) ?? false
    if (name === 'state') return value.split(',').includes(pr.status)
    if (name === 'draft') return (pr.status === 'draft' || pr.sync_data.github?.draft === true) === (value === 'true')
    if (name === 'sort' || name === 'in') return true
    const member = previewMembers.find((candidate) => value === '@me' ? candidate.is_me : candidate.login.toLowerCase() === value.toLowerCase())
    if (!member) return false
    if (name === 'author') return authorId(pr) === member.id
    if (name === 'assignee') return pr.assignee_holark_ids.includes(member.id)
    if (name === 'user-review-requested') return pr.requested_reviewer_holark_ids.includes(member.id)
    return false
  }))
  const date = (pr: PullRequest) => sort.startsWith('updated') ? pr.updated_at : pr.created_at
  matches.sort((a, b) => (date(a).localeCompare(date(b)) || a.id.localeCompare(b.id)) * (sort.endsWith('asc') ? 1 : -1))
  return {
    rows: matches.slice((page - 1) * perPage, page * perPage).map((pr) => ({
      id: pr.id, kind: 'pull_request', title: pr.title, number: pr.sync_data.github?.number ?? 0,
      status: pr.status, author_id: authorId(pr), assignee_ids: pr.assignee_holark_ids,
      reviewer_ids: pr.requested_reviewer_holark_ids, updated_at: pr.updated_at, reasons: [],
    })),
    total: matches.length, page, per_page: perPage, query: tokens.join(' '), identity: previewMembers[0], sync: [],
  }
}

// Mirror the production title matcher for the interactive preview.
export function matchesTitle(title: string, search: string): boolean {
  const normalized = title.toLowerCase()
  const words = normalized.match(/[\p{L}\p{N}]+/gu) ?? []
  return search.toLowerCase().split(/\s+/u).filter(Boolean).every((term) => {
    if (normalized.includes(term)) return true
    const letters = Array.from(term)
    if (letters.length < 4 || !/^[\p{L}\p{N}]+$/u.test(term)) return false
    return words.some((word) => oneEditApart(letters, Array.from(word)))
  })
}

function oneEditApart(first: string[], second: string[]): boolean {
  const [a, b] = first.length <= second.length ? [first, second] : [second, first]
  if (b.length - a.length > 1) return false
  let i = 0
  while (i < a.length && a[i] === b[i]) i++
  if (i === a.length) return true
  if (a.length !== b.length) return a.slice(i).join('') === b.slice(i + 1).join('')
  if (a.slice(i + 1).join('') === b.slice(i + 1).join('')) return true
  return i + 1 < a.length && a[i] === b[i + 1] && a[i + 1] === b[i]
    && a.slice(i + 2).join('') === b.slice(i + 2).join('')
}
