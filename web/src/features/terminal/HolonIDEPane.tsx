import { Code2, RefreshCw } from 'lucide-react'
import { useColorTheme } from '../../app/colorTheme'
import type { HolonIDE } from '../../data/types'
import { api } from '../../data/api'
import styles from './HolonTerminal.module.css'

type HolonIDEPaneProps = {
  holonId: string
  ide: HolonIDE
  active: boolean
  activated: boolean
  retrying?: boolean
  retrySuspended?: boolean
  onRetry: (ide: HolonIDE) => void
}

export function HolonIDEPane({ holonId, ide, active, activated, retrying = false, retrySuspended = false, onRetry }: HolonIDEPaneProps) {
  const { theme } = useColorTheme()
  const ready = ide.state === 'ready'
  const unavailable = ide.state === 'failed' || ide.state === 'lost' || (ide.state === 'suspended' && retrySuspended)
  const stopping = ide.state === 'stopping'
  const suspended = ide.state === 'suspended'

  return (
    <div
      className={styles.ideFrame}
      data-active={active}
      aria-hidden={!active}
      role="tabpanel"
      aria-label="VS Code IDE"
    >
      {ready && activated ? (
        <iframe
          className={styles.ideIframe}
          src={`${api.sessionIDEProxyURL(holonId, ide.id)}?holark-theme=${theme}`}
          title="VS Code IDE"
          allow="clipboard-read; clipboard-write"
        />
      ) : unavailable ? (
        <div className={styles.ideState} role="alert">
          <Code2 />
          <h2>{ide.state === 'lost' ? 'IDE connection lost' : ide.state === 'suspended' ? 'IDE did not reopen' : 'IDE failed to start'}</h2>
          <p>{ide.reason || (ide.state === 'lost' ? 'Holark restarted. Reopen the IDE when you are ready.' : 'Check the local IDE logs and retry.')}</p>
          <button type="button" disabled={retrying} onClick={() => onRetry(ide)}>
            <RefreshCw /> {retrying ? 'Retrying...' : 'Retry'}
          </button>
        </div>
      ) : stopping ? (
        <div className={styles.ideState} aria-busy="true">
          <Code2 />
          <h2>Stopping VS Code</h2>
          <p>The current runtime is shutting down safely.</p>
          <span className={styles.ideSpinner} aria-hidden="true" />
        </div>
      ) : suspended ? (
        <div className={styles.ideState}>
          <Code2 />
          <h2>VS Code stopped</h2>
          <p>It will reopen when the holon resumes.</p>
        </div>
      ) : (
        <div className={styles.ideState} aria-busy="true">
          <Code2 />
          <h2>Starting VS Code</h2>
          <p>Holark is preparing the worktree and private IDE listener.</p>
          <span className={styles.ideSpinner} aria-hidden="true" />
        </div>
      )}
    </div>
  )
}
