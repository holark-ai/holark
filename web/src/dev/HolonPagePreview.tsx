import { Ban, Code2, GitPullRequest, RefreshCw, Send, SquareTerminal } from 'lucide-react'
import { useCallback, useLayoutEffect, useRef, useState } from 'react'
import { useLocation, useParams } from 'react-router-dom'
import { previewHolons } from './devPreviewData'
import { CommitMessageControls, HolonPublishControls } from '../features/terminal/HolonPublishControls'
import { usePreviewPreparation } from './usePreviewPreparation'
import { Button } from '../components/Button'
import { api } from '../data/api'
import { harnessDisplayLabel } from '../data/harness'
import type { HarnessType } from '../data/types'
import { usePolling } from '../data/usePolling'
import { useNavigationArrows } from '../features/navigation/useNavigationArrows'
import { HolonWorkspace } from '../features/terminal/HolonWorkspace'
import { useHolonWorkspaceNavigation } from '../features/terminal/useHolonWorkspaceNavigation'
import { HolonDiffPanel } from '../features/terminal/HolonDiffPanel'
import { HolonNotice } from '../features/terminal/HolonNotice'
import { HolonHeader } from '../features/terminal/HolonHeader'
import { HolonCommitAction } from '../features/terminal/HolonCommitAction'
import { ForkTabIcon, HarnessTabIcon } from '../features/terminal/HarnessTabIcon'
import { HolonTabStrip, type HolonTabStripItem } from '../features/terminal/HolonTabStrip'
import { HolonTabOptions } from '../features/terminal/HolonTabOptions'
import terminalStyles from '../features/terminal/HolonTerminal.module.css'
import styles from './HolonPagePreview.module.css'

type PreviewTabKind = 'agent' | 'terminal' | 'ide'

type PreviewTab = {
  id: string
  kind: PreviewTabKind
  title: string
  harnessType?: HarnessType
}

const initialPreviewTabs: PreviewTab[] = [
  { id: 'agent-1', kind: 'agent', title: 'Agent CLI' },
  { id: 'agent-2', kind: 'agent', title: 'Review CLI', harnessType: 'claude-code' },
  { id: 'terminal-3', kind: 'terminal', title: 'Shell' },
  { id: 'terminal-4', kind: 'terminal', title: 'Dev server' },
]

type PreviewAction = 'commit' | 'pull-request' | 'publish' | 'rebase' | 'resume'
const previewActionByHolon: Record<string, PreviewAction> = {
  'holon-A': 'commit', 'holon-selected': 'pull-request', 'holon-F': 'publish', 'holon-J': 'rebase', 'holon-E': 'resume',
}

export function HolonPagePreview() {
  const { holonId = 'holon-selected' } = useParams()
  return <HolonPreview key={holonId} holonId={holonId} />
}

