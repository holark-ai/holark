import { Bot, ChevronDown } from 'lucide-react'
import { Button } from '../../components/Button'
import { SplitButtonMenu, SplitButtonMenuItem } from '../../components/SplitButtonMenu'
import { useDropdownMenu } from '../../components/useDropdownMenu'
import styles from './PullRequestDetail.module.css'

export function ResolutionAction({ label = 'Resolve automatically', compact = false, primary = false, disabled, onResolve }: {
  label?: string
  compact?: boolean
  primary?: boolean
  disabled: boolean
  onResolve: (mode: 'auto' | 'assisted') => void
}) {
  const { open, rootRef, close, toggle } = useDropdownMenu(false, true)
  return (
    <div className={`${styles.resolutionAction} ${compact ? styles.compactResolution : ''} ${primary ? styles.primaryResolution : ''}`} ref={rootRef}>
      <Button icon={<Bot />} disabled={disabled} onClick={() => { close(); onResolve('auto') }}>{label}</Button>
      <button className={styles.splitChevron} type="button" aria-label={`${label} options`} aria-haspopup="menu" aria-expanded={open} disabled={disabled} onClick={() => toggle(true)}><ChevronDown /></button>
      {open && <SplitButtonMenu className={styles.resolutionMenu}>
        <SplitButtonMenuItem disabled={disabled} onClick={() => { close(); onResolve('assisted') }}><Bot />Assisted resolution</SplitButtonMenuItem>
      </SplitButtonMenu>}
    </div>
  )
}
