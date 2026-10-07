import { createPatch } from 'diff'
import { setApiRequestTransport } from '../data/api'
import type { CreatePullRequestCommentRequest, Issue, ManualPullRequestReview, ManualReviewSubmission, PullRequest, PullRequestComment } from '../data/types'
import { initialPreviewPullRequests, previewCapabilities, previewComments, previewCommits, previewHolons, previewInspection, previewInspectionForRange, previewRepository, previewReviews } from './devPreviewData'
import { previewRepositoryCommits, previewRepositoryRefs, repositoryPreviewEntries, repositoryPreviewFiles } from './repositoryPreviewData'
import { applyPreviewCommitChanges } from './repositoryPreviewChanges'
import { commitHistoryPreview, resetCommitHistoryPreview } from './commitHistoryPreviewState'
import { previewMembers, previewPullRequestSearch } from './pullRequestListPreview'
import { previewMyWork } from './myWorkPreview'
import { additionalPreviewIssues, previewIssueSearch } from './issueListPreview'

const previewIssue: Issue = {
  id: 'issue-preview', title: 'Keep repository navigation consistent', body: 'Use shared navigation and preserve the selected branch when browsing files.', status: 'open',
  sync_data: { github: { number: 37, url: 'https://github.com/example/acme-dashboard/issues/37' } }, labels: [], linked_pull_request_ids: [], issuer_holark_id: '', assignee_holark_ids: ['member-riley'], created_at: '2026-10-01T10:00:00Z', updated_at: '2026-10-01T10:00:00Z',
}

const previewIssues = [previewIssue, ...additionalPreviewIssues]

let holons = previewHolons.map((holon) => ({ ...holon }))
let pullRequests = initialPreviewPullRequests.map((pullRequest) => ({ ...pullRequest }))
let comments = previewComments.map((comment) => ({ ...comment }))
let manualReviews: ManualPullRequestReview[] = []
let uninstall: (() => void) | undefined

export function installDevPreviewApi() {
  if (uninstall) return uninstall
  resetCommitHistoryPreview()
  holons = previewHolons.map((holon) => ({ ...holon }))
  pullRequests = initialPreviewPullRequests.map((pullRequest) => ({ ...pullRequest }))
  comments = previewComments.map((comment) => ({ ...comment }))
  manualReviews = []
  const restore = setApiRequestTransport(handleDevPreviewRequest)
  uninstall = () => {
    restore()
    uninstall = undefined
  }
  return uninstall
}

