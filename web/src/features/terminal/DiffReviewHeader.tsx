import { ChevronsDownUp, ChevronsUpDown } from 'lucide-react'
import type { ReactNode } from 'react'
import type { DiffLayout } from './HolonDiffReview'
import styles from './HolonDiffPanel.module.css'

type Props = {
  title?: ReactNode
  children?: ReactNode
  stats: { files: number; additions: number; deletions: number }
  layout: DiffLayout
  onLayoutChange: (layout: DiffLayout) => void
  allFilesCollapsed: boolean
  onToggleCollapsed: () => void
}

export function DiffReviewHeader({ title, children, stats, layout, onLayoutChange, allFilesCollapsed, onToggleCollapsed }: Props) {
  return <header className={styles.reviewHeader}>
    {title && <div className={styles.reviewTitle}>{title}</div>}
    <div className={styles.fileTotals}><span>{stats.files} {stats.files === 1 ? 'file' : 'files'}</span><span className={styles.additions}>+{stats.additions}</span><span className={styles.deletions}>−{stats.deletions}</span></div>
    {children}
    <div className={styles.reviewActions}>
      <button className={styles.layoutChoice} type="button" onClick={() => onLayoutChange(layout === 'side-by-side' ? 'inline' : 'side-by-side')}>{layout === 'side-by-side' ? 'Inline' : 'Side by side'}</button>
      <button className={styles.collapseAll} type="button" title={allFilesCollapsed ? 'Expand all' : 'Collapse all'} aria-label={allFilesCollapsed ? 'Expand all' : 'Collapse all'} disabled={stats.files === 0} onClick={onToggleCollapsed}>
        {allFilesCollapsed ? <ChevronsUpDown aria-hidden="true" /> : <ChevronsDownUp aria-hidden="true" />}
      </button>
    </div>
  </header>
}
