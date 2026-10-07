import { TriangleAlert } from 'lucide-react'
import type { HarnessCapability } from '../../data/types'
import styles from './HarnessWarning.module.css'

export function HarnessWarning({ capability }: { capability?: HarnessCapability }) {
  if (!capability?.available || !capability.warning) return null
  return <p className={styles.warning} role="status">
    <TriangleAlert aria-hidden="true" />
    <span>{capability.warning}</span>
  </p>
}
