import { useState } from 'react'
import { MarkdownEditor } from '../attachments/MarkdownEditor'
import detail from './PullRequestDetail.module.css'
import { Button } from '../../components/Button'
import type { PullRequestCommentLocation } from '../../data/types'
import { commentLocationLabel, useCommentDrafts } from './useCommentDrafts'
import styles from './PullRequests.module.css'

export function CommentComposer({ location, drafts, onViewComment, onDiscard, autoFocus = false }: {
  location: PullRequestCommentLocation
  drafts: ReturnType<typeof useCommentDrafts>
  onViewComment: (id: string) => void
  onDiscard?: () => void
  autoFocus?: boolean
}) {
  const [uploading, setUploading] = useState(false)
  const draft = drafts.get(location)
  const label = commentLocationLabel(location)
  const scoped = location.scope === 'file' || location.scope === 'line'
  return (
    <form aria-label={`Comment on ${label}`} className={`${styles.commentForm} ${detail.composer} ${scoped ? detail.scopedComposer : ''}`} onKeyDown={(event) => { if ((event.metaKey || event.ctrlKey) && event.key === 'Enter' && !event.nativeEvent.isComposing) { event.preventDefault(); event.currentTarget.requestSubmit() } }} onSubmit={(event) => { event.preventDefault(); if (!uploading) void drafts.post(location) }}>
      <strong className={detail.composerLabel}>{label}</strong>
      <MarkdownEditor aria-label={scoped ? `Comment on ${label}` : 'Comment'} autoFocus={autoFocus} autoResize disabled={draft.posting} value={draft.body} onChange={(body) => drafts.change(location, body)} onUploadingChange={setUploading} maxLength={4000} rows={3} placeholder="Add a comment..."
        previewLabel="Preview comment" editLabel="Edit comment draft" previewRegionLabel="Comment preview" previewResetKey={draft.postedId} actions={<>
          <Button variant="ghost" size="small" disabled={draft.posting || uploading} onClick={() => { drafts.discard(location); onDiscard?.() }}>Discard</Button>
          <Button size="small" disabled={draft.posting || uploading || !draft.body.trim()} type="submit">{draft.posting ? 'Saving…' : 'Save draft'}</Button>
        </>} />
      {draft.error && <p role="alert" className={`${detail.composerNotice} ${styles.formError}`}>{draft.error}</p>}
      {draft.postedId && <p role="status" className={detail.composerNotice}>Draft saved. <button className={detail.commentTextAction} type="button" onClick={() => onViewComment(draft.postedId!)}>View comment</button></p>}
    </form>
  )
}
