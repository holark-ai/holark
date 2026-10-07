import { ChevronDown, GitMerge, GitPullRequest, RotateCcw, XCircle } from 'lucide-react'
import { useId } from 'react'
import type { PullRequestStatus, PullRequestTransitionStatus } from '../../data/types'
import { lifecycleCommands, type LifecycleCommand } from './lifecycle'
import { MenuItem, MenuPopover } from '../../components/Menu'
import { useDropdownMenu } from '../../components/useDropdownMenu'
import styles from './PullRequestLifecycleAction.module.css'

type PullRequestLifecycleActionProps = {
  status: PullRequestStatus
  mergeable: boolean
  mergeBlockedReason?: string
  busy: boolean
  publicationBlocked?: boolean
  onMerge: () => void
  onTransition: (status: PullRequestTransitionStatus) => void
  onClose: () => void
}

export function PullRequestLifecycleAction({
  status,
  mergeable,
  mergeBlockedReason,
  busy,
  publicationBlocked = false,
  onMerge,
  onTransition,
  onClose,
}: PullRequestLifecycleActionProps) {
	const { open, rootRef, close, toggle } = useDropdownMenu(false, true)
	const menuOpen = open && !busy
	const tooltipId = useId()
  const { primary, alternatives, tooltip: lifecycleTooltip } = lifecycleCommands(status, mergeable, mergeBlockedReason)
  const publicationUnavailable = (command: LifecycleCommand) => (status === 'wip' || status === 'draft') && publicationBlocked && Boolean(command.transition)
  const tooltip = publicationUnavailable(primary) ? 'Pull request opening is pending. Complete or retry metadata preparation.' : lifecycleTooltip
  const primaryUnavailable = !primary.transition && !primary.merge
  const primaryDisabled = busy || primaryUnavailable || publicationUnavailable(primary)

	const runCommand = (command: LifecycleCommand) => {
		close()
    if (command.close) {
      onClose()
    } else if (command.merge) {
      onMerge()
    } else if (command.transition) {
      onTransition(command.transition)
    }
  }

  return (
		<div className={styles.root} ref={rootRef}>
      <div className={styles.split}>
        <span
          aria-describedby={primaryDisabled && tooltip ? tooltipId : undefined}
          className={styles.primaryWrapper}
          tabIndex={primaryDisabled && tooltip ? 0 : undefined}
        >
          <button
            className={`${styles.primary} ${alternatives.length === 0 ? styles.primaryOnly : ''}`}
            disabled={primaryDisabled}
            onClick={() => runCommand(primary)}
            type="button"
          >
				{commandIcon(primary)}{primary.label}
          </button>
          {primaryDisabled && tooltip && <span className={styles.tooltip} id={tooltipId} role="tooltip">{tooltip}</span>}
        </span>
        {alternatives.length > 0 && (
          <button
            aria-expanded={menuOpen}
            aria-haspopup="menu"
            aria-label="Lifecycle options"
            className={styles.menuButton}
            disabled={busy}
				onClick={() => toggle(true)}
            type="button"
          >
            <ChevronDown />
          </button>
        )}
      </div>
      {menuOpen && (
		<MenuPopover className={styles.menu}>
			{alternatives.map((command) => (
				<MenuItem
					disabled={publicationUnavailable(command)}
					destructive={command.close}
					key={command.label}
					onClick={() => runCommand(command)}
				>
					{commandIcon(command)}{command.label}
				</MenuItem>
			))}
		</MenuPopover>
      )}
    </div>
  )
}

function commandIcon(command: LifecycleCommand) {
	switch (command.icon) {
		case 'pull-request': return <GitPullRequest />
		case 'retry': return <RotateCcw />
		case 'merge': return <GitMerge />
		case 'close': return <XCircle />
	}
}
