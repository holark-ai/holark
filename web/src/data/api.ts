import { sendApiRequest } from './requestTransport'
import { acceptPullRequest, beginPullRequestMutation, pullRequestObservation, resetPullRequestAcceptance } from './pullRequestAcceptance'
import type { ApiProject, ApiRepository, CreatePullRequestCommentRequest, HolonPublicationReadiness, CommitPromptAction, CurrentUser, GitHubMember, AgentSession, HarnessCapabilities, HarnessCapability, HarnessDefault, HarnessType, HarnessWorkflow, Issue, IssueSnapshot, IssueSyncResponse, ManualTerminal, PullRequest, PullRequestComment, PullRequestCommit, PullRequestDetail, PullRequestMergeResponse, PullRequestMetadata, PullRequestParticipantSnapshot, PullRequestPublishResponse, PullRequestQueueItem, PullRequestOperation, PullRequestRebaseReadiness, PullRequestRebase, PullRequestReview, PullRequestReviewMode, PullRequestSessionLink, PromptTemplate, PullRequestStatus, PullRequestSyncResponse, PullRequestTransitionStatus, PullRequestWorker, PullRequestWorkerMode, RepositorySearchResponse, RepositoryBlobResponse, RepositoryCommitChanges, RepositoryCommitsResponse, RepositoryRefsResponse, RepositoryTreeResponse, Holon, HolonIDE, HolonPullRequestResponse, HolonTabRef, WorkspaceInspection } from './types'
import type { GitHubProfile, IssueComment, IssueDiscussion, IssueLabel } from './types'
import type { ManualPullRequestReview, ManualReviewSubmission } from './types'
import type { ProjectMemberResolution, ProjectMemberSearchResponse } from './types'

export { setApiRequestTransport, type ApiRequestTransport } from './requestTransport'

export type ApiError = Error & { code?: string, status?: number, request_id?: string, github_comment_url?: string, github_comment_id?: string }

export const unauthorizedEventName = 'holark:unauthenticated'

const pendingActionStorageKey = 'holark:pending-pull-request-actions'
const pendingActionIDs = new Map<string, string>()

export function clearPullRequestSession() {
  resetPullRequestAcceptance()
  pendingActionIDs.clear()
  try { sessionStorage.removeItem(pendingActionStorageKey) } catch { /* Storage can be disabled. */ }
}

function pendingActionID(key: string) {
  if (!pendingActionIDs.has(key)) {
    try {
      const saved = JSON.parse(sessionStorage.getItem(pendingActionStorageKey) || '{}') as Record<string, string>
      for (const [action, id] of Object.entries(saved)) if (typeof id === 'string') pendingActionIDs.set(action, id)
    } catch { /* In-memory recovery still works when storage is unavailable. */ }
  }
  const id = pendingActionIDs.get(key) || crypto.randomUUID()
  pendingActionIDs.set(key, id)
  savePendingActions()
  return id
}

function savePendingActions() {
  try { sessionStorage.setItem(pendingActionStorageKey, JSON.stringify(Object.fromEntries(pendingActionIDs))) } catch { /* Storage can be disabled. */ }
}

function finishPendingAction(key: string) {
  pendingActionIDs.delete(key)
  savePendingActions()
}

async function request<T>(path: string, init?: RequestInit, timeoutMs = !init?.method || init.method === 'GET' ? 65_000 : 0): Promise<T> {
  const requestID = new Headers(init?.headers).get('X-Request-ID') || crypto.randomUUID()
  const controller = new AbortController()
  const abort = () => controller.abort(init?.signal?.reason)
  if (init?.signal?.aborted) abort()
  else init?.signal?.addEventListener('abort', abort, { once: true })
  const timer = timeoutMs ? window.setTimeout(() => controller.abort(new DOMException('The request timed out. Try again.', 'TimeoutError')), timeoutMs) : undefined
  try {
    const headers = Object.fromEntries(new Headers(init?.headers).entries())
    delete headers['x-request-id']
    headers['X-Request-ID'] = requestID
    const response = await sendApiRequest(path, { ...init, headers, signal: controller.signal })
    if (!response.ok) {
      const body = await response.json().catch(() => ({})) as { code?: string; message?: string; github_comment_url?: string; github_comment_id?: string }
      const error = new Error(body.message || `Request failed (${response.status})`) as ApiError
      error.code = body.code
      error.github_comment_url = body.github_comment_url
      error.github_comment_id = body.github_comment_id
      error.status = response.status
      if (response.status === 401) {
        clearPullRequestSession()
        window.dispatchEvent(new CustomEvent(unauthorizedEventName))
      }
      throw error
    }
    if (response.status === 204) {
      return undefined as T
    }
    const body = await response.json() as T
    if (response.status === 202 && isProjectionPending(body)) {
      const error = new Error(body.message || 'GitHub accepted the change, but Holark has not stored it yet.') as ApiError
      error.code = body.code
      error.github_comment_url = body.github_comment_url
      error.github_comment_id = body.github_comment_id
      error.status = response.status
      throw error
    }
    return body
  } catch (error) {
    if (controller.signal.aborted) throw controller.signal.reason
    throw error
  } finally {
    window.clearTimeout(timer)
    init?.signal?.removeEventListener('abort', abort)
  }
}

