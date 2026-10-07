import { AlertTriangle, Check, ChevronDown, ChevronRight, X } from 'lucide-react'
import type { CSSProperties, FocusEvent, PointerEvent as ReactPointerEvent, ReactNode } from 'react'
import { useCallback, useEffect, useEffectEvent, useId, useMemo, useRef, useState } from 'react'
import { CopyButton } from '../../components/CopyButton'
import { KeyboardShortcut } from '../../components/KeyboardShortcut'
import { api, type ApiError } from '../../data/api'
import type { Holon, WorkspaceCommit, WorkspaceFileDiff, WorkspaceInspection } from '../../data/types'
import { BranchCombobox } from '../repository/BranchCombobox'
import { useRepositoryBranches } from '../repository/useRepositoryBranches'
import { HolonDiffReview, type DiffLayout, type ReviewRange } from './HolonDiffReview'
import { DiffReviewHeader } from './DiffReviewHeader'
import { createContentLoader } from './workspaceContents'
import styles from './HolonDiffPanel.module.css'

type DiffPanelSummary = { files: number; additions: number; deletions: number }

type HolonDiffPanelProps = {
  holonId: string
  enabled: boolean
  readOnly: boolean
  open: boolean
  onOpenChange: (open: boolean) => void
  inspection?: WorkspaceInspection
  summary?: DiffPanelSummary
  commitControls?: ReactNode
  publishControls?: ReactNode
  onHolonChange?: (holon: Holon) => void
}

type FileTreeNode = {
  name: string
  path: string
  children: FileTreeNode[]
  file?: WorkspaceFileDiff
}

type CommitTimelineData = {
  commits: WorkspaceCommit[]
  sessionCommits: WorkspaceCommit[]
  earlierCommits: WorkspaceCommit[]
  branchBaseCommit: string
  workSessionStartCommit: string
  workspaceHeadCommit: string
}

const refreshInterval = 3000
const defaultBaseRef = 'main'
const defaultTargetRef = 'worktree'
const defaultPanelWidth = 290
const minimumPanelWidth = 230
const maximumPanelWidth = 560
const minimumRemainingWidth = 360
const worktreeTimelineId = 'worktree'

export function HolonDiffPanel(props: HolonDiffPanelProps) {
  const [revision, setRevision] = useState(0)
  const [changingBase, setChangingBase] = useState(false)
  const [baseError, setBaseError] = useState('')
  const pending = useRef(false)
  const changeBase = async (branch: string) => {
    if (pending.current) return
    pending.current = true
    setChangingBase(true)
    setBaseError('')
    try {
      const holon = await api.changeHolonBaseBranch(props.holonId, branch)
      props.onHolonChange?.(holon)
      // Remount to abort old inspections and contents and reset review filters.
      setRevision((current) => current + 1)
    } catch (value) {
      setBaseError((value as ApiError)?.message || 'Could not change the base branch.')
    } finally {
      pending.current = false
      setChangingBase(false)
    }
  }
  return <HolonDiffPanelContent key={`${props.holonId}:${revision}`} {...props} summary={revision ? undefined : props.summary} onBaseBranchChange={changeBase} changingBase={changingBase} baseError={baseError} />
}

