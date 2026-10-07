import fixtures from './sharedTreeFixtures.json'
import { additionalPreviewPullRequests } from './pullRequestListPreview'
import type { AgentActivity, HarnessCapability, Holon, HolonStatus, PullRequest, PullRequestComment, PullRequestCommit, PullRequestReview, WorkspaceInspection } from '../data/types'

export const previewRepository = {
  id: 'holark-preview',
  root: '/preview/Acme Dashboard',
  common_dir: '/preview/holark/.git',
  default_branch: 'main',
}

export const previewCapabilities: HarnessCapability[] = [
  { type: 'codex', available: true, automated_workflows: true, version: 'preview' },
  { type: 'claude-code', available: true, automated_workflows: true, version: 'preview' },
  { type: 'opencode', available: false, unavailable_reason: 'OpenCode CLI was not found' },
]

// Two Holons on one branch without a PR form a branch group in the Holons panel.
const sharedPreviewBranches: Record<string, string> = { L: 'feature/keyboard-shortcuts', M: 'feature/keyboard-shortcuts' }

export const previewHolons: Holon[] = fixtures.holons.map((sample) => {
  const age = Number.parseInt(sample.age) * (sample.age.endsWith('d') ? 86400000 : sample.age.endsWith('h') ? 3600000 : 60000)
  const activity: AgentActivity = sample.status === 'permission' || sample.status === 'input' ? 'needs_input' : sample.status === 'done' ? 'completed' : sample.status === 'working' ? 'working' : 'idle'
  const holon = previewHolon(sample.id === 'B' ? 'holon-selected' : `holon-${sample.id}`, sample.name, 'running', sample.status === 'permission' ? 'permission_required' : sample.status === 'input' ? 'user_input_required' : 'none', new Date(new Date('2026-08-24T12:13:00Z').getTime() - age).toISOString(), {
    activity, branch: sample.pr ? `feature/pr-${sample.pr}` : sharedPreviewBranches[sample.id] ?? `feature/${sample.id.toLowerCase()}`,
  })
  return { ...holon, kind: sample.background ? 'pr_worker' : 'normal', agent_session: { ...holon.agent_session!, context_tokens: sample.tokens } }
})

function previewHolon(id: string, title: string, status: HolonStatus, inputState: Holon['input_state'], createdAt: string, options: { branch?: string, resumable?: boolean, activity?: AgentActivity } = {}): Holon {
  const previewAnchor = new Date('2026-08-24T12:13:00Z').getTime()
  const sampleOffset = previewAnchor - new Date(createdAt).getTime()
  const relativeCreatedAt = new Date(Date.now() - sampleOffset).toISOString()
  return {
    id,
    repository_id: previewRepository.id,
    runtime_id: 'studio-m2',
    title,
    prompt: title,
    status,
    activity: options.activity,
    input_state: inputState,
    worktree_branch: options.branch ?? `pmaviro/${id}`,
    worktree_path: options.resumable ? `/tmp/${id}` : undefined,
    agent_session: {
      id: `harness-${id}`,
      status: 'running',
      holon_id: id,
      agent_type: 'codex',
      activity: options.activity,
      input_state: inputState,
      created_at: relativeCreatedAt,
      updated_at: relativeCreatedAt,
    },
    created_at: relativeCreatedAt,
  }
}

export const initialPreviewPullRequests: PullRequest[] = [...fixtures.pullRequests.map((sample): PullRequest => ({
  view_revision: 1, comparison_state: 'ready', id: sample.number === 142 ? 'pr-preview' : `pr-${sample.number}`,
  title: sample.title, summary: sample.title, base_branch: 'main', base_commit: '15ad9fe1', head_branch: `feature/pr-${sample.number}`, head_commit: '7e9d8b1',
  status: sample.state.toLowerCase() as PullRequest['status'], sync_provider: 'github', sync_data: { github: { number: sample.number } },
  assignee_holark_ids: sample.number === 142 ? ['member-riley'] : ['member-jonas'],
  requested_reviewer_holark_ids: sample.state === 'Open' ? ['member-ada'] : [],
  linked_session_ids: fixtures.holons.filter((holon) => holon.pr === sample.number).map((holon) => holon.id === 'B' ? 'holon-selected' : `holon-${holon.id}`),
  created_at: `2026-08-${sample.number - 120}T10:00:00Z`, updated_at: `2026-08-${sample.number - 119}T12:05:00Z`,
})), ...additionalPreviewPullRequests.map((pullRequest, _index, all) => {
  // Cover the remaining Holons panel header states: a pinned PR without Holons
  // and a closed PR that still has an active Holon.
  if (pullRequest === all.find((candidate) => candidate.status === 'open')) return { ...pullRequest, panel_pinned: true }
  if (pullRequest === all.find((candidate) => candidate.status === 'closed')) return { ...pullRequest, linked_session_ids: ['holon-I'] }
  return pullRequest
})]