type PullRequestAcceptance<T> = (value: T, observation: number, mutation?: boolean) => T

function acceptLinkedPullRequest<T extends { pull_request: PullRequest }>(value: T, observation: number, mutation = false): T {
  return { ...value, pull_request: acceptPullRequest(value.pull_request, observation, mutation) }
}

async function readPullRequest<T>(path: string, accept: PullRequestAcceptance<T>, init?: RequestInit): Promise<T> {
  const observation = pullRequestObservation()
  return accept(await request<T>(path, init), observation)
}

async function pullRequestAction<T>(path: string, init: RequestInit, accept: PullRequestAcceptance<T>, pullRequestID?: string, recovery: 'operation' | 'work' = 'operation'): Promise<T> {
  const observation = pullRequestObservation()
  const finish = pullRequestID ? beginPullRequestMutation(pullRequestID) : undefined
  const key = `${path}:${String(init.body || '')}`
  const requestID = pendingActionID(key)
  init = { ...init, headers: { ...init.headers, 'X-Request-ID': requestID } }
  const perform = async () => {
    const value = accept(await request<T>(path, init), observation, true)
    finishPendingAction(key)
    return value
  }
  try {
    return await perform()
  } catch (value) {
    const error = value as ApiError
    error.request_id = requestID
    if (error.status && error.status < 500 && error.code !== 'pull_request_action_pending') {
      finishPendingAction(key)
      throw error
    }
    // Replaying a completed request returns its actual typed result without
    // repeating effects. Uncertain retries retain the same ID across reloads.
    try {
      if (recovery === 'work') return await perform()
      const operation = await api.pullRequestOperation(requestID)
      if (operation.status === 'failed') finishPendingAction(key)
      if (operation.status === 'succeeded') return await perform()
    } catch { /* Retain the ID until the result can be reconciled. */ }
    throw error
  } finally {
    finish?.()
  }
}

function isProjectionPending(value: unknown): value is { code: 'issue_projection_pending' | 'label_projection_pending' | 'issue_comment_projection_pending'; message?: string; github_comment_url?: string; github_comment_id?: string } {
  return typeof value === 'object' && value !== null && 'code' in value
    && (value.code === 'issue_projection_pending' || value.code === 'label_projection_pending' || value.code === 'issue_comment_projection_pending')
}

type CreateHolonOptions = {
  issueId?: string
  startupMode?: 'agent' | 'terminal'
  title?: string
  baseBranch?: string
  repositoryPreparation?: RepositoryPreparation
  harnessType?: HarnessType
}

export type RepositoryPreparation = {
  id: string
  commit: string
  ref: string
}

type HolonChangesOptions = {
  contents?: boolean
  signal?: AbortSignal
  base?: string
  target?: string
  summary?: boolean
  path?: string
}

