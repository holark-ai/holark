import { AlertTriangle, Check, Circle, CircleDashed, XCircle } from 'lucide-react'
import { sessionStatusClassName } from './holonRowDisplay'
import styles from './HolonStatusIcon.module.css'

export function HolonStatusIcon({ address, status, needsInput }: { address?: string, status: string, needsInput: boolean }) {
  const className = `${styles.sessionStatusIcon} ${address ? styles.addressedStatus : ''} ${sessionStatusClassName(status, needsInput)}`
  if (address) return (
    <span className={className} aria-hidden="true">
      <span className={styles.sessionAddress}>{address}</span>
      <span className={styles.sessionBadge}>{needsInput ? '!' : null}</span>
    </span>
  )
  if (needsInput) return <span className={className} aria-hidden="true"><AlertTriangle /></span>
  if (status === 'completed') return <span className={className} aria-hidden="true"><Check /></span>
  if (status === 'recovery_failed' || status === 'failed' || status === 'lost' || status === 'cancelled' || status === 'expired') return <span className={className} aria-hidden="true"><XCircle /></span>
  if (status === 'idle') return <span className={className} aria-hidden="true"><Circle /></span>
  return <span className={className} aria-hidden="true"><CircleDashed /></span>
}
