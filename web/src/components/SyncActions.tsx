import type { ReactNode } from 'react'
import { CheckCircle2, LoaderCircle, RefreshCw, TriangleAlert, X } from 'lucide-react'
import { Button } from './Button'
import styles from './SyncActions.module.css'

type SyncActionsProps = {
  syncing: boolean
  message: string
  succeeded: boolean
  onSync: () => void
  onDismiss: () => void
  children?: ReactNode
}

export function SyncActions({ syncing, message, succeeded, onSync, onDismiss, children }: SyncActionsProps) {
  return <div className={styles.syncActions}>
    {!syncing && message && <span className={`${styles.syncResult} ${succeeded ? '' : styles.syncWarning}`} role={succeeded ? 'status' : 'alert'}>
      {succeeded ? <CheckCircle2 aria-hidden="true" /> : <TriangleAlert aria-hidden="true" />}
      <span>{message}</span>
      <button className={styles.syncResultDismiss} type="button" aria-label="Dismiss sync result" onClick={onDismiss}><X aria-hidden="true" /></button>
    </span>}
    <Button className={syncing ? styles.syncButton : undefined}
      icon={syncing ? <LoaderCircle aria-hidden="true" /> : <RefreshCw />} disabled={syncing}
      onClick={onSync}>{syncing ? 'Syncing with GitHub…' : 'Sync'}</Button>
    {children}
  </div>
}
