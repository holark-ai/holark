import type { Issue, WorkItemsResult } from '../data/types'
import { parseLabels, searchTokens, type LabelNode } from '../features/workItems/searchQuery'
import { matchesTitle, previewMembers } from './pullRequestListPreview'

const topics = [
  'repository navigation', 'terminal recovery', 'keyboard shortcuts', 'issue labels',
  'workspace settings', 'branch switching', 'file previews', 'commit history',
  'member search', 'GitHub synchronization', 'diff rendering', 'agent permissions',
  'notifications', 'worktree cleanup', 'pull request linking', 'dark theme contrast',
]
const titles = [
  (topic: string) => `Fix ${topic} after reconnecting`,
  (topic: string) => `Improve ${topic} for large repositories`,
  (topic: string) => `Document ${topic}`,
  (topic: string) => `Make ${topic} accessible with a keyboard`,
  (topic: string) => `Preserve ${topic} preferences when switching between repositories and returning to a previous workspace`,
]
const labels = [
  { id: 'label-bug', name: 'bug', color: 'd73a4a', description: 'Something is broken' },
  { id: 'label-enhancement', name: 'enhancement', color: 'a2eeef', description: 'Improve an existing workflow' },
  { id: 'label-documentation', name: 'documentation', color: '0075ca', description: 'Documentation improvements' },
  { id: 'label-accessibility', name: 'accessibility', color: '7057ff', description: 'Make the interface easier to use' },
]

export const additionalPreviewIssues: Issue[] = topics.flatMap((topic, topicIndex) => titles.map((title, titleIndex) => {
  const index = topicIndex * titles.length + titleIndex
  const number = 100 + index
  const local = index % 10 === 0
  const status = (topicIndex + titleIndex) % 4 === 0 ? 'closed' : 'open'
  const created = Date.UTC(2026, 8, 1) + index * 6 * 60 * 60 * 1000
  const updated = created + (index * 13 % 72) * 60 * 60 * 1000
  return {
    id: `issue-preview-${number}`, title: title(topic),
    body: `Investigate ${topic} and make the behavior consistent across repositories.\n\n### Acceptance criteria\n- Preserve the current selection.\n- Provide clear feedback when the operation completes.`,
    status, sync_provider: local ? undefined : 'github',
    sync_data: local ? {} : { github: { number, url: `https://github.com/example/acme-dashboard/issues/${number}` } },
    labels: index % 5 === 0 ? [] : index % 5 === 4 ? labels : [labels[index % labels.length]],
    issuer_holark_id: previewMembers[index % previewMembers.length].id,
    assignee_holark_ids: index % 4 === 0 ? [] : index % 4 === 1
      ? ['member-riley', 'member-jonas'] : [previewMembers[(index + 1) % previewMembers.length].id],
    linked_pull_request_ids: index % 3 === 0 ? ['pr-preview'] : [],
    created_at: new Date(created).toISOString(), updated_at: new Date(updated).toISOString(),
    closed_at: status === 'closed' ? new Date(updated).toISOString() : undefined,
  }
}))

export function previewIssueSearch(issues: Issue[], params: URLSearchParams): WorkItemsResult {
  const query = params.get('q')?.trim() || 'is:open sort:created-desc'
  const tokens = searchTokens(query)
  if (!tokens.some(token => /^(is|state):/.test(token))) tokens.push('is:open')
  const labelExpressions = new Map(tokens.filter(token => token.startsWith('label:')).map(token => [token, parseLabels(token.slice(6))]))
  const matchesLabels = (node: LabelNode, names: string[]): boolean => 'name' in node
    ? names.includes(node.name.toLowerCase())
    : node.operator === 'AND' ? node.children.every(child => matchesLabels(child, names)) : node.children.some(child => matchesLabels(child, names))
  const sort = tokens.find(token => token.startsWith('sort:'))?.slice(5) || 'created-desc'
  const page = Math.max(1, Number(params.get('page')) || 1)
  const perPage = 20
  const matches = issues.filter(issue => matchesTitle(issue.title, params.get('title') ?? '') && tokens.every(token => {
    if (labelExpressions.has(token)) return matchesLabels(labelExpressions.get(token)!, issue.labels.map(label => label.name.toLowerCase()))
    if (token.startsWith('"')) return issue.title.toLowerCase().includes(JSON.parse(token).toLowerCase())
    if (/^#?\d+$/.test(token)) return issue.sync_data.github?.number === Number(token.replace('#', ''))
    if (!token.includes(':')) return issue.title.toLowerCase().includes(token.toLowerCase())
    const [key, value] = token.split(':')
    if (key === 'is') {
      if (!['open', 'closed', 'all'].includes(value)) throw new Error('Issues support is:open, is:closed, or is:all')
      return value === 'all' || issue.status === value
    }
    if (key === 'state') return value.split(',').includes(issue.status)
    if (key === 'sort' || key === 'in' && value === 'title') return true
    if (key === 'author' || key === 'assignee') {
      const member = previewMembers.find(member => value === '@me' ? member.is_me : member.login.toLowerCase() === value.toLowerCase())
      return Boolean(member && (key === 'author' ? issue.issuer_holark_id === member.id : issue.assignee_holark_ids.includes(member.id)))
    }
    throw new Error(`Unsupported preview issue filter: ${token}`)
  }))
  const date = (issue: Issue) => sort.startsWith('updated') ? issue.updated_at : issue.created_at
  matches.sort((a, b) => date(a).localeCompare(date(b)) * (sort.endsWith('asc') ? 1 : -1) || a.id.localeCompare(b.id))
  return {
    rows: matches.slice((page - 1) * perPage, page * perPage).map(issue => ({
      id: issue.id, kind: 'issue', title: issue.title, number: issue.sync_data.github?.number ?? 0,
      status: issue.status, author_id: issue.issuer_holark_id, assignee_ids: issue.assignee_holark_ids,
      reviewer_ids: [], updated_at: issue.updated_at, reasons: [],
      labels: issue.labels, linked_pull_request_ids: issue.linked_pull_request_ids, url: issue.sync_data.github?.url ?? '',
    })),
    total: matches.length, page, per_page: perPage, query, identity: previewMembers[0], sync: [],
  }
}
