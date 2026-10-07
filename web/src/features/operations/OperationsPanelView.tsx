import { useOptionalHolonStore } from '../project/holonStoreContext'
import { AlertTriangle, Bot, ChevronDown, ChevronRight, ChevronUp, CircleDashed, CircleHelp, Gauge, GitBranch, GitMerge, GitPullRequest, GitPullRequestClosed, GitPullRequestDraft, Pencil, Plus } from 'lucide-react'
import { useContext, useEffect, useEffectEvent, useMemo, useRef, useState, type MouseEvent } from 'react'
import { Link, useLocation, useNavigate } from 'react-router-dom'
import { useContextTokenThresholds } from '../../app/contextTokenThresholds'
import { hasAvailableHarness, isSessionResumable, requiredHarnessTypes } from '../../data/harness'
import type { ApiProject, HarnessCapability, PullRequest, Holon } from '../../data/types'
import { holonDisplayName, holonNeedsInput, holonDisplayStatus, holonDisplayStatusLabel } from '../agents/holonDisplay'
import styles from './OperationsPanel.module.css'
import { OperationsContext } from './operationsContext'
import { activeSidebarHolons, useHolonAddresses } from './useHolonAddresses'
import { PullRequestPreviewLink } from './PullRequestPreviewLink'
import { HolonStatusIcon } from '../agents/HolonStatusIcon'
import { displayedContextTokens, formatContextTokens, formatRelativeTime, formatTime, inputStateLabel, sessionStatusClassName } from '../agents/holonRowDisplay'
import { useNavigationArrows } from '../navigation/useNavigationArrows'

const topHeaderProtectionHeightPx = 40
const topHeaderActivationDepthPx = 24
const cyclingInactivityMs = 2500
const panelCollapseTransitionMs = 80
const lastContextTokensStorageKey = 'holark.last-displayed-context-tokens'

function readLastContextTokens(): Record<string, number> {
  try {
    const stored = window.localStorage.getItem(lastContextTokensStorageKey)
    if (!stored) return {}
    const parsed: unknown = JSON.parse(stored)
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return {}
    return Object.fromEntries(Object.entries(parsed).filter(([, value]) =>
      Number.isSafeInteger(value) && (value as number) >= 0))
  } catch {
    return {}
  }
}

export type OperationsPanelRenameController = {
  renaming?: { holonId: string, value: string }
  error?: string
  begin: (holon: Holon) => void
  change: (value: string) => void
  commit: () => void | Promise<void>
  cancel: () => void
}

type OperationsPanelViewProps = {
  project: ApiProject
  holons: Holon[]
  capabilities: HarnessCapability[]
  selectedSessionId?: string
  selectedPullRequestId?: string
  selectedPullRequest?: PullRequest
  pullRequestByHolon?: ReadonlyMap<string, PullRequest>
  pullRequests?: PullRequest[]
  sessionsLoading?: boolean
  sessionsError?: string
  reopenError?: string
  reopeningHolon?: string
  rename: OperationsPanelRenameController
  expanded?: boolean
  onExpandedChange?: (expanded: boolean) => void
  onNewAgent: () => void
  onReopen: (holon: Holon) => void
  onTogglePullRequestPin?: (pullRequest: PullRequest) => Promise<void>
  onSessionSelect?: (holon: Holon) => void
  sessionHref?: (holon: Holon) => string
}