export const previewCommits: PullRequestCommit[] = [
  { sha: '54ce287b', message: 'Refine holon workspace preview\n\nKeep the selected row visible while moving between routes.', author_name: 'Riley', authored_at: '2026-08-24T12:02:00Z' },
  { sha: '2b4ab718', message: 'Extract operations panel presentation', author_name: 'Riley', authored_at: '2026-08-24T10:44:00Z' },
  { sha: '9ac418e2', message: 'Preserve review context during refresh\n\nKeep the current discussion visible until the new review is ready.', author_name: 'Riley', authored_at: '2026-08-24T10:20:00Z' },
  { sha: 'bd170fa3', message: 'Ignore outdated review responses', author_name: 'Riley', authored_at: '2026-08-24T10:10:00Z' },
  { sha: 'e27c5061', message: 'Keep the selected file and lines after refresh', author_name: 'Riley', authored_at: '2026-08-24T10:00:00Z' },
  { sha: 'c901d8ab', message: 'Add a retry action when refreshing fails', author_name: 'Riley', authored_at: '2026-08-24T09:50:00Z' },
]

type PreviewConversation = Pick<PullRequestComment, 'id' | 'body' | 'scope' | 'path' | 'side' | 'line' | 'diff_hunk' | 'status' | 'author_type' | 'author_github_user_id'> & {
  minutesAgo: number
  replies: Array<Pick<PullRequestComment, 'body' | 'author_type' | 'author_github_user_id'> & { minutesAgo: number }>
}

