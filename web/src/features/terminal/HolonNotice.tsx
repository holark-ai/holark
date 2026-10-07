import { CircleAlert, X } from 'lucide-react'
import type { ReactNode } from 'react'
import styles from './HolonTerminal.module.css'

export function HolonNotice({ children, onDismiss, onRetry, dismissLabel = 'Dismiss error message' }: {
  children: ReactNode
  onDismiss: () => void
  onRetry?: () => void
  dismissLabel?: string
}) {
  return (
    <div className={styles.notices} role="alert">
      <CircleAlert className={styles.noticeIcon} aria-hidden="true" />
      <div className={styles.noticeMessages}>{children}</div>
      {onRetry && <button className={styles.noticeRetry} type="button" onClick={onRetry}>Retry</button>}
      <button className={styles.noticeClose} type="button" aria-label={dismissLabel} onClick={onDismiss}>
        <X aria-hidden="true" />
      </button>
    </div>
  )
}