function HolonDiffPanelContent({ holonId, enabled, readOnly, open, onOpenChange, inspection: providedInspection, summary, commitControls, publishControls, onBaseBranchChange, changingBase, baseError }: HolonDiffPanelProps & { onBaseBranchChange: (branch: string) => void; changingBase: boolean; baseError: string }) {
  const loadContents = useMemo(() => createContentLoader(), [])
  const [selectedTimelineIds, setSelectedTimelineIds] = useState<Set<string>>()
  const [commitsExpanded, setCommitsExpanded] = useState(true)
  const [allInspection, setAllInspection] = useState<WorkspaceInspection>()
  const [summaryInspection, setSummaryInspection] = useState<WorkspaceInspection>()
  const [summaryRange, setSummaryRange] = useState('')
  const [reviewRanges, setReviewRanges] = useState<ReviewRange[]>([])
  const [commitStats, setCommitStats] = useState<Record<string, DiffPanelSummary>>({})
  const summaryCache = useRef(new Map<string, WorkspaceInspection>())
  const [drawerOpen, setDrawerOpen] = useState(false)
  const [narrow, setNarrow] = useState(false)
  const [workspaceWidth, setWorkspaceWidth] = useState(0)
  const [layout, setLayout] = useState<DiffLayout>('side-by-side')
  const [collapsedFiles, setCollapsedFiles] = useState<Set<string>>(() => new Set())
  const [navigation, setNavigation] = useState({ path: '', sequence: 0 })
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [selectedFilePath, setSelectedFilePath] = useState(providedInspection?.files[0]?.path ?? '')
  const [collapsedFolders, setCollapsedFolders] = useState<Set<string>>(() => new Set())
  const [panelWidth, setPanelWidth] = useState(defaultPanelWidth)
  const [resizing, setResizing] = useState(false)
  const panelRef = useRef<HTMLElement>(null)
  const reviewPaneRef = useRef<HTMLDivElement>(null)
  const resizeStart = useRef<{ clientX: number, width: number } | undefined>(undefined)

  const toggleFromKeyboard = useEffectEvent((event: KeyboardEvent) => {
    if (!enabled || event.defaultPrevented || event.isComposing || !event.shiftKey || !event.altKey || event.ctrlKey || event.metaKey) return
    // Option changes event.key on macOS; match the physical key when available.
    if (event.code ? event.code !== 'KeyC' : event.key.toLowerCase() !== 'c') return
    const dialogs = document.querySelectorAll('[role="dialog"][aria-modal="true"], dialog[open]')
    if ([...dialogs].some((dialog) => dialog !== panelRef.current)) return
    event.preventDefault()
    event.stopImmediatePropagation()
    if (!event.repeat) onOpenChange(!open)
  })

  useEffect(() => {
    document.addEventListener('keydown', toggleFromKeyboard, true)
    return () => document.removeEventListener('keydown', toggleFromKeyboard, true)
  }, [])

  const currentInspection = providedInspection ?? summaryInspection
  const branches = useRepositoryBranches('', currentInspection?.base_branch ?? '', enabled && open && !providedInspection)
  const files = useMemo(() => currentInspection?.files ?? [], [currentInspection])
  const allFilesCollapsed = files.length > 0 && files.every((file) => collapsedFiles.has(file.path))
  const stats = useMemo(() => diffStats(files), [files])
  const railStats = summary ?? (currentInspection ? stats : undefined)
  const allFiles = useMemo(() => {
    const known = new Map((providedInspection?.files ?? allInspection?.files ?? []).map((file) => [file.path, file]))
    // A selected commit may contain a file later reverted or renamed out of the full diff.
    for (const file of files) if (!known.has(file.path)) known.set(file.path, file)
    return [...known.values()]
  }, [providedInspection, allInspection, files])
  const tree = useMemo(() => fileTree(allFiles), [allFiles])
  const timeline = useMemo(() => commitTimelineData(providedInspection ?? allInspection), [providedInspection, allInspection])
  const effectiveTimelineIds = selectedTimelineIds ?? new Set<string>()
  const allCommitsSelected = effectiveTimelineIds.size === 0
  const selectionKey = [...effectiveTimelineIds].sort().join(':')
  const truncated = Boolean(currentInspection?.diff_truncated)
  const railLabel = railStats ? diffStatsLabel(railStats) : ''
  const disabledReason = readOnly ? 'read-only workspace' : ''
  const showStatusLine = Boolean(truncated || disabledReason || error)
  const panelStyle = {
    maxWidth: workspaceWidth ? `${Math.max(0, workspaceWidth)}px` : undefined,
    '--diff-panel-width': `${panelWidth}px`,
  } as CSSProperties

  const setFileCollapsed = useCallback((path: string, collapsed: boolean) => {
    setCollapsedFiles((current) => {
      if (current.has(path) === collapsed) return current
      const next = new Set(current)
      if (collapsed) next.add(path)
      else next.delete(path)
      return next
    })
  }, [])

  const availableWidth = useCallback(() => {
    const bounds = panelRef.current?.parentElement?.getBoundingClientRect()
    // The shell has a desktop minimum width. Keep review inside the visible
    // workspace even when the underlying shell extends beyond the viewport.
    if (!bounds) return 0
    const rightRail = Math.max(0, document.documentElement.scrollWidth - bounds.right)
    return Math.min(bounds.width, window.innerWidth - bounds.left - rightRail)
  }, [])

  useEffect(() => {
    if (!selectedFilePath || files.some((file) => file.path === selectedFilePath)) return undefined
    let cancelled = false
    queueMicrotask(() => {
      if (!cancelled) setSelectedFilePath(files[0]?.path ?? '')
    })
    return () => { cancelled = true }
  }, [files, selectedFilePath])

  useEffect(() => {
    if (!open) {
      setResizing(false)
      setDrawerOpen(false)
      return
    }
    reviewPaneRef.current?.focus({ preventScroll: true })
  }, [open])

  useEffect(() => {
    if (!enabled || !open || !('ResizeObserver' in window)) return undefined
    const workspace = panelRef.current?.parentElement
    if (!workspace) return undefined
    const constrainPanel = () => {
      const width = availableWidth()
      setWorkspaceWidth(width)
      setNarrow(width > 0 && width < 720)
      setPanelWidth((current) => clampPanelWidth(current, width))
    }
    const observer = new ResizeObserver(constrainPanel)
    observer.observe(workspace)
    constrainPanel()
    window.addEventListener('resize', constrainPanel)
    return () => { observer.disconnect(); window.removeEventListener('resize', constrainPanel) }
  }, [availableWidth, enabled, open])

  useEffect(() => {
    if (!resizing) return undefined
    const previousCursor = document.body.style.cursor
    const previousUserSelect = document.body.style.userSelect
    const resize = (event: PointerEvent) => {
      const start = resizeStart.current
      if (!start) return
      setPanelWidth(clampPanelWidth(start.width + event.clientX - start.clientX, availableWidth()))
    }
    const stop = () => {
      resizeStart.current = undefined
      setResizing(false)
    }
    document.body.style.cursor = 'col-resize'
    document.body.style.userSelect = 'none'
    window.addEventListener('pointermove', resize)
    window.addEventListener('pointerup', stop)
    window.addEventListener('pointercancel', stop)
    return () => {
      document.body.style.cursor = previousCursor
      document.body.style.userSelect = previousUserSelect
      window.removeEventListener('pointermove', resize)
      window.removeEventListener('pointerup', stop)
      window.removeEventListener('pointercancel', stop)
    }
  }, [availableWidth, resizing])

  const loadInspection = useCallback(async (signal: AbortSignal) => {
    if (!enabled || providedInspection) return
    setLoading(true)
    try {
      const all = await api.sessionChanges(holonId, { base: defaultBaseRef, target: defaultTargetRef, summary: true, signal })
      if (signal.aborted) return
      setAllInspection(all)
      const selected = new Set(selectionKey ? selectionKey.split(':') : [])
      const available = new Set([worktreeTimelineId, ...commitTimelineData(all).commits.map((commit) => commit.sha)])
      if ([...selected].some((id) => !available.has(id))) { setSelectedTimelineIds(undefined); return }
      const ranges = timelineSelectionRanges(selected, commitTimelineData(all))
      const comparisons: ReviewRange[] = []
      for (const range of ranges) {
        const cacheKey = `${holonId}:${range.base}:${range.target}`
        let inspection = selected.size === 0 ? all : summaryCache.current.get(cacheKey)
        if (!inspection || range.target === 'worktree' && selected.size > 0) {
          inspection = await api.sessionChanges(holonId, { ...range, summary: true, signal })
          if (range.target !== 'worktree') summaryCache.current.set(cacheKey, inspection)
        }
        comparisons.push({ ...range, inspection })
      }
      if (signal.aborted) return
      setReviewRanges(comparisons)
      setSummaryInspection({ ...all, has_changes: comparisons.some((range) => range.inspection.has_changes), files: combinedRangeFiles(comparisons), diff_truncated: comparisons.some((range) => range.inspection.diff_truncated) })
      setSummaryRange(selectionKey)
      setError('')
    } catch (value) {
      if (signal.aborted) return
      if (['invalid_ref', 'ref_not_found'].includes((value as ApiError)?.code ?? '')) {
        setSelectedTimelineIds(undefined)
      }
      setError(errorMessage(value))
    } finally {
      if (!signal.aborted) setLoading(false)
    }
  }, [enabled, providedInspection, holonId, selectionKey])

  useEffect(() => {
    if (!enabled || providedInspection) return
    const controller = new AbortController()
    let timer: number | undefined
    const poll = async () => {
      await loadInspection(controller.signal)
      if (!controller.signal.aborted) timer = window.setTimeout(poll, refreshInterval)
    }
    timer = window.setTimeout(poll)
    return () => {
      controller.abort()
      window.clearTimeout(timer)
    }
  }, [enabled, loadInspection, providedInspection])

  // Load row statistics only while reviewing, using the same real range summaries.
  useEffect(() => {
    if (!enabled || !open || providedInspection || !allInspection) return
    const controller = new AbortController()
    const rows = [worktreeTimelineId, ...timeline.commits.map((commit) => commit.sha)]
    const load = async () => {
      const next: Record<string, DiffPanelSummary> = {}
      for (const id of rows) {
        if (controller.signal.aborted) return
        const range = timelineSelectionRanges(new Set([id]), timeline)[0]
        if (!range) continue
        const key = `${holonId}:${range.base}:${range.target}`
        try {
          let inspection = range.target === 'worktree' ? undefined : summaryCache.current.get(key)
          inspection ??= await api.sessionChanges(holonId, { ...range, summary: true, signal: controller.signal })
          if (controller.signal.aborted) return
          if (range.target !== 'worktree') summaryCache.current.set(key, inspection)
          next[id] = diffStats(inspection.files)
        } catch {
          if (controller.signal.aborted) return
          // Unavailable statistics must not prevent selecting a comparison.
        }
      }
      if (!controller.signal.aborted) setCommitStats(next)
      while (summaryCache.current.size > 200) summaryCache.current.delete(summaryCache.current.keys().next().value!)
    }
    void load()
    return () => controller.abort()
  }, [enabled, open, providedInspection, allInspection, holonId, timeline])

  if (!enabled) return null

  const selectFile = (file: WorkspaceFileDiff) => {
    setSelectedFilePath(file.path)
    setNavigation((current) => ({ path: file.path, sequence: current.sequence + 1 }))
    setDrawerOpen(false)
  }

  const toggleFolder = (path: string) => {
    setCollapsedFolders((current) => {
      const next = new Set(current)
      if (next.has(path)) next.delete(path)
      else next.add(path)
      return next
    })
  }

  const selectTimelineItem = (id: string) => {
    setSelectedTimelineIds((current) => {
      const next = new Set(current)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next.size ? next : undefined
    })
    setNavigation({ path: '', sequence: 0 })
  }
  const selectAllCommits = () => setSelectedTimelineIds(undefined)
  const rangeKey = selectionKey
  return (
    <>
      <section className={styles.closedPanel} aria-label="Holon diff">
        <button className={styles.handle} type="button" aria-expanded={open} aria-keyshortcuts="Shift+Alt+C" aria-label={open ? 'Close changes panel' : railLabel ? 'Open changes panel, ' + railLabel : 'Open changes panel'} onClick={() => onOpenChange(!open)}>
          <span className={styles.shortcut}><KeyboardShortcut letter="C" /></span>
          <ChevronRight className={styles.handleChevron} />
          <DiffRailSummary stats={railStats} />
        </button>
      </section>
      {open && <section ref={panelRef} className={`${styles.overlay} ${narrow ? styles.narrow : ''}`} style={panelStyle} aria-label="Holon changes" role="dialog" aria-modal="true"
        onKeyDownCapture={(event) => {
          if (event.defaultPrevented || event.nativeEvent.isComposing || !event.shiftKey || !event.altKey || event.ctrlKey || event.metaKey) return
          if (event.key !== 'ArrowLeft' && event.key !== 'ArrowRight') return
          event.preventDefault()
          event.stopPropagation()
          onOpenChange(false)
        }}
        onKeyDown={(event) => {
          if (event.key === 'Escape' && event.target instanceof Element && event.target.closest('[data-holark-dialog-nested-layer]')) {
            event.stopPropagation()
            return
          }
          // Monaco gets first refusal for its find/other editor widgets.
          if (event.key === 'Escape' && !event.defaultPrevented) {
            event.preventDefault()
            if (drawerOpen) { setDrawerOpen(false); reviewPaneRef.current?.focus() }
            else onOpenChange(false)
          }
          if (event.key === 'Tab') {
            const items = Array.from(event.currentTarget.querySelectorAll<HTMLElement>('button:not(:disabled), select, input, textarea, [tabindex="0"]')).filter((item) => item.getClientRects().length > 0)
            const first = items[0], last = items.at(-1)
            if (event.shiftKey && (document.activeElement === first || document.activeElement === reviewPaneRef.current)) { event.preventDefault(); last?.focus() }
            else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first?.focus() }
          }
          event.stopPropagation()
        }}>
      {narrow && drawerOpen && <button className={styles.drawerBackdrop} aria-label="Dismiss commits and files" onClick={() => { setDrawerOpen(false); reviewPaneRef.current?.focus() }} />}
      <aside data-unfiltered={!selectedTimelineIds || allCommitsSelected} className={`${styles.sidebar} ${drawerOpen ? styles.drawerOpen : ''}`} inert={narrow && !drawerOpen}>
      <header className={styles.navigationHeader}><strong>Files &amp; commits</strong><button className={styles.selectAll} onClick={selectAllCommits} disabled={allCommitsSelected}>View all</button></header>
      <div className={styles.navigationBody}>
      <CommitTimeline
        controls={commitControls}
        timeline={timeline}
        stats={commitStats}
        onClose={narrow ? () => { setDrawerOpen(false); reviewPaneRef.current?.focus() } : undefined}
        selectedIds={effectiveTimelineIds}
        expanded={commitsExpanded}
        allSelected={allCommitsSelected}
        onToggle={() => setCommitsExpanded((current) => !current)}
        onSelectAll={selectAllCommits}
        onSelect={selectTimelineItem}
      />

      <div className={styles.fileTree}>
        <header className={styles.filesHeader}>
          <h3>Changed files</h3>
          <span className={styles.filesCount}>{stats.files === allFiles.length ? stats.files : `${stats.files} / ${allFiles.length}`}</span>

        </header>
        <div className={styles.treeRows}>
          {showStatusLine && (
            <div className={styles.statusLine}>
              {truncated && <span className={styles.state}>Truncated</span>}
              {disabledReason && <span className={styles.state}>{disabledReason}</span>}
              {error && <span className={styles.errorState}><AlertTriangle /> {currentInspection ? 'Refresh failed' : 'Unavailable'}</span>}
            </div>
          )}
          {error && <p className={styles.errorBanner}><AlertTriangle /> {error}</p>}
          {!currentInspection && loading && <p className={styles.message}>Loading changes...</p>}
          {currentInspection && !currentInspection.has_changes && <p className={styles.emptyFiles}>No changes in this range.</p>}
          {tree.map((node) => <TreeNode key={node.path} node={node} activePaths={new Set(files.map((file) => file.path))} depth={0} selectedFilePath={selectedFilePath} collapsedFolders={collapsedFolders} onToggleFolder={toggleFolder} onSelectFile={selectFile} />)}
        </div>
      </div>
      </div>
      {publishControls}
      <div className={styles.resizeHandle} role="separator" aria-label="Resize changed files panel" aria-orientation="vertical"
        onPointerDown={(event) => {
          if (event.button !== 0) return
          event.preventDefault()
          resizeStart.current = { clientX: event.clientX, width: panelWidth }
          setResizing(true)
        }} />
      </aside>
      <div ref={reviewPaneRef} className={styles.reviewPane} tabIndex={-1}>
        <DiffReviewHeader
          title={currentInspection?.branch ? <div className={styles.branches}>
            <strong title={currentInspection.branch}>{currentInspection.branch}</strong>
            <CopyButton text={currentInspection.branch} label="Copy current branch name" />
            <span aria-hidden="true">→</span>
            <BranchCombobox
              variant="base"
              branches={branches.branches.filter((branch) => branch.short_name && branch.short_name !== currentInspection.branch)}
              value={currentInspection.base_branch}
              defaultBranch={branches.response?.default_ref?.replace('refs/heads/', '') ?? ''}
              onValueChange={(branch) => { if (branch !== currentInspection.base_branch) onBaseBranchChange(branch) }}
              onOpen={() => void branches.refresh()}
              loading={branches.loading} refreshing={branches.refreshing} error={branches.error}
              disabled={readOnly || Boolean(providedInspection) || changingBase}
              popupClassName={styles.basePopup}
            />
            <CopyButton text={currentInspection.base_branch} label="Copy base branch name" />
          </div> : <strong>Changes</strong>}
          stats={stats} layout={layout} onLayoutChange={setLayout} allFilesCollapsed={allFilesCollapsed}
          onToggleCollapsed={() => setCollapsedFiles(allFilesCollapsed ? new Set() : new Set(files.map((file) => file.path)))}>
          {narrow && <button className={styles.drawerButton} onClick={() => setDrawerOpen((current) => !current)} aria-expanded={drawerOpen}>Commits &amp; files</button>}
        </DiffReviewHeader>
        {changingBase && <p className={styles.message} role="status">Changing base branch…</p>}
        {baseError && <p className={styles.errorBanner} role="alert">{baseError}</p>}
        {currentInspection && (providedInspection || summaryRange === rangeKey) && <HolonDiffReview key={rangeKey} load={loadContents} holonId={holonId} provided={Boolean(providedInspection)} inspection={currentInspection} ranges={providedInspection ? undefined : reviewRanges} base={defaultBaseRef} target={defaultTargetRef} layout={layout} navigation={navigation} collapsedFiles={collapsedFiles} onFileCollapsedChange={setFileCollapsed} />}

      </div>
      </section>}
    </>
  )
}

