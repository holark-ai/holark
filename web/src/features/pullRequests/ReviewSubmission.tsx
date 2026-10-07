import { Check, CheckCircle2, ExternalLink, Eye, MessageSquare, Pencil } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { Button } from '../../components/Button'
import { Dialog } from '../../components/Dialog'
import { api, type ApiError } from '../../data/api'
import type { ManualPullRequestReview, ManualReviewSubmission, PullRequest } from '../../data/types'
import { PullRequestMarkdown } from './PullRequestMetadataCard'
import styles from './ReviewSubmission.module.css'

export function useManualReviews(pullRequestId: string) {
  const [reviews, setReviews] = useState<ManualPullRequestReview[]>([])
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)
  const [reloadToken, setReloadToken] = useState(0)
  useEffect(() => {
    const controller = new AbortController()
    setLoading(true)
    setError('')
    void api.manualPullRequestReviews(pullRequestId, controller.signal).then((items) => {
      if (!controller.signal.aborted) setReviews((current) => mergeReviews(Array.isArray(items) ? items : [], current))
    }).catch((value) => { if (!controller.signal.aborted) setError(message(value)) }).finally(() => {
      if (!controller.signal.aborted) setLoading(false)
    })
    return () => { controller.abort() }
  }, [pullRequestId, reloadToken])
  const reload = () => setReloadToken((token) => token + 1)
  const accept = (review: ManualPullRequestReview) => {
    setError('')
    setReviews((current) => [...current.filter((item) => item.id !== review.id), review])
  }
  return { reviews, error, loading, reload, accept }
}

function mergeReviews(history: ManualPullRequestReview[], accepted: ManualPullRequestReview[]) {
  const byId = new Map(history.map((review) => [review.id, review]))
  accepted.forEach((review) => byId.set(review.id, review))
  return [...byId.values()]
}

export function ReviewSubmission({ open, pullRequest, head, draftCount, onClose, onViewDrafts, onSubmit }: {
  open: boolean
  pullRequest: PullRequest
  head: string
  draftCount: number
  onClose: () => void
  onViewDrafts: () => void
  onSubmit: (input: ManualReviewSubmission) => Promise<void>
}) {
  const [event, setEvent] = useState<ManualReviewSubmission['event']>('comment')
  const [body, setBody] = useState('')
  const [preview, setPreview] = useState(false)
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState('')
  const [attempt, setAttempt] = useState<ManualReviewSubmission>()
  const inFlight = useRef(false)
  const textarea = useRef<HTMLTextAreaElement>(null)
  const stale = head !== pullRequest.head_commit || Boolean(pullRequest.comparison_state && pullRequest.comparison_state !== 'ready')
  const readOnly = !['wip', 'draft', 'open'].includes(pullRequest.status)
  const newSubmissionBlocked = stale || readOnly || !head || (event === 'comment' && !body.trim() && !draftCount)
  // Saved attempts remain retryable after polling removes accepted drafts, or
  // when recovering a receipt for an older or closed PR.
  const disabled = submitting || (!attempt && newSubmissionBlocked)
  const submit = async () => {
    if (disabled || inFlight.current) return
    inFlight.current = true
    setSubmitting(true)
    setError('')
    const input = attempt ?? { id: crypto.randomUUID(), event, body: body.trim(), head_commit: head }
    setAttempt(input)
    try {
      await onSubmit(input)
      setAttempt(undefined)
      setBody('')
      setPreview(false)
      onClose()
    } catch (value) {
      // Only a rejection before anything was accepted allows editing. Partial
      // or uncertain submissions retain their payload and ID for Retry.
      const failure = value as ApiError & { retainSubmission?: boolean }
      if (failure?.status && failure.status < 500 && !failure.retainSubmission) setAttempt(undefined)
      setError(message(value))
    } finally {
      inFlight.current = false
      setSubmitting(false)
    }
  }
  const frozen = submitting || Boolean(attempt)
  const label = event === 'approve'
    ? draftCount ? `Publish ${draftCount} & approve` : 'Approve pull request'
    : draftCount ? `Publish ${draftCount} ${draftCount === 1 ? 'comment' : 'comments'}` : 'Publish review'
  return <Dialog open={open} title="Submit review" initialFocusRef={textarea} onClose={() => { if (!submitting) onClose() }}
    onKeyDownCapture={(e) => { if ((e.metaKey || e.ctrlKey) && e.key === 'Enter' && !e.nativeEvent.isComposing) { e.preventDefault(); void submit() } }}
    footer={<><Button disabled={submitting} onClick={onClose}>Cancel</Button><Button disabled={disabled} icon={event === 'approve' ? <Check /> : <MessageSquare />} onClick={() => void submit()}>{submitting ? 'Submitting…' : attempt ? 'Retry submission' : label}</Button></>}>
    <div className={styles.form}>
      <fieldset className={styles.verdict} disabled={frozen}>
        <legend className="sr-only">Review outcome</legend>
        <label><input type="radio" name="review-outcome" checked={event === 'comment'} onChange={() => setEvent('comment')} /><MessageSquare /><span>Comment</span></label>
        <label><input type="radio" name="review-outcome" checked={event === 'approve'} onChange={() => setEvent('approve')} /><CheckCircle2 /><span>Approve</span></label>
      </fieldset>
      {draftCount > 0 && <button type="button" className={styles.draftSummary} disabled={submitting} onClick={onViewDrafts}><span>{draftCount} draft {draftCount === 1 ? 'comment' : 'comments'} included</span><span>Review drafts</span></button>}
      <div className={styles.editor}>
        <div className={styles.editorHeading}><label htmlFor="review-note">{event === 'approve' ? 'Approval note' : 'Review summary'} {(event === 'approve' || draftCount > 0) && <span>(optional)</span>}</label><Button size="small" variant="ghost" icon={preview ? <Pencil /> : <Eye />} aria-label={preview ? 'Edit review note' : 'Preview review note'} aria-pressed={preview} onClick={() => setPreview(!preview)} /></div>
        {preview ? <div className={styles.preview}>{body.trim() ? <PullRequestMarkdown value={body.trim()} /> : <p>Nothing to preview yet.</p>}</div> : <textarea id="review-note" ref={textarea} disabled={frozen} maxLength={4000} rows={5} value={body} onChange={(e) => setBody(e.target.value)} placeholder={event === 'approve' ? 'Add a note, or approve without one…' : 'Summarize your review…'} />}
      </div>
      <p className={styles.hint}>{pullRequest.sync_provider === 'github' ? 'Publishes to GitHub' : 'Saves to this pull request'} · <code>{head.slice(0, 8)}</code><span>⌘ / Ctrl + Enter</span></p>
      {stale && <p className={styles.error} role="alert">The PR changed. Review the latest changes before submitting.</p>}
      {readOnly && <p className={styles.error} role="alert">This pull request is read-only.</p>}
      {error && <p className={styles.error} role="alert">{error}</p>}
    </div>
  </Dialog>
}