async function handleDevPreviewRequest(path: string, init?: RequestInit): Promise<Response> {
  const url = new URL(path, window.location.origin)
  const method = init?.method?.toUpperCase() ?? 'GET'
  const pathname = url.pathname

  if (method === 'GET' && pathname === '/api/v1/repository') return json(previewRepository)
  if ((method === 'GET' && pathname === '/api/v1/repository/refs')
    || (method === 'POST' && pathname === '/api/v1/repository/refs/refresh')) return json({ ...previewRepositoryRefs, refs: previewRepositoryRefs.refs.map(ref => {
      if (!commitHistoryPreview.newCommit || ref.short_name !== 'main') return ref
      const commit = previewRepositoryCommits[0]
      return { ...ref, target: commit.sha, committed_at: commit.authored_at, author_name: commit.author_name, subject: commit.message.split('\n')[0] }
    }) })
  if (method === 'GET' && ['/api/v1/repository/search', '/api/v1/repository/tree', '/api/v1/repository/blob', '/api/v1/repository/commits', '/api/v1/repository/commit-changes'].includes(pathname)) {
    const requestedRef = url.searchParams.get('ref') || previewRepositoryRefs.default_ref
    const snapshot = previewRepositoryCommits.find((commit) => commit.sha === requestedRef)
    const originalRef = previewRepositoryRefs.refs.find((item) => item.name === requestedRef || item.short_name === requestedRef || item.target === requestedRef)
      ?? (snapshot ? previewRepositoryRefs.refs.find((item) => item.name === previewRepositoryRefs.default_ref) : undefined)
    const ref = originalRef && { ...originalRef, target: commitHistoryPreview.newCommit && originalRef.short_name === 'main' ? previewRepositoryCommits[0].sha : originalRef.target }
    if (!ref) return error(404, 'Reference not found.', 'ref_not_found')
    if (pathname.endsWith('/commits')) {
      const cursor = url.searchParams.get('cursor')
      const limit = Math.min(200, Math.max(1, Number(url.searchParams.get('limit')) || 50))
      let head = snapshot?.sha ?? ref.target
      let offset = 0
      if (cursor) {
        try {
          const position = JSON.parse(atob(cursor.replace(/-/g, '+').replace(/_/g, '/')))
          head = position.head
          offset = position.offset
          if (!Number.isInteger(offset) || offset < 1) throw new Error('Invalid offset')
        } catch { return error(400, 'Invalid commit history cursor.', 'invalid_cursor') }
      }
      const start = previewRepositoryCommits.findIndex(commit => commit.sha === head)
      if (start < 0) return error(400, 'Invalid commit history cursor.', 'invalid_cursor')
      if (limit > 1) await new Promise(resolve => setTimeout(resolve, commitHistoryPreview.delayMs))
      if (init?.signal?.aborted) throw new DOMException('Aborted', 'AbortError')
      if (cursor && commitHistoryPreview.failNextPage) {
        commitHistoryPreview.failNextPage = false
        return error(503, 'Simulated page failure. Retry to continue.', 'repository_unavailable')
      }
      const commits = previewRepositoryCommits.slice(start + offset, start + offset + limit)
      const next_cursor = start + offset + limit < previewRepositoryCommits.length
        ? btoa(JSON.stringify({ head, offset: offset + limit })).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '') : undefined
      return json({ head, commits, next_cursor })
    }
    if (pathname.endsWith('/commit-changes')) {
      const commit = snapshot?.sha ?? ref.target
      const index = previewRepositoryCommits.findIndex((item) => item.sha === commit)
      const parent = previewRepositoryCommits[index + 1]?.sha ?? ''
      const after = previewSnapshotFiles(ref.name, commit)
      const before = parent ? previewSnapshotFiles(ref.name, parent) : {}
      const paths = [...new Set([...Object.keys(before), ...Object.keys(after)])].sort()
      const files = paths.filter(path => before[path]?.content !== after[path]?.content).map(path => ({
        path,
        status: !before[path] ? 'A' : !after[path] ? 'D' : 'M',
        patch: createPatch(path, before[path]?.content ?? '', after[path]?.content ?? ''),
        binary: false,
        truncated: false,
      }))
      return json({ commit, parent, files })
    }
    const path = (url.searchParams.get('path') ?? '').replace(/^\/+|\/+$/g, '')
    const files = previewSnapshotFiles(ref.name, snapshot?.sha ?? ref.target)
    if (pathname.endsWith('/search')) {
      const query = (url.searchParams.get('q') ?? '').trim().toLowerCase()
      const matches = query ? Object.entries(files).sort(([a], [b]) => a.localeCompare(b)).map(([path, file]) => ({ path, name_match: path.toLowerCase().includes(query), content_match: file.content.toLowerCase().includes(query) })).filter((match) => match.name_match || match.content_match) : []
      return json({ ref: ref.name, matches: matches.slice(0, 200), truncated: matches.length > 200 })
    }
    const file = Object.hasOwn(files, path) ? files[path] : undefined
    const entries = repositoryPreviewEntries(files, path)
    if (!file && path && entries.length === 0) return error(404, 'Path not found.', 'path_not_found')
    if (pathname.endsWith('/tree')) {
      if (file) return error(409, 'This path is not a directory.', 'path_not_directory')
      return json({ ref: ref.name, commit: snapshot?.sha ?? ref.target, path, entries })
    }
    if (!file) return error(409, 'This path is not a file.', 'path_not_file')
    const size = new TextEncoder().encode(file.content).length
    if (size > 2 * 1024 * 1024) return error(413, 'File is too large.', 'file_too_large')
    if (file.content.includes('\0')) return error(415, 'Binary file.', 'binary_file')
    return json({ ref: ref.name, commit: snapshot?.sha ?? ref.target, path, ...file, encoding: 'utf-8', size, truncated: false })
  }
  if (method === 'GET' && pathname === '/api/v1/agent-capabilities') {
    return json({ capabilities: previewCapabilities, default_harness: 'codex', default_harness_explicit: false })
  }
  if (method === 'GET' && pathname === '/api/v1/issues/search/labels') {
    return json([...new Map(previewIssues.flatMap(issue => issue.labels).map(label => [label.id, label])).values()])
  }
  if (method === 'GET' && pathname === '/api/v1/issues/search') {
    try { return json(previewIssueSearch(previewIssues, url.searchParams)) }
    catch { return error(400, 'Invalid preview issue search query.', 'invalid_query') }
  }
  if (method === 'POST' && pathname === '/api/v1/issues/sync') return json({ issues: previewIssues, imported: 0, updated: previewIssues.length, exported: 0, synced_at: new Date().toISOString() })
  if (method === 'GET' && pathname === '/api/v1/issues') return json(previewIssues)
  const issueMatch = pathname.match(/^\/api\/v1\/issues\/([^/]+)(\/comments(?:\/sync)?)?$/)
  if (issueMatch && (method === 'GET' || method === 'POST' && issueMatch[2] === '/comments/sync')) {
    const issue = previewIssues.find(issue => issue.id === decodeURIComponent(issueMatch[1]))
    if (!issue) return error(404, 'Preview issue not found.', 'issue_not_found')
    return json(issueMatch[2] ? { comments: [], synced_at: null, can_comment: false } : issue)
  }
  if (method === 'GET' && pathname === '/api/v1/holons') return json({ holons })
  if (method === 'POST' && pathname === '/api/v1/holons') {
    const body = requestBody<{ prompt?: string; title?: string; base_branch?: string; issue_id?: string }>(init)
    const holon = { ...previewHolons[0], id: `preview-${crypto.randomUUID()}`, title: body.title || body.prompt || 'New Holon', prompt: body.prompt || '', kind: body.issue_id ? 'issue' as const : 'normal' as const, issue_id: body.issue_id, status: 'running' as const, activity: 'idle' as const, input_state: 'none' as const, agent_session: undefined, worktree_branch: body.base_branch || 'main', created_at: new Date().toISOString() }
    holons = [...holons, holon]
    return json(holon)
  }
  const baseBranchMatch = pathname.match(/^\/api\/v1\/holons\/([^/]+)\/base-branch$/)
  if (method === 'PUT' && baseBranchMatch) {
    const id = decodeURIComponent(baseBranchMatch[1])
    const holon = holons.find((candidate) => candidate.id === id)
    if (!holon) return error(404, 'Holon not found.')
    const { base_branch } = requestBody<{ base_branch: string }>(init)
    if (!previewRepositoryRefs.refs.some((branch) => branch.short_name === base_branch)) return error(400, 'Branch not found.')
    const inspection = previewInspectionForRange('main', 'worktree', base_branch)
    const updated = { ...holon, base_branch, base_commit: inspection.branch_base_commit }
    holons = holons.map((candidate) => candidate.id === id ? updated : candidate)
    return json(updated)
  }

  const workspaceMatch = pathname.match(/^\/api\/v1\/holons\/([^/]+)\/workspace$/)
  if (method === 'GET' && workspaceMatch) {
    const holon = holons.find((candidate) => candidate.id === decodeURIComponent(workspaceMatch[1]))
    const inspection = previewInspectionForRange(url.searchParams.get('base') || 'main', url.searchParams.get('target') || 'worktree', holon?.base_branch || 'main')
    const path = url.searchParams.get('path')
    return json({ ...inspection, files: path ? inspection.files.filter((file) => file.path === path) : inspection.files })
  }

  const holonMatch = pathname.match(/^\/api\/v1\/holons\/([^/]+)$/)
  if (method === 'GET' && holonMatch) return json(holons.find((holon) => holon.id === decodeURIComponent(holonMatch[1])))
  if (method === 'PATCH' && holonMatch) {
    const id = decodeURIComponent(holonMatch[1])
    const holon = holons.find((candidate) => candidate.id === id)
    if (!holon) return error(404, `Preview Holon ${id} was not found.`)
    const { title } = requestBody<{ title: string }>(init)
    const updated = { ...holon, title }
    holons = holons.map((candidate) => candidate.id === id ? updated : candidate)
    return json(updated)
  }
  if (method === 'GET' && pathname === '/api/v1/pull-requests') return json(pullRequests)
  if (method === 'GET' && pathname === '/api/v1/pull-requests/search') {
    try {
      return json(previewPullRequestSearch(pullRequests, url.searchParams))
    } catch {
      return error(400, 'Invalid preview search query.', 'invalid_query')
    }
  }
  if (method === 'GET' && pathname === '/api/v1/github-members') return json(previewMembers)
  if (method === 'GET' && pathname === '/api/v1/my-work') return json(previewMyWork(pullRequests, [previewIssue], url.searchParams))
  if (method === 'POST' && pathname === '/api/v1/my-work/sync') return json({ synced: true })
  if (method === 'POST' && pathname === '/api/v1/pull-requests/sync') {
    return json({ pull_requests: pullRequests, imported: 0, updated: 0, synced_at: new Date().toISOString() })
  }
  if (method === 'GET' && pathname === '/api/v1/pull-request-holon-links') {
    return json(pullRequests.flatMap((pullRequest) => pullRequest.linked_session_ids.map((sessionId) => ({ pull_request_id: pullRequest.id, session_id: sessionId }))))
  }
  if (method === 'POST' && pathname === '/api/v1/github-members/resolve') {
    const ids = requestBody<{ ids?: string[] }>(init).ids ?? []
    const members = previewMembers.filter((member) => ids.includes(member.id))
    return json({ members, missing_ids: ids.filter((id) => !members.some((member) => member.id === id)) })
  }

  const match = pathname.match(/^\/api\/v1\/pull-requests\/([^/]+)(?:\/(.+))?$/)
  if (match) {
    const id = decodeURIComponent(match[1])
    const endpoint = match[2] ?? ''
    const pullRequest = pullRequests.find((candidate) => candidate.id === id)
    if (!pullRequest) return error(404, `Preview pull request ${id} was not found.`)

    if (!endpoint && method === 'GET') return json(pullRequest)
    if (endpoint === 'panel-pin' && (method === 'PUT' || method === 'DELETE')) {
      updatePullRequest(id, { panel_pinned: method === 'PUT' })
      return new Response(null, { status: 204 })
    }
    if ((endpoint === 'comments' && method === 'GET') || (endpoint === 'comments/sync' && method === 'POST')) {
      return json(comments.filter((comment) => comment.pull_request_id === id))
    }
    if (endpoint === 'comments' && method === 'POST') {
      const input = requestBody<CreatePullRequestCommentRequest>(init)
      const parent = comments.find((comment) => comment.id === input.parent_comment_id)
      const now = new Date().toISOString()
      const comment: PullRequestComment = { ...parent, ...input, id: `draft-${crypto.randomUUID()}`, pull_request_id: id, author_type: 'user', author_github_user_id: previewMembers.find((member) => member.is_me)?.id,
        scope: parent?.scope ?? input.scope ?? 'pull_request', status: parent ? 'resolved' : 'unresolved', publication_state: 'draft', original_head_commit: pullRequest.head_commit, created_at: now, updated_at: now }
      comments.push(comment)
      return json(comment)
    }
    if (endpoint === 'comments/publish' && method === 'POST') {
      const input = requestBody<{ comment_ids: string[] }>(init)
      comments = comments.map((comment) => input.comment_ids.includes(comment.id) ? { ...comment, publication_state: 'published' } : comment)
      return json(comments.filter((comment) => comment.pull_request_id === id))
    }
    if (endpoint === 'manual-reviews' && method === 'GET') return json(manualReviews.filter((review) => review.pull_request_id === id))
    if (endpoint === 'manual-reviews' && method === 'POST') {
      const input = requestBody<ManualReviewSubmission>(init)
      const review: ManualPullRequestReview = { ...input, pull_request_id: id, state: 'local', created_at: new Date().toISOString() }
      manualReviews.push(review)
      return json(review)
    }
    if (endpoint === 'comments/refresh' && method === 'POST') return json({ refreshed: false, synced_at: null, sync_applicable: false })
    if (endpoint === 'reviews' && method === 'GET') return json(id === 'pr-preview' ? previewReviews : [])
    const inputs = { head_commit: pullRequest.head_commit, diff_base_commit: pullRequest.diff_base_commit || pullRequest.base_commit, comparison_state: pullRequest.comparison_state }
    if (endpoint === 'commits' && method === 'GET') return json({ inputs, data: id === 'pr-preview' ? previewCommits : [] })
    if (endpoint === 'changes' && method === 'GET') return json({ inputs, data: { ...previewInspection, branch: pullRequest.head_branch, head_commit: pullRequest.head_commit } })
    if (endpoint === 'workers' && method === 'POST') {
      const body = requestBody<{ prompt?: string; title?: string; mode?: string }>(init)
      const holon = { ...previewHolons[0], id: `preview-${crypto.randomUUID()}`, title: body.title || 'PR follow-up', prompt: body.prompt || '', kind: 'pr_worker' as const, worktree_branch: pullRequest.head_branch, created_at: new Date().toISOString() }
      holons = [...holons, holon]
      updatePullRequest(id, { linked_session_ids: [...pullRequest.linked_session_ids, holon.id] })
      return json([{ id: `worker-${holon.id}`, pull_request_id: id, session_id: holon.id, mode: body.mode || 'continue', status: 'running' }])
    }
    if ((endpoint === 'queue'  || endpoint === 'workers' || endpoint === 'rebases') && method === 'GET') return json([])
    if (endpoint === 'rebase-readiness' && method === 'GET') {
      return json({ base_commit: pullRequest.base_commit, head_commit: pullRequest.head_commit, base_commits_ahead: 0, branch_freshness: 'up_to_date', rebase_conflict_state: 'not_applicable' })
    }
    if (endpoint === 'metadata' && method === 'GET') {
      return json({ pull_request_id: id, title: pullRequest.title, description: pullRequest.summary, updated_at: pullRequest.updated_at, generation_complete: true })
    }
  }

  const commentMatch = pathname.match(/^\/api\/v1\/pull-request-comments\/([^/]+)$/)
  if (commentMatch) {
    const id = decodeURIComponent(commentMatch[1])
    if (method === 'PATCH') {
      const input = requestBody<{ body: string }>(init)
      comments = comments.map((comment) => comment.id === id ? { ...comment, body: input.body } : comment)
      return json(comments.find((comment) => comment.id === id))
    }
    if (method === 'DELETE') {
      comments = comments.filter((comment) => comment.id !== id && comment.parent_comment_id !== id)
      return json({ deleted: true })
    }
  }

  return error(501, `The dev preview does not support ${method} ${pathname}.`)
}

function updatePullRequest(id: string, update: Partial<PullRequest>) {
  pullRequests = pullRequests.map((pullRequest) => pullRequest.id === id ? { ...pullRequest, ...update } : pullRequest)
}

function requestBody<T>(init?: RequestInit): T {
  if (typeof init?.body !== 'string') return {} as T
  try {
    return JSON.parse(init.body) as T
  } catch {
    return {} as T
  }
}

function json(value: unknown) {
  return new Response(JSON.stringify(value), { status: 200, headers: { 'Content-Type': 'application/json' } })
}

function error(status: number, message: string, code = 'dev_preview_unsupported') {
  return new Response(JSON.stringify({ code, message }), { status, headers: { 'Content-Type': 'application/json' } })
}

// Keep complete file contents and patch previews consistent at each sample revision.
function previewSnapshotFiles(refName: string, commit: string) {
  const files = repositoryPreviewFiles(refName)
  const index = previewRepositoryCommits.findIndex((item) => item.sha === commit)
  applyPreviewCommitChanges(files, index < 0 ? 0 : index)
  if (index > 0) files['README.md'] = { ...files['README.md'], content: files['README.md'].content + `\nRepository snapshot ${index}.\n` }
  return files
}
