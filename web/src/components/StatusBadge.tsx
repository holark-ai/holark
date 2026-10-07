import styles from './StatusBadge.module.css'

type StatusBadgeProps = {
  status: string
  label?: string
  prominent?: boolean
}

export function StatusBadge({ status, label, prominent = false }: StatusBadgeProps) {
  return (
    <span className={`${styles.badge} ${styles[status] ?? styles.neutral} ${prominent ? styles.prominent : ''}`}>
      <span className={styles.dot} aria-hidden="true" />
      {label ?? status.replace(/[-_]/g, ' ')}
    </span>
  )
}