const previewConversations: PreviewConversation[] = [
  {
    id: 'comment-preview-file-unresolved', author_type: 'user', author_github_user_id: 'member-ada',
    body: 'Could we use the same loading and retry wording throughout this component? The file view and discussion should describe refreshes consistently.',
    scope: 'file', path: 'web/src/features/reviews/Review.tsx',
    status: 'unresolved', minutesAgo: 52,
    replies: [],
  },
  {
    id: 'comment-preview-file-resolved', author_type: 'user', author_github_user_id: 'member-jonas',
    body: 'Keep the refresh error handling consistent across this service so callers can preserve the previous review when a request fails.',
    scope: 'file', path: 'internal/reviews/service.go',
    status: 'resolved', minutesAgo: 50,
    replies: [
      { author_type: 'user', author_github_user_id: 'member-riley', body: 'Updated the refresh paths to return the error while keeping the previous review intact.', minutesAgo: 40 },
    ],
  },
  {
    id: 'comment-preview-1', author_type: 'user', author_github_user_id: 'member-ada',
    body: "Can we keep the current review visible while the new one loads?\n\nClearing it immediately makes the discussion jump. Keeping the previous content until the refresh completes would make this much easier to follow.",
    scope: 'line', path: "internal/reviews/service.go", side: 'RIGHT', line: 85, diff_hunk: "@@ -84,3 +84,7 @@\n review := loadReview(request.ID)\n-review.Refresh(ctx)\n+next, err := review.Refresh(ctx)\n+if err == nil {\n+    review = next\n+}\n+saveReview(review)\n return review\n",
    status: 'unresolved', minutesAgo: 48,
    replies: [
      { author_type: 'user', author_github_user_id: 'member-riley', body: "Agreed. I’ll replace the content only after the new review is ready, and keep the scroll position stable.", minutesAgo: 35 },
      { author_type: 'user', author_github_user_id: 'member-ada', body: "That sounds right. What happens if the file I’m reading disappears in the new revision?", minutesAgo: 33 },
      { author_type: 'user', author_github_user_id: 'member-riley', body: "I’d keep the discussion in place and show that the file was removed. We should only switch files when the reader chooses another one.", minutesAgo: 31 },
      { author_type: 'user', author_github_user_id: 'member-jonas', body: "Please keep the selected lines too. I often read a comment, scroll through the diff, and come back to it.", minutesAgo: 29 },
      { author_type: 'user', author_github_user_id: 'member-riley', body: "Yes. The file and line selection will survive the refresh. If those lines changed, the comment will keep its original context instead of silently moving.", minutesAgo: 27 },
      { author_type: 'user', author_github_user_id: 'member-ada', body: "Perfect. That keeps the conversation understandable even after several updates.", minutesAgo: 25 },
    ],
  },
  {
    id: 'comment-preview-2', author_type: 'agent',
    body: "A slower response could overwrite a newer review if two refreshes run together.\n\nCheck the revision before applying the result so the latest accepted review stays visible.",
    scope: 'line', path: "web/src/features/reviews/Review.tsx", side: 'RIGHT', line: 52, diff_hunk: "@@ -51,3 +51,4 @@\n async function refresh() {\n-  setReview(await refreshReview(id))\n+  const next = await refreshReview(id)\n+  setReview(next)\n }\n",
    status: 'unresolved', minutesAgo: 45,
    replies: [
      { author_type: 'user', author_github_user_id: 'member-riley', body: "Would comparing the revision number be enough, or should we cancel the earlier request too?", minutesAgo: 32 },
      { author_type: 'agent', body: "The revision check is enough to prevent stale content. Cancellation can save work, but it cannot guarantee that a response will not arrive.\n\nApply the result only when its revision is at least as new as the currently accepted review.", minutesAgo: 30 },
      { author_type: 'user', author_github_user_id: 'member-riley', body: "Understood. I’ll keep the guard at the point where the response is applied.", minutesAgo: 28 },
      { author_type: 'user', author_github_user_id: 'member-ada', body: "This should also cover a refresh started in another tab, right?", minutesAgo: 26 },
      { author_type: 'agent', body: "Yes, provided both responses carry the server revision. A local request counter would only cover requests from this tab.", minutesAgo: 24 },
    ],
  },
  {
    id: 'comment-preview-3', author_type: 'user', author_github_user_id: 'member-jonas',
    body: "The shorter refresh message reads much better. Could we use the same wording in the file view?",
    scope: 'pull_request',
    status: 'unresolved', minutesAgo: 42,
    replies: [
      { author_type: 'user', author_github_user_id: 'member-riley', body: "Do you mean “Updating review…” instead of “Synchronizing review context”?", minutesAgo: 29 },
      { author_type: 'user', author_github_user_id: 'member-jonas', body: "Exactly. The second one makes me stop and work out what is happening.", minutesAgo: 27 },
      { author_type: 'user', author_github_user_id: 'member-ada', body: "Agreed. We can leave the current content visible without adding an explanation to every refresh.", minutesAgo: 25 },
      { author_type: 'user', author_github_user_id: 'member-riley', body: "Updated both places.", minutesAgo: 23 },
    ],
  },
  {
    id: 'comment-preview-4', author_type: 'agent',
    body: "A failed refresh currently clears the loading indicator without telling the reader that the content is still from the previous revision.\n\nKeep the previous review, but show a small retry action near the refresh control.",
    scope: 'line', path: "internal/reviews/service.go", side: 'RIGHT', line: 112, diff_hunk: "@@ -111,3 +111,4 @@\n if err != nil {\n-    return nil\n+    keepPreviousReview()\n+    return err\n }\n",
    status: 'unresolved', minutesAgo: 39,
    replies: [
      { author_type: 'user', author_github_user_id: 'member-riley', body: "Would you put the error inside the discussion, or next to the refresh button?", minutesAgo: 26 },
      { author_type: 'user', author_github_user_id: 'member-ada', body: "Next to refresh. It belongs to the page state, and I don’t want it to interrupt the conversation.", minutesAgo: 24 },
      { author_type: 'user', author_github_user_id: 'member-riley', body: "That makes sense. I’ll use one line with a Retry action, and keep it there until the next successful refresh.", minutesAgo: 22 },
    ],
  },
  {
    id: 'comment-preview-5', author_type: 'user', author_github_user_id: 'member-ada',
    body: "One small detail: the empty state briefly appears before the first response arrives. Can we wait until we know there are no comments?",
    scope: 'line', path: "web/src/features/reviews/Review.tsx", side: 'RIGHT', line: 96, diff_hunk: "@@ -95,3 +95,3 @@\n return (\n-  reviews.length === 0 ? <EmptyState /> : <ReviewList />\n+  loaded && reviews.length === 0 ? <EmptyState /> : <ReviewList />\n )\n",
    status: 'unresolved', minutesAgo: 36,
    replies: [

    ],
  },
  {
    id: 'comment-preview-6', author_type: 'user', author_github_user_id: 'member-jonas',
    body: "I tried this with a longer review: twelve files, several resolved threads, and a conversation with eight replies.\n\nThe main thing I’m looking for is a clear break between conversations. Replies should feel connected, but the next person’s new topic should be obvious without having to reread the header.",
    scope: 'pull_request',
    status: 'unresolved', minutesAgo: 33,
    replies: [
      { author_type: 'user', author_github_user_id: 'member-ada', body: "Yes. I tend to lose my place when every reply looks like a new top-level comment.", minutesAgo: 20 },
      { author_type: 'user', author_github_user_id: 'member-riley', body: "Would a little more space between threads help more than a border around each one?", minutesAgo: 18 },
      { author_type: 'user', author_github_user_id: 'member-jonas', body: "Possibly. A very light border is helpful when the thread is long, though. I’d avoid boxes around individual replies.", minutesAgo: 16 },
      { author_type: 'user', author_github_user_id: 'member-ada', body: "Same here. One boundary per conversation, then let the text breathe inside it.", minutesAgo: 14 },
    ],
  },
  {
    id: 'comment-preview-resolved', author_type: 'user', author_github_user_id: 'member-jonas',
    body: 'Preserve the selected file after refresh', scope: 'pull_request', status: 'resolved', minutesAgo: 60,
    replies: [
      { author_type: 'user', author_github_user_id: 'member-riley', body: 'Done. The selection is preserved as long as the file still exists.', minutesAgo: 55 },
      { author_type: 'user', author_github_user_id: 'member-jonas', body: 'Checked the latest changes. This works well now.', minutesAgo: 50 },
    ],
  },
]