export function ManualReviewActivity({ reviews, historyError, historyLoading, onReload, onRetry }: { reviews: ManualPullRequestReview[], historyError: string, historyLoading: boolean, onReload: () => void, onRetry: (id: string) => Promise<void> }) {
  const [retrying, setRetrying] = useState('')
  const [error, setError] = useState('')
  const retry = async (id: string) => {
    if (retrying) return
    setRetrying(id)
    setError('')
    try { await onRetry(id) } catch (value) { setError(message(value)) } finally { setRetrying('') }
  }
  if (!reviews.length && !historyError) return null
  return <section className={styles.activity} aria-label="Submitted reviews">
    {historyError && <p className={styles.error} role="alert">Could not load saved reviews: {historyError} <Button size="small" disabled={historyLoading} onClick={onReload}>{historyLoading ? 'Loading…' : 'Retry loading'}</Button></p>}
    {reviews.map((review) => <article key={review.id} className={styles.review}>
      <div className={styles.reviewHeading}>{review.event === 'approve' ? <CheckCircle2 /> : <MessageSquare />}<strong>{review.state === 'failed' || review.state === 'pending' ? review.event === 'approve' ? 'Approval draft' : 'Review draft' : review.event === 'approve' ? 'You approved' : 'You reviewed'}</strong><code>{review.head_commit.slice(0, 8)}</code><time dateTime={review.created_at} title={new Date(review.created_at).toLocaleString()}>{new Date(review.created_at).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}</time>{review.url && <a href={review.url} target="_blank" rel="noreferrer" aria-label="View submitted review on GitHub"><ExternalLink /></a>}</div>
      {review.body && <PullRequestMarkdown value={review.body} />}
      {(review.state === 'failed' || review.state === 'pending') && <div className={styles.delivery}><span>{review.state === 'failed' ? `Saved locally. ${review.error || 'GitHub publication failed.'}` : 'GitHub publication is unconfirmed.'}</span><Button size="small" disabled={Boolean(retrying)} onClick={() => void retry(review.id)}>{retrying === review.id ? 'Checking…' : 'Retry publication'}</Button></div>}
    </article>)}
    {error && <p className={styles.error} role="alert">{error}</p>}
  </section>
}

function message(value: unknown) { return value instanceof Error ? value.message : 'Could not submit the review.' }
