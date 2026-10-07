import { compareHarnessTabs, compareIDETabs, compareManualTabs, orderedSessionTabs, selectedHolonTab, terminalStatuses, visibleAgentSessions, visibleHolonIDEs, visibleManualTerminals, type HolonTerminalTab } from './holonTabs'
import { useAgentCapabilities } from '../../data/useAgentCapabilities'
import { agentDisplayActivity, holonDisplayStatus, holonDisplayStatusLabel } from '../agents/holonDisplay'
import { Ban, Code2, GitPullRequest, RefreshCw, Send, SquareTerminal } from 'lucide-react'
import { useCallback, useEffect, useLayoutEffect, useRef, useState, type SetStateAction } from 'react'
import { Link, useLocation, useNavigate, useParams } from 'react-router-dom'
import { Button } from '../../components/Button'
import { Dialog } from '../../components/Dialog'
import { api, type ApiError } from '../../data/api'
import { defaultHarnessType, harnessDisplayLabel, harnessTypes, hasAvailableHarness, isSessionResumable, requiredHarnessTypes } from '../../data/harness'
import type { AgentSession, HarnessType, ManualTerminal, Holon, HolonIDE, HolonStatus, HolonTabRef, WorkspaceInspection } from '../../data/types'
import { usePolling } from '../../data/usePolling'
import { useProject } from '../project/ProjectContext'
import { useHolonStore } from '../project/holonStoreContext'
import { useNavigationArrows } from '../navigation/useNavigationArrows'
import { CreatePullRequestMenu } from '../pullRequests/PullRequestViews'
import { pullRequestCreationOptions, type PullRequestCreationStatus } from '../pullRequests/lifecycle'
import { defaultPullRequestTitle } from '../pullRequests/pullRequestHelpers'
import { HolonDiffPanel } from './HolonDiffPanel'
import { HolonNotice } from './HolonNotice'
import { HolonHeader } from './HolonHeader'
import { HolonCommitAction } from './HolonCommitAction'
import { HolonWorkspace } from './HolonWorkspace'
import { useHolonWorkspaceNavigation } from './useHolonWorkspaceNavigation'
import { ForkTabIcon, HarnessTabIcon } from './HarnessTabIcon'
import { HolonTabStrip, type HolonTabStripItem, type SessionTabTone } from './HolonTabStrip'
import { HolonTabOptions } from './HolonTabOptions'
import type { TerminalAvailabilityProbe } from './terminalProtocol'
import { TerminalSurface, useTerminalWorkspace } from './TerminalWorkspaceProvider'
import { HolonIDEPane } from './HolonIDEPane'
import styles from './HolonTerminal.module.css'


function newerHolonIDE(previous: HolonIDE | undefined, candidate: HolonIDE) {
  if (!previous || timestamp(candidate.updated_at) >= timestamp(previous.updated_at)) return candidate
  return previous
}

function ideIconTone(ide: HolonIDE): SessionTabTone {
  if (ide.state === 'failed' || ide.state === 'lost') return 'attention'
  if (ide.state === 'starting' || ide.state === 'stopping' || ide.state === 'suspended') return 'idle'
  return 'running'
}

type SocketFactory = (url: string) => WebSocket
const defaultSocketFactory: SocketFactory = (url) => new WebSocket(url)
const defaultTerminalAvailabilityProbe: TerminalAvailabilityProbe = async (socketURL) => {
  const url = new URL(socketURL)
  url.protocol = url.protocol === 'wss:' ? 'https:' : 'http:'
  try {
    const response = await fetch(url, { method: 'HEAD', cache: 'no-store' })
    if (response.status === 404) return 'missing'
    return response.ok ? 'available' : 'retry'
  } catch {
    return 'retry'
  }
}
const maxManualTabs = 100

type AgentCreationAttempt = { id: string; harness: HarnessType }
// Survive navigation even when storage is unavailable. Null marks a completed
// attempt so a failed storage removal cannot resurrect it.
const pendingAgentAttempts = new Map<string, AgentCreationAttempt | null>()

type FocusLayer = 'diff-review' | 'terminal'

type HarnessStatusRecord = {
  status: HolonStatus
  updatedAt?: string
}

type HolonTerminalProps = {
  socketFactory?: SocketFactory
}

