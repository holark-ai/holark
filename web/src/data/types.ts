export type RepositoryTreeEntry = {
  name: string
  path: string
  type: 'directory' | 'file'
  mode: string
  size?: number
  language?: string
}

export type RepositoryTreeResponse = {
  commit: string
  ref: string
  path: string
  entries: RepositoryTreeEntry[]
}

export type RepositoryBlobResponse = {
  commit: string
  ref: string
  path: string
  language: string
  content: string
  encoding: 'utf-8'
  size: number
  truncated: boolean
}

export type RepositoryRefKind = 'branch' | 'remote_branch' | 'holark_branch'

export type RepositoryRef = {
  name: string
  short_name: string
  kind: RepositoryRefKind
  target: string
  committed_at: string
  author_name?: string
  subject?: string
}

export type RepositoryRefsResponse = {
  refs: RepositoryRef[]
  refresh_error?: string
  default_ref?: string
}

export type RepositoryCommit = {
  sha: string
  message: string
  author_name?: string
  author_email?: string
  authored_at: string
}

export type RepositoryCommitsResponse = {
  head?: string
  next_cursor?: string
  commits: RepositoryCommit[]
  ref?: string
}

export type ApiRepository = {
  id: string
  root: string
  common_dir: string
  default_branch: string
  repository_url?: string
}

// Temporary internal alias for domain pages migrated in later phases.
export type ApiProject = {
  id: string
  name: string
  default_branch: string
  root?: string
  common_dir?: string
}

export type CurrentUser = {
  id: string
  email: string
  display_name: string
  auth_provider: string
}

export type GitHubProfile = {
  login: string
  name?: string
  avatar_url?: string
  profile_url?: string
}

export type HarnessType = 'codex' | 'claude-code' | 'opencode'

export type HarnessWorkflow = 'default' | 'issue' | 'pull_request_review' | 'pull_request_feedback' | 'pull_request_metadata' | 'pull_request_rebase'

export type HarnessDefault = {
  permissions?: string
	model?: string
  harness_type: HarnessType
  explicit: boolean
}

export type ModelChoice = { id: string; label: string }

export type ObservabilityStatus = 'starting' | 'healthy' | 'degraded'

export type HarnessCapability = {
  type: HarnessType
  available: boolean
  automated_workflows?: boolean
  version?: string
  unavailable_reason?: string
  support_status?: 'supported' | 'unsupported' | 'unknown'
  supported_ranges?: string[]
  latest_supported_version?: string
  warning?: string
}

export type HarnessCapabilities = HarnessCapability[] & {
  default_harness?: HarnessType
  default_harness_explicit?: boolean
  harness_defaults?: Partial<Record<HarnessWorkflow, HarnessDefault>>
}

export type IDEProvider = 'vscode'

export type IDECapability = {
  provider: IDEProvider
  available: boolean
  version?: string
  install_source?: 'configured' | 'path' | 'managed' | 'downloaded'
  unavailable_reason?: string
}

export type HolonStatus =
  | 'naming'
  | 'queued'
  | 'preparing'
  | 'running'
  | 'cancelling'
  | 'cancelled'
  | 'completed'
  | 'failed'
  | 'lost'
  | 'restoring'
  | 'recovery_failed'
  | 'expired'

export type AgentActivity = 'starting' | 'idle' | 'working' | 'needs_input' | 'completed' | 'unknown' | 'failed'
export type InputState = 'none' | 'permission_required' | 'user_input_required' | 'task_complete'

export type CommitPromptState = 'commit_close_pending' | 'clean' | 'dirty_prompt_visible' | 'commit_discussion_started' | 'skipped_for_change' | 'continued_for_now'

export type CommitPromptAction = 'discuss_commit_message' | 'skip_for_change' | 'keep_discussing'

export type HolonKind = 'normal' | 'issue' | 'pr_review' | 'pr_worker' | 'pr_metadata' | 'rebase'

export type ManualTerminal = {
  closed_at?: string
  id: string
  terminal_id?: string
  holon_id?: string
  title: string
  cwd: string
  tab_order?: number
  created_at: string
  updated_at: string
}

export type HolonIDEState = 'starting' | 'ready' | 'stopping' | 'suspended' | 'failed' | 'lost' | 'closed'

export type HolonIDE = {
  id: string
  holon_id: string
  provider: IDEProvider
  state: HolonIDEState
  tab_order?: number
  desired_open: boolean
  reason?: string
  created_at: string
  updated_at: string
  ready_at?: string
  closed_at?: string
}

