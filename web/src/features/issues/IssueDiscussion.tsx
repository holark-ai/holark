import { MoreVertical, Pencil, Quote, Trash2 } from 'lucide-react'
import { useCallback, useEffect, useRef, useState } from 'react'
import { api, type ApiError } from '../../data/api'
import type { IssueComment, IssueDiscussion as Discussion } from '../../data/types'
import { Button } from '../../components/Button'
import { Dialog } from '../../components/Dialog'
import { MenuItem, MenuPopover } from '../../components/Menu'
import { useDropdownMenu } from '../../components/useDropdownMenu'
import { IssueMarkdown } from './IssueMarkdown'
import { MarkdownEditor } from '../attachments/MarkdownEditor'
import styles from './IssueDiscussion.module.css'
import { quoteComment, validBody } from './commentBody'

function message(error: unknown) { return error instanceof Error ? error.message : 'Discussion unavailable.' }
function safeURL(value: string) { return /^https?:\/\//i.test(value) ? value : undefined }
const emptyDiscussion: Discussion = { comments: [], synced_at: null, can_comment: null }

// Keying the state owner also isolates late responses from the previous issue.
export function IssueDiscussion({ issueId, githubURL }: { issueId: string; githubURL?: string }) {
  return <DiscussionContent key={issueId} issueId={issueId} githubURL={githubURL} />
}
function DiscussionContent({ issueId, githubURL }: { issueId: string; githubURL?: string }) {
  const [discussion, setDiscussion] = useState<Discussion>(emptyDiscussion)
  const [draft, setDraft] = useState('')
  const [draftVersion, setDraftVersion] = useState(0)
  const [uploadingDraft, setUploadingDraft] = useState(false)
  const [uploadingEdit, setUploadingEdit] = useState(false)
  const [editing, setEditing] = useState<{ id: string; body: string } | null>(null)
  const [deleting, setDeleting] = useState<IssueComment | null>(null)
  const [syncError, setSyncError] = useState('')
  const [mutationError, setMutationError] = useState('')
  const [recovery, setRecovery] = useState<{ url: string } | null>(null)
  const [activity, setActivity] = useState<'refresh' | 'mutation' | null>(null)
  const request = useRef<{ kind: 'refresh' | 'mutation'; done: Promise<void> } | null>(null)
  const composer = useRef<HTMLTextAreaElement>(null)
  const { openValue: openMenuId, rootRef: menuRef, close: closeMenu, toggle: toggleMenu } = useDropdownMenu('', true)
  const pending = activity === 'mutation' || uploadingDraft || uploadingEdit
  const syncing = activity === 'refresh'

  // Finish a refresh before publishing; coalesce refreshes and reject double submits.
  // The keyed component owns these requests, so late responses cannot affect another issue.
  const run = useCallback((kind: 'refresh' | 'mutation', operation: () => Promise<void>) => {
    const previous = request.current
    if (previous && (kind === 'refresh' || previous.kind === 'mutation')) return previous.done
    const current = { kind, done: Promise.resolve() }
    setActivity(kind)
    current.done = (previous?.done ?? Promise.resolve()).then(operation).finally(() => {
      if (request.current === current) { request.current = null; setActivity(null) }
    })
    request.current = current
    return current.done
  }, [])

  const refresh = useCallback((loadCache = false) => run('refresh', async () => {
    if (loadCache) {
      try { setDiscussion(await api.issueComments(issueId)) }
      catch (error) { setSyncError(message(error)) }
    }
    try {
      setDiscussion(await api.syncIssueComments(issueId))
      setSyncError('')
    } catch (error) { setSyncError(message(error)) }
  }), [issueId, run])

  useEffect(() => { void refresh(true) }, [refresh])

  const mutate = (operation: () => Promise<IssueComment | void>, done: (comment: IssueComment | void) => void) => run('mutation', async () => {
    setMutationError('')
    try { done(await operation()) }
    catch (error) {
      setMutationError(message(error))
      const e = error as ApiError
      if (e.code === 'issue_comment_projection_pending' || e.code === 'issue_comment_outcome_uncertain' || !e.status) {
        setRecovery({ url: e.github_comment_url || githubURL || '' })
        setDeleting(null)
      }
    }
  })
  const reconcile = (comment: IssueComment | void) => {
    if (!comment) return
    setDiscussion((current) => ({ ...current, comments: [...current.comments.filter((c) => c.id !== comment.id), comment].sort((a, b) => Date.parse(a.created_at) - Date.parse(b.created_at) || a.github_id.length - b.github_id.length || a.github_id.localeCompare(b.github_id)) }))
  }
  const canSaveEdit = editing !== null && discussion.comments.some((c) => c.id === editing.id && c.can_edit)
  const editor = editing && <form onSubmit={(e) => { e.preventDefault(); if (!pending && canSaveEdit && validBody(editing.body) && !recovery) void mutate(() => api.updateIssueComment(editing.id, editing.body), (c) => { reconcile(c); setEditing(null) }) }}>
    <label htmlFor={`edit-issue-comment-${editing.id}`}>Edit comment</label>
    <MarkdownEditor key={editing.id} id={`edit-issue-comment-${editing.id}`} autoFocus value={editing.body} disabled={pending} maxLength={65536} onChange={(body) => setEditing({ ...editing, body })} onUploadingChange={setUploadingEdit} />
    {!canSaveEdit && <p>GitHub no longer reports edit access. Your draft is kept.</p>}
    <div className={styles.actions}><Button type="submit" disabled={pending || recovery !== null || !canSaveEdit || !validBody(editing.body)}>Save comment</Button><Button type="button" disabled={pending} onClick={() => setEditing(null)}>Cancel edit</Button></div>
  </form>

  return <section className={styles.discussion} aria-label="Issue discussion">
    <div className={styles.heading}><h2>Discussion</h2><Button disabled={syncing || pending} onClick={() => void refresh()}>{syncing ? 'Refreshing…' : 'Refresh'}</Button></div>
    <p className={styles.meta}>{discussion.synced_at ? `Last synced ${new Date(discussion.synced_at).toLocaleString()}` : 'Discussion has not been synced.'}</p>
    {syncError && <p role="alert">{syncError} Cached comments are shown below.</p>}
    {mutationError && !deleting && <p role="alert">{mutationError}</p>}
    {recovery && <div role="status"><p>Your draft has been kept. Refresh or check GitHub to see whether the change was applied before attempting it again.</p>
      {safeURL(recovery.url) && <a href={safeURL(recovery.url)} target="_blank" rel="noreferrer">Check on GitHub</a>}
      <Button disabled={pending} onClick={() => { setRecovery(null); setMutationError('') }}>I checked GitHub; allow another attempt</Button>
    </div>}
    {discussion.comments.length === 0 && <p>No cached comments.</p>}
    {discussion.comments.map((comment) => <article key={comment.id} className={styles.comment}>
      <header className={styles.commentHeader}>
        <div className={styles.meta}>
          {safeURL(comment.author.avatar_url) && <img src={safeURL(comment.author.avatar_url)} alt="" width="24" height="24" />}
          {safeURL(comment.author.url) ? <a href={safeURL(comment.author.url)} target="_blank" rel="noreferrer">{comment.author.login || 'Unknown author'}</a> : <span>{comment.author.login || 'Unknown author'}</span>}
          <time dateTime={comment.created_at}>{new Date(comment.created_at).toLocaleString()}</time>
          {Date.parse(comment.updated_at) > Date.parse(comment.created_at) && <span title={`Updated ${new Date(comment.updated_at).toLocaleString()}`}>edited</span>}
          {safeURL(comment.url) && <a href={safeURL(comment.url)} target="_blank" rel="noreferrer">GitHub</a>}
        </div>
        {editing?.id !== comment.id && (
          <div className={styles.commentMenu} ref={openMenuId === comment.id ? menuRef : undefined}>
            <Button
              variant="ghost"
              size="small"
              className={styles.menuButton}
              aria-label="Comment actions"
              aria-haspopup="menu"
              aria-expanded={openMenuId === comment.id}
              disabled={pending}
              onClick={() => toggleMenu(comment.id)}
            >
              <MoreVertical />
            </Button>
            {openMenuId === comment.id && (
              <MenuPopover className={styles.menuPopover}>
                <MenuItem disabled={pending || discussion.can_comment === false} onClick={() => {
                  closeMenu()
                  setDraft((value) => value + (value && !value.endsWith('\n\n') ? '\n\n' : '') + quoteComment(comment.body))
                  composer.current?.focus()
                }}>
                  <Quote /> Quote reply
                </MenuItem>
                {comment.can_edit && <MenuItem disabled={pending || editing !== null} onClick={() => {
                  closeMenu()
                  setEditing({ id: comment.id, body: comment.body })
                }}>
                  <Pencil /> Edit
                </MenuItem>}
                {comment.can_delete && <MenuItem destructive disabled={pending || recovery !== null} onClick={() => {
                  closeMenu()
                  setMutationError('')
                  setDeleting(comment)
                }}>
                  <Trash2 /> Delete
                </MenuItem>}
              </MenuPopover>
            )}
          </div>
        )}
      </header>
      {editing?.id === comment.id ? editor : <IssueMarkdown body={comment.body} />}
    </article>)}
    {editing && !discussion.comments.some((c) => c.id === editing.id) && <div><p>This comment is no longer cached. Your edit draft is preserved.</p>{editor}</div>}
    <form className={styles.composer} onSubmit={(e) => { e.preventDefault(); if (!pending && validBody(draft) && discussion.can_comment !== false && !recovery) void mutate(() => api.createIssueComment(issueId, draft), (c) => { reconcile(c); setDraft(''); setDraftVersion((version) => version + 1) }) }}>
      <label htmlFor={`new-issue-comment-${issueId}`}>Add a comment</label>
      <MarkdownEditor id={`new-issue-comment-${issueId}`} ref={composer} value={draft} disabled={pending} maxLength={65536} onChange={setDraft} onUploadingChange={setUploadingDraft} previewResetKey={String(draftVersion)} placeholder="Write Markdown…" />
      <p className={styles.meta}>Markdown supported. Up to 65,536 characters.</p>
      {discussion.can_comment === false && <p>GitHub currently does not allow you to comment here. Refresh to check again.</p>}
      <Button type="submit" variant="primary" disabled={pending || recovery !== null || discussion.can_comment === false || !validBody(draft)}>{pending ? 'Saving…' : 'Comment'}</Button>
    </form>
    <Dialog open={deleting !== null} title="Delete comment?" onClose={() => { if (!pending) setDeleting(null) }} footer={<><Button disabled={pending} onClick={() => setDeleting(null)}>Cancel</Button><Button disabled={pending || recovery !== null} onClick={() => { if (deleting) void mutate(() => api.deleteIssueComment(deleting.id), () => { setDiscussion((d) => ({ ...d, comments: d.comments.filter((c) => c.id !== deleting.id) })); if (editing?.id === deleting.id) setEditing(null); setDeleting(null) }) }}>Delete comment</Button></>}>
      <p>This deletes the comment from GitHub and Holark.</p>
      {mutationError && <p role="alert">{mutationError}</p>}
    </Dialog>
  </section>
}