export function HolonTerminal({
  socketFactory = defaultSocketFactory,
}: HolonTerminalProps) {
  const availabilityProbe = socketFactory === defaultSocketFactory ? defaultTerminalAvailabilityProbe : undefined
  const { holonId = '' } = useParams()
  const terminalWorkspace = useTerminalWorkspace()
  const navigate = useNavigate()
  const location = useLocation()
  const focusedNavigation = useRef<string | undefined>(undefined)
  const project = useProject()
  const { getHolon, updateHolon, selectHolonTab, dismissSelectionError, selectionErrors, refresh: refreshSessions } = useHolonStore()
  const loadSessionPullRequest = useCallback(async (signal: AbortSignal) => {
    try {
      const pullRequest = (await api.sessionPullRequest(holonId, signal)).pull_request
      return { holonId, pullRequest }
    } catch (value) {
      if ((value as ApiError).code === 'pull_request_not_found') return { holonId, pullRequest: undefined }
      throw value
    }
  }, [holonId])
  const sessionPullRequest = usePolling(loadSessionPullRequest)
  const capabilityState = useAgentCapabilities()
  const acceptSessionEvent = useCallback((next: SetStateAction<Holon | undefined>) => {
    updateHolon(holonId, next)
  }, [holonId, updateHolon])
  const creationInFlight = useRef(new Set<string>())
  const [creationByHolon, setCreationByHolon] = useState<Record<string, { status?: PullRequestCreationStatus; error?: string }>>({})
  const creatingStatus = creationByHolon[holonId]?.status
  const creationError = creationByHolon[holonId]?.error
  const activeHolonId = useRef<string | undefined>(holonId)
  useLayoutEffect(() => {
    activeHolonId.current = holonId
    return () => { activeHolonId.current = undefined }
  }, [holonId])
  const [endSessionDialogOpen, setEndSessionDialogOpen] = useState(false)
  const [lastAgentChoice, setLastAgentChoice] = useState<{ holonId: string; harnessType: HarnessType }>()
  const commitForkInFlight = useRef(false)
  const [startingCommitFork, setStartingCommitFork] = useState(false)
  const [forkingAgentId, setForkingAgentId] = useState('')
  const [resuming, setResuming] = useState(false)
  const publishingInFlight = useRef(false)
  const [publishing, setPublishing] = useState(false)
  const rebaseForkInFlight = useRef(false)
  const [startingRebaseFork, setStartingRebaseFork] = useState(false)
  const agentRequests = useRef(new Set<string>())
  const [addingAgentHolons, setAddingAgentHolons] = useState<Record<string, boolean>>({})
  const [agentCreationErrors, setAgentCreationErrors] = useState<Record<string, string>>({})
  const activeHolon = useRef(holonId)
  useLayoutEffect(() => { activeHolon.current = holonId; return () => { activeHolon.current = '' } }, [holonId])
  const addingAgent = Boolean(addingAgentHolons[holonId])
  const agentCreationError = agentCreationErrors[holonId]
  const [addingManualTerminal, setAddingManualTerminal] = useState(false)
  const [retryingIDE, setRetryingIDE] = useState('')
  const [ending, setEnding] = useState(false)
  const [socketError, setSocketError] = useState('')
  const [renamingTab, setRenamingTab] = useState<{ id: string; value: string } | null>(null)
  const [terminalFocusRequest, setTerminalFocusRequest] = useState<{ holonId: string, tabId: string, sequence: number }>()
  const [activatedTabsByHolon, setActivatedTabsByHolon] = useState<Record<string, Record<string, boolean>>>({})
  const harnessStatusByHolon = useRef<Record<string, HarnessStatusRecord | undefined>>({})
  const terminalFocusSequence = useRef(0)
  const terminalFocusOwner = useRef<{ holonId: string, tabId: string } | undefined>(undefined)
  const previousSelectedPane = useRef<{ holonId: string, tabId: string } | undefined>(undefined)
  const [terminalBindingsByHolon, setTerminalBindingsByHolon] = useState<Record<string, Record<string, string>>>({})
  const committedTerminalBindingsByHolon = useRef<Record<string, Record<string, string>>>({})
  const currentHolon = getHolon(holonId)
  const pullRequest = sessionPullRequest.data?.holonId === holonId ? sessionPullRequest.data.pullRequest : undefined
  const displayStatus = currentHolon ? holonDisplayStatus(currentHolon) : undefined
  const readOnly = Boolean(currentHolon?.read_only)
  const reviewHolon = currentHolon?.kind === 'pr_review'
  const archived = Boolean(currentHolon?.archived_at)
  const canInspectWorkspace = Boolean(currentHolon?.worktree_branch && !archived && !readOnly && !reviewHolon)
  const loadBranchInspection = useCallback(async (signal: AbortSignal) => ({
    holonId,
    inspection: canInspectWorkspace
      ? await api.sessionChanges(holonId, { base: 'main', target: 'worktree', summary: true, signal })
      : undefined,
  }), [canInspectWorkspace, holonId])
  const branchInspection = usePolling(loadBranchInspection)
  const inspection = branchInspection.data?.holonId === holonId ? branchInspection.data.inspection : undefined
  const inspectionLoaded = canInspectWorkspace && Boolean(inspection)
  const branchDiffersFromBase = inspectionLoaded && inspection?.head_commit !== inspection?.base_commit
  const pullRequestKnown = Boolean(pullRequest)
  const pullRequestContextLoaded = sessionPullRequest.data?.holonId === holonId && !sessionPullRequest.loading
  const loadPublicationReadiness = useCallback(async (signal: AbortSignal) => ({
    holonId,
    readiness: canInspectWorkspace ? await api.holonPublicationReadiness(holonId, signal) : undefined,
  }), [canInspectWorkspace, holonId])
  const publicationReadiness = usePolling(loadPublicationReadiness)
  const readiness = !publicationReadiness.error && publicationReadiness.data?.holonId === holonId ? publicationReadiness.data.readiness : undefined
  const rebaseAttemptState = currentHolon?.rebase_attempt?.state
  useEffect(() => {
    if (rebaseAttemptState === 'succeeded' || rebaseAttemptState === 'failed') void Promise.all([publicationReadiness.refresh(), branchInspection.refresh()])
  }, [rebaseAttemptState, publicationReadiness.refresh, branchInspection.refresh])
  const publishExpectedHead = readiness?.target_commit
  const hasPublishTarget = Boolean(currentHolon?.upstream_branch || pullRequestKnown)
  const showCreatePullRequest = pullRequestContextLoaded && !pullRequestKnown && !hasPublishTarget && branchDiffersFromBase && !inspection?.dirty
  const canCreatePullRequest = Boolean(currentHolon?.proposed_upstream_branch)
  const showPublishAction = Boolean(readiness?.publication_available && !inspection?.dirty)
  const rebaseActive = readiness?.attempt?.state === 'running' || readiness?.attempt?.state === 'waiting'
  const showRebaseAction = Boolean(readiness?.rebase_available || startingRebaseFork || (rebaseActive && !readiness?.attempt?.agent_id && readiness?.attempt?.state === 'running'))
  const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  const manualAvailable = Boolean(currentHolon?.worktree_path) && !archived && !readOnly
  const visibleAgentTabs = visibleAgentSessions(currentHolon)
  const visibleManualTabs = visibleManualTerminals(currentHolon)
  const openManualTabs = visibleManualTabs
  const visibleIDETabs = visibleHolonIDEs(currentHolon)
  const previousTerminalBindings = terminalBindingsByHolon[holonId] ?? {}
  const nextTerminalBindings: Record<string, string> = {}
  visibleAgentTabs.forEach((harness) => {
    const product = `agent:${harness.id}`
    nextTerminalBindings[product] = harness.terminal_id || previousTerminalBindings[product] || ''
  })
  visibleManualTabs.forEach((terminal) => { nextTerminalBindings[`manual:${terminal.id}`] = terminal.terminal_id ?? '' })
  const nextTerminalBindingEntries = Object.entries(nextTerminalBindings)
  const terminalBindingsChanged = nextTerminalBindingEntries.length !== Object.keys(previousTerminalBindings).length
    || nextTerminalBindingEntries.some(([product, terminalId]) => previousTerminalBindings[product] !== terminalId)
  if (terminalBindingsChanged) {
    setTerminalBindingsByHolon((current) => ({ ...current, [holonId]: nextTerminalBindings }))
  }
  const sessionTerminalBindings = terminalBindingsChanged ? nextTerminalBindings : previousTerminalBindings
  const ideUnavailableReason = readOnly
    ? 'This holon is read-only.'
    : reviewHolon
      ? 'Review holons do not support IDE tabs.'
      : archived
        ? 'Reopen this holon before starting an IDE.'
        : !currentHolon?.worktree_path
          ? 'Holon workspace is not ready.'
          : ''
  const ideCreateAvailable = ideUnavailableReason === ''
  const capabilities = capabilityState.data ?? []
  const retryHarnessType = lastAgentChoice?.holonId === holonId ? lastAgentChoice.harnessType : defaultHarnessType(capabilities)
  const resumeHarnessTypes = currentHolon ? requiredHarnessTypes(currentHolon) : []
  const resumeHarnessesAvailable = resumeHarnessTypes.every((type) => hasAvailableHarness(capabilities, type))
  const canResume = Boolean(currentHolon && isSessionResumable(currentHolon))
  const fallbackAgentTabId = visibleAgentTabs[0]?.id ?? `agent-${holonId}`
  const sessionTabs = orderedSessionTabs(visibleAgentTabs, visibleManualTabs, visibleIDETabs)
  const preparationPlaceholder = displayStatus === 'starting' && !sessionTabs.some((tab) =>
    tab.kind === 'ide' || (tab.kind === 'agent' ? tab.harness.terminal_id : tab.terminal.terminal_id))
  const selectedTab = selectedHolonTab(currentHolon) ?? fallbackAgentTabId
  useEffect(() => {
    if (currentHolon && currentHolon.last_selected_tab_id !== selectedTab) selectHolonTab(holonId, selectedTab)
  }, [currentHolon, holonId, selectedTab, selectHolonTab])
  const selectedAgentHarness = visibleAgentTabs.find((harness) => harness.id === selectedTab)
  const commitSource = selectedAgentHarness
  const activeCommitAgent = visibleAgentTabs.some((agent) =>
    !terminalStatuses.has(agent.status ?? 'queued') && agent.input_state !== 'task_complete'
      && agent.commit_prompt?.state === 'commit_discussion_started')
  const conversationForkUnavailableReason = (source: AgentSession) => {
    if (!source.agent_type || !harnessTypes.includes(source.agent_type)
      || !source.resume_target?.trim()
      || (source.agent_type === 'claude-code' && !source.rollout_path?.trim())) {
      return "This agent's conversation is not ready to fork yet."
    }
    if (!hasAvailableHarness(capabilities, source.agent_type)) return `${harnessDisplayLabel(source.agent_type)} is unavailable.`
    return ''
  }
  const agentTabForkUnavailableReason = (source: AgentSession) => {
    if (readOnly) return 'Read-only holons cannot be forked.'
    if (currentHolon?.archived_at) return 'Archived holons cannot be forked.'
    if (!currentHolon?.worktree_path) return 'Holon workspace is not ready.'
    return conversationForkUnavailableReason(source)
  }
  const actionAgentUnavailableReason = (source?: AgentSession) => source
    ? conversationForkUnavailableReason(source)
    : hasAvailableHarness(capabilities, defaultHarnessType(capabilities)) ? '' : 'The default agent is unavailable.'
  const rebaseAttempt = readiness?.attempt ?? currentHolon?.rebase_attempt
  const savedRebaseSourceId = rebaseAttempt && (rebaseAttempt.state === 'running' || rebaseAttempt.state === 'waiting')
    ? rebaseAttempt.source_agent_id
    : undefined
  // Retry availability follows the saved source, falling back to the default
  // agent when that source is closed or gone, just like the backend.
  const rebaseSource = savedRebaseSourceId
    ? visibleAgentTabs.find((agent) => agent.id === savedRebaseSourceId)
    : selectedAgentHarness
  const rebaseUnavailableReason = startingRebaseFork
    ? 'Rebasing…'
    : readiness?.reason || actionAgentUnavailableReason(rebaseSource)
  const commitUnavailableReason = startingCommitFork
    ? 'The commit agent is starting.'
    : activeCommitAgent
      ? 'A commit agent is already active.'
      : actionAgentUnavailableReason(commitSource)
  const showCommitAction = Boolean(canInspectWorkspace && currentHolon?.worktree_path && inspection?.dirty)
  const primarySessionAction = canResume
    ? 'resume'
    : showCommitAction
      ? 'commit'
      : showCreatePullRequest
        ? 'create-pull-request'
        : showRebaseAction
          ? 'rebase'
          : showPublishAction
          ? 'publish'
          : undefined
  const observabilityWarning = selectedAgentHarness?.observability_status === 'degraded'
    ? selectedAgentHarness.observability_message || `${harnessDisplayLabel(selectedAgentHarness.agent_type)} monitoring is unavailable.`
    : ''
  const activatedTabs = activatedTabsByHolon[holonId] ?? {}
  const rememberActivatedTabs = (...tabIds: string[]) => {
    setActivatedTabsByHolon((current) => {
      const nextSessionTabs = { ...(current[holonId] ?? {}) }
      let changed = false
      tabIds.forEach((tabId) => {
        if (nextSessionTabs[tabId]) return
        nextSessionTabs[tabId] = true
        changed = true
      })
      return changed ? { ...current, [holonId]: nextSessionTabs } : current
    })
  }
  // Restored selections and store-driven tab changes also activate the displayed pane.
  if (currentHolon && !activatedTabs[selectedTab]) {
    rememberActivatedTabs(selectedTab)
  }
  const requestTerminalFocus = useCallback((tabId: string) => {
    terminalFocusSequence.current += 1
    setTerminalFocusRequest({ holonId, tabId, sequence: terminalFocusSequence.current })
  }, [holonId])
  const { diffOpen, setDiffOpen, selectTab } = useHolonWorkspaceNavigation((id) => {
    rememberActivatedTabs(selectedTab, id)
    selectHolonTab(holonId, id)
    requestTerminalFocus(id)
  })
  const activeLayer: FocusLayer = diffOpen ? 'diff-review' : 'terminal'
  const consumeTerminalFocusRequest = useCallback((tabId: string, sequence: number) => {
    setTerminalFocusRequest((current) => current?.holonId === holonId && current.tabId === tabId && current.sequence === sequence ? undefined : current)
  }, [holonId])
  const noteTerminalFocus = useCallback((tabId: string) => {
    terminalFocusOwner.current = { holonId, tabId }
  }, [holonId])

  useEffect(() => {
    if (!location.state?.focusHolonTerminal || !currentHolon || focusedNavigation.current === location.key) return
    focusedNavigation.current = location.key
    requestTerminalFocus(selectedTab)
  }, [currentHolon, location.key, location.state, requestTerminalFocus, selectedTab])
  useLayoutEffect(() => {
    const previous = previousSelectedPane.current
    previousSelectedPane.current = { holonId, tabId: selectedTab }
    if (!previous || previous.holonId !== holonId || previous.tabId === selectedTab) return
    const focusOwner = terminalFocusOwner.current
    if (focusOwner?.holonId === holonId && focusOwner.tabId === previous.tabId) requestTerminalFocus(selectedTab)
  }, [requestTerminalFocus, selectedTab, holonId])

  useEffect(() => {
    const previous = committedTerminalBindingsByHolon.current[holonId] ?? {}
    Object.entries(previous).forEach(([product, terminalId]) => {
      const stillVisible = Object.hasOwn(sessionTerminalBindings, product)
      const nextTerminalId = sessionTerminalBindings[product]
      if (terminalId && (!stillVisible || (nextTerminalId && nextTerminalId !== terminalId))) {
        terminalWorkspace.evict(terminalId)
      }
    })
    committedTerminalBindingsByHolon.current[holonId] = sessionTerminalBindings
  }, [holonId, sessionTerminalBindings, terminalWorkspace])

  useEffect(() => {
    const releaseTerminalFocus = (event: Event) => {
      const target = event.target
      if (target instanceof Element && target.closest('[data-terminal-pane]')) return
      terminalFocusOwner.current = undefined
    }
    document.addEventListener('focusin', releaseTerminalFocus, true)
    document.addEventListener('pointerdown', releaseTerminalFocus, true)
    return () => {
      document.removeEventListener('focusin', releaseTerminalFocus, true)
      document.removeEventListener('pointerdown', releaseTerminalFocus, true)
    }
  }, [])

  useEffect(() => {
    if (canInspectWorkspace) return undefined
    let cancelled = false
    queueMicrotask(() => {
      if (cancelled) return
      setDiffOpen(false)
    })
    return () => { cancelled = true }
  }, [canInspectWorkspace, holonId])

  useEffect(() => {
    clearHarnessStatuses(harnessStatusByHolon.current, holonId)
    let cancelled = false
    queueMicrotask(() => {
      if (cancelled) return
      setSocketError('')
      setDiffOpen(false)
      setRenamingTab(null)
      setEndSessionDialogOpen(false)
      setForkingAgentId('')
      setResuming(false)
      setPublishing(false)
      setAddingManualTerminal(false)
      setRetryingIDE('')
    })
    return () => { cancelled = true }
  }, [holonId])

  useEffect(() => {
    if (currentHolon) replaceHarnessStatuses(harnessStatusByHolon.current, currentHolon)
  }, [currentHolon])





  const tabAfterClose = (closedId: string) => {
    const index = sessionTabs.findIndex((tab) => tab.id === closedId)
    const remaining = sessionTabs.filter((tab) => tab.id !== closedId)
    if (remaining.length === 0) return fallbackAgentTabId
    return remaining[Math.min(Math.max(index, 0), remaining.length - 1)]?.id ?? remaining[remaining.length - 1].id
  }

  useNavigationArrows('horizontal', (direction) => {
    if (sessionTabs.length === 0) return false
    const index = sessionTabs.findIndex((tab) => tab.id === selectedTab)
    const target = sessionTabs[(index + direction + sessionTabs.length) % sessionTabs.length]
    selectTab(target.id)
    return true
  })

  const relaunchManualTab = (id: string) => {
    setSocketError('')
    void api.relaunchManualTerminal(holonId, id).then((terminal) => {
      acceptSessionEvent((current) => mergeManualTerminal(current ?? currentHolon, terminal))
      requestTerminalFocus(id)
    }).catch((value) => setSocketError(value instanceof Error ? value.message : 'Manual terminal could not be relaunched.'))
  }

  const reorderTab = async (sourceKey: string, insertIndex: number) => {
    if (!currentHolon) return
    const sourceIndex = sessionTabs.findIndex((tab) => terminalTabKey(tab) === sourceKey)
    if (sourceIndex < 0 || insertIndex < 0 || insertIndex > sessionTabs.length) return
    const targetIndex = insertIndex > sourceIndex ? insertIndex - 1 : insertIndex
    if (targetIndex === sourceIndex) return
    const nextTabs = [...sessionTabs]
    const [moved] = nextTabs.splice(sourceIndex, 1)
    nextTabs.splice(targetIndex, 0, moved)
    const refs = nextTabs.map(tabToRef)
    const previous = currentHolon
    setSocketError('')
    acceptSessionEvent((current) => applyTabOrderToHolon(current ?? currentHolon, refs))
    selectHolonTab(holonId, moved.id)
    try {
      const reordered = await api.reorderSessionTabs(holonId, refs)
      acceptSessionEvent(reordered)
    } catch (value) {
      acceptSessionEvent(previous)
      setSocketError(value instanceof Error ? value.message : 'Tabs could not be reordered.')
    }
  }

  const addManualTab = async () => {
    if (openManualTabs.length >= maxManualTabs) {
      setSocketError(`Manual terminal limit reached (${maxManualTabs}).`)
      return
    }
    if (!manualAvailable) {
      setSocketError('Holon workspace is not ready for a manual terminal.')
      return
    }
    setSocketError('')
    setAddingManualTerminal(true)
    try {
      const terminal = await api.createManualTerminal(holonId)
      acceptSessionEvent((current) => mergeManualTerminal(current ?? currentHolon, terminal))
      rememberActivatedTabs(selectedTab, terminal.id)
      selectHolonTab(holonId, terminal.id)
      setAddingManualTerminal(false)
      requestTerminalFocus(terminal.id)
    } catch (value) {
      setAddingManualTerminal(false)
      setSocketError(value instanceof Error ? value.message : 'Manual terminal could not be created.')
    }
  }

  const closeManualTab = async (id: string) => {
    setSocketError('')
    const nextTabId = tabAfterClose(id)
    const focusFallback = selectedTab === id
    try {
      const terminalId = visibleManualTabs.find((candidate) => candidate.id === id)?.terminal_id
      await api.closeManualTerminal(holonId, id)
      if (terminalId) terminalWorkspace.evict(terminalId)
      acceptSessionEvent((current) => removeManualTerminal(current ?? currentHolon, id))
      selectHolonTab(holonId, nextTabId, id)
      setRenamingTab((current) => current?.id === id ? null : current)
      if (focusFallback) requestTerminalFocus(nextTabId)
    } catch (value) {
      setSocketError(value instanceof Error ? value.message : 'Manual terminal could not be closed.')
    }
  }

  const addIDETab = async () => {
    const existing = visibleIDETabs[0]
    if (existing) {
      selectTab(existing.id)
      return
    }
    if (!ideCreateAvailable) {
      setSocketError(ideUnavailableReason || 'VS Code is unavailable.')
      return
    }
    setSocketError('')
    try {
      const ide = await api.createHolonIDE(holonId)
      acceptSessionEvent((current) => mergeHolonIDE(current ?? currentHolon, ide))
      rememberActivatedTabs(selectedTab, ide.id)
      selectHolonTab(holonId, ide.id)
    } catch (value) {
      setSocketError(value instanceof Error ? value.message : 'IDE could not be created.')
    }
  }

  const closeIDETab = async (id: string) => {
    setSocketError('')
    try {
      const ide = await api.closeHolonIDE(holonId, id)
      acceptSessionEvent((current) => mergeHolonIDE(current ?? currentHolon, ide))
      selectHolonTab(holonId, tabAfterClose(id), id)
    } catch (value) {
      setSocketError(value instanceof Error ? value.message : 'IDE could not be closed.')
    }
  }

  const retryIDE = async (previous: HolonIDE) => {
    if (retryingIDE) return
    setRetryingIDE(previous.id)
    setSocketError('')
    try {
      const closed = await api.closeHolonIDE(holonId, previous.id)
      acceptSessionEvent((current) => mergeHolonIDE(current ?? currentHolon, closed))
      const ide = await api.createHolonIDE(holonId)
      acceptSessionEvent((current) => mergeHolonIDE(current ?? currentHolon, ide))
      rememberActivatedTabs(ide.id)
      selectHolonTab(holonId, ide.id)
    } catch (value) {
      setSocketError(value instanceof Error ? value.message : 'IDE retry failed.')
    } finally {
      setRetryingIDE('')
    }
  }

  const beginRenameTab = (tab: { id: string; title?: string }) => {
    setTerminalFocusRequest(undefined)
    setRenamingTab({ id: tab.id, value: tab.title || 'Agent' })
  }

  const commitRenameTab = async () => {
    if (!renamingTab) return
    const pending = renamingTab
    setRenamingTab(null)
    const title = pending.value.trim()
    if (!title) return
    try {
      const agent = visibleAgentTabs.find((tab) => tab.id === pending.id)
      if (agent) {
        const harness = await api.updateAgentSession(holonId, pending.id, title)
        acceptSessionEvent((current) => mergeAgentSession(current ?? currentHolon, harness))
      } else {
        const terminal = await api.updateManualTerminal(holonId, pending.id, title)
        acceptSessionEvent((current) => mergeManualTerminal(current ?? currentHolon, terminal))
      }
    } catch (value) {
      setSocketError(value instanceof Error ? value.message : 'Tab could not be renamed.')
    }
  }

  const cancelRenameTab = () => {
    setRenamingTab(null)
  }

  const endHolon = async () => {
    if (ending) return
    setEndSessionDialogOpen(false)
    setEnding(true)
    setSocketError('')
    try {
      const ended = await api.endHolon(holonId)
      replaceHarnessStatuses(harnessStatusByHolon.current, ended)
      acceptSessionEvent(ended)
      void refreshSessions()
    } catch (value) {
      setSocketError(value instanceof Error ? value.message : 'Cancellation failed.')
    } finally {
      setEnding(false)
    }
  }

  const requestCancel = () => {
    if (openManualTabs.length > 0) {
      setEndSessionDialogOpen(true)
      return
    }
    void endHolon()
  }

  const closeAgentTab = async (harnessId: string) => {
    setSocketError('')
    const nextTabId = tabAfterClose(harnessId)
    const focusFallback = selectedTab === harnessId
    try {
      const terminalId = visibleAgentTabs.find((candidate) => candidate.id === harnessId)?.terminal_id
      const harness = await api.closeAgentSession(holonId, harnessId)
      if (harness.terminal_id || terminalId) terminalWorkspace.evict(harness.terminal_id || terminalId || '')
      acceptSessionEvent((current) => mergeAgentSession(current ?? currentHolon, harness))
      selectHolonTab(holonId, nextTabId, harnessId)
      setRenamingTab((current) => current?.id === harnessId ? null : current)
      if (focusFallback) requestTerminalFocus(nextTabId)
    } catch (value) {
      setSocketError(value instanceof Error ? value.message : 'Agent could not be closed.')
    }
  }

  const addAgentTab = async (harnessType = retryHarnessType) => {
    if (agentRequests.current.has(holonId)) return
    if (!manualAvailable || !hasAvailableHarness(capabilities, harnessType)) {
      setSocketError('Holon workspace is not ready for a new agent.')
      return
    }
    setLastAgentChoice({ holonId, harnessType })
    agentRequests.current.add(holonId)
    setAddingAgentHolons((current) => ({ ...current, [holonId]: true }))
    setAgentCreationErrors((current) => ({ ...current, [holonId]: '' }))
    const storageKey = `holark:add-agent:${holonId}`
    const clearAttempt = () => {
      pendingAgentAttempts.set(holonId, null)
      try {
        sessionStorage.removeItem(storageKey)
      } catch { /* The in-memory tombstone still prevents reuse. */ }
    }
    try {
      // Keep the same identity across uncertain responses and navigation, with
      // best-effort storage persistence for recovery after a reload.
      let attempt = pendingAgentAttempts.get(holonId)
      if (!pendingAgentAttempts.has(holonId)) {
        try {
          const saved = sessionStorage.getItem(storageKey)
          if (saved) attempt = JSON.parse(saved) as AgentCreationAttempt
        } catch { /* Storage may be unavailable or contain invalid JSON. */ }
      }
      attempt ??= { id: crypto.randomUUID(), harness: harnessType }
      pendingAgentAttempts.set(holonId, attempt)
      try {
        sessionStorage.setItem(storageKey, JSON.stringify(attempt))
      } catch { /* The in-memory attempt still supports retry. */ }
      const harness = await api.createAgentSession(holonId, '', attempt.harness, attempt.id)
      clearAttempt()
      acceptSessionEvent((current) => mergeAgentSession(current ?? currentHolon, harness))
      if (activeHolon.current === holonId) {
        rememberActivatedTabs(selectedTab, harness.id)
        selectHolonTab(holonId, harness.id)
        requestTerminalFocus(harness.id)
      }
    } catch (value) {
      const error = value as ApiError
      // These API errors reject creation before reservation. Other failures,
      // including cancellation and timeouts, may have left an agent behind.
      const rejected = ['harness_unavailable', 'invalid_harness', 'invalid_request', 'holon_not_found'].includes(error.code ?? '')
      if (rejected) clearAttempt()
      setAgentCreationErrors((current) => ({ ...current, [holonId]: rejected
        ? error.message
        : 'Couldn’t confirm whether the agent was added. Retry to recover this request.' }))
    } finally {
      agentRequests.current.delete(holonId)
      setAddingAgentHolons((current) => ({ ...current, [holonId]: false }))
    }
  }

  const forkAgentTab = async (source: AgentSession) => {
    if (forkingAgentId || agentTabForkUnavailableReason(source)) return
    setForkingAgentId(source.id)
    setSocketError('')
    try {
      const agent = await api.forkAgentSession(holonId, source.id)
      acceptSessionEvent((current) => mergeAgentSession(current ?? currentHolon, agent))
      rememberActivatedTabs(selectedTab, agent.id)
      selectHolonTab(holonId, agent.id)
      requestTerminalFocus(agent.id)
    } catch (value) {
      setSocketError(value instanceof Error ? value.message : 'Agent could not be forked.')
    } finally {
      setForkingAgentId('')
    }
  }

  const createPullRequest = async (status: PullRequestCreationStatus) => {
    if (creationInFlight.current.has(holonId)) return
    creationInFlight.current.add(holonId)
    setCreationByHolon((current) => ({ ...current, [holonId]: { status } }))
    try {
      const created = await api.createPullRequest(holonId, defaultPullRequestTitle(currentHolon), '', status)
      if (activeHolonId.current === holonId) navigate(`/pulls/${created.id}`)
    } catch (value) {
      const error = value instanceof Error ? value.message : 'Pull request could not be created.'
      setCreationByHolon((current) => ({ ...current, [holonId]: { error } }))
    } finally {
      creationInFlight.current.delete(holonId)
      setCreationByHolon((current) => ({ ...current, [holonId]: { ...current[holonId], status: undefined } }))
    }
  }

  const startCommitFork = async () => {
    if (commitForkInFlight.current || !showCommitAction || commitUnavailableReason) return
    const sourceAgentId = commitSource?.id
    commitForkInFlight.current = true
    setStartingCommitFork(true)
    setSocketError('')
    try {
      const agent = await api.createCommitAgent(holonId, sourceAgentId)
      acceptSessionEvent((current) => mergeAgentSession(current ?? currentHolon, agent))
    } catch (value) {
      setSocketError(value instanceof Error ? value.message : 'Commit agent could not be created.')
    } finally {
      commitForkInFlight.current = false
      setStartingCommitFork(false)
    }
  }

  const startRebaseFork = async () => {
    if (rebaseForkInFlight.current || !readiness?.rebase_available || rebaseUnavailableReason) return
    const requestedHolon = holonId
    rebaseForkInFlight.current = true
    setStartingRebaseFork(true)
    setSocketError('')
    try {
      const agent = await api.createRebaseAgent(requestedHolon, commitSource?.id)
      if (agent) {
        acceptSessionEvent((current) => mergeAgentSession(current ?? currentHolon, agent))
        rememberActivatedTabs(selectedTab, agent.id)
        selectHolonTab(requestedHolon, agent.id)
        requestTerminalFocus(agent.id)
      }
    } catch (value) {
      setSocketError(value instanceof Error ? value.message : 'Rebase could not complete.')
    } finally {
      rebaseForkInFlight.current = false
      setStartingRebaseFork(false)
      await Promise.all([refreshSessions(), publicationReadiness.refresh(), branchInspection.refresh()])
    }
  }

  const publishHolon = async () => {
    if (publishingInFlight.current) return
    const upstreamBranch = readiness?.target_branch
    if (!upstreamBranch) {
      setSocketError('Push requires a Holon worktree branch.')
      return
    }
    publishingInFlight.current = true
    setPublishing(true)
    setSocketError('')
    try {
      await api.publishHolon(holonId, { remote: 'origin', upstream_branch: upstreamBranch, expected_remote_head: publishExpectedHead || '' })
      setDiffOpen(false)
    } catch (value) {
      setSocketError(value instanceof Error ? value.message : 'Push failed.')
    } finally {
      await Promise.allSettled([refreshSessions(), branchInspection.refresh(), publicationReadiness.refresh()])
      publishingInFlight.current = false
      setPublishing(false)
    }
  }

  const retryAgentRestoration = async (agentId: string) => {
    if (resuming) return
    setResuming(true)
    try {
      await api.resumeAgentSession(holonId, agentId)
      acceptSessionEvent(await api.holon(holonId))
    } catch (value) {
      setSocketError(value instanceof Error ? value.message : 'Restoration failed.')
    } finally { setResuming(false) }
  }

  const resume = async () => {
    if (!canResume || !resumeHarnessesAvailable || resuming) return
    setResuming(true)
    setSocketError('')
    try {
      const resumed = await api.reopenHolon(holonId)
      replaceHarnessStatuses(harnessStatusByHolon.current, resumed)
      acceptSessionEvent(resumed)
      const resumedTab = visibleAgentTabs.some((tab) => tab.id === selectedTab) ? selectedTab : fallbackAgentTabId
      selectHolonTab(holonId, resumedTab)
      void refreshSessions()
    } catch (value) {
      setSocketError(value instanceof Error ? value.message : 'Reopen failed.')
    } finally {
      setResuming(false)
    }
  }

  const tabStripItems: HolonTabStripItem[] = sessionTabs.map((tab) => ({
    id: tab.id,
    layoutKey: terminalTabKey(tab),
    title: tab.title,
    icon: tab.kind === 'agent' ? <HarnessTabIcon harnessType={tab.harness.agent_type ?? 'codex'} /> : tab.kind === 'ide' ? <Code2 /> : <SquareTerminal />,
    iconOnlyWhenCompact: tab.kind === 'terminal',
    iconLabel: tab.kind === 'agent' ? harnessIconLabel(tab.harness) : tab.kind === 'ide' ? 'VS Code' : 'Terminal',
    tone: tab.kind === 'agent' ? agentIconTone(tab.harness, currentHolon) : tab.kind === 'ide' ? ideIconTone(tab.ide) : 'neutral',
    renaming: renamingTab?.id === tab.id,
    action: tab.kind === 'agent' ? {
      label: `Fork ${tab.title}`,
      icon: <ForkTabIcon />,
      disabled: Boolean(forkingAgentId || agentTabForkUnavailableReason(tab.harness)),
      title: forkingAgentId === tab.id
        ? 'Forking agent…'
        : forkingAgentId
          ? 'Another agent is being forked.'
          : agentTabForkUnavailableReason(tab.harness) || `Fork ${tab.title}`,
    } : undefined,
    label: renamingTab?.id === tab.id ? (
      <input
        className={styles.tabInput}
        value={renamingTab.value}
        aria-label={`Rename ${tab.title}`}
        autoFocus
        onBlur={() => void commitRenameTab()}
        onClick={(event) => event.stopPropagation()}
        onDoubleClick={(event) => event.stopPropagation()}
        onChange={(event) => setRenamingTab({ id: tab.id, value: event.target.value })}
        onFocus={(event) => event.currentTarget.select()}
        onKeyDown={(event) => {
          event.stopPropagation()
          if (event.key === 'Enter') void commitRenameTab()
          if (event.key === 'Escape') cancelRenameTab()
        }}
      />
    ) : undefined,
  }))
  const sessionTabByLayoutKey = new Map(sessionTabs.map((tab) => [terminalTabKey(tab), tab]))

  const hasNotice = Boolean(socketError || creationError || agentCreationError || selectionErrors[holonId])
  const sessionBar = <HolonHeader
        notice={hasNotice ? <>
          {agentCreationError && <HolonNotice onDismiss={() => setAgentCreationErrors((current) => ({ ...current, [holonId]: '' }))} onRetry={addingAgent ? undefined : () => void addAgentTab()}>
            <p>{agentCreationError}</p>
          </HolonNotice>}
          {(socketError || creationError) && <HolonNotice onDismiss={() => { setSocketError(''); setCreationByHolon((current) => ({ ...current, [holonId]: { ...current[holonId], error: undefined } })) }}>
            {socketError && <p>{socketError}</p>}
            {creationError && <p>{creationError}</p>}
          </HolonNotice>}
          {selectionErrors[holonId] && <HolonNotice
            dismissLabel="Dismiss tab selection error"
            onDismiss={() => dismissSelectionError(holonId)}
            onRetry={() => selectHolonTab(holonId, selectedTab)}
          >
            <p>Couldn’t save your selected tab. {selectionErrors[holonId].message}</p>
          </HolonNotice>}
        </> : undefined}
        tabs={(sessionTabs.length > 0 && !preparationPlaceholder) || currentHolon?.worktree_path ? (
          <HolonTabStrip
            key={holonId}
            tabs={preparationPlaceholder ? [] : tabStripItems}
            selectedTabId={diffOpen ? '' : selectedTab}
            addingAgent={addingAgent}
            addOptions={(
              <HolonTabOptions
                onAddAgent={(type) => void addAgentTab(type)}
                onAddTerminal={() => void addManualTab()}
                onAddIDE={() => void addIDETab()}
                agentDisabled={(type) => !manualAvailable || !hasAvailableHarness(capabilities, type) || addingAgent}
                terminalDisabled={openManualTabs.length >= maxManualTabs || !manualAvailable || addingManualTerminal}
                ideDisabled={visibleIDETabs.length === 0 && !ideCreateAvailable}
                ideUnavailableReason={visibleIDETabs.length === 0 ? ideUnavailableReason : undefined}
                agentNotice={capabilityState.error && <span role="alert">Couldn’t load available agents. <button type="button" disabled={capabilityState.loading} onClick={() => void capabilityState.load().catch(() => {})}>Retry</button></span>}
              />
            )}
            onSelect={(item) => selectTab(item.id)}
            onAction={(item) => {
              const tab = sessionTabByLayoutKey.get(item.layoutKey)
              if (tab?.kind === 'agent') void forkAgentTab(tab.harness)
            }}
            onClose={(item) => {
              const tab = sessionTabByLayoutKey.get(item.layoutKey)
              if (!tab) return
              if (tab.kind === 'agent') void closeAgentTab(tab.id)
              else if (tab.kind === 'ide') void closeIDETab(tab.id)
              else void closeManualTab(tab.id)
            }}
            onDoubleClick={(item) => {
              const tab = sessionTabByLayoutKey.get(item.layoutKey)
              if (tab && tab.kind !== 'ide') beginRenameTab(tab)
            }}
            onReorder={reorderTab}
          />
        ) : null}
        prompt={currentHolon?.prompt}
        actions={<>
          {hasPublishTarget && publicationReadiness.error && <Button variant="ghost" title="Couldn’t check whether this Holon is ready to push." onClick={() => void publicationReadiness.refresh()}>Push check failed · Retry</Button>}
          {primarySessionAction && <>
          {primarySessionAction === 'resume' && <Button variant="primary" icon={<RefreshCw />} disabled={!resumeHarnessesAvailable || resuming} onClick={() => void resume()}>{resuming ? 'Reopening...' : 'Reopen'}</Button>}
          {primarySessionAction === 'commit' && <HolonCommitAction agentSelected={Boolean(selectedAgentHarness)} changesOpen={diffOpen} reason={commitUnavailableReason} starting={startingCommitFork} onClick={() => void startCommitFork()} />}
          {primarySessionAction === 'create-pull-request' && <CreatePullRequestMenu sessionHeaderMenu branch={currentHolon?.worktree_branch} baseBranch={currentHolon?.base_branch || project.default_branch} onCompare={() => setDiffOpen(true)} disabled={!canCreatePullRequest} pendingStatus={creatingStatus} onSelect={(status) => { void createPullRequest(status) }} />}
          {primarySessionAction === 'rebase' && <span title={rebaseUnavailableReason}><Button variant="ai" icon={<RefreshCw />} disabled={Boolean(rebaseUnavailableReason)} onClick={() => void startRebaseFork()}>{startingRebaseFork ? 'Rebasing…' : 'Rebase'}</Button></span>}
          {primarySessionAction === 'publish' && <Button variant="primary" icon={<Send />} disabled={publishing} onClick={() => void publishHolon()}>{publishing ? 'Pushing...' : 'Push'}</Button>}
        </>}
        </>}
        overflowActions={
          <>
            {pullRequest && <Link className={styles.actionLink} to={`/pulls/${pullRequest.id}`}><GitPullRequest /> View pull request</Link>}
            {showCommitAction && primarySessionAction !== 'commit' && <HolonCommitAction agentSelected={Boolean(selectedAgentHarness)} changesOpen={diffOpen} variant="ghost" reason={commitUnavailableReason} starting={startingCommitFork} onClick={() => void startCommitFork()} />}
            {showCreatePullRequest && primarySessionAction !== 'create-pull-request' && pullRequestCreationOptions.map((option) => (
              <Button key={option.status} variant="ghost" icon={<GitPullRequest />} disabled={!canCreatePullRequest || Boolean(creatingStatus)} onClick={() => void createPullRequest(option.status)}>
                {creatingStatus === option.status ? option.pendingLabel : option.label}
              </Button>
            ))}
            {showRebaseAction && primarySessionAction !== 'rebase' && <span title={rebaseUnavailableReason}><Button variant="ghost" icon={<RefreshCw />} disabled={Boolean(rebaseUnavailableReason)} onClick={() => void startRebaseFork()}>{startingRebaseFork ? 'Rebasing…' : 'Rebase'}</Button></span>}
            {rebaseActive && readiness?.attempt?.agent_id && <Button variant="ghost" icon={<RefreshCw />} onClick={() => selectTab(readiness.attempt!.agent_id)}>View Rebase</Button>}
            {hasPublishTarget && readiness?.reason && !showRebaseAction && !rebaseActive && <span title={readiness.reason}><Button variant="ghost" disabled>Push unavailable</Button></span>}
            {showPublishAction && primarySessionAction !== 'publish' && <Button variant="ghost" icon={<Send />} disabled={publishing} onClick={() => void publishHolon()}>{publishing ? 'Pushing...' : 'Push'}</Button>}
            {!archived && <Button variant="ghost" className={styles.dangerAction} icon={<Ban />} disabled={!currentHolon || ending} onClick={requestCancel}>
              {ending ? 'Ending...' : 'End holon'}
            </Button>}
          </>
        }
      />

  return (
    <HolonWorkspace header={sessionBar} reviewing={diffOpen} changes={
        <HolonDiffPanel
          key={holonId}
          holonId={holonId}
          onHolonChange={(holon) => {
            updateHolon(holon.id, holon)
            void Promise.all([branchInspection.refresh(), publicationReadiness.refresh()])
          }}
          enabled={canInspectWorkspace}
          readOnly={readOnly}
          open={diffOpen}
          onOpenChange={(open) => {
            setDiffOpen(open)
            if (!open) requestTerminalFocus(selectedTab)
          }}
          summary={inspection ? workspaceChangeStats(inspection) : undefined}
        />
      }>
      {displayStatus === 'finalizing' && (currentHolon?.reason || readiness?.reason) && <div role="alert">{currentHolon?.reason || readiness?.reason}</div>}
      {observabilityWarning && selectedAgentHarness && (
        <div className={styles.observabilityWarning} role="alert">
          <strong>{harnessDisplayLabel(selectedAgentHarness.agent_type)} monitoring is degraded.</strong>
          <span>{observabilityWarning} Terminal input remains available.</span>
        </div>
      )}
      <div className={styles.terminalArea}>
        <div className={styles.panes}>
        {(preparationPlaceholder || sessionTabs.length === 0) && currentHolon && <div className={styles.workspaceStatus} data-terminal-pane data-active="true" role="status">
          {displayStatus === 'starting' && <span className={styles.workspaceStatusIcon} aria-hidden="true"><SquareTerminal /></span>}
          <p className={styles.workspaceStatusTitle}>{displayStatus === 'starting' ? 'Preparing your workspace…' : currentHolon.reason || holonDisplayStatusLabel(currentHolon)}</p>
          {displayStatus === 'starting' && <p className={styles.workspaceStatusDescription}>Your terminal will appear here when it’s ready.</p>}
        </div>}
        {sessionTabs.map((tab) => {
          if (tab.kind === 'agent') {
            if (archived && displayStatus !== 'finalizing') return selectedTab === tab.id ? (
              <div key={`archived-${tab.id}`} data-terminal-pane data-active="true" role="status">
                <p>This workspace is archived. Reopen it to restore the saved conversation.</p>
              </div>
            ) : null
            const terminalId = tab.harness.terminal_id || sessionTerminalBindings[`agent:${tab.id}`]
            if ((tab.harness.status === 'restoring' && !terminalId) || tab.harness.status === 'recovery_failed') return selectedTab === tab.id ? (
              <div key={`recovering-${tab.id}`} data-terminal-pane data-active="true" role={tab.harness.status === 'recovery_failed' ? 'alert' : 'status'}>
                <p>{tab.harness.status === 'restoring' ? 'Restoring the saved conversation...' : tab.harness.reason || 'Could not restore the saved conversation.'}</p>
                {tab.harness.status === 'recovery_failed' && <button type="button" disabled={resuming} onClick={() => void retryAgentRestoration(tab.id)}>Retry restoration</button>}
              </div>
            ) : null
            if (!terminalId) {
              const reason = tab.harness.reason || currentHolon?.reason
              return selectedTab === tab.id && reason ? (
                <div key={`failed-${tab.id}`} data-terminal-pane data-active="true" role="alert">
                  <p>{reason}</p>
                </div>
              ) : null
            }
            const paneAgentURL = `${protocol}//${window.location.host}/api/v1/holons/${encodeURIComponent(holonId)}/terminals/${encodeURIComponent(terminalId)}/attach`
            const agentActive = !terminalStatuses.has((tab.harness.status ?? currentHolon?.status ?? 'queued') as HolonStatus)
            return (
              <TerminalSurface
                key={terminalId}
                terminalId={terminalId}
                label={`${tab.title} terminal`}
                url={paneAgentURL}
                active={selectedTab === tab.id}
                activated={selectedTab === tab.id || Boolean(activatedTabs[tab.id])}
                socketFactory={socketFactory}
                availabilityProbe={availabilityProbe}
                inputEnabled={agentActive && !readOnly && activeLayer === 'terminal'}
                resizeEnabled={agentActive}
                focusSuppressed={renamingTab !== null || activeLayer !== 'terminal'}
                focusRequest={terminalFocusRequest?.holonId === holonId && terminalFocusRequest.tabId === tab.id ? terminalFocusRequest.sequence : 0}
                onFocusRequestHandled={(sequence) => consumeTerminalFocusRequest(tab.id, sequence)}
                onTerminalFocus={() => noteTerminalFocus(tab.id)}
              />
            )
          }
          if (tab.kind === 'ide') {
            return (
              <HolonIDEPane
                key={`ide-${holonId}-${tab.id}`}
                holonId={holonId}
                ide={tab.ide}
                active={selectedTab === tab.id}
                activated={selectedTab === tab.id || Boolean(activatedTabs[tab.id])}
                retrying={retryingIDE === tab.id}
                retrySuspended={!archived}
                onRetry={(ide) => void retryIDE(ide)}
              />
            )
          }
          const terminalId = tab.terminal.terminal_id
          if (!terminalId) return selectedTab === tab.id ? (
            <div key={`stale-${tab.id}`} data-terminal-pane data-active="true" role="status">
              <p>Terminal process ended when Holark stopped.</p>
              <button type="button" onClick={() => relaunchManualTab(tab.id)}>Relaunch terminal</button>
            </div>
          ) : null
          const paneManualURL = `${protocol}//${window.location.host}/api/v1/holons/${encodeURIComponent(holonId)}/terminals/${encodeURIComponent(terminalId)}/attach`
          return (
            <TerminalSurface
              key={terminalId}
              terminalId={terminalId}
              label={`${tab.title} terminal`}
              url={paneManualURL}
              active={!addingManualTerminal && selectedTab === tab.id}
              activated={selectedTab === tab.id || Boolean(activatedTabs[tab.id])}
              socketFactory={socketFactory}
              availabilityProbe={availabilityProbe}
              inputEnabled={!readOnly && activeLayer === 'terminal'}
              resizeEnabled
              focusSuppressed={renamingTab !== null || activeLayer !== 'terminal'}
              focusRequest={terminalFocusRequest?.holonId === holonId && terminalFocusRequest.tabId === tab.id ? terminalFocusRequest.sequence : 0}
              onFocusRequestHandled={(sequence) => consumeTerminalFocusRequest(tab.id, sequence)}
              onTerminalFocus={() => noteTerminalFocus(tab.id)}
            />
          )
        })}
        </div>
      </div>
      <EndSessionDialog
        open={endSessionDialogOpen}
        terminalCount={openManualTabs.length}
        ending={ending}
        onClose={() => setEndSessionDialogOpen(false)}
        onConfirm={() => void endHolon()}
      />
    </HolonWorkspace>
  )
}