export type AgentSession = {
  model?: string
  id: string
  holon_id?: string
  terminal_id?: string
  agent_type?: HarnessType
  title?: string
  prompt?: string
  status?: HolonStatus
  reason?: string
  exit_code?: number
  resume_target?: string
  rollout_path?: string
  observability_status?: ObservabilityStatus
  observability_message?: string
  context_tokens?: number
  tab_order?: number
  activity?: AgentActivity
  input_state?: InputState
  commit_prompt?: { state: CommitPromptState }
  created_at: string
  updated_at: string
  started_at?: string
  finished_at?: string
  closed_at?: string
}

export type HolonRebaseAttempt = {
  id?: string
  source_agent_id?: string
  progress?: string
  agent_id: string
  target_branch: string
  target_commit: string
  start_head_commit: string
  result_head_commit?: string
  state: 'running' | 'waiting' | 'succeeded' | 'failed'
  failure_reason?: string
}

export type HolonPublicationReadiness = {
  target_branch: string
  target_commit: string
  workspace_head: string
  rebase_required: boolean
  rebase_available: boolean
  publication_available: boolean
  reason?: string
  attempt?: HolonRebaseAttempt
}

export type Holon = {
  application_phase?: 'finalizing'
  last_selected_tab_id?: string
  synchronized_target_commit?: string
  rebase_attempt?: HolonRebaseAttempt
  id: string
  /** Temporary adapter fields for domain pages migrating in phases 9-12. */
  repository_id?: string
  runtime_id?: string
  title?: string
  prompt: string
  kind?: HolonKind
  worktree_branch?: string
  base_branch?: string
  base_commit?: string
  proposed_upstream_branch?: string
  upstream_branch?: string
  upstream_head_commit?: string
  worktree_path?: string
  agent_sessions?: AgentSession[]
  manual_terminals?: ManualTerminal[]
  agent_session?: AgentSession
  ides?: HolonIDE[] | null
  read_only?: boolean
  status: HolonStatus
  activity?: AgentActivity
  input_state?: InputState
  reason?: string
  exit_code?: number
  created_at: string
  started_at?: string
  finished_at?: string
  archived_at?: string
  issue_id?: string
  pull_request_id?: string
  published?: boolean
}

export type HolonTabRef = {
  type: 'agent' | 'terminal' | 'ide'
  id: string
}

export type TerminalSize = {
  columns: number
  rows: number
}

export type PullRequestStatus = 'wip' | 'draft' | 'open' | 'closed' | 'merged'

export type PullRequestTransitionStatus = 'wip' | 'draft' | 'open' | 'closed'

export type PullRequestSyncData = {
  github?: {
    owner?: string
    repo?: string
    number?: number
    url?: string
    runtime_id?: string
    draft?: boolean
    mergeable?: boolean
    state?: string
    readiness?: {
      head_commit: string
      checks_state: 'passing' | 'pending' | 'failing' | 'error' | 'unknown'
      mergeability_state: 'mergeable' | 'blocked' | 'conflicting' | 'unknown'
      details_url?: string
      synced_at: string
      error?: string
    }
  }
}

export type ProjectMember = {
  id: string
  login: string
  avatar_url?: string
  profile_url?: string
  permission: string
  is_me: boolean
}

export type GitHubMember = ProjectMember

export type ProjectMemberSearchResponse = {
  members: ProjectMember[]
}

export type ProjectMemberResolution = {
  members: ProjectMember[]
  missing_ids: string[]
}

export type PullRequestCommentStatus = 'unresolved' | 'resolved'

export type PullRequestCommentScope = 'pull_request' | 'file' | 'line'

export type PullRequestReviewStatus = 'queued' | 'running' | 'waiting_user' | 'cancelling' | 'completed' | 'failed' | 'cancelled' | 'stale'

export type PullRequestWorkerMode = 'auto' | 'assisted' | 'continue'

export type PullRequestWorkerStatus = 'queued' | 'running' | 'waiting_user' | 'completed' | 'failed' | 'stale' | 'cancelling' | 'cancelled' | 'skipped'

export type PullRequestQueueStatus = PullRequestWorkerStatus

export type PullRequestReviewInput = {
  diff_base_commit: string
  head_commit: string
  patch_fingerprint: string
  messages_fingerprint: string
}

export type PullRequestReviewMode = 'assisted' | 'auto'

export type PullRequestReview = {
  mode?: PullRequestReviewMode
  provenance?: PullRequestReviewInput
  freshness?: {
    generated: PullRequestReviewInput
    current: PullRequestReviewInput
    outdated: boolean
  }
  id: string
  pull_request_id: string
  session_id: string
  status: PullRequestReviewStatus
  base_branch: string
  base_commit: string
  head_branch: string
  head_commit: string
  summary?: string
  error?: string
  created_at: string
  completed_at?: string
}

export type PullRequestWorker = {
  id: string
  pull_request_id: string
  comment_id?: string
  session_id?: string
  mode: PullRequestWorkerMode
  status: PullRequestWorkerStatus
  base_head_commit?: string
  result_head_commit?: string
  reply_body?: string
  error?: string
  created_at: string
  started_at?: string
  completed_at?: string
}

