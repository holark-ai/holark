import { useEffect, useState } from 'react'
import { useLocation, useNavigationType } from 'react-router-dom'
import { Popover } from '@base-ui/react/popover'
import { ChevronDown, Plus, X } from 'lucide-react'
import { api } from '../../data/api'
import type { IssueLabel } from '../../data/types'
import { Button } from '../../components/Button'
import { labelGroupsFromQuery, labelGroupsText, type LabelGroups } from './searchQuery'
import filterStyles from './SearchFilter.module.css'
import styles from './LabelFilter.module.css'

export function LabelFilter({ tokens, onChange, onEditAdvanced }: { tokens: string[]; onChange: (query: string) => void; onEditAdvanced: () => void }) {
  const [open, setOpen] = useState(false)
  const [groups, setGroups] = useState<LabelGroups>([[]])
  const [editingGroup, setEditingGroup] = useState<number | null>(0)
  const [labels, setLabels] = useState<IssueLabel[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [parseError, setParseError] = useState('')
  const [retry, setRetry] = useState(0)
  const location = useLocation()
  const navigationType = useNavigationType()
  const [locationKey, setLocationKey] = useState(location.key)
  if (locationKey !== location.key) {
    setLocationKey(location.key)
    // History navigation invalidates the draft; normal picker edits push a new URL.
    if (navigationType === 'POP') setOpen(false)
  }
  const active = tokens.some((token) => token.startsWith('label:'))
  let summary = 'Any label'
  let caption = 'Any label'
  try {
    const applied = labelGroupsFromQuery(tokens)
    if (applied.length) {
      summary = applied.map((group) => group.length === 1 ? group[0] : `All of: ${group.join(', ')}`).join(' or ')
      caption = `${applied.length} ${applied.length === 1 ? 'group' : 'groups'}`
    }
  } catch { summary = caption = 'Custom search' }
  useEffect(() => {
    if (!open) return
    let cancelled = false
    api.searchIssueLabels().then((value) => { if (!cancelled) setLabels(value) }, (value: unknown) => {
      if (!cancelled) setError(value instanceof Error ? value.message : 'Could not load cached labels.')
    }).finally(() => { if (!cancelled) setLoading(false) })
    return () => { cancelled = true }
  }, [open, retry])
  const changeOpen = (next: boolean) => {
    if (next) {
      setLoading(true); setError('')
      try {
        const applied = labelGroupsFromQuery(tokens)
        setGroups(applied.length ? applied : [[]])
        setEditingGroup(applied.length ? null : 0)
        setParseError('')
      } catch (value) {
        setGroups([[]]); setEditingGroup(0)
        setParseError(value instanceof Error ? value.message : 'Invalid label expression')
      }
    }
    setOpen(next)
  }
  const update = (next: LabelGroups) => {
    setGroups(next)
    setParseError('')
    // An empty group is only a place to choose labels, never a matching alternative.
    const expression = labelGroupsText(next)
    if (!parseError && expression === labelGroupsText(groups)) return
    const rest = tokens.filter((token) => !token.startsWith('label:'))
    if (expression) rest.push(`label:${expression}`)
    onChange(rest.join(' '))
  }
  const toggle = (index: number, name: string, checked: boolean) => update(groups.map((group, i) => i !== index ? group : checked
    ? [...group, name]
    : group.filter((selected) => selected !== name)))
  const removeGroup = (index: number) => {
    const next = groups.filter((_, i) => i !== index)
    update(next.length ? next : [[]])
    setEditingGroup(!next.length ? 0 : editingGroup === index ? null : editingGroup !== null && editingGroup > index ? editingGroup - 1 : editingGroup)
  }
  const addGroup = () => {
    const empty = groups.findIndex((group) => !group.length)
    if (empty >= 0) { setEditingGroup(empty); return }
    setEditingGroup(groups.length)
    setGroups([...groups, []])
  }
  return <Popover.Root open={open} onOpenChange={changeOpen}>
    <Popover.Trigger className={filterStyles.trigger} aria-label="Labels" data-active={active} title={summary}>
      <span className={filterStyles.label}>Labels</span><span className={filterStyles.value}>{caption}</span><ChevronDown aria-hidden="true" />
    </Popover.Trigger>
    <Popover.Portal>
      <Popover.Positioner className={filterStyles.positioner} sideOffset={6} align="start" collisionPadding={8}>
        <Popover.Popup className={styles.popup} aria-label="Filter labels">
          <div className={styles.heading}><Popover.Title>Labels</Popover.Title><Button size="small" variant="ghost" onClick={() => { update([[]]); setEditingGroup(0) }}>Clear</Button></div>
          <div className={styles.body}>
            {parseError ? <div><p role="alert">{parseError}. Edit advanced filters or clear this label filter.</p><Button size="small" onClick={() => { setOpen(false); onEditAdvanced() }}>Edit advanced filters</Button></div> : <>
              <p className={styles.explanation}><strong>Match any group</strong><span>Every label in a group must match.</span></p>
              <div className={styles.groups}>
                {groups.map((group, index) => <fieldset className={styles.group} key={index}>
                  <legend className="sr-only">Label group {index + 1}</legend>
                  <div className={styles.groupHeading}>
                    <span>{group.length > 1 ? 'All of' : group.length ? 'Has label' : 'Choose labels'}</span>
                    <button type="button" className={styles.removeGroup} aria-label={`Remove group ${index + 1}`} onClick={() => removeGroup(index)}><X aria-hidden="true" /></button>
                  </div>
                  <div className={styles.chips}>
                    {group.map((name) => <button key={name} type="button" className={styles.chip} aria-label={`Remove label ${name}`} onClick={() => toggle(index, name, false)}>
                      <LabelSwatch color={labels.find((label) => label.name === name)?.color} />
                      <span>{name}</span><X aria-hidden="true" />
                    </button>)}
                  </div>
                  {editingGroup === index ? <div className={styles.picker}>
                    <LabelPicker key={index} labels={labels} selected={group} onToggle={(name, checked) => toggle(index, name, checked)} />
                    <button type="button" className={styles.done} onClick={() => setEditingGroup(null)}>Done</button>
                  </div> : <button type="button" className={styles.addLabel} title="Require another label in this group" onClick={() => setEditingGroup(index)}><Plus aria-hidden="true" /> Add label</button>}
                </fieldset>)}
              </div>
              <button type="button" className={styles.addGroup} title="Add another way to match" onClick={addGroup}><Plus aria-hidden="true" /> Add group</button>
            </>}
            {loading && <p role="status">Loading cached labels…</p>}
            {error && <p role="alert">{error} <Button size="small" onClick={() => { setLoading(true); setError(''); setRetry((value) => value + 1) }}>Retry</Button></p>}
            {!loading && !error && labels.length === 0 && <p>No cached labels yet. Sync issues to import labels.</p>}
          </div>
        </Popover.Popup>
      </Popover.Positioner>
    </Popover.Portal>
  </Popover.Root>
}

function LabelSwatch({ color }: { color?: string }) {
  return <span className={styles.swatch} style={{ backgroundColor: color && /^[0-9a-f]{6}$/i.test(color) ? `#${color}` : undefined }} />
}

function LabelPicker({ labels, selected, onToggle }: { labels: IssueLabel[]; selected: string[]; onToggle: (name: string, checked: boolean) => void }) {
  const [search, setSearch] = useState('')
  const known = new Set(labels.map((label) => label.name))
  const options = [...labels, ...selected.filter((name) => !known.has(name)).map((name) => ({ id: name, name, description: '', color: '' }))]
    .filter((label) => `${label.name} ${label.description}`.toLowerCase().includes(search.toLowerCase()))
  return <>
    <input autoFocus className={styles.search} type="search" aria-label="Search filter labels" placeholder="Find a label…" value={search} onChange={(event) => setSearch(event.target.value)} />
    <div className={styles.options}>
      {options.map((label) => <label className={styles.option} key={label.id}>
        <input type="checkbox" checked={selected.includes(label.name)} onChange={(event) => onToggle(label.name, event.target.checked)} />
        <LabelSwatch color={label.color} /><span>{label.name}</span>
      </label>)}
      {search && options.length === 0 && <p>No matching labels.</p>}
    </div>
  </>
}