function EndSessionDialog({ open, terminalCount, ending, onClose, onConfirm }: {
  open: boolean
  terminalCount: number
  ending: boolean
  onClose: () => void
  onConfirm: () => void
}) {
  const terminalLabel = terminalCount === 1 ? 'manual terminal' : 'manual terminals'
  return (
    <Dialog
      open={open}
      title="End holon"
      onClose={onClose}
      footer={<><Button disabled={ending} onClick={onClose}>Cancel</Button><Button variant="primary" icon={<Ban />} disabled={ending} onClick={onConfirm}>{ending ? 'Ending...' : 'End holon'}</Button></>}
    >
      <p>Ending this holon will close {terminalCount} {terminalLabel}.</p>
    </Dialog>
  )
}

function workspaceChangeStats(inspection: WorkspaceInspection | undefined) {
  return (inspection?.files ?? []).reduce((total, file) => ({
    files: total.files + 1,
    additions: total.additions + file.additions,
    deletions: total.deletions + file.deletions,
  }), { files: 0, additions: 0, deletions: 0 })
}

function terminalTabKey(tab: HolonTerminalTab) {
  return `${tab.kind}:${tab.id}`
}

function tabToRef(tab: HolonTerminalTab): HolonTabRef {
  return { type: tab.kind, id: tab.id }
}