type CommitTimelineProps = {
  controls?: ReactNode
  onClose?: () => void
  timeline: CommitTimelineData
  stats: Record<string, DiffPanelSummary>
  selectedIds: Set<string>
  expanded: boolean
  allSelected: boolean
  onToggle: () => void
  onSelectAll: () => void
  onSelect: (id: string) => void
}

type TimelineRowProps = {
  commit?: WorkspaceCommit
  id: string
  label: string
  meta: string
  stats?: DiffPanelSummary
  selected: boolean
  onSelect: (id: string) => void
}

function CommitTimeline({ controls, onClose, timeline, stats, selectedIds, expanded, onToggle, onSelect }: CommitTimelineProps) {
  return <section className={styles.commitsSection}>
    <header className={styles.commitsHeader}>
      <button className={styles.commitsDisclosure} type="button" aria-expanded={expanded} onClick={onToggle}>
        {expanded ? <ChevronDown /> : <ChevronRight />}<h3>Commits</h3>
      </button>
      <span className={styles.commitCount}>{selectedIds.size || 'All'}</span>
      {onClose && <button className={styles.iconButton} aria-label="Close commits and files" onClick={onClose}><X /></button>}
    </header>
    {expanded && <>
      <div className={styles.timelineRows}>
        <div className={`${styles.worktreeControls} ${selectedIds.has(worktreeTimelineId) ? styles.selectedWorktreeControls : ''}`}><TimelineRow id={worktreeTimelineId} stats={stats[worktreeTimelineId]} label="Uncommitted changes" meta={`HEAD ${shortCommit(timeline.workspaceHeadCommit)}`} selected={selectedIds.has(worktreeTimelineId)} onSelect={onSelect} />
        {controls}</div>
        {timeline.commits.map((commit) => <TimelineRow key={commit.sha} commit={commit} id={commit.sha} stats={stats[commit.sha]} label={commit.subject || shortCommit(commit.sha)} meta={shortCommit(commit.sha)} selected={selectedIds.has(commit.sha)} onSelect={onSelect} />)}
      </div>
    </>}
  </section>
}