export type PullRequestQueueItem = {
  id: string
  pull_request_id: string
  comment_id?: string
  session_id?: string
  status: PullRequestQueueStatus
  base_head_commit?: string
  result_head_commit?: string
  error?: string
  created_at: string
  started_at?: string
  completed_at?: string
} & ({ kind: 'worker', mode?: PullRequestWorkerMode } | { kind: 'rebase', mode?: never })

export type PullRequestCommentLocation =
  | { scope?: 'pull_request', path?: never, old_path?: never, side?: never, line?: never, diff_hunk?: never }
  | { scope: 'file', path: string, old_path?: string, side?: never, line?: never, diff_hunk?: never }
  | { scope: 'line', path: string, old_path?: string, side: 'LEFT' | 'RIGHT', line: number, diff_hunk?: string }

export type CreatePullRequestCommentRequest = { body: string, draft?: boolean } & (
  | (PullRequestCommentLocation & { parent_comment_id?: never })
  | { parent_comment_id: string, scope?: never, path?: never, old_path?: never, side?: never, line?: never, diff_hunk?: never }
)

export type PullRequestComment = {
  publication_state?: 'draft' | 'local' | 'pending' | 'published' | 'failed'
  id: string
  pull_request_id: string
  body: string
  scope: PullRequestCommentScope
  path?: string
  old_path?: string
  side?: string
  line?: number
  diff_hunk?: string
  original_head_commit: string
  status: PullRequestCommentStatus
  author_type: 'user' | 'agent'
  author_github_user_id?: string
  source_session_id?: string
  source_review_id?: string
  source_worker_id?: string
  parent_comment_id?: string
  created_at: string
  updated_at: string
  resolved_at?: string
}

export type ManualPullRequestReview = {
  id: string
  pull_request_id: string
  event: 'comment' | 'approve'
  body: string
  head_commit: string
  state: 'local' | 'pending' | 'published' | 'failed'
  url?: string
  error?: string
  created_at: string
}

export type ManualReviewSubmission = Pick<ManualPullRequestReview, 'id' | 'event' | 'body' | 'head_commit'>

export type PullRequestOperation = {
  request_id: string
  pull_request_id?: string
  holon_id?: string
  kind: string
  status: 'running' | 'uncertain' | 'succeeded' | 'failed'
  requested_status?: PullRequestStatus
}

export type PullRequestDetail<T> = {
  inputs: { head_commit: string, diff_base_commit: string }
  data: T
}

export type PullRequest = {
  comparison_state?: 'ready' | 'stale' | 'unavailable'
  view_revision: number
  operations?: PullRequestOperation[]
  lifecycle_generation?: number
  topology_generation?: number
  metadata_generation?: number
	publication_blocked?: boolean
  id: string
  title: string
  summary: string
  base_branch: string
  base_commit: string
  head_branch: string
  head_commit: string
  diff_base_commit?: string
  status: PullRequestStatus
  sync_provider?: string
  sync_external_id?: string
  sync_data: PullRequestSyncData
  mergeable?: boolean
  merge_blocked_reason?: string
  merge_provider?: string
  merge_request_strategy?: string
  unresolved_comment_count?: number
  assignee_holark_ids: string[]
  requested_reviewer_holark_ids: string[]
  linked_session_ids: string[]
  panel_pinned?: boolean
  created_at: string
  updated_at: string
  closed_at?: string
  merged_at?: string
  merged_commit?: string
  merge_strategy?: string
  synced_at?: string
}

export type PullRequestSessionLink = {
  pull_request_id: string
  session_id: string
}

export type PullRequestParticipantSnapshot = {
  pull_request_id: string
  assignee_holark_ids: string[]
  requested_reviewer_holark_ids: string[]
}

export type PullRequestMetadata = {
  view_revision: number
  head_commit?: string
  diff_base_commit?: string
  generation_complete?: boolean
  generation_error?: string
  application_error?: { operation: 'save' | 'publication', message: string }
  freshness?: {
    status?: 'current' | 'outdated' | 'unknown'
    generated: { diff_base_commit: string, head_commit: string }
    current: { diff_base_commit: string, head_commit: string }
    outdated: boolean
  }
  pull_request_id: string
  title: string
  description: string
  updated_at: string
  preparation_target?: 'wip' | 'draft' | 'open'
  agent_session?: {
    id: string
    runtime_id: string
    status: HolonStatus
    input_state: InputState
    error: string
  }
}

export type PullRequestMergeResponse = {
  pull_request: PullRequest
  merged_commit: string
}

export type PullRequestRebase = PullRequestQueueItem & {
  kind: 'rebase'
  head_commit?: string
  target_base_commit?: string
  target_diff_base_commit?: string
  mechanical_only?: boolean
  summary?: string
}

