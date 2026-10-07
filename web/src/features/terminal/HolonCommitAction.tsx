import { GitCommitHorizontal } from 'lucide-react'
import { useId, useState, type ReactNode } from 'react'
import { Button } from '../../components/Button'
import styles from './HolonTerminal.module.css'

type HolonCommitActionProps = {
  agentSelected: boolean
  changesOpen: boolean
  message?: string
  reason?: string
  starting?: boolean
  onClick: () => void
  label?: string
  title?: string
  icon?: ReactNode
  variant?: 'ai' | 'ghost'
}

export function HolonCommitAction({
  agentSelected, changesOpen, message = '', reason = '', starting = false, onClick,
  label = 'Commit', title, icon = <GitCommitHorizontal />, variant = 'ai',
}: HolonCommitActionProps) {
  const tooltipId = useId()
  const [showTooltip, setShowTooltip] = useState(false)
  if (!agentSelected || changesOpen || message.length > 0) return null

  return (
    <span className={styles.commitAction} tabIndex={reason ? 0 : undefined}
      role={reason ? 'group' : undefined} aria-label={reason ? 'Commit unavailable' : undefined}
      aria-describedby={reason ? tooltipId : undefined}
      onMouseEnter={() => setShowTooltip(true)} onMouseLeave={() => setShowTooltip(false)}
      onFocus={() => setShowTooltip(true)} onBlur={() => setShowTooltip(false)}
      onKeyDown={(event) => { if (event.key === 'Escape') setShowTooltip(false) }}>
      <Button variant={variant} size="small" icon={icon} title={title} disabled={Boolean(reason) || starting} onClick={onClick}>
        {starting ? 'Starting…' : label}
      </Button>
      {reason && <span id={tooltipId} role="tooltip" className={styles.commitTooltip} hidden={!showTooltip}>{reason}</span>}
    </span>
  )
}