function TimelineRow({ id, label, meta, stats, selected, onSelect, commit }: TimelineRowProps) {
  const isWorktree = id === worktreeTimelineId
  const tooltipId = useId()
  const tooltip = useRef<HTMLDivElement>(null)
  const trigger = useRef<HTMLButtonElement>(null)
  const [open, setOpen] = useState(false)
  const hideDetails = () => tooltip.current?.hidePopover?.()
  const leave = (event: ReactPointerEvent<HTMLElement> | FocusEvent<HTMLElement>) => {
    const next = event.relatedTarget
    if (next instanceof Node && (trigger.current?.contains(next) || tooltip.current?.contains(next))) return
    hideDetails()
  }
  useEffect(() => {
    if (!open) return
    const close = () => tooltip.current?.hidePopover?.()
    window.addEventListener('resize', close)
    window.addEventListener('scroll', close, true)
    return () => { window.removeEventListener('resize', close); window.removeEventListener('scroll', close, true) }
  }, [open])
  const showDetails = () => {
    const bounds = trigger.current?.getBoundingClientRect()
    const panel = trigger.current?.closest('aside')?.getBoundingClientRect()
    const element = tooltip.current
    if (!element || !bounds || !panel) return
    const width = Math.min(380, window.innerWidth - 24)
    const left = Math.max(12, Math.min(panel.right + 10, window.innerWidth - width - 12))
    element.style.left = `${left}px`
    element.style.top = '12px'
    element.style.setProperty('--bridge-width', `${Math.max(0, left - bounds.right)}px`)
    element.showPopover?.()
    element.style.top = `${Math.max(12, Math.min(bounds.top - 8, window.innerHeight - element.offsetHeight - 12))}px`
  }
  return <div onKeyDown={(event) => { if (event.key === 'Escape' && tooltip.current?.matches(':popover-open')) { event.stopPropagation(); hideDetails() } }}>
    <button ref={trigger} className={`${styles.commitRow} ${isWorktree ? styles.uncommittedRow : ''} ${selected ? styles.selectedCommit : ''}`} type="button" aria-pressed={selected} aria-label={`Select ${label}`} aria-haspopup={isWorktree ? undefined : 'dialog'} aria-expanded={isWorktree ? undefined : open} aria-controls={isWorktree ? undefined : tooltipId}
      onPointerEnter={(event) => { if (!isWorktree && event.pointerType !== 'touch') showDetails() }} onPointerLeave={leave} onBlur={leave} onFocus={isWorktree ? undefined : showDetails} onClick={() => { hideDetails(); onSelect(id) }}>
      <span className={styles.commitText}>{id !== worktreeTimelineId && <small>{id.slice(0, 5)} ·</small>}<strong>{label}</strong></span>
      <span className={styles.commitCheckbox} aria-hidden="true">{selected && <Check />}</span>
      {stats && <span className={styles.commitStats} aria-label={`${stats.additions} additions, ${stats.deletions} deletions`}><span className={styles.additions}>+{stats.additions}</span><span className={styles.deletions}>−{stats.deletions}</span></span>}
    </button>
    {!isWorktree && <div ref={tooltip} id={tooltipId} popover="auto" role="dialog" aria-label="Commit details" className={styles.commitDetails} onPointerLeave={leave} onBlur={leave} onToggle={(event) => setOpen(event.newState === 'open')}>
      <div className={styles.commitPreview}>
        <h3>{label}</h3>
        <p className={styles.commitPreviewMeta}><>{commit?.author && `${commit.author} · `}<span className={styles.commitPreviewSHA}><code>{meta}</code><CopyButton text={id} label="Copy commit SHA" /></span>{commit?.authored_at && <> · <time dateTime={commit.authored_at} title={new Date(commit.authored_at).toLocaleString()}>{commitAge(commit.authored_at)}</time></>}</></p>
        {commit?.body && <div className={styles.commitPreviewBody}>{commit.body}</div>}
      </div>
    </div>}
  </div>
}

