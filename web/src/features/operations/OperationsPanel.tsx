import { useAgentCapabilities } from '../../data/useAgentCapabilities'
import { useContext, useState } from 'react'
import { useMatch, useNavigate, useParams } from 'react-router-dom'
import { api } from '../../data/api'
import { hasAvailableHarness, requiredHarnessTypes } from '../../data/harness'
import type { HarnessCapability, PullRequest, Holon } from '../../data/types'
import { NewAgentDialog } from '../agents/NewAgentDialog'
import { useCreationShortcut } from '../navigation/useCreationShortcut'
import { useSessionRename } from '../agents/holonRename'
import { useProject } from '../project/ProjectContext'
import { useHolonStore } from '../project/holonStoreContext'
import { OperationsPanelView } from './OperationsPanelView'
import { OperationsContext } from './operationsContext'

export function OperationsPanel() {
  const [expanded, setExpanded] = useState(false)
  const [composerOpen, setComposerOpen] = useState(false)
  useCreationShortcut('KeyN', () => {
    const headerButton = document.querySelector<HTMLButtonElement>('main button[aria-keyshortcuts="Shift+Alt+N"]')
    if (headerButton) headerButton.click()
    else setComposerOpen(true)
  })
  const [reopeningHolon, setReopeningHolon] = useState('')
  const [reopenError, setReopenError] = useState('')
  const project = useProject()
  const holons = useHolonStore()
  const holonRename = useSessionRename()
  const navigate = useNavigate()
  const { holonId } = useParams()
  const capabilityState = useAgentCapabilities()
  const capabilities: HarnessCapability[] = capabilityState.data ?? []
  const operations = useContext(OperationsContext)
  const pullRequestByHolon = operations?.pullRequestByHolon ?? new Map<string, PullRequest>()
  const pullRequestMatch = useMatch('/pulls/:pullRequestId')
  const selectedPullRequestId = pullRequestMatch?.params.pullRequestId
  const pullRequests = operations?.pullRequestList?.data ?? []
  const selectedPullRequest = pullRequests.find((pullRequest) => pullRequest.id === selectedPullRequestId)

  const reopen = async (holon: Holon) => {
    const requiredTypes = requiredHarnessTypes(holon)
    const harnessesAvailable = requiredTypes.every((type) => hasAvailableHarness(capabilities, type))
    if (!harnessesAvailable || reopeningHolon) return
    setReopeningHolon(holon.id)
    setReopenError('')
    try {
      const reopened = await api.reopenHolon(holon.id)
      holons.updateHolon(holon.id, reopened)
      navigate(`/holons/${holon.id}`)
      void holons.refresh()
    } catch (value) {
      setReopenError(value instanceof Error ? value.message : 'Reopen failed.')
    } finally {
      setReopeningHolon('')
    }
  }

  return (
    <>
      <OperationsPanelView
        project={project}
        holons={holons.holons}
        capabilities={capabilities}
        selectedSessionId={holonId}
        pullRequestByHolon={pullRequestByHolon}
        selectedPullRequestId={selectedPullRequestId}
        selectedPullRequest={selectedPullRequest}
        pullRequests={pullRequests}
        onTogglePullRequestPin={async (pullRequest) => {
          if (pullRequest.panel_pinned) await api.unpinPullRequestFromPanel(pullRequest.id)
          else await api.pinPullRequestToPanel(pullRequest.id)
          void operations?.pullRequestList?.refresh().catch((value) => {
            console.error('Failed to refresh pull requests after updating a panel pin.', value)
          })
        }}
        sessionsLoading={holons.loading}
        sessionsError={holons.error?.message}
        reopenError={reopenError}
        reopeningHolon={reopeningHolon}
        rename={holonRename}
        expanded={expanded}
        onExpandedChange={setExpanded}
        onNewAgent={() => setComposerOpen(true)}
        onReopen={(holon) => void reopen(holon)}
      />
      <NewAgentDialog open={composerOpen} project={project} capabilities={capabilities} onClose={() => setComposerOpen(false)} />
    </>
  )
}