export const previewComments: PullRequestComment[] = previewConversations.flatMap(({ minutesAgo, replies, ...thread }) => {
  const createdAt = new Date(Date.now() - minutesAgo * 60000).toISOString()
  return [
    { ...thread, pull_request_id: 'pr-preview', original_head_commit: '54ce287b', created_at: createdAt, updated_at: createdAt },
    ...replies.map(({ minutesAgo: replyMinutes, ...reply }, index): PullRequestComment => {
      const repliedAt = new Date(Date.now() - replyMinutes * 60000).toISOString()
      return { ...reply, id: thread.id + '-reply-' + (index + 1), parent_comment_id: thread.id, pull_request_id: 'pr-preview', original_head_commit: '54ce287b', scope: 'pull_request', status: thread.status, created_at: repliedAt, updated_at: repliedAt }
    }),
  ]
})

export const previewReviews: PullRequestReview[] = [{
  id: 'review-preview', pull_request_id: 'pr-preview', session_id: 'holon-keyboard-audit', status: 'completed', base_branch: 'main', base_commit: '15ad9fe1',
  head_branch: 'pmaviro/group-agent-sessions', head_commit: '54ce287b', summary: 'The routing and pin refresh flow look coherent. One inline comment remains.',
  created_at: '2026-08-24T11:25:00Z', completed_at: '2026-08-24T11:32:00Z',
}]

const revisionIds = ['15ad9fe1', 'ac3e807', '3f2a1c4', '7e9d8b1', 'worktree']

export function previewInspectionForRange(base = 'main', target = 'worktree', baseBranch = 'main'): WorkspaceInspection {
  const branchBase = baseBranch === 'main' ? 0 : baseBranch === 'develop' ? 1 : 2
  const index = (ref: string, fallback: number) => {
    const found = revisionIds.indexOf(ref.replace('commit:', ''))
    return found < 0 ? fallback : found
  }
  const start = index(base, base === 'main' ? branchBase : 0)
  const end = index(target, 4)
  const files = fixtures.ranges[`${start}:${end}` as keyof typeof fixtures.ranges] ?? []
  return {
    branch: 'feature/workspace-navigation', base_branch: baseBranch, base_commit: revisionIds[start], head_commit: revisionIds[end],
    selected_base_commit: revisionIds[start], selected_target_commit: revisionIds[end],
    has_changes: files.length > 0, dirty: true, diff_truncated: false, branch_base_commit: revisionIds[branchBase],
    work_session_start_commit: revisionIds[0], workspace_head_commit: revisionIds[3],
    commits: [
      { author: 'Alex Morgan', authored_at: new Date(Date.now() - 12 * 60000).toISOString(), body: 'Make the active session discoverable to assistive technology and keep each tab tied to a stable session key.\n\nAdd an accessible label to the session navigation and expose the selected state on each button. This also avoids reusing the wrong tab when sessions are added or removed.', sha: revisionIds[3], parent_commit: revisionIds[2], subject: 'Make session selection accessible' },
      { author: 'Alex Morgan', authored_at: new Date(Date.now() - 28 * 60000).toISOString(), body: 'Remember the selected terminal separately for each workspace.\n\nRestore the saved selection when returning to a workspace, and fall back to the first agent session when no selection has been saved.', sha: revisionIds[2], parent_commit: revisionIds[1], subject: 'Remember the active terminal' },
      { author: 'Sam Lee', authored_at: new Date(Date.now() - 120 * 60000).toISOString(), body: 'Bring the session controls into the Soft workspace style with rounded tabs and a restrained selected background.', sha: revisionIds[1], parent_commit: revisionIds[0], subject: 'Soften workspace controls' },
    ].filter((commit) => revisionIds.indexOf(commit.sha) > branchBase),
    files: files as WorkspaceInspection['files'],
  }
}
export const previewInspection = previewInspectionForRange()