function commitAge(date: string) {
  const minutes = Math.max(0, Math.floor((Date.now() - new Date(date).getTime()) / 60000))
  if (minutes < 1) return 'Just now'
  if (minutes < 60) return `${minutes}m ago`
  if (minutes < 1440) return `${Math.floor(minutes / 60)}h ago`
  return `${Math.floor(minutes / 1440)}d ago`
}

type TreeNodeProps = {
  activePaths: Set<string>
  node: FileTreeNode
  depth: number
  selectedFilePath: string
  collapsedFolders: Set<string>
  onToggleFolder: (path: string) => void
  onSelectFile: (file: WorkspaceFileDiff) => void
}

function TreeNode({ node, activePaths, depth, selectedFilePath, collapsedFolders, onToggleFolder, onSelectFile }: TreeNodeProps) {
  if (node.file) {
    const selected = node.file.path === selectedFilePath
    return (
      <button className={`${styles.treeFile} ${selected ? styles.selectedFile : ''}`} style={{ paddingLeft: `${10 + depth * 14}px` }} type="button" disabled={!activePaths.has(node.file.path)} onMouseDown={(event) => event.preventDefault()} onClick={() => onSelectFile(node.file!)} aria-current={selected ? 'true' : undefined} title={node.file.path}>
        <span className={`${styles.status} ${styles[statusClass(node.file.status)]}`}>{statusLabel(node.file.status)}</span>
        <strong>{node.name.includes('/') && <span className={styles.directoryPrefix}>{node.name.slice(0, node.name.lastIndexOf('/') + 1)}</span>}{node.name.split('/').at(-1)}</strong>
      </button>
    )
  }
  const collapsed = collapsedFolders.has(node.path)
  return (
    <div className={styles.treeGroup}>
      <button className={styles.treeFolder} style={{ paddingLeft: `${10 + depth * 14}px` }} type="button" onClick={() => onToggleFolder(node.path)} aria-expanded={!collapsed}>
        {collapsed ? <ChevronRight /> : <ChevronDown />}
        <strong>{node.name}</strong>
      </button>
      {!collapsed && node.children.map((child) => (
        <TreeNode key={child.path} node={child} activePaths={activePaths} depth={depth + 1} selectedFilePath={selectedFilePath} collapsedFolders={collapsedFolders} onToggleFolder={onToggleFolder} onSelectFile={onSelectFile} />
      ))}
    </div>
  )
}