export function applyTabOrderToHolon(holon: Holon | undefined, tabs: HolonTabRef[]): Holon | undefined {
  if (!holon) return holon
  const orderByKey = new Map(tabs.map((tab, index) => [`${tab.type}:${tab.id}`, index + 1]))
  const harnesses = (holon.agent_sessions?.length ? holon.agent_sessions : holon.agent_session ? [holon.agent_session] : []).map((harness) => {
    const order = orderByKey.get(`agent:${harness.id}`)
    return order ? { ...harness, tab_order: order } : harness
  }).sort(compareHarnessTabs)
  const manualTerminals = (holon.manual_terminals ?? []).map((terminal) => {
    const order = orderByKey.get(`terminal:${terminal.id}`)
    return order ? { ...terminal, tab_order: order } : terminal
  }).sort(compareManualTabs)
  const ides = (holon.ides ?? []).map((ide) => {
    const order = orderByKey.get(`ide:${ide.id}`)
    return order ? { ...ide, tab_order: order } : ide
  }).sort(compareIDETabs)
  return { ...holon, agent_session: harnesses.find((harness) => !harness.closed_at) ?? holon.agent_session, agent_sessions: harnesses, manual_terminals: manualTerminals, ides }
}


function harnessIconLabel(harness: AgentSession) {
  return harnessDisplayLabel(harness.agent_type)
}