export const api = {
  uploadGitHubImage: (file: File, signal?: AbortSignal) => request<{ url: string }>(`/api/v1/attachments?${new URLSearchParams({ name: file.name })}`, { method: 'POST', headers: { 'Content-Type': 'application/octet-stream' }, body: file, signal }, 65_000),
  resolveGitHubImage: (url: string, signal?: AbortSignal) => request<{ url: string }>('/api/v1/attachments/resolve', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ url }), signal }, 20_000),
  searchIssues: (q: string, page: number, signal?: AbortSignal, title = '') => request<import('./types').WorkItemsResult>(`/api/v1/issues/search?${new URLSearchParams({ q, title, page: String(page) })}`, { signal }),
  searchIssueLabels: () => request<IssueLabel[]>('/api/v1/issues/search/labels'),
  searchPullRequests: (q: string, page: number, signal?: AbortSignal, title = '') => request<import('./types').WorkItemsResult>(`/api/v1/pull-requests/search?${new URLSearchParams({ q, title, page: String(page) })}`, { signal }),
  myWork: (view: string, page: number, signal?: AbortSignal) => request<import('./types').WorkItemsResult>(`/api/v1/my-work?${new URLSearchParams({ view, page: String(page) })}`, { signal }),
  syncMyWork: () => request<{ synced: boolean }>('/api/v1/my-work/sync', { method: 'POST' }),
  issueComments: (id: string) => request<IssueDiscussion>(`/api/v1/issues/${encodeURIComponent(id)}/comments`),
  syncIssueComments: (id: string) => request<IssueDiscussion>(`/api/v1/issues/${encodeURIComponent(id)}/comments/sync`, { method: 'POST' }),
  createIssueComment: (id: string, body: string) => request<IssueComment>(`/api/v1/issues/${encodeURIComponent(id)}/comments`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ body }) }),
  updateIssueComment: (id: string, body: string) => request<IssueComment>(`/api/v1/issue-comments/${encodeURIComponent(id)}`, { method: 'PATCH', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ body }) }),
  deleteIssueComment: (id: string) => request<void>(`/api/v1/issue-comments/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  me: () => request<CurrentUser>('/api/v1/me'),
  githubProfile: (signal?: AbortSignal) => request<GitHubProfile>('/api/v1/github-profile', { signal }),
  repository: () => request<ApiRepository>('/api/v1/repository'),
  // Compatibility adapters keep unmigrated issue/PR pages mounted until their phases.
  projects: async () => [asProject(await request<ApiRepository>('/api/v1/repository'))],
  project: async (id: string) => { void id; return asProject(await request<ApiRepository>('/api/v1/repository')) },
  repositoryTree: (_repositoryId: string, path: string, ref?: string, signal?: AbortSignal) => request<RepositoryTreeResponse>(repositoryPath('tree', path, ref), { signal }),
  repositoryBlob: (_repositoryId: string, path: string, ref?: string, signal?: AbortSignal) => request<RepositoryBlobResponse>(repositoryPath('blob', path, ref), { signal }),
  repositoryRefs: (repositoryId?: string, signal?: AbortSignal) => { void repositoryId; return request<RepositoryRefsResponse>('/api/v1/repository/refs', { signal }) },
  refreshRepositoryRefs: (repositoryId?: string, signal?: AbortSignal) => { void repositoryId; return request<RepositoryRefsResponse>('/api/v1/repository/refs/refresh', { method: 'POST', signal }, 65_000) },
  repositorySearch: (_repositoryId: string, ref: string, query: string, signal?: AbortSignal) => request<RepositorySearchResponse>(`/api/v1/repository/search?${new URLSearchParams({ ref, q: query })}`, { signal }),
  repositoryCommitChanges: (ref: string, signal?: AbortSignal) => request<RepositoryCommitChanges>(`/api/v1/repository/commit-changes?${new URLSearchParams({ ref })}`, { signal }),
  repositoryCommits: (_repositoryId: string, ref: string, limit = 50, cursor?: string, signal?: AbortSignal) => request<RepositoryCommitsResponse>(`/api/v1/repository/commits?${new URLSearchParams({ ref, limit: String(limit), ...(cursor ? { cursor } : {}) }).toString()}`, { signal }),
  agentCapabilities: async (): Promise<HarnessCapabilities> => {
    const local = await request<{ capabilities?: HarnessCapability[]; default_harness?: HarnessType; default_harness_explicit?: boolean; harness_defaults?: HarnessCapabilities['harness_defaults'] }>('/api/v1/agent-capabilities')
    const capabilities = (Array.isArray(local.capabilities) ? local.capabilities : []) as HarnessCapabilities
    Object.defineProperties(capabilities, {
      default_harness: { value: local.default_harness, writable: true },
      default_harness_explicit: { value: local.default_harness_explicit, writable: true },
      harness_defaults: { value: local.harness_defaults, writable: true },
    })
    return capabilities
  },
  agentLaunchCommands: () => request<{ commands: Record<HarnessType, string> }>('/api/v1/settings/agent-launch-commands'),
  updateAgentLaunchCommands: (commands: Partial<Record<HarnessType, string>>) => request<void>('/api/v1/settings/agent-launch-commands', {
    method: 'PUT', body: JSON.stringify({ commands }),
  }),
  updateDefaultAgentHarness: (harnessType: HarnessType) => request<{ default_harness: HarnessType; default_harness_explicit: boolean }>('/api/v1/settings/default-agent-harness', {
    method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ harness_type: harnessType }),
  }),
  agentModels: (harnessType: HarnessType) => request<import('./types').ModelChoice[]>(`/api/v1/agents/${encodeURIComponent(harnessType)}/models`),
  updateAgentHarnessDefault: (workflow: HarnessWorkflow, harnessType: HarnessType, model = '', permissions = '') => request<HarnessDefault>(`/api/v1/settings/agent-harness-defaults/${encodeURIComponent(workflow)}`, {
    method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ harness_type: harnessType, model, ...(permissions ? { permissions } : {}) }),
  }),
  resetAgentHarnessDefault: (workflow: HarnessWorkflow) => request<void>(`/api/v1/settings/agent-harness-defaults/${encodeURIComponent(workflow)}`, { method: 'DELETE' }),
  holons: async () => {
    const value = await request<{ holons: Holon[] } | Holon[]>('/api/v1/holons')
    return Array.isArray(value) ? value : value.holons
  },
  selectHolonTab: (id: string, tabId: string) => request<void>(`/api/v1/holons/${encodeURIComponent(id)}/selected-tab`, {
    method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ tab_id: tabId }),
  }, 15_000),
  holon: (id: string) => request<Holon>(`/api/v1/holons/${encodeURIComponent(id)}`),
  holonWorkspace: (id: string, signal?: AbortSignal) => request<WorkspaceInspection>(holonWorkspacePath(id), { signal }),
  sessionChanges: (id: string, options: HolonChangesOptions = {}) => request<WorkspaceInspection>(holonWorkspacePath(id, options), { signal: options.signal }),
  changeHolonBaseBranch: (id: string, branch: string) => request<Holon>(`/api/v1/holons/${encodeURIComponent(id)}/base-branch`, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ base_branch: branch }) }),
  publishHolon: (id: string, publish: { remote: string; upstream_branch: string; expected_remote_head: string }) => request<Holon>(`/api/v1/holons/${encodeURIComponent(id)}/publish`, { method: 'POST', headers: { 'Content-Type': 'application/json', 'If-Match': JSON.stringify(publish.expected_remote_head) }, body: JSON.stringify(publish) }),
  sessionPullRequest: (id: string, signal?: AbortSignal) => readPullRequest<HolonPullRequestResponse>(`/api/v1/holons/${encodeURIComponent(id)}/pull-request`, acceptLinkedPullRequest, { signal }),
  prepareHolon: (ref: string, signal?: AbortSignal) => request<RepositoryPreparation>('/api/v1/repository/prepare', {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ ref }), signal,
  }, 65_000),
  prepareSessionStartup: (_repositoryId: string, baseBranch = '', signal?: AbortSignal) => api.prepareHolon(baseBranch, signal),
  createHolon: (prompt: string, options: CreateHolonOptions & { baseCommit?: string; respondAsync?: boolean }) => request<Holon>('/api/v1/holons', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', ...(options.respondAsync ? { Prefer: 'respond-async' } : {}) },
    body: JSON.stringify({
      title: options.title,
      prompt,
      base_branch: options.baseBranch,
      base_commit: options.baseCommit,
      kind: options.issueId ? 'issue' : 'normal',
      issue_id: options.issueId,
      startup_mode: options.startupMode,
      agent_type: options.startupMode === 'terminal' ? undefined : options.harnessType ?? 'codex',
    }),
  }),
  updateSessionTitle: (id: string, title: string) => request<Holon>(`/api/v1/holons/${encodeURIComponent(id)}`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ title }),
  }),
  endHolon: (id: string) => request<Holon>(`/api/v1/holons/${encodeURIComponent(id)}/end`, { method: 'POST' }),
  reopenHolon: (id: string) => request<Holon>(`/api/v1/holons/${encodeURIComponent(id)}/reopen`, { method: 'POST' }),
  reorderSessionTabs: (id: string, tabs: HolonTabRef[]) => request<Holon>(`/api/v1/holons/${encodeURIComponent(id)}/tabs`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ tabs }),
  }),
  agentSessions: async (holonId: string) => (await request<{ agent_sessions: AgentSession[] }>(`/api/v1/holons/${encodeURIComponent(holonId)}/agent-sessions`)).agent_sessions,
  createAgentSession: (holonId: string, prompt: string, harnessType: HarnessType = 'codex', requestId?: string) => request<AgentSession>(`/api/v1/holons/${encodeURIComponent(holonId)}/agent-sessions`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', ...(requestId ? { 'Idempotency-Key': requestId } : {}) },
    body: JSON.stringify({ prompt, agent_type: harnessType }),
  }, 15_000),
  forkAgentSession: (holonId: string, agentSessionId: string) => request<AgentSession>(`/api/v1/holons/${encodeURIComponent(holonId)}/agent-sessions/${encodeURIComponent(agentSessionId)}/fork`, { method: 'POST' }),
  updateAgentSession: (holonId: string, agentSessionId: string, title: string) => request<AgentSession>(`/api/v1/holons/${encodeURIComponent(holonId)}/agent-sessions/${encodeURIComponent(agentSessionId)}`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ title }),
  }),
  cancelAgentSession: (holonId: string, agentSessionId: string) => request<AgentSession>(`/api/v1/holons/${encodeURIComponent(holonId)}/agent-sessions/${encodeURIComponent(agentSessionId)}/cancel`, { method: 'POST' }),
  closeAgentSession: (holonId: string, agentSessionId: string) => request<AgentSession>(`/api/v1/holons/${encodeURIComponent(holonId)}/agent-sessions/${encodeURIComponent(agentSessionId)}/close`, { method: 'POST' }),
  resumeAgentSession: (holonId: string, agentSessionId: string) => request<AgentSession>(`/api/v1/holons/${encodeURIComponent(holonId)}/agent-sessions/${encodeURIComponent(agentSessionId)}/resume`, {
    method: 'POST',
  }),
  holonPublicationReadiness: (id: string, signal?: AbortSignal) => request<HolonPublicationReadiness>(`/api/v1/holons/${encodeURIComponent(id)}/publication-readiness`, { signal }),
  createRebaseAgent: (holonId: string, sourceAgentId?: string) => request<AgentSession | undefined>(`/api/v1/holons/${encodeURIComponent(holonId)}/rebase-agent`, { method: 'POST', body: JSON.stringify({ source_agent_id: sourceAgentId }) }),
  createCommitAgent: (holonId: string, sourceAgentId?: string) => request<AgentSession>(`/api/v1/holons/${encodeURIComponent(holonId)}/commit-agent`, { method: 'POST', body: JSON.stringify({ source_agent_id: sourceAgentId }) }),
  commitPromptAction: (holonId: string, agentSessionId: string, action: CommitPromptAction) => request<void>(`/api/v1/holons/${encodeURIComponent(holonId)}/agent-sessions/${encodeURIComponent(agentSessionId)}/commit-prompt`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ action }),
  }),
  manualTerminals: async (holonId: string) => (await request<{ manual_terminals: ManualTerminal[] }>(`/api/v1/holons/${encodeURIComponent(holonId)}/terminals`)).manual_terminals,
  createManualTerminal: (holonId: string, title = '') => request<ManualTerminal>(`/api/v1/holons/${encodeURIComponent(holonId)}/terminals`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ title }),
  }),
  updateManualTerminal: (holonId: string, terminalId: string, title: string) => request<ManualTerminal>(`/api/v1/holons/${encodeURIComponent(holonId)}/terminals/${encodeURIComponent(terminalId)}`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ title }),
  }),
  closeManualTerminal: (holonId: string, terminalId: string) => request<ManualTerminal>(`/api/v1/holons/${encodeURIComponent(holonId)}/terminals/${encodeURIComponent(terminalId)}/close`, { method: 'POST' }),
  relaunchManualTerminal: (holonId: string, terminalId: string) => request<ManualTerminal>(`/api/v1/holons/${encodeURIComponent(holonId)}/terminals/${encodeURIComponent(terminalId)}/relaunch`, { method: 'POST' }),
  sessionIDEs: (holonId: string) => request<HolonIDE[]>(`/api/v1/holons/${encodeURIComponent(holonId)}/ides`),
  createHolonIDE: (holonId: string) => request<HolonIDE>(`/api/v1/holons/${encodeURIComponent(holonId)}/ides`, { method: 'POST' }),
  closeHolonIDE: (holonId: string, ideId: string) => request<HolonIDE>(`/api/v1/holons/${encodeURIComponent(holonId)}/ides/${encodeURIComponent(ideId)}/close`, { method: 'POST' }),
  sessionIDEProxyURL: (holonId: string, ideId: string) => `/api/v1/holons/${encodeURIComponent(holonId)}/ides/${encodeURIComponent(ideId)}/proxy/`,
  createPullRequest: (holonId: string, title: string, summary: string, target: PullRequestStatus) => pullRequestAction<PullRequest>(`/api/v1/holons/${encodeURIComponent(holonId)}/pull-request`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ title, summary, target }),
  }, acceptPullRequest),
  pullRequests: (projectId: string) => { void projectId; return readPullRequest<PullRequest[]>('/api/v1/pull-requests', (items, observation) => items.map((pr) => acceptPullRequest(pr, observation))) },
  pullRequestSessionLinks: (projectId: string) => { void projectId; return request<PullRequestSessionLink[]>('/api/v1/pull-request-holon-links') },
  syncPullRequests: (projectId: string, scope: 'active' | 'history' = 'history') => { void projectId; return readPullRequest<PullRequestSyncResponse>(`/api/v1/pull-requests/sync${scope === 'active' ? '?scope=active' : ''}`, (value, observation) => ({ ...value, pull_requests: value.pull_requests.map((pr) => acceptPullRequest(pr, observation)) }), { method: 'POST' }) },
  pullRequestOperation: (requestId: string) => request<PullRequestOperation>(`/api/v1/pull-request-operations/${encodeURIComponent(requestId)}`),
  pullRequest: (id: string) => readPullRequest<PullRequest>(`/api/v1/pull-requests/${encodeURIComponent(id)}`, acceptPullRequest),
  replacePullRequestAssignees: (id: string, memberIds: string[]) => request<PullRequestParticipantSnapshot>(`/api/v1/pull-requests/${encodeURIComponent(id)}/assignees`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ assignee_holark_ids: memberIds }),
  }),
  addPullRequestAssignee: (id: string, memberId: string) => request<PullRequestParticipantSnapshot>(`/api/v1/pull-requests/${encodeURIComponent(id)}/assignees`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ holark_id: memberId }),
  }),
  removePullRequestAssignee: (id: string, memberId: string) => request<PullRequestParticipantSnapshot>(`/api/v1/pull-requests/${encodeURIComponent(id)}/assignees/${encodeURIComponent(memberId)}`, { method: 'DELETE' }),
  replacePullRequestRequestedReviewers: (id: string, memberIds: string[]) => request<PullRequestParticipantSnapshot>(`/api/v1/pull-requests/${encodeURIComponent(id)}/requested-reviewers`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ requested_reviewer_holark_ids: memberIds }),
  }),
  addPullRequestRequestedReviewer: (id: string, memberId: string) => request<PullRequestParticipantSnapshot>(`/api/v1/pull-requests/${encodeURIComponent(id)}/requested-reviewers`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ holark_id: memberId }),
  }),
  removePullRequestRequestedReviewer: (id: string, memberId: string) => request<PullRequestParticipantSnapshot>(`/api/v1/pull-requests/${encodeURIComponent(id)}/requested-reviewers/${encodeURIComponent(memberId)}`, { method: 'DELETE' }),
  syncPullRequestParticipants: (id: string) => request<PullRequestParticipantSnapshot>(`/api/v1/pull-requests/${encodeURIComponent(id)}/participants/sync`, { method: 'POST' }),
  refreshPullRequestGitHubReadiness: (id: string) => readPullRequest<PullRequest>(`/api/v1/pull-requests/${encodeURIComponent(id)}/github-readiness`, acceptPullRequest, { method: 'POST' }),
  syncPullRequest: async (id: string, signal?: AbortSignal): Promise<PullRequest> => {
    // Keep the observation barrier from before synchronization across the GET.
    const observation = pullRequestObservation()
    await request<void>(`/api/v1/pull-requests/${encodeURIComponent(id)}/sync`, { method: 'POST', signal })
    return acceptPullRequest(await request<PullRequest>(`/api/v1/pull-requests/${encodeURIComponent(id)}`, { signal }), observation)
  },
  retryPullRequestMetadata: (id: string) => request<PullRequestMetadata>(`/api/v1/pull-requests/${encodeURIComponent(id)}/metadata/retry`, { method: 'POST' }),
  pullRequestMetadata: (id: string) => request<PullRequestMetadata>(`/api/v1/pull-requests/${encodeURIComponent(id)}/metadata`),
  updatePullRequestMetadata: (id: string, metadata: { title?: string, description?: string }) => request<PullRequestMetadata>(`/api/v1/pull-requests/${encodeURIComponent(id)}/metadata`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(metadata),
  }),
  startPullRequestMetadataAgent: (id: string, action: 'improve' | 'regenerate', instruction: string) => request<PullRequestMetadata>(`/api/v1/pull-requests/${encodeURIComponent(id)}/metadata/agent`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ action, instruction }),
  }),
  mergePullRequest: (id: string, strategy: string, bypassUnresolvedComments: boolean) => pullRequestAction<PullRequestMergeResponse>(`/api/v1/pull-requests/${encodeURIComponent(id)}/merge`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ strategy, bypass_unresolved_comments: bypassUnresolvedComments }),
  }, acceptLinkedPullRequest, id),
  pinPullRequestToPanel: (id: string) => request<void>(`/api/v1/pull-requests/${encodeURIComponent(id)}/panel-pin`, { method: 'PUT' }),
  unpinPullRequestFromPanel: (id: string) => request<void>(`/api/v1/pull-requests/${encodeURIComponent(id)}/panel-pin`, { method: 'DELETE' }),
  transitionPullRequest: (id: string, status: PullRequestTransitionStatus) => pullRequestAction<PullRequest>(`/api/v1/pull-requests/${encodeURIComponent(id)}/transition`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ status }),
  }, acceptPullRequest, id),
  attemptPullRequestRebase: (id: string, expectedBaseCommit: string) => pullRequestAction<PullRequestRebase>(`/api/v1/pull-requests/${encodeURIComponent(id)}/rebase`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ mechanical_only: true, expected_base_commit: expectedBaseCommit }),
  }, (value) => value, id, 'work'),
  resolvePullRequestRebase: (id: string, expectedBaseCommit: string) => pullRequestAction<PullRequestRebase>(`/api/v1/pull-requests/${encodeURIComponent(id)}/rebase`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ mechanical_only: false, expected_base_commit: expectedBaseCommit }),
  }, (value) => value, id, 'work'),
  pullRequestRebases: (id: string) => request<PullRequestRebase[]>(`/api/v1/pull-requests/${encodeURIComponent(id)}/rebases`),
  pullRequestRebaseReadiness: (id: string) => request<PullRequestRebaseReadiness>(`/api/v1/pull-requests/${encodeURIComponent(id)}/rebase-readiness`, { method: 'POST' }),
  publishPullRequest: (id: string) => pullRequestAction<PullRequestPublishResponse>(`/api/v1/pull-requests/${encodeURIComponent(id)}/publish`, { method: 'POST' }, acceptLinkedPullRequest, id),
  pullRequestComments: (id: string) => request<PullRequestComment[]>(`/api/v1/pull-requests/${encodeURIComponent(id)}/comments`),
  pullRequestCommentRefreshStatus: (id: string, signal: AbortSignal) => request<{ syncing: boolean }>(`/api/v1/pull-requests/${encodeURIComponent(id)}/comments/refresh`, { signal }),
  refreshPullRequestComments: (id: string, force = false) => request<{ refreshed: boolean, synced_at: string | null, sync_applicable: boolean }>(`/api/v1/pull-requests/${encodeURIComponent(id)}/comments/refresh`, { method: 'POST', body: JSON.stringify({ force }) }),
  pullRequestReviews: (id: string, signal?: AbortSignal) => request<PullRequestReview[]>(`/api/v1/pull-requests/${encodeURIComponent(id)}/reviews`, { signal }),
  pullRequestQueue: (id: string) => request<PullRequestQueueItem[]>(`/api/v1/pull-requests/${encodeURIComponent(id)}/queue`),
  cancelPullRequestWork: (id: string, workId: string) => request<PullRequestQueueItem>(`/api/v1/pull-requests/${encodeURIComponent(id)}/queue/${encodeURIComponent(workId)}/cancel`, { method: 'POST' }),
  pullRequestWorkers: (id: string) => request<PullRequestWorker[]>(`/api/v1/pull-requests/${encodeURIComponent(id)}/workers`),
  createPullRequestWorkers: (id: string, commentIds: string[], mode: PullRequestWorkerMode, prompt?: string, startup?: { title?: string; agent_type?: HarnessType; startup_mode?: 'agent' | 'terminal' }) => request<PullRequestWorker[]>(`/api/v1/pull-requests/${encodeURIComponent(id)}/workers`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ comment_ids: commentIds, mode, prompt, startup }),
  }),
  createPullRequestReview: (id: string, mode: PullRequestReviewMode = 'assisted') => request<PullRequestReview>(`/api/v1/pull-requests/${encodeURIComponent(id)}/reviews`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ mode }),
  }),
  manualPullRequestReviews: (id: string, signal?: AbortSignal) => request<ManualPullRequestReview[]>(`/api/v1/pull-requests/${encodeURIComponent(id)}/manual-reviews`, { signal }),
  submitManualPullRequestReview: (id: string, input: ManualReviewSubmission) => request<ManualPullRequestReview>(`/api/v1/pull-requests/${encodeURIComponent(id)}/manual-reviews`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(input) }),
  retryManualPullRequestReview: (id: string, reviewId: string) => request<ManualPullRequestReview>(`/api/v1/pull-requests/${encodeURIComponent(id)}/manual-reviews/${encodeURIComponent(reviewId)}/retry`, { method: 'POST' }),
  publishPullRequestCommentDrafts: (id: string, head: string, commentIds: string[]) => request<PullRequestComment[]>(`/api/v1/pull-requests/${encodeURIComponent(id)}/comments/publish`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ head_commit: head, comment_ids: commentIds }) }),
  createPullRequestComment: (id: string, input: CreatePullRequestCommentRequest) => request<PullRequestComment>(`/api/v1/pull-requests/${encodeURIComponent(id)}/comments`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(input),
  }),
  updatePullRequestComment: (id: string, body: string) => request<PullRequestComment>(`/api/v1/pull-request-comments/${encodeURIComponent(id)}`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ body }),
  }),
  deletePullRequestComment: (id: string) => request<{ deleted: boolean }>(`/api/v1/pull-request-comments/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  resolvePullRequestComment: (id: string) => request<PullRequestComment>(`/api/v1/pull-request-comments/${encodeURIComponent(id)}/resolve`, { method: 'POST' }),
  reopenPullRequestComment: (id: string) => request<PullRequestComment>(`/api/v1/pull-request-comments/${encodeURIComponent(id)}/reopen`, { method: 'POST' }),
  pullRequestCommits: (id: string) => request<PullRequestDetail<PullRequestCommit[]>>(`/api/v1/pull-requests/${encodeURIComponent(id)}/commits`),
  pullRequestChanges: (id: string, options: { path?: string; summary?: boolean; signal?: AbortSignal } = {}) => {
    const query = new URLSearchParams()
    if (options.path) query.set('path', options.path)
    if (options.summary) query.set('summary', 'true')
    return request<PullRequestDetail<WorkspaceInspection>>(`/api/v1/pull-requests/${encodeURIComponent(id)}/changes${query.size ? `?${query}` : ''}`, { signal: options.signal })
  },
  githubMembers: (projectId: string) => {
    void projectId
    return request<GitHubMember[]>('/api/v1/github-members')
  },
  syncGitHubMembers: (projectId: string) => {
    void projectId
    return request<GitHubMember[]>('/api/v1/github-members/sync', { method: 'POST' })
  },
  projectMemberSearch: (projectId: string, query: string, limit?: number) => {
    const search = new URLSearchParams({ q: query })
    if (limit !== undefined) search.set('limit', String(limit))
    void projectId
    return request<ProjectMemberSearchResponse>(`/api/v1/github-members/search?${search.toString()}`)
  },
  resolveProjectMembers: (projectId: string, ids: string[]) => {
    void projectId
    return request<ProjectMemberResolution>('/api/v1/github-members/resolve', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ ids }),
    })
  },
  issues: (projectId: string) => {
    void projectId
    return request<Issue[]>('/api/v1/issues')
  },
  issue: (id: string) => request<Issue>(`/api/v1/issues/${encodeURIComponent(id)}`),
  updateIssueBody: (id: string, body: string) => request<Issue>(`/api/v1/issues/${encodeURIComponent(id)}`, { method: 'PATCH', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ body }) }),
  replaceIssueAssignees: (id: string, memberIds: string[]) => request<Issue>(`/api/v1/issues/${encodeURIComponent(id)}/assignees`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ assignee_holark_ids: memberIds }),
  }),
  issueLabels: (projectId: string) => {
    void projectId
    return request<IssueLabel[]>('/api/v1/labels')
  },
  createIssueLabel: (projectId: string, label: { name: string; color: string; description: string }) => {
    void projectId
    return request<IssueLabel>('/api/v1/labels', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(label),
    })
  },
  addIssueLabels: (id: string, names: string[]) => request<Issue>(`/api/v1/issues/${encodeURIComponent(id)}/labels/add`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ labels: names }),
  }),
  removeIssueLabels: (id: string, names: string[]) => request<Issue>(`/api/v1/issues/${encodeURIComponent(id)}/labels/remove`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ labels: names }),
  }),
  issueSnapshot: (id: string) => request<IssueSnapshot>(`/api/v1/issues/${encodeURIComponent(id)}/snapshot`, { method: 'POST' }),
  createIssue: (projectId: string, title: string, body: string) => {
    void projectId
    return request<Issue>('/api/v1/issues', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ title, body }),
    })
  },
  closeIssue: (id: string) => request<Issue>(`/api/v1/issues/${encodeURIComponent(id)}/close`, { method: 'POST' }),
  reopenIssue: (id: string) => request<Issue>(`/api/v1/issues/${encodeURIComponent(id)}/reopen`, { method: 'POST' }),
  syncIssues: (projectId: string) => {
    void projectId
    return request<IssueSyncResponse>('/api/v1/issues/sync', { method: 'POST' })
  },
  startIssueAgent: (id: string) => request<Holon>(`/api/v1/issues/${encodeURIComponent(id)}/start-agent`, { method: 'POST' }),
  promptTemplates: () => request<PromptTemplate[]>('/api/v1/prompt-templates'),
  updatePromptTemplate: (key: string, value: string) => request<PromptTemplate>(`/api/v1/prompt-templates/${encodeURIComponent(key)}`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ value }),
  }),
}