function diffStatsLabel(stats: DiffPanelSummary) {
  const files = `${stats.files} ${stats.files === 1 ? 'changed file' : 'changed files'}`
  const additions = `${stats.additions} ${stats.additions === 1 ? 'addition' : 'additions'}`
  const deletions = `${stats.deletions} ${stats.deletions === 1 ? 'deletion' : 'deletions'}`
  return `${files}, ${deletions}, ${additions}`
}

function DiffRailSummary({ stats }: { stats?: DiffPanelSummary }) {
  return (
    <span className={styles.railSummary} aria-hidden="true">
      <strong>Changes · Review &amp; Publish</strong>
      {stats && <>
        <span>{stats.files} {stats.files === 1 ? 'file' : 'files'}</span>
        <span className={styles.additions}>+{stats.additions}</span>
        <span className={styles.deletions}>−{stats.deletions}</span>
      </>}
    </span>
  )
}

function diffStats(files: WorkspaceFileDiff[]) {
  return files.reduce((total, file) => ({
    files: total.files + 1,
    additions: total.additions + file.additions,
    deletions: total.deletions + file.deletions,
  }), { files: 0, additions: 0, deletions: 0 })
}

function fileTree(files: WorkspaceFileDiff[]) {
  const root: FileTreeNode = { name: '', path: '', children: [] }
  for (const file of files) {
    const parts = file.path.split('/').filter(Boolean)
    let current = root
    for (let index = 0; index < parts.length; index += 1) {
      const name = parts[index]
      const path = parts.slice(0, index + 1).join('/')
      const existing = current.children.find((child) => child.name === name)
      if (existing) {
        current = existing
        continue
      }
      const next: FileTreeNode = { name, path, children: [], file: index === parts.length - 1 ? file : undefined }
      current.children.push(next)
      current = next
    }
  }
  const compact = (node: FileTreeNode): FileTreeNode => {
    while (!node.file && node.children.length === 1) {
      const child = node.children[0]
      node = { ...child, name: `${node.name}/${child.name}` }
    }
    return { ...node, children: node.children.map(compact) }
  }
  root.children = root.children.map(compact)
  sortTree(root.children)
  return root.children
}