function agentIconTone(harness: AgentSession, holon?: Holon): SessionTabTone {
  const status = (harness.status ?? holon?.status ?? 'queued') as HolonStatus
  if (status === 'recovery_failed') return 'attention'
  if (status === 'restoring') return 'idle'
  const activity = agentDisplayActivity({ ...harness, status: harness.status ?? holon?.status })
  if (activity === 'needs_input') return 'attention'
  if (activity === 'working') return 'running'
  if (activity === 'failed') return 'failed'
  if (activity === 'completed') return 'completed'
  return 'neutral'
}


function newerHarness(previous: AgentSession | undefined, candidate: AgentSession): AgentSession
function newerHarness(previous: AgentSession | undefined, candidate: AgentSession | undefined): AgentSession | undefined
function newerHarness(previous: AgentSession | undefined, candidate: AgentSession | undefined) {
  if (!candidate) return previous
  if (!previous || timestamp(candidate.updated_at) >= timestamp(previous.updated_at)) return candidate
  return previous
}

function timestamp(value?: string) {
  const parsed = Date.parse(value ?? '')
  return Number.isFinite(parsed) ? parsed : 0
}

function clearHarnessStatuses(statuses: Record<string, HarnessStatusRecord | undefined>, holonId: string) {
  const prefix = `${holonId}:`
  for (const key of Object.keys(statuses)) {
    if (key.startsWith(prefix)) delete statuses[key]
  }
}