export function OperationsPanelView({
  project,
  holons,
  capabilities,
  selectedSessionId,
  selectedPullRequestId,
  selectedPullRequest,
  pullRequestByHolon = new Map(),
  pullRequests = [],
  sessionsLoading = false,
  sessionsError = '',
  reopenError = '',
  reopeningHolon = '',
  rename,
  expanded: controlledExpanded,
  onExpandedChange,
  onNewAgent,
  onReopen,
  onTogglePullRequestPin = async () => {},
  onSessionSelect,
  sessionHref = (holon) => `/holons/${holon.id}`,
}: OperationsPanelViewProps) {
  const navigate = useNavigate()
  const location = useLocation()
  const holonStore = useOptionalHolonStore()
  const { thresholds: contextTokenThresholds } = useContextTokenThresholds()
  const panelRef = useRef<HTMLElement>(null)
  const keepOpenAfterCycling = useRef(false)
  const cyclingInactivityTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)
  const panelCollapseTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)
  const panelClosing = useRef(false)
  const pointerPosition = useRef<{ x: number, y: number } | undefined>(undefined)
  const [internalExpanded, setInternalExpanded] = useState(false)
  const [instantExpansion, setInstantExpansion] = useState(false)
  const [clippedNames, setClippedNames] = useState<Record<string, boolean>>({})
  const [hideCancelledOrError, setHideCancelledOrError] = useState(true)
  const [collapsedGroups, setCollapsedGroups] = useState<ReadonlySet<string>>(() => new Set())
  const [cycleScrollRequest, setCycleScrollRequest] = useState(0)
  const [panelPinPending, setPanelPinPending] = useState(false)
  const [panelPinPosition, setPanelPinPosition] = useState<{ pullRequestId: string, selectedPullRequestId?: string }>()
  const [optimisticPanelPin, setOptimisticPanelPin] = useState<PullRequest>()
  const [confirmedPanelPins, setConfirmedPanelPins] = useState<ReadonlyMap<string, PullRequest>>(() => new Map())
  const [lastContextTokens, setLastContextTokens] = useState<Record<string, number>>(readLastContextTokens)
  const cycleExpandedGroup = useRef<string | undefined>(undefined)
  const cycleScrollTarget = useRef<string | undefined>(undefined)
  const sessionRows = useRef(new Map<string, HTMLElement>())
  const expanded = controlledExpanded ?? internalExpanded
  useEffect(() => {
    setLastContextTokens((previous) => {
      let next: Record<string, number> | undefined
      for (const holon of holons) {
        const current = displayedContextTokens(holon)
        const key = `${project.id}:${holon.id}`
        if (current !== undefined && previous[key] !== current) {
          next ??= { ...previous }
          next[key] = current
        }
      }
      return next ?? previous
    })
  }, [holons, project.id])

  useEffect(() => {
    try {
      window.localStorage.setItem(lastContextTokensStorageKey, JSON.stringify(lastContextTokens))
    } catch {
      // Context usage remains available for this mounted panel if storage is unavailable.
    }
  }, [lastContextTokens])
  const finishPanelCollapse = () => {
    panelClosing.current = false
    if (panelCollapseTimer.current !== undefined) clearTimeout(panelCollapseTimer.current)
    panelCollapseTimer.current = undefined
  }
  const beginPanelCollapse = () => {
    if (panelRef.current?.dataset.expanded !== 'true') return
    if (window.matchMedia('(prefers-reduced-motion: reduce)').matches) {
      finishPanelCollapse()
      return
    }
    panelClosing.current = true
    if (panelCollapseTimer.current !== undefined) clearTimeout(panelCollapseTimer.current)
    panelCollapseTimer.current = setTimeout(finishPanelCollapse, panelCollapseTransitionMs)
  }
  const setExpanded = (next: boolean) => {
    if (next) finishPanelCollapse()
    else beginPanelCollapse()
    if (!next) setInstantExpansion(false)
    if (controlledExpanded === undefined) setInternalExpanded(next)
    onExpandedChange?.(next)
  }
  const clearCyclingInactivityTimer = () => {
    if (cyclingInactivityTimer.current !== undefined) clearTimeout(cyclingInactivityTimer.current)
    cyclingInactivityTimer.current = undefined
  }
  const stopCyclingExpansion = () => {
    clearCyclingInactivityTimer()
    keepOpenAfterCycling.current = false
    setExpanded(false)
  }
  const scheduleCyclingCollapse = () => {
    clearCyclingInactivityTimer()
    cyclingInactivityTimer.current = setTimeout(stopCyclingExpansion, cyclingInactivityMs)
  }
  const expandForPointer = (event: MouseEvent<HTMLElement>) => {
    if (expanded) return
    const bounds = event.currentTarget.getBoundingClientRect()
    const isBesideTopHeader = event.clientY < bounds.top + topHeaderProtectionHeightPx
    if (!isBesideTopHeader || event.clientX >= bounds.left + topHeaderActivationDepthPx) setExpanded(true)
  }
  const pinPosition = panelPinPosition
  if (pinPosition && pinPosition.selectedPullRequestId !== selectedPullRequestId) {
    // Discard the temporary position so revisiting this PR cannot reorder the cycle.
    setPanelPinPosition(undefined)
  }
  const pinnedPullRequestKeptAtEnd = pinPosition && pinPosition.selectedPullRequestId === selectedPullRequestId
    ? pinPosition.pullRequestId
    : undefined
  // A completed refresh may have failed or been superseded. Keep saved pins
  // until tracking acknowledges each write.
  const pullRequestById = useMemo(() => new Map(pullRequests.map((pullRequest) => [pullRequest.id, pullRequest])), [pullRequests])
  const unacknowledgedPins = new Map([...confirmedPanelPins].filter(([id, pullRequest]) => {
    const current = pullRequestById.get(id)
    return Boolean(current?.panel_pinned) !== Boolean(pullRequest.panel_pinned)
  }))
  if (unacknowledgedPins.size !== confirmedPanelPins.size) setConfirmedPanelPins(unacknowledgedPins)
  const { panelPinOverrides, effectivePinnedPullRequests } = useMemo(() => {
    const panelPinOverrides = new Map(confirmedPanelPins)
    if (optimisticPanelPin) panelPinOverrides.set(optimisticPanelPin.id, optimisticPanelPin)
    const retainedPullRequests = new Map(pullRequests
      .filter((pullRequest) => pullRequest.panel_pinned)
      .map((pullRequest) => [pullRequest.id, pullRequest]))
    for (const [id, override] of panelPinOverrides) {
      const current = pullRequestById.get(id) ?? override
      const pullRequest = { ...current, panel_pinned: override.panel_pinned }
      panelPinOverrides.set(id, pullRequest)
      if (pullRequest.panel_pinned) retainedPullRequests.set(id, pullRequest)
      else retainedPullRequests.delete(id)
    }
    return { panelPinOverrides, effectivePinnedPullRequests: [...retainedPullRequests.values()] }
  }, [confirmedPanelPins, optimisticPanelPin, pullRequests, pullRequestById])
  const { independentSessions, sessionGroups, endedSessions } = useMemo(() => {
    const current = activeSidebarHolons(holons, pullRequestByHolon)
    const currentIds = new Set(current.map((holon) => holon.id))
    const ended = holons.filter((holon) => !currentIds.has(holon.id))
    current.sort(compareCreatedAt)
    const branchSessionCounts = countSessionsByBranch(current)
    const belongsToGroup = (holon: Holon) => pullRequestByHolon.has(holon.id)
      || Boolean(holon.worktree_branch && (branchSessionCounts.get(holon.worktree_branch) ?? 0) > 1)
    const independentSessions = current.filter((holon) => !belongsToGroup(holon))
    const attachedSessions = current.filter(belongsToGroup)
    const sessionGroups = groupSessions(attachedSessions, pullRequestByHolon, effectivePinnedPullRequests, selectedPullRequest, pinnedPullRequestKeptAtEnd)
    ended.sort(compareEndedAt)
    return { independentSessions, sessionGroups, endedSessions: ended }
  }, [holons, pullRequestByHolon, effectivePinnedPullRequests, selectedPullRequest, pinnedPullRequestKeptAtEnd])
  const localAddresses = useHolonAddresses(holons, pullRequestByHolon)
  const operations = useContext(OperationsContext)
  const sessionAddresses = operations?.addresses ?? localAddresses
  const previewHolonId = operations?.preview?.holonId
  const scrollArea = useRef<HTMLDivElement>(null)
  const selectedPullRequestGroup = useRef<HTMLElement>(null)
  const pinnedPullRequestIds = new Set(effectivePinnedPullRequests.map((pullRequest) => pullRequest.id))
  const selectedGroupKey = sessionGroups.find((group) => group.pullRequest && group.pullRequest.id === selectedPullRequestId)?.key
  const previewGroup = sessionGroups.find((group) => group.holons.some((holon) => holon.id === previewHolonId))
  // Keep revealed rows available after hover ends, preserving the list position.
  if (previewGroup && collapsedGroups.has(previewGroup.key)) {
    const next = new Set(collapsedGroups)
    next.delete(previewGroup.key)
    setCollapsedGroups(next)
  }
  const orderedCurrentSessions = [...independentSessions, ...sessionGroups.flatMap((group) => group.holons)]
  const visibleSessionCount = orderedCurrentSessions.length + (hideCancelledOrError ? 0 : endedSessions.length)
  useEffect(() => {
    const holonId = cycleScrollTarget.current
    if (!holonId) return
    cycleScrollTarget.current = undefined
    sessionRows.current.get(holonId)?.scrollIntoView({ block: 'nearest', inline: 'nearest' })
  }, [cycleScrollRequest])

  useEffect(() => {
    const group = selectedPullRequestGroup.current
    const list = scrollArea.current
    if (!group || !list) return

    const revealGroup = () => {
      const groupBounds = group.getBoundingClientRect()
      const listBounds = list.getBoundingClientRect()
      // For oversized groups, show the header and as many holons as fit below it.
      const visibleHeight = Math.min(groupBounds.height, list.clientHeight)
      const delta = groupBounds.top < listBounds.top
        ? groupBounds.top - listBounds.top
        : Math.max(0, groupBounds.top + visibleHeight - listBounds.top - list.clientHeight)
      if (delta) {
        list.scrollTo({ top: list.scrollTop + delta, behavior: window.matchMedia('(prefers-reduced-motion: reduce)').matches ? 'instant' : 'smooth' })
      }
    }

    revealGroup()
    if (typeof ResizeObserver === 'undefined') return
    const observer = new ResizeObserver(revealGroup)
    observer.observe(group)
    observer.observe(list)
    return () => observer.disconnect()
  }, [selectedPullRequestId, selectedGroupKey])

  useEffect(() => {
    if (!previewHolonId) return
    const row = sessionRows.current.get(previewHolonId)
    const list = scrollArea.current
    if (!row || !list) return
    const rowBounds = row.getBoundingClientRect()
    const listBounds = list.getBoundingClientRect()
    const delta = rowBounds.top < listBounds.top
      ? rowBounds.top - listBounds.top
      : rowBounds.bottom > listBounds.bottom ? rowBounds.bottom - listBounds.bottom : 0
    if (delta) {
      // Scroll this list explicitly: scrollIntoView can also move the PR page.
      list.scrollTo({ top: list.scrollTop + delta, behavior: window.matchMedia('(prefers-reduced-motion: reduce)').matches ? 'instant' : 'smooth' })
    }
  }, [previewHolonId, operations?.preview, holons, pullRequestByHolon])

  const revealCycleGroup = (targetGroupKey?: string, expandGroup = true) => {
    const previousGroupKey = cycleExpandedGroup.current
    const nextCollapsedGroups = new Set(collapsedGroups)
    let nextCycleExpandedGroup = previousGroupKey

    if (previousGroupKey && previousGroupKey !== targetGroupKey) {
      nextCollapsedGroups.add(previousGroupKey)
      nextCycleExpandedGroup = undefined
    }
    if (expandGroup && targetGroupKey && nextCollapsedGroups.has(targetGroupKey)) {
      nextCollapsedGroups.delete(targetGroupKey)
      nextCycleExpandedGroup = targetGroupKey
    }
    cycleExpandedGroup.current = nextCycleExpandedGroup
    setCollapsedGroups(nextCollapsedGroups)
  }

  const expandForCycling = () => {
    keepOpenAfterCycling.current = true
    setInstantExpansion(true)
    setExpanded(true)
    scheduleCyclingCollapse()
  }

  const selectCycleTarget = (holon: Holon) => {
    expandForCycling()
    const group = sessionGroups.find((candidate) => candidate.holons.some((candidateHolon) => candidateHolon.id === holon.id))
    revealCycleGroup(group?.key)
    cycleScrollTarget.current = holon.id
    setCycleScrollRequest((current) => current + 1)
    onSessionSelect?.(holon)
  }

  useNavigationArrows('vertical', (direction) => {
    type CycleRow = { holon: Holon } | { pullRequest: PullRequest, groupKey: string }
    const rows: CycleRow[] = [
      ...independentSessions.map((holon) => ({ holon })),
      ...sessionGroups.flatMap((group): CycleRow[] => [
        ...(group.pullRequest && (group.holons.length > 0 || pinnedPullRequestIds.has(group.pullRequest.id)) ? [{ pullRequest: group.pullRequest, groupKey: group.key }] : []),
        ...group.holons.map((holon) => ({ holon })),
      ]),
    ]
    if (rows.length === 0) return false
    expandForCycling()
    const index = rows.findIndex((row) => 'holon' in row
      ? row.holon.id === selectedSessionId
      : row.pullRequest.id === selectedPullRequestId)
    const nextIndex = index < 0
      ? direction === 1 ? 0 : rows.length - 1
      : (index + direction + rows.length) % rows.length
    const target = rows[nextIndex]
    if ('pullRequest' in target) {
      if (target.pullRequest.id !== selectedPullRequestId) {
        revealCycleGroup(target.groupKey, false)
        navigate(`/pulls/${target.pullRequest.id}`)
      }
    } else if (target.holon.id !== selectedSessionId) {
      holonStore?.activateHolon(target.holon.id, false)
      selectCycleTarget(target.holon)
      if (!onSessionSelect) navigate(sessionHref(target.holon), { state: { focusHolonTerminal: true } })
    }
    return true
  })

  const isToRightOfPanel = (event: { clientX: number, clientY: number }) => {
    const bounds = panelRef.current?.getBoundingClientRect()
    return Boolean(bounds && bounds.height > 0 && event.clientX >= bounds.right && event.clientY >= bounds.top && event.clientY <= bounds.bottom)
  }

  const handleOutsideActivity = useEffectEvent((event: PointerEvent | KeyboardEvent | WheelEvent) => {
    if (event instanceof KeyboardEvent && event.key === 'Escape' && panelRef.current?.querySelector('[popover]:popover-open')) {
      // Let the browser dismiss the preview before Escape reaches the panel or terminal.
      event.stopImmediatePropagation()
      return
    }
    if (event.type === 'pointermove') {
      const pointer = event as PointerEvent
      const previous = pointerPosition.current
      pointerPosition.current = { x: pointer.clientX, y: pointer.clientY }
      if (previous && previous.x === pointer.clientX && previous.y === pointer.clientY) return
    }
    const eventIsInsidePanel = event.target instanceof Node && panelRef.current?.contains(event.target)
    if (event.type === 'pointermove') {
      if (isToRightOfPanel(event as PointerEvent)) {
        if (keepOpenAfterCycling.current) scheduleCyclingCollapse()
        return
      }
      if (expanded && !eventIsInsidePanel && !keepOpenAfterCycling.current) setExpanded(false)
    }
    if (event instanceof KeyboardEvent && event.key === 'Escape' && !eventIsInsidePanel && !document.querySelector('[role="dialog"][aria-modal="true"], dialog[open]')) {
      const panelExpanded = panelRef.current?.dataset.expanded === 'true'
      if (panelExpanded || panelClosing.current) {
        event.preventDefault()
        event.stopImmediatePropagation()
        if (panelExpanded) stopCyclingExpansion()
      }
      return
    }
    if (!keepOpenAfterCycling.current) return
    if (event instanceof KeyboardEvent && ['Alt', 'Shift', 'Control', 'Meta'].includes(event.key)) return
    if (eventIsInsidePanel) {
      scheduleCyclingCollapse()
      return
    }
    stopCyclingExpansion()
  })

  useEffect(() => {
    // Register after navigation so handled cycling shortcuts never dismiss the panel.
    document.addEventListener('keydown', handleOutsideActivity, true)
    document.addEventListener('pointermove', handleOutsideActivity, true)
    document.addEventListener('pointerdown', handleOutsideActivity, true)
    document.addEventListener('wheel', handleOutsideActivity, { capture: true, passive: true })
    return () => {
      clearCyclingInactivityTimer()
      finishPanelCollapse()
      document.removeEventListener('keydown', handleOutsideActivity, true)
      document.removeEventListener('pointermove', handleOutsideActivity, true)
      document.removeEventListener('pointerdown', handleOutsideActivity, true)
      document.removeEventListener('wheel', handleOutsideActivity, true)
    }
  }, [])

  const renderSessionName = (holon: Holon) => {
    const name = holonDisplayName(holon)
    if (rename.renaming?.holonId !== holon.id) return <SessionTitle name={name} onOverflowChange={(overflowed) => {
      setClippedNames((current) => current[holon.id] === overflowed ? current : { ...current, [holon.id]: overflowed })
    }} />
    return (
      <input
        className={styles.sessionNameInput}
        value={rename.renaming.value}
        aria-label={`Rename ${name}`}
        autoFocus
        onBlur={() => void rename.commit()}
        onChange={(event) => rename.change(event.target.value)}
        onClick={(event) => { event.preventDefault(); event.stopPropagation() }}
        onDoubleClick={(event) => { event.preventDefault(); event.stopPropagation() }}
        onFocus={(event) => event.currentTarget.select()}
        onKeyDown={(event) => {
          event.stopPropagation()
          if (event.key === 'Enter') { event.preventDefault(); void rename.commit() }
          if (event.key === 'Escape') { event.preventDefault(); rename.cancel() }
        }}
      />
    )
  }

  const renderHolon = (holon: Holon, group?: SessionGroup) => {
    const inputLabel = inputStateLabel(holon)
    const badgeStatus = holonDisplayStatus(holon)
    const badgeLabel = holonDisplayStatusLabel(holon)
    const address = sessionAddresses.get(holon.id)
    const needsInput = sessionNeedsInput(holon)
    const resumable = isSessionResumable(holon)
    const resumeAvailable = requiredHarnessTypes(holon).every((type) => hasAvailableHarness(capabilities, type))
    const renamingHolon = rename.renaming?.holonId === holon.id
    const nameTooltip = !renamingHolon && clippedNames[holon.id] ? holonDisplayName(holon) : undefined
    const selected = selectedSessionId === holon.id
    const recovering = holon.status === 'restoring' || holon.status === 'recovery_failed'
    const stateLabel = recovering ? badgeLabel : inputLabel || (resumable && badgeStatus !== 'finalizing' ? 'Workspace archived.' : badgeLabel)
    const branch = holon.worktree_branch || project.default_branch
    // A group header already names the branch its Holons share.
    const showBranch = branch !== group?.branch
    const statusClassName = sessionStatusClassName(badgeStatus, needsInput)
    const contextTokens = displayedContextTokens(holon) ?? lastContextTokens[`${project.id}:${holon.id}`]
    const contextUsageLabel = formatContextTokens(contextTokens)
    const showsContextUsage = Boolean(contextUsageLabel)
    const contextUsageClassName = contextTokens !== undefined && contextTokens >= contextTokenThresholds.danger
      ? styles.sessionContextUsageDanger
      : contextTokens !== undefined && contextTokens >= contextTokenThresholds.warning
        ? styles.sessionContextUsageWarning
        : ''
    const content = (
      <>
        <HolonStatusIcon address={address} status={badgeStatus} needsInput={needsInput} />
        <span className={styles.sessionCopy}>
          <span className={styles.sessionNameLine}>{renderSessionName(holon)}</span>
          <span className={styles.sessionCollapsedMeta}>
            {showsContextUsage && <><span className={`${styles.sessionContextUsage} ${contextUsageClassName}`}>{contextUsageLabel}</span><span className={styles.sessionMetaSeparator} aria-hidden="true">·</span></>}
            <time dateTime={holon.created_at}>{formatRelativeTime(holon.created_at)}</time>
          </span>
          <span className={[styles.sessionMeta, showsContextUsage ? styles.contextUsageMeta : ''].filter(Boolean).join(' ')}>
            {showsContextUsage && <><span className={`${styles.sessionContextUsage} ${contextUsageClassName}`} title={nameTooltip ?? `Current context: ${contextTokens?.toLocaleString()} tokens`}>{contextUsageLabel}</span><span className={styles.sessionMetaSeparator} aria-hidden="true">·</span></>}
            {showBranch && <><span className={styles.sessionBranch} title={nameTooltip ?? branch}><GitBranch aria-hidden="true" />{branch}</span><span className={styles.sessionMetaSeparator} aria-hidden="true">·</span></>}
            {!showsContextUsage && <><span className={`${styles.sessionState} ${statusClassName}`}>{stateLabel}</span><span className={styles.sessionMetaSeparator} aria-hidden="true">·</span></>}
            <time dateTime={holon.created_at} title={nameTooltip ?? formatTime(holon.created_at)}>{formatRelativeTime(holon.created_at)}</time>
          </span>
        </span>
      </>
    )
    const className = [
      styles.sessionRow,
      resumable ? styles.resumableCard : '',
      showsContextUsage ? styles.contextUsageCard : '',
      renamingHolon ? styles.renamingCard : '',
      needsInput ? styles.needsInput : '',
      selected ? styles.selectedCard : '',
      holon.id === previewHolonId ? styles.previewCard : '',
    ].filter(Boolean).join(' ')

    return (
      <article
        className={className}
        key={holon.id}
        data-holon-id={holon.id}
        title={nameTooltip}
        data-previewed={holon.id === previewHolonId || undefined}
        ref={(element) => {
          if (element) sessionRows.current.set(holon.id, element)
          else sessionRows.current.delete(holon.id)
        }}
      >
        {renamingHolon
          ? <div className={styles.sessionRowLink}>{content}</div>
          : <Link
              className={styles.sessionRowLink}
              to={sessionHref(holon)}
              state={{ focusHolonTerminal: true }}
              aria-label={`${address ? `Agent ${address}, ` : ''}${holonDisplayName(holon)}, ${stateLabel}, branch ${branch}${selected ? ', current holon' : ''}`}
              aria-current={selected ? 'page' : undefined}
              onClick={(event) => {
                if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return
                holonStore?.activateHolon(holon.id, selected && location.pathname === sessionHref(holon))
                onSessionSelect?.(holon)
              }}
            >
              {content}
            </Link>}
        {!renamingHolon && (
          <button type="button" className={styles.sessionRenameButton} aria-label={`Rename ${holonDisplayName(holon)}`} title={nameTooltip ?? 'Rename holon'} onClick={() => rename.begin(holon)}>
            <Pencil />
          </button>
        )}
        {resumable && (
          <button
            type="button"
            className={styles.resumeButton}
            disabled={!resumeAvailable || reopeningHolon === holon.id}
            aria-label={`Reopen ${holonDisplayName(holon)}`}
            title={nameTooltip ?? (reopeningHolon === holon.id ? 'Reopening...' : 'Reopen')}
            onClick={() => onReopen(holon)}
          >
            <ChevronRight />
            <span>{reopeningHolon === holon.id ? 'Reopening...' : 'Reopen'}</span>
          </button>
        )}
        {needsInput && !renamingHolon && (
          <span
            className={styles.attentionEstimateStatus}
            role="img"
            aria-label={`Effort estimate for ${holonDisplayName(holon)} (coming soon)`}
            title="Estimate the work needed to unblock this Holon — coming soon"
          >
            <span className={styles.attentionEstimateTitle}>
              <Gauge aria-hidden="true" />
              <span>Effort</span>
            </span>
            <span className={styles.attentionEstimateSubtitle}>Coming soon</span>
          </span>
        )}
      </article>
    )
  }

  const renderPanelPin = (pullRequest: PullRequest) => {
    const pinned = Boolean((panelPinOverrides.get(pullRequest.id) ?? pullRequest).panel_pinned)
    const reference = pullRequestLabel(pullRequest)
    return (
      <button
        className={styles.groupPin}
        type="button"
        aria-label={`${pinned ? 'Unpin' : 'Pin'} ${reference} ${pinned ? 'from' : 'to'} Holons panel`}
        aria-pressed={pinned}
        disabled={panelPinPending}
        title={pinned ? 'Remove from Holons panel' : 'Keep in Holons panel'}
        onClick={() => {
          if (panelPinPending) return
          setPanelPinPending(true)
          setPanelPinPosition(pinned ? undefined : { pullRequestId: pullRequest.id, selectedPullRequestId })
          const updatedPin = { ...pullRequest, panel_pinned: !pinned }
          setOptimisticPanelPin(updatedPin)
          void onTogglePullRequestPin(panelPinOverrides.get(pullRequest.id) ?? pullRequest)
            .then(() => {
              setConfirmedPanelPins((pins) => new Map(pins).set(pullRequest.id, updatedPin))
              setOptimisticPanelPin(undefined)
            })
            .catch((value) => {
              setPanelPinPosition(undefined)
              setOptimisticPanelPin(undefined)
              console.error(`Failed to update ${reference} pin in the Holons panel.`, value)
            })
            .finally(() => { setPanelPinPending(false) })
        }}
      >
        <PanelPinIcon filled={pinned} />
      </button>
    )
  }

  const renderSessionGroup = (group: SessionGroup) => {
    const { pullRequest } = group
    const empty = group.holons.length === 0
    const collapsed = !empty && collapsedGroups.has(group.key)
    const attentionCount = group.holons.filter(sessionNeedsInput).length
    const workingCount = group.holons.filter(sessionIsWorking).length
    const selectedPullRequest = Boolean(pullRequest && pullRequest.id === selectedPullRequestId)
    const containsSelectedHolon = group.holons.some((holon) => holon.id === selectedSessionId)
    const reference = pullRequest ? pullRequestLabel(pullRequest) : ''
    const title = pullRequest ? `${reference} · ${pullRequest.title}` : group.branch || 'Unattached'
    const detail = pullRequest
      ? `${pullRequestStatusLabel(pullRequest.status)} · ${pullRequest.head_branch}`
      : 'Branch · no pull request'
    const holonCount = `${group.holons.length} holon${group.holons.length === 1 ? '' : 's'}`
    const activity = [
      workingCount > 0 ? `${workingCount} working` : '',
      attentionCount > 0 ? `${attentionCount} awaiting input` : '',
    ].filter(Boolean).join(', ')
    const toggle = () => {
      if (cycleExpandedGroup.current === group.key) cycleExpandedGroup.current = undefined
      setCollapsedGroups((current) => {
        const next = new Set(current)
        if (next.has(group.key)) next.delete(group.key)
        else next.add(group.key)
        return next
      })
    }
    const pullRequestNumber = pullRequest?.sync_data.github?.number
    const icon = (
      <span className={styles.groupIcon} data-status={pullRequest?.status ?? 'branch'} data-attention={(collapsed && attentionCount > 0) || undefined} aria-hidden="true">
        {pullRequest ? <PullRequestStatusIcon status={pullRequest.status} /> : <GitBranch />}
      </span>
    )
    const titleText = <GroupTitle name={pullRequest ? pullRequest.title : title} />
    // The rail has room for one signal; the expanded panel shows them all.
    const railSignal = empty ? null
      : attentionCount > 0 ? <span className={styles.groupAttention}><AlertTriangle /> {attentionCount}</span>
        : workingCount > 0 ? <span className={styles.groupWorking}><CircleDashed /> {workingCount}</span>
          : <span>{group.holons.length}</span>
    return (
      <section className={styles.sessionGroup} key={group.key} ref={selectedPullRequest ? selectedPullRequestGroup : undefined} aria-label={title}>
        <div className={[
          styles.groupHeader,
          empty ? styles.emptyGroupHeader : '',
        ].filter(Boolean).join(' ')}>
          {!empty && <button
                className={styles.groupToggle}
                type="button"
                aria-expanded={!collapsed}
                aria-label={`${collapsed ? 'Expand' : 'Collapse'} ${title}, ${detail}, ${holonCount}${activity ? `, ${activity}` : ''}${containsSelectedHolon ? ', contains current holon' : ''}`}
                title={collapsed ? 'Expand' : 'Collapse'}
                onClick={toggle}
              >
                <ChevronRight />
              </button>}
          {pullRequest
            ? <>
                <PullRequestPreviewLink
                  className={`${styles.groupLink} ${styles.groupPullRequestLink}`}
                  pullRequest={pullRequest}
                  label={reference}
                  aria-current={selectedPullRequest ? 'page' : undefined}
                  aria-label={`${selectedPullRequest && !empty ? collapsed ? 'Expand' : 'Collapse' : 'Open'} ${reference}`}
                  onClick={(event) => {
                    if (!selectedPullRequest || empty || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return
                    event.preventDefault()
                    toggle()
                  }}
                >
                  {icon}
                  {pullRequestNumber && <span className={styles.groupNumber}>#{pullRequestNumber}</span>}
                </PullRequestPreviewLink>
                {empty
                  ? <span className={`${styles.groupLink} ${styles.groupTitleStatic}`}>{titleText}</span>
                  : <button className={`${styles.groupLink} ${styles.groupTitleButton}`} type="button" tabIndex={-1} aria-hidden="true" onClick={toggle}>{titleText}</button>}
              </>
            : <button className={styles.groupLink} type="button" aria-expanded={!collapsed} title={`${title} — ${detail}`} onClick={toggle}>{icon}{titleText}</button>}
          {!pullRequest && <span className={styles.groupRailSignal} aria-hidden="true">{railSignal}</span>}
          {!pullRequest && <span className={styles.groupMetrics}>
            {empty
              ? <span className={styles.groupEmpty}>No active holons</span>
              : <>
                  {attentionCount > 0 && <span className={styles.groupAttention} title={`${attentionCount} holon${attentionCount === 1 ? '' : 's'} awaiting input`}><AlertTriangle /> {attentionCount}</span>}
                  {workingCount > 0 && <span className={styles.groupWorking} title={`${workingCount} working holon${workingCount === 1 ? '' : 's'}`}><CircleDashed /> {workingCount}</span>}
                  {(workingCount > 0 || attentionCount > 0) && <span className={styles.sessionMetaSeparator} aria-hidden="true">·</span>}
                  <span title={holonCount}>{group.holons.length}</span>
                </>}
          </span>}
          {pullRequest && renderPanelPin(pullRequest)}
        </div>
        {!empty && !collapsed && <div className={styles.groupSessions}>{group.holons.map((holon) => renderHolon(holon, group))}</div>}
      </section>
    )
  }

  return (
    <aside
      ref={panelRef}
      className={styles.panel}
      aria-label="Holons"
      data-app-panel="operations"
      data-testid="operations-panel"
      data-expanded={expanded}
      data-instant-expansion={instantExpansion}
      onTransitionEnd={(event) => {
        if (event.target === event.currentTarget && event.propertyName === 'width' && event.currentTarget.dataset.expanded !== 'true') finishPanelCollapse()
      }}
      onMouseEnter={expandForPointer}
      onMouseMove={expandForPointer}
      onMouseLeave={(event) => { if (!keepOpenAfterCycling.current && !isToRightOfPanel(event)) setExpanded(false) }}
      onFocusCapture={() => setExpanded(true)}
      onBlurCapture={(event) => {
        if (!keepOpenAfterCycling.current && !event.currentTarget.contains(event.relatedTarget)) setExpanded(false)
      }}
    >
      <section className={`${styles.section} ${styles.agents}`} aria-labelledby="agents-title">
        <header className={`${styles.sectionHeader} ${styles.agentsHeader}`}>
          <span className={`${styles.headingIcon} ${styles.agentHeadingIcon}`} aria-hidden="true"><Bot /></span>
          <div className={styles.headingCopy}>
            <h2 id="agents-title">Holons</h2>
            <span className={styles.headingCount}>{visibleSessionCount}</span>
          </div>
          {!sessionsLoading && !sessionsError && (
            <button className={styles.newAgentButton} type="button" aria-label="Create new Holon from holons panel" title="New Holon" onClick={onNewAgent}>
              <Plus />
            </button>
          )}
        </header>
        <div className={styles.scrollArea} ref={scrollArea} role="region" aria-label="Holon list">
          {sessionsLoading && <PanelMessage>Loading holons…</PanelMessage>}
          {sessionsError && <PanelMessage>Sessions unavailable.</PanelMessage>}
          {reopenError && <PanelMessage>{reopenError}</PanelMessage>}
          {rename.error && <p className={styles.empty} role="alert">{rename.error}</p>}
          {independentSessions.length > 0 && <div className={styles.independentSessions} aria-label="Independent holons">{independentSessions.map((holon) => renderHolon(holon))}</div>}
          {sessionGroups.map(renderSessionGroup)}
          {endedSessions.length > 0 && (
            <button className={styles.endedDisclosure} type="button" onClick={() => setHideCancelledOrError((current) => !current)}>
              <span className={styles.endedDisclosureLabel}>{hideCancelledOrError ? `Show ended holons (${endedSessions.length})` : 'Hide ended holons'}</span>
              {hideCancelledOrError ? <ChevronDown /> : <ChevronUp />}
            </button>
          )}
          {!hideCancelledOrError && endedSessions.map((holon) => renderHolon(holon))}
        </div>
      </section>
      <footer className={styles.hint}><CircleHelp /> <span>Hover or focus to expand</span></footer>
    </aside>
  )
}

type SessionGroup = {
  key: string
  branch?: string
  pullRequest?: PullRequest
  holons: Holon[]
}

function compareCreatedAt(left: Holon, right: Holon) {
  return new Date(left.created_at).getTime() - new Date(right.created_at).getTime() || left.id.localeCompare(right.id)
}

function compareEndedAt(left: Holon, right: Holon) {
  const leftEndedAt = left.finished_at ?? left.created_at
  const rightEndedAt = right.finished_at ?? right.created_at
  return new Date(rightEndedAt).getTime() - new Date(leftEndedAt).getTime() || left.id.localeCompare(right.id)
}

function countSessionsByBranch(holons: Holon[]) {
  const counts = new Map<string, number>()
  holons.forEach((holon) => {
    if (!holon.worktree_branch) return
    counts.set(holon.worktree_branch, (counts.get(holon.worktree_branch) ?? 0) + 1)
  })
  return counts
}

function groupSessions(
  holons: Holon[],
  pullRequestByHolon: ReadonlyMap<string, PullRequest>,
  pinnedPullRequests: PullRequest[],
  selectedPullRequest?: PullRequest,
  pinnedPullRequestKeptAtEnd?: string,
): SessionGroup[] {
  const groups = new Map<string, SessionGroup>()
  holons.forEach((holon) => {
    const pullRequest = pullRequestByHolon.get(holon.id)
    const branch = pullRequest?.head_branch || holon.worktree_branch
    const key = pullRequest ? `pull-request:${pullRequest.id}` : branch ? `branch:${branch}` : 'unattached'
    const existing = groups.get(key)
    if (existing) {
      existing.holons.push(holon)
      if (!existing.pullRequest && pullRequest) existing.pullRequest = pullRequest
      return
    }
    groups.set(key, { key, branch, pullRequest, holons: [holon] })
  })
  const result = [...groups.values()]
  const visibleGroupKeys = new Set(groups.keys())
  const retainedPullRequests = [...pinnedPullRequests].sort(comparePullRequestCreatedAt)
  const keptIndex = retainedPullRequests.findIndex((pullRequest) => pullRequest.id === pinnedPullRequestKeptAtEnd)
  if (keptIndex >= 0) retainedPullRequests.push(...retainedPullRequests.splice(keptIndex, 1))
  if (selectedPullRequest && !retainedPullRequests.some((pullRequest) => pullRequest.id === selectedPullRequest.id)) {
    retainedPullRequests.push(selectedPullRequest)
  }
  for (const pullRequest of retainedPullRequests) {
    const key = `pull-request:${pullRequest.id}`
    if (visibleGroupKeys.has(key)) continue
    result.push({ key, branch: pullRequest.head_branch, pullRequest, holons: [] })
    visibleGroupKeys.add(key)
  }
  return result
}

function comparePullRequestCreatedAt(left: PullRequest, right: PullRequest) {
  return new Date(left.created_at).getTime() - new Date(right.created_at).getTime() || left.id.localeCompare(right.id)
}

function sessionNeedsInput(holon: Holon) {
  return Boolean(holonNeedsInput(holon))
}

function sessionIsWorking(holon: Holon) {
  return holonDisplayStatus(holon) === 'working'
}

function pullRequestLabel(pullRequest: PullRequest) {
  const number = pullRequest.sync_data.github?.number
  return number ? `PR #${number}` : 'PR'
}

function pullRequestStatusLabel(status: PullRequest['status']) {
  if (status === 'wip') return 'In preparation'
  return `${status.slice(0, 1).toUpperCase()}${status.slice(1)}`
}

function useTextOverflow<T extends HTMLElement>(text: string, onOverflowChange?: (overflowed: boolean) => void) {
  const containerRef = useRef<T>(null)
  const textRef = useRef<HTMLSpanElement>(null)
  const [overflowed, setOverflowed] = useState(false)
  const reportOverflow = useEffectEvent((clipped: boolean) => onOverflowChange?.(clipped))

  useEffect(() => {
    const container = containerRef.current
    const content = textRef.current
    if (!container || !content) return
    const updateOverflow = () => {
      const width = container.clientWidth
      const overflow = Math.max(0, content.scrollWidth - width)
      const clipped = width > 0 && overflow > 1
      setOverflowed(clipped)
      reportOverflow(clipped)
    }
    updateOverflow()
    if (typeof ResizeObserver === 'undefined') return
    const observer = new ResizeObserver(updateOverflow)
    observer.observe(container)
    observer.observe(content)
    return () => observer.disconnect()
  }, [text])

  return { containerRef, textRef, overflowed }
}

function SessionTitle({ name, onOverflowChange }: { name: string, onOverflowChange: (overflowed: boolean) => void }) {
  const { containerRef, textRef, overflowed } = useTextOverflow<HTMLElement>(name, onOverflowChange)
  return <strong ref={containerRef} className={styles.sessionTitle} data-overflowed={overflowed || undefined}><span ref={textRef} className={styles.sessionTitleText}>{name}</span></strong>
}

function GroupTitle({ name }: { name: string }) {
  const { containerRef, textRef, overflowed } = useTextOverflow<HTMLSpanElement>(name)
  return <span ref={containerRef} className={styles.groupTitle} data-overflowed={overflowed || undefined}><span ref={textRef} className={styles.groupTitleText}>{name}</span></span>
}

function PullRequestStatusIcon({ status }: { status: PullRequest['status'] }) {
  if (status === 'merged') return <GitMerge />
  if (status === 'closed') return <GitPullRequestClosed />
  if (status === 'wip' || status === 'draft') return <GitPullRequestDraft />
  return <GitPullRequest />
}

// Tabler Pin, MIT licensed: https://github.com/tabler/tabler-icons/tree/v3.46.0/icons
function PanelPinIcon({ filled }: { filled: boolean }) {
  if (filled) {
    return (
      <svg viewBox="0 0 24 24" fill="currentColor" aria-hidden="true">
        <path d="M15.113 3.21l.094.083 5.5 5.5a1 1 0 0 1-1.175 1.59l-3.172 3.171-1.424 3.797a1 1 0 0 1-.158.277l-.07.08-1.5 1.5a1 1 0 0 1-1.32.082l-.095-.083L9 16.335l-3.793 3.792a1 1 0 0 1-1.497-1.32l.083-.094L7.585 15l-2.792-2.793a1 1 0 0 1-.083-1.32l.083-.094 1.5-1.5a1 1 0 0 1 .258-.187l.098-.042 3.796-1.425 3.171-3.17a1 1 0 0 1 1.497-1.26z" />
      </svg>
    )
  }
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d="M15 4.5l-4 4l-4 1.5l-1.5 1.5l7 7l1.5-1.5l1.5-4l4-4" />
      <path d="M9 15l-4.5 4.5" />
      <path d="M14.5 4l5.5 5.5" />
    </svg>
  )
}

function PanelMessage({ children }: { children: React.ReactNode }) {
  return <p className={styles.empty}>{children}</p>
}
