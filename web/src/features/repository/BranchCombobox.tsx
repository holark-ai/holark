import { Combobox } from '@base-ui/react/combobox'
import { ChevronDown, GitBranch } from 'lucide-react'
import { matchSorter } from 'match-sorter'
import { useEffect, useEffectEvent, useLayoutEffect, useMemo, useRef, useState } from 'react'
import type { RepositoryRef } from '../../data/types'
import styles from './BranchCombobox.module.css'

type Props = {
  branches: RepositoryRef[]
  value: string
  defaultBranch: string
  onValueChange: (branch: string) => void
  onCommitSelect?: (branch: RepositoryRef) => void
  onOpen?: () => void
  loading?: boolean
  refreshing?: boolean
  error?: string
  disabled?: boolean
  variant?: 'header' | 'detail' | 'timeline' | 'base'
  popupClassName?: string
  popupWidth?: number
  enableOpenShortcut?: boolean
}

export function BranchCombobox({ branches, value, defaultBranch, onValueChange, onCommitSelect, onOpen, loading = false, refreshing = false, error = '', disabled = false, variant = 'header', popupClassName = '', popupWidth, enableOpenShortcut = false }: Props) {
  const triggerRef = useRef<HTMLButtonElement>(null)
  const searchRef = useRef<HTMLInputElement>(null)
  const isMac = /Mac|iPhone|iPad|iPod/i.test(navigator.platform)
  const [open, setOpen] = useState(false)
  const [query, setQuery] = useState('')
  const [portalContainer, setPortalContainer] = useState<HTMLElement | null>(null)
  const branchesByName = useMemo(() => new Map(branches.map((branch) => [branch.short_name, branch])), [branches])
  const names = useMemo(() => [...branchesByName.values()]
    .sort((left, right) => {
      if (left.short_name === value) return -1
      if (right.short_name === value) return 1
      return (Date.parse(right.committed_at) || 0) - (Date.parse(left.committed_at) || 0) || left.short_name.localeCompare(right.short_name)
    })
    .map((branch) => branch.short_name), [branchesByName, value])
  const displayed = useMemo(() => {
    const search = query.trim()
    return search ? matchSorter(names, search) : names
  }, [names, query])

  useLayoutEffect(() => {
    setPortalContainer(triggerRef.current?.closest<HTMLElement>('[role="dialog"]') ?? null)
  }, [])

  const changeOpen = (nextOpen: boolean) => {
    if (nextOpen) {
      onOpen?.()
    } else {
      setQuery('')
    }
    setOpen(nextOpen)
  }

  const handleOpenShortcut = useEffectEvent((event: KeyboardEvent) => {
    if (disabled || event.defaultPrevented || event.isComposing || !event.shiftKey || !event.altKey || event.ctrlKey || event.metaKey) return
    // Option changes the typed character on macOS; match the physical B key.
    if (event.code ? event.code !== 'KeyB' : event.key.toLowerCase() !== 'b') return
    if (document.querySelector('[role="dialog"][aria-modal="true"], dialog[open]')) return
    event.preventDefault()
    event.stopImmediatePropagation()
    if (event.repeat) return
    if (open) {
      searchRef.current?.focus()
    } else {
      triggerRef.current?.focus({ preventScroll: true })
      changeOpen(true)
    }
  })

  useEffect(() => {
    if (!enableOpenShortcut) return
    window.addEventListener('keydown', handleOpenShortcut)
    return () => window.removeEventListener('keydown', handleOpenShortcut)
  }, [enableOpenShortcut])

  return (
    <Combobox.Root<string>
      items={names}
      filteredItems={displayed}
      filter={null}
      autoHighlight
      value={value}
      open={open}
      disabled={disabled}
      inputValue={query}
      onOpenChange={(nextOpen, details) => {
        if (!nextOpen && details.reason === 'escape-key' && query.length > 0) {
          details.cancel()
          setQuery('')
          return
        }
        changeOpen(nextOpen)
      }}
      onInputValueChange={(nextQuery, details) => {
        if (details.reason !== 'item-press') setQuery(nextQuery)
      }}
      onValueChange={(nextValue) => {
        if (nextValue) onValueChange(nextValue)
      }}
    >
      <div className={styles.triggerWrap}>
        <Combobox.Trigger ref={triggerRef} className={`${styles.trigger} ${styles[variant]}`} aria-label={variant === 'base' ? `Change base branch, currently ${value}` : 'Branch'} aria-keyshortcuts={enableOpenShortcut ? 'Shift+Alt+B' : undefined}
          onKeyDown={(event) => {
            if (open && event.key === 'Escape') event.stopPropagation()
          }}>
          {variant === 'detail' ? (
            <span className={styles.detailCopy}><span className={styles.label}>Branch</span><span className={styles.value}>{value}</span></span>
          ) : (
            <>{variant !== 'base' && <GitBranch className={styles.branchIcon} aria-hidden="true" />}<span className={styles.value}>{value}</span></>
          )}
          {enableOpenShortcut && <kbd className={styles.shortcut} aria-hidden="true" title={`Shift + ${isMac ? 'Option' : 'Alt'} + B`}>⇧ {isMac ? '⌥' : 'Alt'} B</kbd>}
          <ChevronDown className={styles.chevron} aria-hidden="true" />
        </Combobox.Trigger>
      </div>
      <Combobox.Portal container={portalContainer ?? undefined}>
        <Combobox.Positioner className={styles.positioner} align="start" sideOffset={6} collisionPadding={8}>
          <Combobox.Popup className={`${styles.popup} ${variant === 'timeline' ? styles.timelinePopup : ''} ${popupClassName}`} style={{ width: popupWidth }} aria-label="Select branch" data-holark-dialog-nested-layer=""
            onKeyDown={(event) => {
              if (event.key === 'Escape') event.stopPropagation()
            }}>
            <div className={styles.searchWrap}>
              <Combobox.Input ref={searchRef} className={styles.search} placeholder="Search branches…" aria-label="Search branches" />
            </div>
            {(loading || refreshing || error) && (
              <div className={`${styles.status} ${error ? styles.statusError : ''}`} role={error ? 'alert' : 'status'}>
                {error || (loading ? 'Loading cached branches…' : 'Refreshing branches…')}
                {error && onOpen && <button type="button" disabled={refreshing} onClick={onOpen}>Retry</button>}
              </div>
            )}
            <Combobox.Empty><div className={styles.empty}>No branches found.</div></Combobox.Empty>
            <Combobox.List className={styles.list}>
              {(branch: string) => {
                const ref = branchesByName.get(branch)
                const updatedAt = ref?.committed_at ? new Date(ref.committed_at) : undefined
                const hasUpdatedAt = updatedAt && Number.isFinite(updatedAt.getTime()) && updatedAt.getUTCFullYear() > 1
                return (
                  <Combobox.Item key={branch} value={branch} className={styles.item} aria-label={branch}>
                    <span className={styles.branchCopy}>
                      <span className={styles.branchHeading}>
                        <span className={styles.branchIdentity}>
                          <span className={styles.branchName} title={branch}>{branch}</span>
                          {branch === defaultBranch && <small className={styles.defaultLabel}>default</small>}
                        </span>
                        {hasUpdatedAt && <time dateTime={ref?.committed_at} aria-label={`Last updated ${updatedAt.toLocaleString()}`} title={`Last updated ${updatedAt.toLocaleString()}`}>
                          {formatBranchUpdatedAt(updatedAt)}
                        </time>}
                      </span>
                      {(ref?.author_name || ref?.target || ref?.subject) && <span className={styles.commitLine} title={ref?.subject || undefined}>
                        {ref?.author_name && <span className={styles.author}>{ref.author_name}</span>}
                        {ref?.author_name && (ref?.target || ref?.subject) && <span className={styles.separator} aria-hidden="true">·</span>}
                        {ref?.target && (onCommitSelect ? <button
                          type="button"
                          className={styles.commitLink}
                          aria-label={`View changes for ${ref.target.slice(0, 7)} on ${branch}`}
                          onMouseUp={(event) => event.stopPropagation()}
                          onKeyDown={(event) => {
                            if (event.key === 'Enter' || event.key === ' ') event.stopPropagation()
                          }}
                          onClick={(event) => {
                            event.stopPropagation()
                            changeOpen(false)
                            onCommitSelect(ref)
                          }}
                        ><code>{ref.target.slice(0, 7)}</code></button> : <code>{ref.target.slice(0, 7)}</code>)}
                        {ref?.target && ref?.subject && <span className={styles.separator} aria-hidden="true">·</span>}
                        {ref?.subject && <span className={styles.subject}>{ref.subject}</span>}
                      </span>}
                    </span>
                  </Combobox.Item>
                )
              }}
            </Combobox.List>
          </Combobox.Popup>
        </Combobox.Positioner>
      </Combobox.Portal>
    </Combobox.Root>
  )
}

function formatBranchUpdatedAt(date: Date) {
  const minutes = Math.max(0, Math.floor((Date.now() - date.getTime()) / 60_000))
  if (minutes < 1) return 'just now'
  if (minutes < 60) return `${minutes}m ago`
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `${hours}h ago`
  const days = Math.floor(hours / 24)
  if (days < 7) return `${days}d ago`
  return date.toLocaleDateString(undefined, {
    month: 'short', day: 'numeric',
    ...(date.getFullYear() !== new Date().getFullYear() ? { year: 'numeric' as const } : {}),
  })
}