function sortTree(nodes: FileTreeNode[]) {
  nodes.sort((left, right) => {
    if (Boolean(left.file) !== Boolean(right.file)) return left.file ? 1 : -1
    return left.name.localeCompare(right.name)
  })
  for (const node of nodes) sortTree(node.children)
}


function commitTimelineData(inspection?: WorkspaceInspection): CommitTimelineData {
  const refs = inspection?.ref_options ?? []
  const branchBaseCommit = inspection?.branch_base_commit ?? refs.find((ref) => ref.id === 'main')?.commit ?? inspection?.base_commit ?? ''
  const workSessionStartCommit = inspection?.work_session_start_commit ?? refs.find((ref) => ref.id === 'session-start')?.commit ?? branchBaseCommit
  const workspaceHeadCommit = inspection?.workspace_head_commit ?? refs.find((ref) => ref.id === 'worktree')?.commit ?? inspection?.head_commit ?? branchBaseCommit
  const suppliedCommits = inspection?.commits
  const legacyCommits = refs
    .filter((ref) => ref.kind === 'commit' && ref.commit)
    .map((ref) => ({ sha: ref.commit!, parent_commit: '', subject: ref.label.replace(new RegExp(`^${shortCommit(ref.commit!)}\\s*`), '') }))
    .reverse()
  const rawCommits = suppliedCommits ?? legacyCommits
  const commits = rawCommits.map((commit, index) => ({
    ...commit,
    parent_commit: commit.parent_commit || rawCommits[index + 1]?.sha || branchBaseCommit,
  }))
  const startIndex = commits.findIndex((commit) => commit.sha === workSessionStartCommit)
  const sessionCommits = workSessionStartCommit === branchBaseCommit ? commits : startIndex >= 0 ? commits.slice(0, startIndex) : commits
  const earlierCommits = startIndex >= 0 && workSessionStartCommit !== branchBaseCommit ? commits.slice(startIndex) : []
  return {
    commits,
    sessionCommits,
    earlierCommits,
    branchBaseCommit,
    workSessionStartCommit,
    workspaceHeadCommit,
  }
}

