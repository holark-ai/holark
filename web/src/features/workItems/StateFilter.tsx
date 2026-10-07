import { Popover } from '@base-ui/react/popover'
import { ChevronDown } from 'lucide-react'
import styles from './SearchFilter.module.css'

const prStates = [['open', 'Open'], ['draft', 'Draft'], ['wip', 'WIP'], ['merged', 'Merged'], ['closed', 'Closed']] as const
type State = typeof prStates[number][0]
const lifecycleStates: Record<string, readonly State[]> = {
  active: ['wip', 'draft', 'open'], wip: ['wip'], open: ['draft', 'open'],
  closed: ['closed', 'merged'], merged: ['merged'], unmerged: ['wip', 'draft', 'open', 'closed'],
  all: prStates.map(([state]) => state),
}

export function StateFilter({ tokens, onChange, kind = 'pull_request' }: { tokens: string[]; onChange: (query: string) => void; kind?: 'issue' | 'pull_request' }) {
  const issues = kind === 'issue'
  const states = issues ? prStates.filter(([state]) => state === 'open' || state === 'closed') : prStates
  const lifecycleMap: Record<string, readonly State[]> = issues ? { open: ['open'], closed: ['closed'], all: ['open', 'closed'] } : lifecycleStates
  const explicit = tokens.find((token) => token.startsWith('state:'))?.slice(6).split(',')
  const lifecycle = tokens.filter((token) => token.startsWith('is:')).map((token) => token.slice(3))
  if (!explicit && lifecycle.length === 0) lifecycle.push(issues ? 'open' : 'active')
  const selected = states.filter(([state]) =>
    (!explicit || explicit.includes(state)) &&
    lifecycle.every((value) => lifecycleMap[value]?.includes(state)) &&
    (!tokens.includes('draft:false') || state !== 'draft') &&
    (!tokens.includes('draft:true') || state === 'draft' || state === 'closed'),
  ).map(([state]) => state)
  const summary = selected.length === states.length ? 'All' : selected.length === 0 ? 'None' : states.filter(([state]) => selected.includes(state)).map(([, label]) => label).join(', ')
  const update = (next: readonly State[]) => {
    const values = states.filter(([state]) => next.includes(state)).map(([state]) => state)
    const filter = values.length === states.length ? 'is:all' : `state:${values.join(',') || 'none'}`
    onChange([...tokens.filter((token) => !/^(is|state|draft):/.test(token)), filter].join(' '))
  }
  return <Popover.Root>
    <Popover.Trigger className={styles.trigger} aria-label="State" data-active={selected.length !== states.length} title={summary}>
      <span className={styles.label}>State</span><span className={styles.value}>{summary}</span><ChevronDown aria-hidden="true" />
    </Popover.Trigger>
    <Popover.Portal>
      <Popover.Positioner className={styles.positioner} sideOffset={6} align="start" collisionPadding={8}>
        <Popover.Popup className={`${styles.content} ${styles.stateContent}`} aria-label="Filter by state">
          <div className={styles.actions}>
            <button type="button" disabled={selected.length === states.length} onClick={() => update(states.map(([state]) => state))}>Select all</button>
            <button type="button" disabled={selected.length === 0} onClick={() => update([])}>Clear all</button>
          </div>
          <div className={styles.viewport}>
            {states.map(([state, label]) => <label key={state} className={`${styles.option} ${styles.checkboxOption}`} data-state={selected.includes(state) ? 'checked' : 'unchecked'}>
              <input type="checkbox" checked={selected.includes(state)} onChange={(event) => update(event.target.checked ? [...selected, state] : selected.filter((value) => value !== state))} />
              {label}
            </label>)}
          </div>
        </Popover.Popup>
      </Popover.Positioner>
    </Popover.Portal>
  </Popover.Root>
}