function HolonPreview({ holonId }: { holonId: string }) {
  const location = useLocation()
  const previewSurface = useRef<HTMLDivElement>(null)
  const loadHolon = useCallback(() => api.holon(holonId), [holonId])
  const { data: loadedHolon } = usePolling(loadHolon, 10000)
  const holon = loadedHolon ?? previewHolons.find((candidate) => candidate.id === holonId) ?? previewHolons[0]
  const [action, setAction] = useState<PreviewAction>(previewActionByHolon[holonId] ?? 'commit')
  const [actionMessage, setActionMessage] = useState('')
  const performAction = (message: string, next: PreviewAction) => { setActionMessage(message + ' · preview only'); setAction(next) }

  const [notice, setNotice] = useState(() => new URLSearchParams(window.location.search).get('notice') ?? 'none')
  const [previewTabs, setPreviewTabs] = useState(initialPreviewTabs)
  const [selectedPreviewTabId, setSelectedPreviewTabId] = useState(initialPreviewTabs[0].id)
  const { diffOpen, setDiffOpen, selectTab: selectPreviewTab } = useHolonWorkspaceNavigation((id) => {
    setSelectedPreviewTabId(id)
    previewSurface.current?.focus({ preventScroll: true })
  })
  const nextPreviewTabNumber = useRef(initialPreviewTabs.length + 1)
  const ensureGenerationTab = () => setPreviewTabs((tabs) => tabs.some((tab) => tab.id === 'commit-message') ? tabs : [...tabs, { id: 'commit-message', kind: 'agent', title: 'Commit message' }])
  const viewGeneration = () => { ensureGenerationTab(); selectPreviewTab('commit-message') }
  const preparation = usePreviewPreparation(ensureGenerationTab, viewGeneration)
  const startCommit = () => { setDiffOpen(true); preparation.onGenerate() }

  const selectedPreviewTabIdForRender = previewTabs.some((tab) => tab.id === selectedPreviewTabId)
    ? selectedPreviewTabId
    : (previewTabs[0]?.id ?? '')
  useLayoutEffect(() => {
    if (!diffOpen) previewSurface.current?.focus({ preventScroll: true })
  }, [location.key, selectedPreviewTabIdForRender, diffOpen])
  useNavigationArrows('horizontal', (direction) => {
    if (previewTabs.length === 0) return false
    const index = previewTabs.findIndex((tab) => tab.id === selectedPreviewTabIdForRender)
    const target = previewTabs[(index + direction + previewTabs.length) % previewTabs.length]
    selectPreviewTab(target.id)
    return true
  })
  const previewTabItems: HolonTabStripItem[] = previewTabs.map((tab) => ({
    id: tab.id,
    layoutKey: tab.id,
    title: tab.title,
    icon: tab.kind === 'agent' ? <HarnessTabIcon harnessType={tab.harnessType ?? 'codex'} /> : tab.kind === 'ide' ? <Code2 /> : <SquareTerminal />,
    iconOnlyWhenCompact: tab.kind === 'terminal' || tab.id === 'commit-message',
    action: tab.kind === 'agent' && tab.id !== 'commit-message' ? { label: `Fork ${tab.title}`, icon: <ForkTabIcon /> } : undefined,
    iconLabel: tab.kind === 'agent' ? harnessDisplayLabel(tab.harnessType ?? 'codex') : previewTabKindLabel(tab.kind),
    tone: tab.kind === 'agent' ? 'running' : 'neutral',
  }))

  const addPreviewTab = (kind: PreviewTabKind, harnessType?: HarnessType) => {
    const number = nextPreviewTabNumber.current
    nextPreviewTabNumber.current += 1
    const tab = { id: `${kind}-${number}`, kind, harnessType, title: harnessType ? `${harnessDisplayLabel(harnessType)} ${number}` : previewTabTitle(kind, number) }
    setPreviewTabs((current) => [...current, tab])
    selectPreviewTab(tab.id)
  }

  const closePreviewTab = (id: string) => {
    const closingIndex = previewTabs.findIndex((tab) => tab.id === id)
    if (closingIndex < 0) return
    const next = previewTabs.filter((tab) => tab.id !== id)
    setPreviewTabs(next)
    if (selectedPreviewTabIdForRender === id) {
      setSelectedPreviewTabId(next[Math.min(closingIndex, next.length - 1)]?.id ?? '')
    }
  }

  const reorderPreviewTabs = (sourceKey: string, insertIndex: number) => {
    const sourceIndex = previewTabs.findIndex((tab) => tab.id === sourceKey)
    if (sourceIndex < 0 || insertIndex < 0 || insertIndex > previewTabs.length) return
    const targetIndex = insertIndex > sourceIndex ? insertIndex - 1 : insertIndex
    if (targetIndex === sourceIndex) return
    const next = [...previewTabs]
    const [moved] = next.splice(sourceIndex, 1)
    next.splice(targetIndex, 0, moved)
    setPreviewTabs(next)
    setSelectedPreviewTabId(moved.id)
  }

  const setPreviewTabCount = (count: number) => {
    const next = previewTabs.slice(0, count)
    while (next.length < count) {
      const number = nextPreviewTabNumber.current
      nextPreviewTabNumber.current += 1
      next.push({ id: `terminal-${number}`, kind: 'terminal', title: previewTabTitle('terminal', number) })
    }
    setPreviewTabs(next)
    if (!next.some((tab) => tab.id === selectedPreviewTabIdForRender)) setSelectedPreviewTabId(next[0]?.id ?? '')
  }

  const previewTabStrip = (
    <HolonTabStrip
      tabs={previewTabItems}
      selectedTabId={diffOpen ? '' : selectedPreviewTabIdForRender}
      tabListLabel="Preview terminal tabs"
      viewportTestId="preview-tab-viewport"
      addOptions={(
        <HolonTabOptions
          onAddAgent={(type) => addPreviewTab('agent', type)}
          onAddTerminal={() => addPreviewTab('terminal')}
          onAddIDE={() => addPreviewTab('ide')}
        />
      )}
      onSelect={(tab) => selectPreviewTab(tab.id)}
      onAction={(tab) => {
        const source = previewTabs.find((candidate) => candidate.id === tab.id)
        if (!source) return
        const number = nextPreviewTabNumber.current++
        const fork = { ...source, id: `agent-${number}`, title: `Agent fork ${number}` }
        setPreviewTabs((tabs) => tabs.flatMap((candidate) => candidate.id === source.id ? [candidate, fork] : [candidate]))
        selectPreviewTab(fork.id)
      }}
      onClose={(tab) => closePreviewTab(tab.id)}
      onReorder={reorderPreviewTabs}
    />
  )
  const sessionHeader = (
    <HolonHeader
      notice={notice !== 'none' ? <HolonNotice
        onDismiss={() => setNotice('none')}
        onRetry={notice === 'selection' ? () => setNotice('none') : undefined}
      >
        <p>{notice === 'selection' ? 'Couldn’t save your selected tab. The request timed out.' : 'Couldn’t confirm whether the agent was added.'}</p>
      </HolonNotice> : undefined}
      prompt={holon.prompt}
      tabs={previewTabStrip}
      actions={<>
        {action === 'commit' && preparation.dirty && <HolonCommitAction
          agentSelected={previewTabs.find((tab) => tab.id === selectedPreviewTabIdForRender)?.kind === 'agent'}
          changesOpen={diffOpen}
          message={preparation.message}
          reason={['generating', 'committing', 'publishing'].includes(preparation.state) ? 'Commit preparation is in progress.' : undefined}
          label="Generate commit message"
          title="Fork this agent tab to generate a commit message in Changes."
          onClick={startCommit}
        />}
        {(action === 'pull-request' || action === 'commit' && !preparation.dirty) && !diffOpen && <Button variant="primary" icon={<GitPullRequest />} onClick={() => setDiffOpen(true)}>Open PR</Button>}
        {action === 'publish' && <Button variant="primary" icon={<Send />} onClick={() => performAction('Changes pushed', 'commit')}>Push</Button>}
        {action === 'rebase' && <Button variant="ai" icon={<RefreshCw />} onClick={() => performAction('Branch rebased', 'publish')}>Rebase</Button>}
        {action === 'resume' && <Button variant="primary" icon={<RefreshCw />} onClick={() => performAction('Holon reopened', 'commit')}>Reopen</Button>}
      </>}
      overflowActions={(
        <>
          <Button variant="ghost" className={terminalStyles.dangerAction} icon={<Ban />} onClick={() => performAction('Holon ended', 'resume')}>End holon</Button>
        </>
      )}
    />
  )

  return (
    <>
      <HolonWorkspace header={sessionHeader} reviewing={diffOpen} changes={
            <HolonDiffPanel
              holonId={holonId}
              enabled
              readOnly={false}
              open={diffOpen}
              onOpenChange={setDiffOpen}
              commitControls={preparation.dirty ? <CommitMessageControls preparation={preparation} /> : undefined}
              publishControls={<HolonPublishControls preparation={preparation} />}

            />
          }>
              <div className={styles.terminalPlaceholder} ref={previewSurface} tabIndex={-1} role="region" aria-label="Terminal preview">
                <span className="sr-only">Terminal preview</span>
                {actionMessage && <p role="status">{actionMessage}</p>}
                {selectedPreviewTabIdForRender === 'commit-message' ? <pre>{preparation.state === 'generating' ? 'Reading uncommitted changes…\nPreparing a commit subject and description…' : preparation.message || 'Message generation cancelled.'}{'\n\nSample generation · no connected process'}</pre> : <pre><span className={styles.terminalDim}>╭─ Agent CLI ─────────────────────────────────────────╮</span>{'\n'}
                  {'  Acme Dashboard · feature/workspace-navigation\n\n'}
                  <span className={styles.terminalPrompt}>› {holon.title}</span>{'\n\n'}
                  {'  I’ll review the workspace and simplify the navigation.\n\n'}
                  <span className={styles.terminalDim}>  Read src/hooks/useActiveTerminal.ts{'\n'}  Read src/components/SessionTabs.tsx</span>{'\n\n'}
                  {'  Updated the shared workspace components.\n'}
                  <span className={styles.terminalSuccess}>  ✓ Session selection is accessible{'\n'}  ✓ Active terminal is remembered{'\n'}  ✓ Workspace controls use the shared styles</span>{'\n\n'}
                  {'  4 files changed · +26 −5\n\n'}
                  <span className={styles.terminalDim}>  Sample terminal output · no connected process</span>
                </pre>}
              </div>
      </HolonWorkspace>

      <details className={styles.previewTools}><summary>Preview controls</summary><form className={styles.controls} onSubmit={(event) => event.preventDefault()}>
        <strong>Holon preview</strong>
        <label>
          Action
          <select value={action} onChange={(event) => setAction(event.target.value as PreviewAction)}>
            <option value="commit">Commit</option><option value="pull-request">Open PR</option><option value="publish">Push</option><option value="rebase">Rebase</option><option value="resume">Reopen</option>
          </select>
        </label>
        <label>
          Notice
          <select value={notice} onChange={(event) => { setNotice(event.target.value); if (event.target.value !== 'none') setDiffOpen(false) }}>
            <option value="none">Hidden</option>
            <option value="error">Error</option>
            <option value="selection">Tab selection</option>
          </select>
        </label>
        <label>
          Diff
          <select value={diffOpen ? 'open' : 'closed'} onChange={(event) => setDiffOpen(event.target.value === 'open')}>
            <option value="closed">Closed</option>
            <option value="open">Open</option>
          </select>
        </label>
        <label>
          Tabs
          <input
            type="number"
            min={1}
            max={100}
            step={1}
            value={previewTabs.length}
            onChange={(event) => {
              const count = event.currentTarget.valueAsNumber
              if (Number.isInteger(count) && count >= 1 && count <= 100) setPreviewTabCount(count)
            }}
          />
        </label>
      </form></details>
    </>
  )
}

function previewTabTitle(kind: PreviewTabKind, number: number) {
  if (kind === 'agent') return `Agent ${number}`
  if (kind === 'ide') return `IDE ${number}`
  return `Shell ${number}`
}

function previewTabKindLabel(kind: PreviewTabKind) {
  if (kind === 'agent') return 'Agent'
  if (kind === 'ide') return 'VS Code'
  return 'Terminal'
}