function asProject(repository: ApiRepository): ApiProject {
  const localName = repository.root.split(/[/\\]/).filter(Boolean).at(-1)
  return { ...repository, name: remoteRepositoryName(repository.repository_url) || localName || 'Repository' }
}

function remoteRepositoryName(remote?: string): string {
  if (!remote?.trim()) return ''
  let path = remote.trim()
  const isURL = path.includes('://')
  if (isURL) {
    // Git treats query and fragment delimiters as filename characters in SSH and file URLs.
    if (/^(ssh|file):\/\//i.test(path)) {
      path = path.replace(/\?/g, '%3F').replace(/#/g, '%23')
    }
    try {
      path = new URL(path).pathname
    } catch {
      return ''
    }
  } else {
    // Git also accepts SCP-style remotes, such as git@github.com:owner/repo.git.
    path = path.replace(/^[^/\\]+:/, '')
  }
  let name = path.split(/[/\\]/).filter(Boolean).at(-1) || ''
  if (isURL) {
    try {
      name = decodeURIComponent(name)
    } catch { /* Preserve the original name when URL escapes are malformed. */ }
  }
  return name.replace(/\.git$/, '')
}

function repositoryPath(endpoint: 'tree' | 'blob', path: string, ref?: string) {
  const query = new URLSearchParams()
  query.set('path', path)
  if (ref) query.set('ref', ref)
  return `/api/v1/repository/${endpoint}?${query.toString()}`
}

function holonWorkspacePath(id: string, options: HolonChangesOptions = {}) {
  const query = new URLSearchParams()
  if (options.base) query.set('base', options.base)
  if (options.target) query.set('target', options.target)
  if (options.summary) query.set('summary', 'true')
  if (options.contents) query.set('contents', 'true')
  if (options.path) query.set('path', options.path)
  const suffix = query.toString()
  return `/api/v1/holons/${encodeURIComponent(id)}/workspace${suffix ? `?${suffix}` : ''}`
}