function timelineSelectionRanges(selection: Set<string>, timeline: CommitTimelineData) {
  if (!selection.size) return [{ base: defaultBaseRef, target: defaultTargetRef, label: 'All changes' }]
  const revisions = [
    ...[...timeline.commits].reverse().map((commit) => ({ id: commit.sha, base: `commit:${commit.parent_commit}`, target: `commit:${commit.sha}`, label: commit.subject })),
    { id: worktreeTimelineId, base: `commit:${timeline.workspaceHeadCommit}`, target: 'worktree', label: 'Uncommitted changes' },
  ]
  const ranges: Array<{ base: string, target: string, label: string }> = []
  let adjacent = false
  for (const revision of revisions) {
    if (!selection.has(revision.id)) { adjacent = false; continue }
    const last = ranges.at(-1)
    if (adjacent && last) { last.target = revision.target; last.label = `${last.label.split(' → ')[0]} → ${revision.label}` }
    else ranges.push({ base: revision.base, target: revision.target, label: revision.label })
    adjacent = true
  }
  return ranges
}

function combinedRangeFiles(ranges: ReviewRange[]) {
  const files = new Map<string, WorkspaceFileDiff>()
  for (const { inspection } of ranges) for (const file of inspection.files) {
    const previous = files.get(file.path)
    files.set(file.path, previous ? { ...previous, additions: previous.additions + file.additions, deletions: previous.deletions + file.deletions, binary: previous.binary || file.binary, diff_truncated: previous.diff_truncated || file.diff_truncated } : file)
  }
  return [...files.values()]
}

function shortCommit(value: string) {
  return value.length > 8 ? value.slice(0, 8) : value
}

function statusLabel(status: string) {
  if (status.toLowerCase() === 'removed') return 'D'
  return status.slice(0, 1).toUpperCase() || 'M'
}

function statusClass(status: string) {
  const normalized = status.toLowerCase()
  if (normalized.startsWith('a') || normalized.includes('new')) return 'statusAdded'
  if (normalized.startsWith('d') || normalized.includes('delete') || normalized === 'removed') return 'statusDeleted'
  if (normalized.startsWith('r') || normalized.includes('rename')) return 'statusRenamed'
  return 'statusModified'
}

function errorMessage(value: unknown) {
  const error = value as ApiError
  return error?.message || 'Workspace inspection failed.'
}

function maximumAllowedPanelWidth(availableWidth: number) {
  if (!Number.isFinite(availableWidth) || availableWidth <= 0) return maximumPanelWidth
  return Math.max(minimumPanelWidth, Math.min(maximumPanelWidth, availableWidth - minimumRemainingWidth))
}

function clampPanelWidth(width: number, availableWidth: number) {
  return Math.round(Math.min(Math.max(width, minimumPanelWidth), maximumAllowedPanelWidth(availableWidth)))
}
