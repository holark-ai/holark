import { Code, File } from 'lucide-react'
import type { PullRequestComment } from '../../data/types'
import { selectDiffExcerpt } from './unifiedDiff'
import styles from './PullRequestThreadContext.module.css'

export function PullRequestThreadLocation({ comment, onViewInChanges }: {
  comment: PullRequestComment
  onViewInChanges?: () => void
}) {
  if (comment.scope !== 'file' && comment.scope !== 'line') {
    return <span className={`${styles.scopeLabel} ${styles.generalScope}`}>PR comment</span>
  }

  const location = comment.path ?? ''
  const filenameStart = location.lastIndexOf('/') + 1
  const pathLabel = <code><span className={styles.directory}>{location.slice(0, filenameStart)}</span><span className={styles.filename}>{location.slice(filenameStart)}</span></code>

  return (
    <span className={styles.threadLocation}>
      {comment.scope === 'file' ? <File aria-hidden="true" /> : <Code aria-hidden="true" />}
      {onViewInChanges
        ? <button className={styles.locationButton} type="button" onClick={onViewInChanges} aria-label={location} title={`View ${location} in Changes`}>{pathLabel}</button>
        : <span className={styles.locationText} title={location}>{pathLabel}</span>}
    </span>
  )
}

export function PullRequestThreadContext({ comment, showExcerpt }: {
  comment: PullRequestComment
  showExcerpt: boolean
}) {
  if (comment.scope !== 'file' && comment.scope !== 'line') return null
  const side = comment.side === 'LEFT' || comment.side === 'RIGHT' ? comment.side : undefined
  const excerpt = showExcerpt && comment.scope === 'line' && side && comment.line !== undefined
    ? selectDiffExcerpt(comment.diff_hunk, side, comment.line)
    : []

  if (excerpt.length === 0) return null

  return (
    <div className={styles.threadContext}>
      <div aria-label={`Saved diff excerpt for ${comment.path}`} className={styles.threadExcerpt} tabIndex={0}>
        {excerpt.map((row, index) => (
          <div
            className={`${styles.threadExcerptRow} ${styles[row.kind] ?? ''}`}
            data-commented={row.commented || undefined}
            key={`${row.oldLine ?? ''}:${row.newLine ?? ''}:${index}`}
          >
            {row.commented && <span className={styles.srOnly}>Commented line. </span>}
            <span aria-hidden="true" className={styles.threadLineNumber}>{row.oldLine ?? ''}</span>
            <span aria-hidden="true" className={styles.threadLineNumber}>{row.newLine ?? ''}</span>
            <span className={styles.diffSign}>{row.text[0]}</span>
            <code>{row.text.slice(1) || ' '}</code>
          </div>
        ))}
      </div>
    </div>
  )
}