export type PullRequestRebaseReadiness = {
  base_commit: string
  head_commit: string
  base_commits_ahead: number
  branch_freshness: 'up_to_date' | 'not_up_to_date'
  rebase_conflict_state: 'not_applicable' | 'clean' | 'conflicting'
}

export type PullRequestPublishResponse = {
  pull_request: PullRequest
  worker?: PullRequestWorker
  head_commit: string
}

export type HolonPullRequestResponse = {
  pull_request: PullRequest
}

export type IssueStatus = 'open' | 'closed'

export type IssueSyncData = {
  github?: {
    number?: number
    url?: string
    runtime_id?: string
    state?: string
    updated_at?: string
  }
}

export type IssueLabel = {
  id: string
  name: string
  color: string
  description: string
}

export type Issue = {
  id: string
  title: string
  body: string
  status: IssueStatus
  sync_provider?: string
  sync_external_id?: string
  sync_data: IssueSyncData
  labels: IssueLabel[]
  linked_pull_request_ids: string[]
  issuer_holark_id: string
  assignee_holark_ids: string[]
  created_at: string
  updated_at: string
  closed_at?: string
  synced_at?: string
}

export type IssueSnapshot = {
  discussion?: { text: string; synced_at: string | null; freshness: string; total_comments: number; included_comments: number; omitted_comments: number; truncated: boolean }
  issue_id: string
  title: string
  body: string
}

export type PullRequestSyncResponse = {
  pull_requests: PullRequest[]
  imported: number
  updated: number
  synced_at: string
}

export type IssueSyncResponse = {
  issues: Issue[]
  imported: number
  updated: number
  exported: number
  synced_at: string
}

export type PullRequestCommit = {
  sha: string
  message: string
  author_name?: string
  author_email?: string
  authored_at: string
  external_url?: string
}

export type WorkspaceFileDiff = {
  content_status?: 'ready' | 'binary' | 'too_large' | 'unsupported'
  contents?: { original: string; modified: string }
  path: string
  old_path?: string
  status: string
  additions: number
  deletions: number
  binary: boolean
  diff?: string
  diff_truncated: boolean
}

export type WorkspaceDiffRef = {
  id: string
  label: string
  kind: string
  commit?: string
}

export type WorkspaceCommit = {
  author?: string
  authored_at?: string
  body?: string
  sha: string
  parent_commit: string
  subject: string
}

export type WorkspaceInspection = {
  request_id?: string
  branch: string
  base_branch: string
  base_commit: string
  common_ancestor_commit?: string
  common_ancestor?: PullRequestCommit
  head_commit: string
  has_changes: boolean
  dirty: boolean
  files: WorkspaceFileDiff[]
  diff_truncated: boolean
  summary_only?: boolean
  selected_base_ref?: string
  selected_target_ref?: string
  selected_base_commit?: string
  selected_target_commit?: string
  ref_options?: WorkspaceDiffRef[]
  branch_base_commit?: string
  work_session_start_commit?: string
  workspace_head_commit?: string
  commits?: WorkspaceCommit[]
}


export type PromptTemplateVariable = {
  name: string
  description: string
}

export type PromptTemplate = {
  key: string
  name: string
  use: string
  variables: PromptTemplateVariable[] | null
  value: string
  default_value: string
  overridden: boolean
}

export type IssueComment = {
  id: string
  issue_id: string
  body: string
  author: { login: string; avatar_url: string; url: string }
  github_id: string
  github_node_id: string
  url: string
  created_at: string
  updated_at: string
  can_edit: boolean
  can_delete: boolean
}
export type IssueDiscussion = {
  comments: IssueComment[]
  synced_at: string | null
  can_comment: boolean | null
}

export type WorkItemSummary = {
  labels?: IssueLabel[]
  linked_pull_request_ids?: string[]
  url?: string
  id: string
  kind: 'pull_request' | 'issue'
  title: string
  number: number
  status: PullRequestStatus | 'open' | 'closed'
  author_id?: string
  assignee_ids: string[]
  reviewer_ids: string[]
  updated_at: string
  reasons: string[]
}
export type WorkItemsResult = {
  rows: WorkItemSummary[]
  total: number
  page: number
  per_page: number
  query?: string
  identity: ProjectMember | null
  counts?: Record<string, number>
  sync: { section: string; attempted_at: string; synced_at: string; error?: string }[]
}

export type RepositorySearchResponse = {
  ref: string
  matches: { path: string; name_match: boolean; content_match: boolean }[]
  truncated: boolean
}

export type RepositoryCommitChanges = {
  commit: string
  parent: string
  files: { path: string; status: string; patch: string; binary: boolean; truncated: boolean }[]
}

export type IssueListItem = WorkItemSummary
