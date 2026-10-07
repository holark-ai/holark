import { Code2, SquareTerminal } from 'lucide-react'
import type { ReactNode } from 'react'
import { harnessDisplayLabel, harnessTypes } from '../../data/harness'
import type { HarnessType } from '../../data/types'
import { HarnessTabIcon } from './HarnessTabIcon'
import styles from './HolonTerminal.module.css'

type HolonTabOptionsProps = {
  onAddAgent: (type: HarnessType) => void
  onAddTerminal: () => void
  onAddIDE: () => void
  agentDisabled?: (type: HarnessType) => boolean
  terminalDisabled?: boolean
  ideDisabled?: boolean
  ideUnavailableReason?: string
  agentNotice?: ReactNode
}

export function HolonTabOptions({ onAddAgent, onAddTerminal, onAddIDE, agentDisabled, terminalDisabled, ideDisabled, ideUnavailableReason, agentNotice }: HolonTabOptionsProps) {
  return <>
    {harnessTypes.map((type) => (
      <button key={type} type="button" disabled={agentDisabled?.(type)} onClick={() => onAddAgent(type)}>
        <HarnessTabIcon harnessType={type} />{harnessDisplayLabel(type)}
      </button>
    ))}
    {agentNotice}
    <div className={styles.addTabSeparator} />
    <button type="button" data-default-tab-option disabled={terminalDisabled} onClick={onAddTerminal}><SquareTerminal />Terminal</button>
    <button type="button" disabled={ideDisabled} title={ideUnavailableReason} onClick={onAddIDE}><Code2 />IDE</button>
  </>
}
