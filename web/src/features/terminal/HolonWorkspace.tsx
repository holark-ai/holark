import type { ReactNode } from 'react'
import styles from './HolonTerminal.module.css'

export function HolonWorkspace({ header, changes, reviewing, children }: {
  header: ReactNode
  changes: ReactNode
  reviewing: boolean
  children: ReactNode
}) {
  return <div className={styles.page} data-holon-workspace>
    {header}
    <div className={styles.workspace}>
      {changes}
      <div className={styles.sessionSurface} inert={reviewing}>{children}</div>
    </div>
  </div>
}