function replaceHarnessStatuses(statuses: Record<string, HarnessStatusRecord | undefined>, holon: Holon) {
  clearHarnessStatuses(statuses, holon.id)
  const harnesses = holon.agent_sessions?.length
    ? holon.agent_sessions
    : holon.agent_session ? [holon.agent_session] : []
  for (const harness of harnesses) {
    if (!harness.closed_at) recordHarnessStatus(statuses, holon.id, harness.id, (harness.status ?? holon.status) as HolonStatus, harness.updated_at)
  }
}

function recordHarnessStatus(statuses: Record<string, HarnessStatusRecord | undefined>, holonId: string, harnessId: string, status: HolonStatus, updatedAt?: string) {
  const key = `${holonId}:${harnessId}`
  const current = statuses[key]
  if (current?.updatedAt && updatedAt && timestamp(updatedAt) < timestamp(current.updatedAt)) return
  statuses[key] = { status, updatedAt }
}




function mergeAgentSession(holon: Holon | undefined, harness: AgentSession) {
  if (!holon || (harness.holon_id && holon.id !== harness.holon_id)) return holon
  const currentHarnesses = holon.agent_sessions?.length ? holon.agent_sessions : holon.agent_session ? [holon.agent_session] : []
  const mergedHarness = newerHarness(currentHarnesses.find((candidate) => candidate.id === harness.id), harness) ?? harness
  const nextHarnesses = currentHarnesses.filter((candidate) => candidate.id !== harness.id)
  if (!mergedHarness.closed_at) nextHarnesses.push(mergedHarness)
  nextHarnesses.sort(compareHarnessTabs)
  const primary = nextHarnesses[0] ?? (holon.agent_session?.id === harness.id ? undefined : holon.agent_session)
  return { ...holon, agent_session: primary, agent_sessions: nextHarnesses }
}


function mergeManualTerminal(holon: Holon | undefined, terminal: ManualTerminal) {
  if (!holon || holon.id !== terminal.holon_id) return holon
  const nextTerminals = (holon.manual_terminals ?? []).filter((candidate) => candidate.id !== terminal.id)
  nextTerminals.push(terminal)
  nextTerminals.sort(compareManualTabs)
  return { ...holon, manual_terminals: nextTerminals }
}

function removeManualTerminal(holon: Holon | undefined, terminalID: string) {
  if (!holon) return holon
  return { ...holon, manual_terminals: (holon.manual_terminals ?? []).filter((candidate) => candidate.id !== terminalID) }
}

function mergeHolonIDE(holon: Holon | undefined, ide: HolonIDE) {
  if (!holon || holon.id !== ide.holon_id) return holon
  const nextIDEs = (holon.ides ?? []).filter((candidate) => candidate.id !== ide.id)
  if (ide.desired_open) nextIDEs.push(newerHolonIDE((holon.ides ?? []).find((candidate) => candidate.id === ide.id), ide))
  nextIDEs.sort(compareIDETabs)
  return { ...holon, ides: nextIDEs }
}
