import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { MemoryRouter, useLocation } from 'react-router-dom'
import type { PullRequest, Holon } from '../../data/types'
import { HolonStoreProvider } from '../project/HolonStore'
import { useHolonStore } from '../project/holonStoreContext'
import { OperationsPanelView } from './OperationsPanelView'
import { OperationsContext } from './operationsContext'
import styles from './OperationsPanel.module.css'

const holons = [
  previewHolon('attention-independent', 'Independent attention', 'permission_required', '2026-08-26T10:04:00Z'),
  previewHolon('working-first', 'First working holon', 'none', '2026-08-26T10:03:00Z'),
  previewHolon('attention-grouped', 'Grouped attention', 'user_input_required', '2026-08-26T10:02:00Z', 'holark/cycle-attention'),
  previewHolon('idle-grouped', 'Grouped idle', 'task_complete', '2026-08-26T10:01:30Z', 'holark/cycle-attention'),
  previewHolon('working-second', 'Second working holon', 'none', '2026-08-26T10:01:00Z'),
]

function Preview({ pullRequestByHolon, expanded = true }: { pullRequestByHolon?: ReadonlyMap<string, PullRequest>, expanded?: boolean }) {
  const { pathname } = useLocation()
  return (
    <OperationsPanelView
      project={{ id: 'preview', name: 'Preview', default_branch: 'main' }}
      holons={holons}
      capabilities={[]}
      selectedSessionId={pathname.startsWith('/holons/') ? pathname.split('/')[2] : undefined}
      selectedPullRequestId={pathname.startsWith('/pulls/') ? pathname.split('/')[2] : undefined}
      pullRequestByHolon={pullRequestByHolon}
      rename={{ begin: vi.fn(), change: vi.fn(), commit: vi.fn(), cancel: vi.fn() }}
      expanded={expanded || undefined}
      onNewAgent={vi.fn()}
      onReopen={vi.fn()}
    />
  )
}

function AddressPreview({ holons }: { holons: Holon[] }) {
  return (
    <OperationsPanelView
      project={{ id: 'preview', name: 'Preview', default_branch: 'main' }}
      holons={holons}
      capabilities={[]}
      rename={{ begin: vi.fn(), change: vi.fn(), commit: vi.fn(), cancel: vi.fn() }}
      expanded
      onNewAgent={vi.fn()}
      onReopen={vi.fn()}
      sessionHref={() => '/'}
    />
  )
}

it('shows the highest working-agent context usage and retains the last value when work stops', async () => {
  window.localStorage.removeItem('holark.last-displayed-context-tokens')
  const observed = previewHolon('observed-context', 'Observed context', 'none', '2026-08-26T10:01:00Z', 'holark/context')
  if (!observed.agent_session) throw new Error('agent session is missing')
  observed.agent_sessions = [
    { ...observed.agent_session, id: 'completed-high', status: 'completed', activity: 'completed', context_tokens: 80_000 },
    { ...observed.agent_session, id: 'idle-high', status: 'running', activity: 'idle', context_tokens: 70_000 },
    { ...observed.agent_session, id: 'running-low', status: 'running', activity: 'working', context_tokens: 14_470 },
    { ...observed.agent_session, id: 'running-high', status: 'running', activity: 'working', context_tokens: 25_990 },
    { ...observed.agent_session, id: 'closed-running', status: 'running', activity: 'working', context_tokens: 900_000, closed_at: '2026-08-26T10:02:00Z' },
  ]
  const fallback = previewHolon('fallback-context', 'Fallback context', 'none', '2026-08-26T10:01:30Z', 'holark/fallback')
  if (!fallback.agent_session) throw new Error('fallback agent session is missing')
  fallback.agent_sessions = [
    { ...fallback.agent_session, id: 'completed-low', status: 'completed', activity: 'completed', context_tokens: 32_000 },
    { ...fallback.agent_session, id: 'queued-high', status: 'queued', activity: 'starting', context_tokens: 47_000 },
  ]
  const unknown = previewHolon('unknown-context', 'Unknown context', 'none', '2026-08-26T10:02:00Z', 'holark/unknown')

  const view = render(<MemoryRouter><AddressPreview holons={[observed, fallback, unknown]} /></MemoryRouter>)

  const observedLink = screen.getByRole('link', { name: /Observed context/ })
  expect(within(observedLink).getAllByText('25k')).toHaveLength(2)
  expect(within(observedLink).getByTitle('holark/context')).toBeInTheDocument()
  const expandedMeta = within(observedLink).getByTitle('Current context: 25,990 tokens').parentElement
  expect(expandedMeta?.children[0]).toHaveClass(styles.sessionContextUsage)
  expect(expandedMeta?.children[2]).toHaveClass(styles.sessionBranch)
  expect(within(screen.getByRole('link', { name: /Fallback context/ })).queryByTitle(/Current context/)).not.toBeInTheDocument()
  expect(within(screen.getByRole('link', { name: /Unknown context/ })).queryByTitle(/Current context/)).not.toBeInTheDocument()

  const lowerTabTakesLead = {
    ...observed,
    agent_sessions: observed.agent_sessions.map((agent) => agent.id === 'running-low' ? { ...agent, context_tokens: 31_200 } : agent),
  }
  view.rerender(<MemoryRouter><AddressPreview holons={[lowerTabTakesLead, fallback, unknown]} /></MemoryRouter>)

  expect(within(screen.getByRole('link', { name: /Observed context/ })).getAllByText('31k')).toHaveLength(2)

  const lowerTabStops = {
    ...lowerTabTakesLead,
    agent_sessions: lowerTabTakesLead.agent_sessions.map((agent) => agent.id === 'running-low' ? { ...agent, activity: 'idle' as const } : agent),
  }
  view.rerender(<MemoryRouter><AddressPreview holons={[lowerTabStops, fallback, unknown]} /></MemoryRouter>)
  expect(within(screen.getByRole('link', { name: /Observed context/ })).getAllByText('25k')).toHaveLength(2)

  const allStopped = {
    ...lowerTabStops,
    agent_sessions: lowerTabStops.agent_sessions.map((agent) => agent.id === 'running-high' ? { ...agent, activity: 'idle' as const } : agent),
  }
  view.rerender(<MemoryRouter><AddressPreview holons={[allStopped, fallback, unknown]} /></MemoryRouter>)
  expect(within(screen.getByRole('link', { name: /Observed context/ })).getAllByText('25k')).toHaveLength(2)

  const newWork = {
    ...allStopped,
    agent_sessions: [...allStopped.agent_sessions, { ...observed.agent_session, id: 'new-working', status: 'running' as const, activity: 'working' as const, context_tokens: 12_400 }],
  }
  view.rerender(<MemoryRouter><AddressPreview holons={[newWork, fallback, unknown]} /></MemoryRouter>)
  expect(within(screen.getByRole('link', { name: /Observed context/ })).getAllByText('12k')).toHaveLength(2)

  const newWorkStops = {
    ...newWork,
    agent_sessions: newWork.agent_sessions.map((agent) => agent.id === 'new-working' ? { ...agent, activity: 'idle' as const } : agent),
  }
  view.rerender(<MemoryRouter><AddressPreview holons={[newWorkStops, fallback, unknown]} /></MemoryRouter>)
  expect(within(screen.getByRole('link', { name: /Observed context/ })).getAllByText('12k')).toHaveLength(2)

  await waitFor(() => expect(JSON.parse(window.localStorage.getItem('holark.last-displayed-context-tokens') ?? '{}')['preview:observed-context']).toBe(12_400))
  view.unmount()
  const remounted = render(<MemoryRouter><AddressPreview holons={[newWorkStops, fallback, unknown]} /></MemoryRouter>)
  expect(within(screen.getByRole('link', { name: /Observed context/ })).getAllByText('12k')).toHaveLength(2)

  const resumedWork = {
    ...newWorkStops,
    agent_sessions: newWorkStops.agent_sessions.map((agent) => agent.id === 'running-low' ? { ...agent, activity: 'working' as const } : agent),
  }
  remounted.rerender(<MemoryRouter><AddressPreview holons={[resumedWork, fallback, unknown]} /></MemoryRouter>)
  expect(within(screen.getByRole('link', { name: /Observed context/ })).getAllByText('31k')).toHaveLength(2)
})

it('cycles Holons and PR rows with Alt+Shift+arrows in sidebar order, wrapping and revealing collapsed groups', async () => {
  const pullRequest = previewPullRequest('idle-grouped')
  const pullRequestByHolon = new Map([['idle-grouped', pullRequest], ['attention-grouped', pullRequest]])
  render(<MemoryRouter><Preview pullRequestByHolon={pullRequestByHolon} /></MemoryRouter>)
  await userEvent.click(screen.getByRole('button', { name: /Collapse PR #42/ }))

  const move = (key: string) => fireEvent.keyDown(document.body, { key, altKey: true, shiftKey: true })
  expect(move('ArrowDown')).toBe(false)
  expect(screen.getByRole('link', { name: /Second working holon/ })).toHaveAttribute('aria-current', 'page')
  move('ArrowDown')
  expect(screen.getByRole('link', { name: /First working holon/ })).toHaveAttribute('aria-current', 'page')
  move('ArrowDown')
  expect(screen.getByRole('link', { name: /Independent attention/ })).toHaveAttribute('aria-current', 'page')
  move('ArrowDown')
  expect(screen.getByRole('link', { name: 'Expand PR #42' })).toHaveAttribute('aria-current', 'page')
  expect(screen.queryByRole('link', { name: /Grouped idle/ })).not.toBeInTheDocument()
  move('ArrowDown')
  expect(screen.getByRole('link', { name: /Grouped idle/ })).toHaveAttribute('aria-current', 'page')
  move('ArrowDown')
  expect(screen.getByRole('link', { name: /Grouped attention/ })).toHaveAttribute('aria-current', 'page')
  move('ArrowDown')
  expect(screen.getByRole('link', { name: /Second working holon/ })).toHaveAttribute('aria-current', 'page')
  move('ArrowUp')
  expect(screen.getByRole('link', { name: /Grouped attention/ })).toHaveAttribute('aria-current', 'page')
  move('ArrowUp')
  expect(screen.getByRole('link', { name: /Grouped idle/ })).toHaveAttribute('aria-current', 'page')
  move('ArrowUp')
  expect(screen.getByRole('link', { name: 'Collapse PR #42' })).toHaveAttribute('aria-current', 'page')
  move('ArrowUp')
  expect(screen.getByRole('link', { name: /Independent attention/ })).toHaveAttribute('aria-current', 'page')
  expect(screen.getByRole('button', { name: /Expand PR #42/ })).toHaveAttribute('aria-expanded', 'false')
})

it('cycles pinned pull requests without active Holons while excluding a current unpinned pull request', () => {
  const first = { ...previewPullRequest(''), id: 'first', title: 'First pinned', panel_pinned: true, sync_data: { github: { number: 41 } } }
  const second = { ...previewPullRequest(''), id: 'second', title: 'Second pinned', panel_pinned: true, sync_data: { github: { number: 42 } } }
  const unpinned = { ...previewPullRequest(''), id: 'current', title: 'Current unpinned', sync_data: { github: { number: 43 } } }

  function PinnedPullRequestsPreview() {
    const { pathname } = useLocation()
    const selectedPullRequestId = pathname.split('/')[2]
    const selectedPullRequest = [first, second, unpinned].find((pullRequest) => pullRequest.id === selectedPullRequestId)
    return (
      <OperationsPanelView
        project={{ id: 'preview', name: 'Preview', default_branch: 'main' }}
        holons={[]}
        capabilities={[]}
        selectedPullRequestId={selectedPullRequestId}
        selectedPullRequest={selectedPullRequest}
        pullRequests={[first, second]}
        rename={{ begin: vi.fn(), change: vi.fn(), commit: vi.fn(), cancel: vi.fn() }}
        expanded
        onNewAgent={vi.fn()}
        onReopen={vi.fn()}
      />
    )
  }

  render(<MemoryRouter initialEntries={['/pulls/current']}><PinnedPullRequestsPreview /></MemoryRouter>)
  const move = (key: string) => fireEvent.keyDown(document.body, { key, altKey: true, shiftKey: true })

  move('ArrowDown')
  expect(screen.getByRole('link', { name: 'Open PR #41' })).toHaveAttribute('aria-current', 'page')
  move('ArrowDown')
  expect(screen.getByRole('link', { name: 'Open PR #42' })).toHaveAttribute('aria-current', 'page')
  move('ArrowDown')
  expect(screen.getByRole('link', { name: 'Open PR #41' })).toHaveAttribute('aria-current', 'page')
  move('ArrowUp')
  expect(screen.getByRole('link', { name: 'Open PR #42' })).toHaveAttribute('aria-current', 'page')
})

it('selects waiting agents when entering holons and cycles them when clicking the current holon', async () => {
  const current = previewHolon('current', 'Current holon', 'permission_required', '2026-08-26T10:01:00Z')
  const currentShell = { id: 'current-shell', title: 'Current shell', cwd: '/tmp',
    created_at: current.created_at, updated_at: current.created_at }
  current.manual_terminals = [currentShell]
  current.last_selected_tab_id = currentShell.id
  const destination = previewHolon('destination', 'Destination holon', 'user_input_required', '2026-08-26T10:02:00Z')
  const firstAgent = { ...destination.agent_session!, id: 'waiting-first', title: 'First waiting', tab_order: 1 }
  const secondAgent = { ...firstAgent, id: 'waiting-second', title: 'Second waiting', tab_order: 2 }
  const shell = { id: 'remembered-shell', title: 'Shell', cwd: '/tmp', tab_order: 3,
    created_at: destination.created_at, updated_at: destination.created_at }
  destination.agent_session = firstAgent
  destination.agent_sessions = [firstAgent, secondAgent]
  destination.manual_terminals = [shell]
  destination.last_selected_tab_id = shell.id
  const savedTabs: string[] = []
  vi.stubGlobal('fetch', vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
    if (String(input).endsWith('/selected-tab')) {
      savedTabs.push((JSON.parse(String(init?.body)) as { tab_id: string }).tab_id)
      return new Response(null, { status: 204 })
    }
    return new Response(JSON.stringify([current, destination]), { status: 200, headers: { 'Content-Type': 'application/json' } })
  }))

  function StoredPanel() {
    const store = useHolonStore()
    const { pathname } = useLocation()
    return <>
      <span data-testid="selected-tab">{store.getHolon(destination.id)?.last_selected_tab_id}</span>
      <span data-testid="current-selected-tab">{store.getHolon(current.id)?.last_selected_tab_id}</span>
      <OperationsPanelView
        project={{ id: 'preview', name: 'Preview', default_branch: 'main' }}
        holons={store.holons}
        capabilities={[]}
        selectedSessionId={pathname.split('/')[2]}
        rename={{ begin: vi.fn(), change: vi.fn(), commit: vi.fn(), cancel: vi.fn() }}
        expanded
        onNewAgent={vi.fn()}
        onReopen={vi.fn()}
      />
    </>
  }

  render(<MemoryRouter initialEntries={['/holons/current']}>
    <HolonStoreProvider projectId="preview" interval={60_000}><StoredPanel /></HolonStoreProvider>
  </MemoryRouter>)
  await screen.findByRole('link', { name: /Destination holon/ })
  expect(screen.getByTestId('selected-tab')).toHaveTextContent(shell.id)

  fireEvent.keyDown(document.body, { key: 'ArrowDown', altKey: true, shiftKey: true })
  expect(screen.getByRole('link', { name: /Destination holon/ })).toHaveAttribute('aria-current', 'page')
  expect(screen.getByTestId('selected-tab')).toHaveTextContent(firstAgent.id)
  await waitFor(() => expect(savedTabs).toEqual([firstAgent.id]))

  await userEvent.click(screen.getByRole('link', { name: /Destination holon/ }))
  expect(screen.getByTestId('selected-tab')).toHaveTextContent(secondAgent.id)
  await waitFor(() => expect(savedTabs).toEqual([firstAgent.id, secondAgent.id]))

  await userEvent.click(screen.getByRole('link', { name: /Destination holon/ }))
  expect(screen.getByTestId('selected-tab')).toHaveTextContent(firstAgent.id)
  await waitFor(() => expect(savedTabs).toEqual([firstAgent.id, secondAgent.id, firstAgent.id]))

  await userEvent.click(screen.getByRole('link', { name: /Current holon/ }))
  expect(screen.getByRole('link', { name: /Current holon/ })).toHaveAttribute('aria-current', 'page')
  expect(screen.getByTestId('current-selected-tab')).toHaveTextContent(current.agent_session!.id)
  await waitFor(() => expect(savedTabs).toEqual([firstAgent.id, secondAgent.id, firstAgent.id, current.agent_session!.id]))
})

it('preserves editing keys and disables Holon shortcuts while a modal is open', () => {
  const view = render(<MemoryRouter><Preview /><input aria-label="Draft" /><div contentEditable aria-label="Editor" /></MemoryRouter>)
  const shortcut = { key: 'ArrowDown', altKey: true, shiftKey: true }
  expect(fireEvent.keyDown(screen.getByRole('textbox', { name: 'Draft' }), shortcut)).toBe(true)
  expect(fireEvent.keyDown(screen.getByLabelText('Editor'), shortcut)).toBe(true)
  expect(fireEvent.keyDown(document.body, { ...shortcut, shiftKey: false })).toBe(true)
  expect(fireEvent.keyDown(document.body, { ...shortcut, ctrlKey: true })).toBe(true)
  expect(fireEvent.keyDown(document.body, { ...shortcut, metaKey: true })).toBe(true)
  expect(fireEvent.keyDown(document.body, { ...shortcut, isComposing: true })).toBe(true)
  expect(screen.getByRole('link', { name: /Second working holon/ })).not.toHaveAttribute('aria-current')

  view.rerender(<MemoryRouter><Preview /><div role="dialog" aria-modal="true">Dialog</div></MemoryRouter>)
  expect(fireEvent.keyDown(document.body, shortcut)).toBe(true)
  expect(screen.getByRole('link', { name: /Second working holon/ })).not.toHaveAttribute('aria-current')
})

it('keeps the established sidebar title and rename styles after Holon renaming', () => {
  render(<MemoryRouter><Preview /></MemoryRouter>)
  const link = screen.getByRole('link', { name: /Independent attention/ })
  const article = link.closest('article')
  if (!article) throw new Error('holon article is missing')

  expect(within(link).getByText('Independent attention').closest('strong')).toHaveClass(styles.sessionTitle)
  expect(within(article).getByRole('button', { name: 'Rename Independent attention' })).toHaveClass(styles.sessionRenameButton)
})

it('orders active holons oldest-created first', () => {
  const existing = previewHolon('existing', 'Existing holon', 'none', '2026-08-26T10:01:00Z')
  const added = previewHolon('added', 'New holon', 'none', '2026-08-26T10:02:00Z')
  const view = render(<MemoryRouter><AddressPreview holons={[existing]} /></MemoryRouter>)

  view.rerender(<MemoryRouter><AddressPreview holons={[added, existing]} /></MemoryRouter>)

  expect(within(screen.getByLabelText('Independent holons')).getAllByRole('link').map((link) => link.textContent)).toEqual([
    expect.stringContaining('Existing holon'),
    expect.stringContaining('New holon'),
  ])
})

it('orders ended holons newest-ended first using timestamp fallbacks', async () => {
  const user = userEvent.setup()
  const finishedNewest = {
    ...previewHolon('finished-newest', 'Finished newest', 'none', '2026-08-26T10:01:00Z'),
    status: 'failed' as const,
    finished_at: '2026-08-26T10:10:00Z',
  }
  const finishedFallback = {
    ...previewHolon('finished-fallback', 'Finished fallback', 'none', '2026-08-26T10:04:00Z'),
    status: 'cancelled' as const,
    finished_at: '2026-08-26T10:09:00Z',
  }
  const createdFallback = {
    ...previewHolon('created-fallback', 'Created fallback', 'none', '2026-08-26T10:08:00Z'),
    status: 'lost' as const,
  }
  const finishedOldest = {
    ...previewHolon('finished-oldest', 'Finished oldest', 'none', '2026-08-26T10:07:00Z'),
    status: 'expired' as const,
    finished_at: '2026-08-26T10:06:00Z',
  }

  render(
    <MemoryRouter>
      <AddressPreview holons={[finishedOldest, createdFallback, finishedFallback, finishedNewest]} />
    </MemoryRouter>,
  )

  await user.click(screen.getByRole('button', { name: 'Show ended holons (4)' }))

  expect(screen.getAllByRole('link').filter((link) => !link.getAttribute('href')?.startsWith('/pulls/')).map((link) => link.textContent)).toEqual([
    expect.stringContaining('Finished newest'),
    expect.stringContaining('Finished fallback'),
    expect.stringContaining('Created fallback'),
    expect.stringContaining('Finished oldest'),
  ])
})

it('keeps active holon addresses stable and reuses the smallest released letter', () => {
  const oldest = previewHolon('oldest', 'Oldest holon', 'none', '2026-08-26T10:01:00Z')
  const middle = previewHolon('middle', 'Middle holon', 'none', '2026-08-26T10:02:00Z')
  const newest = previewHolon('newest', 'Newest holon', 'none', '2026-08-26T10:03:00Z')
  const view = render(<MemoryRouter><AddressPreview holons={[newest, middle, oldest]} /></MemoryRouter>)

  expect(within(screen.getByRole('link', { name: /Agent A, Oldest holon/ })).getByText('A')).toBeInTheDocument()
  expect(within(screen.getByRole('link', { name: /Agent B, Middle holon/ })).getByText('B')).toBeInTheDocument()
  expect(within(screen.getByRole('link', { name: /Agent C, Newest holon/ })).getByText('C')).toBeInTheDocument()

  const replacement = previewHolon('replacement', 'Replacement holon', 'none', '2026-08-26T10:04:00Z')
  view.rerender(
    <MemoryRouter>
      <AddressPreview holons={[replacement, newest, { ...middle, status: 'failed' }, oldest]} />
    </MemoryRouter>,
  )

  expect(screen.queryByRole('link', { name: /Middle holon/ })).not.toBeInTheDocument()
  expect(screen.getByRole('link', { name: /Agent A, Oldest holon/ })).toBeInTheDocument()
  expect(screen.getByRole('link', { name: /Agent C, Newest holon/ })).toBeInTheDocument()
  expect(screen.getByRole('link', { name: /Agent B, Replacement holon/ })).toBeInTheDocument()
})

it('uses only single-letter holon addresses through Z', () => {
  const manySessions = Array.from({ length: 27 }, (_, index) => previewHolon(
    `holon-${index}`,
    `Holon ${index}`,
    'none',
    `2026-08-26T10:00:${String(index).padStart(2, '0')}Z`,
  ))
  render(<MemoryRouter><AddressPreview holons={manySessions} /></MemoryRouter>)

  expect(screen.getByRole('link', { name: /Agent Z, Holon 25/ })).toBeInTheDocument()
  expect(screen.getByRole('link', { name: /^Holon 26,/ })).toBeInTheDocument()
  expect(screen.queryByRole('link', { name: /Agent AA/ })).not.toBeInTheDocument()
})

it('summarizes working and attention holons on branch and pull request rows', () => {
  const branchWorking = previewHolon('branch-working', 'Branch working', 'none', '2026-08-26T10:06:00Z', 'holark/branch-summary')
  const branchAttention = previewHolon('branch-attention', 'Branch attention', 'permission_required', '2026-08-26T10:05:00Z', 'holark/branch-summary')
  const branchIdle = previewHolon('branch-idle', 'Branch idle', 'task_complete', '2026-08-26T10:04:00Z', 'holark/branch-summary')
  const pullRequestWorking = previewHolon('pr-working', 'PR working', 'none', '2026-08-26T10:03:00Z', 'holark/pr-summary')
  const pullRequest = previewPullRequest(pullRequestWorking.id)

  render(
    <MemoryRouter>
      <OperationsPanelView
        project={{ id: 'preview', name: 'Preview', default_branch: 'main' }}
        holons={[branchWorking, branchAttention, branchIdle, pullRequestWorking]}
        capabilities={[]}
        pullRequestByHolon={new Map([[pullRequestWorking.id, pullRequest]])}
        rename={{ begin: vi.fn(), change: vi.fn(), commit: vi.fn(), cancel: vi.fn() }}
        expanded
        onNewAgent={vi.fn()}
        onReopen={vi.fn()}
        sessionHref={() => '/'}
      />
    </MemoryRouter>,
  )

  const branchGroup = screen.getByLabelText('holark/branch-summary')
  expect(within(branchGroup).getByRole('button', { name: /1 working, 1 awaiting input/ })).toBeInTheDocument()
  expect(within(branchGroup).getByTitle('1 working holon')).toHaveTextContent('1')
  expect(within(branchGroup).getByTitle('3 holons')).toHaveTextContent('3')

  const pullRequestGroup = screen.getByLabelText('PR #42 · Summarize holons')
  expect(within(pullRequestGroup).getByRole('button', { name: /1 working/ })).toBeInTheDocument()
  expect(within(pullRequestGroup).getByRole('link', { name: 'Open PR #42' })).toHaveAttribute('href', `/pulls/${pullRequest.id}`)
})

it('keeps pull request activity holons under one umbrella across worker branches', () => {
  const pullRequest = previewPullRequest('feature-holon')
  const feature = previewHolon('feature-holon', 'Feature holon', 'task_complete', '2026-08-26T10:04:00Z', pullRequest.head_branch)
  const review = previewHolon('review-holon', 'Review holon', 'none', '2026-08-26T10:03:00Z', 'holark/review/one')
  const address = previewHolon('address-holon', 'Address comments', 'none', '2026-08-26T10:02:00Z', 'holark/worker/two')
  const metadata = previewHolon('metadata-holon', 'Improve metadata', 'none', '2026-08-26T10:01:00Z', 'holark/metadata/three')
  const pullRequestByHolon = new Map([feature, review, address, metadata].map((holon) => [holon.id, pullRequest]))

  render(
    <MemoryRouter>
      <OperationsPanelView
        project={{ id: 'preview', name: 'Preview', default_branch: 'main' }}
        holons={[feature, review, address, metadata]}
        capabilities={[]}
        pullRequestByHolon={pullRequestByHolon}
        rename={{ begin: vi.fn(), change: vi.fn(), commit: vi.fn(), cancel: vi.fn() }}
        expanded
        onNewAgent={vi.fn()}
        onReopen={vi.fn()}
        sessionHref={() => '/'}
      />
    </MemoryRouter>,
  )

  const pullRequestGroup = screen.getByLabelText('PR #42 · Summarize holons')
  expect(within(pullRequestGroup).getByRole('link', { name: /Feature holon/ })).toBeInTheDocument()
  expect(within(pullRequestGroup).getByRole('link', { name: /Review holon/ })).toBeInTheDocument()
  expect(within(pullRequestGroup).getByRole('link', { name: /Address comments/ })).toBeInTheDocument()
  expect(within(pullRequestGroup).getByRole('link', { name: /Improve metadata/ })).toBeInTheDocument()
  expect(within(pullRequestGroup).getAllByRole('link')
    .filter((link) => link.getAttribute('href') === '/')
    .map((link) => link.textContent)).toEqual([
    expect.stringContaining('Improve metadata'),
    expect.stringContaining('Address comments'),
    expect.stringContaining('Review holon'),
    expect.stringContaining('Feature holon'),
  ])
})

it('renders a pinned pull request without Holons as one direct empty row', () => {
  const pullRequest = { ...previewPullRequest(''), panel_pinned: true }
  render(
    <MemoryRouter>
      <OperationsPanelView
        project={{ id: 'preview', name: 'Preview', default_branch: 'main' }}
        holons={[]}
        capabilities={[]}
        pullRequests={[pullRequest]}
        rename={{ begin: vi.fn(), change: vi.fn(), commit: vi.fn(), cancel: vi.fn() }}
        expanded
        onNewAgent={vi.fn()}
        onReopen={vi.fn()}
      />
    </MemoryRouter>,
  )

  const group = screen.getByLabelText('PR #42 · Summarize holons')
  expect(within(group).getByRole('link', { name: 'Open PR #42' })).toHaveAttribute('href', `/pulls/${pullRequest.id}`)
  expect(within(group).queryByRole('button', { name: /^(Expand|Collapse)/ })).not.toBeInTheDocument()
  expect(within(group).getByRole('button', { name: 'Unpin PR #42 from Holons panel' })).toHaveAttribute('aria-pressed', 'true')
  expect(within(group).queryByTitle('0 holons')).not.toBeInTheDocument()
  expect(group.querySelector(`.${styles.groupSessions}`)).not.toBeInTheDocument()
})

it('temporarily renders the current unpinned pull request without Holons', () => {
  const pullRequest = previewPullRequest('')
  const props = {
    project: { id: 'preview', name: 'Preview', default_branch: 'main' },
    holons: [],
    capabilities: [],
    selectedPullRequestId: pullRequest.id,
    selectedPullRequest: pullRequest,
    rename: { begin: vi.fn(), change: vi.fn(), commit: vi.fn(), cancel: vi.fn() },
    expanded: true,
    onNewAgent: vi.fn(),
    onReopen: vi.fn(),
  }
  const view = render(<MemoryRouter><OperationsPanelView {...props} /></MemoryRouter>)

  const group = screen.getByLabelText('PR #42 · Summarize holons')
  expect(within(group).getByRole('button', { name: 'Pin PR #42 to Holons panel' })).toHaveAttribute('aria-pressed', 'false')

  view.rerender(
    <MemoryRouter>
      <OperationsPanelView {...props} selectedPullRequestId={undefined} selectedPullRequest={undefined} />
    </MemoryRouter>,
  )
  expect(screen.queryByLabelText('PR #42 · Summarize holons')).not.toBeInTheDocument()
})

it('keeps a selected pull request in place when it becomes pinned', async () => {
  const user = userEvent.setup()
  const pullRequest = previewPullRequest('')
  const existingPin = {
    ...previewPullRequest(''),
    id: 'existing-pin',
    title: 'Existing pin',
    panel_pinned: true,
    sync_data: { github: { number: 41 } },
    created_at: '2026-08-26T11:00:00Z',
  }
  const props = {
    project: { id: 'preview', name: 'Preview', default_branch: 'main' },
    holons: [],
    capabilities: [],
    selectedPullRequestId: pullRequest.id,
    selectedPullRequest: pullRequest,
    rename: { begin: vi.fn(), change: vi.fn(), commit: vi.fn(), cancel: vi.fn() },
    expanded: true,
    onNewAgent: vi.fn(),
    onReopen: vi.fn(),
    onTogglePullRequestPin: vi.fn().mockResolvedValue(undefined),
  }
  const view = render(
    <MemoryRouter>
      <OperationsPanelView {...props} pullRequests={[existingPin]} />
    </MemoryRouter>,
  )
  const groupLabels = () => Array.from(screen.getByRole('region', { name: 'Holon list' }).querySelectorAll(`.${styles.sessionGroup}`))
    .map((group) => group.getAttribute('aria-label'))

  expect(groupLabels()).toEqual(['PR #41 · Existing pin', 'PR #42 · Summarize holons'])
  await user.click(screen.getByRole('button', { name: 'Pin PR #42 to Holons panel' }))

  const pinnedPullRequest = { ...pullRequest, panel_pinned: true }
  view.rerender(
    <MemoryRouter>
      <OperationsPanelView
        {...props}
        selectedPullRequest={pinnedPullRequest}
        pullRequests={[existingPin, pinnedPullRequest]}
      />
    </MemoryRouter>,
  )
  expect(groupLabels()).toEqual(['PR #41 · Existing pin', 'PR #42 · Summarize holons'])
})

it.each([
  ['ArrowDown', [41, 42, 43, 41]],
  ['ArrowUp', [43, 42, 41, 43]],
] as const)('reaches every pinned PR with %s after pinning the selected inactive PR', async (key, expectedNumbers) => {
  const first = { ...previewPullRequest(''), id: 'first', panel_pinned: true, created_at: '2026-08-26T10:00:00Z', sync_data: { github: { number: 41 } } }
  const current = { ...previewPullRequest(''), id: 'current', created_at: '2026-08-26T11:00:00Z', sync_data: { github: { number: 42 } } }
  const last = { ...previewPullRequest(''), id: 'last', panel_pinned: true, created_at: '2026-08-26T12:00:00Z', sync_data: { github: { number: 43 } } }

  function PinningPreview() {
    const { pathname } = useLocation()
    const [pinnedPullRequests, setPinnedPullRequests] = useState<PullRequest[]>([first, last])
    const selectedPullRequestId = pathname.split('/')[2]
    return (
      <OperationsPanelView
        project={{ id: 'preview', name: 'Preview', default_branch: 'main' }}
        holons={[]}
        capabilities={[]}
        selectedPullRequestId={selectedPullRequestId}
        selectedPullRequest={[...pinnedPullRequests, current].find((pr) => pr.id === selectedPullRequestId)}
        pullRequests={pinnedPullRequests}
        rename={{ begin: vi.fn(), change: vi.fn(), commit: vi.fn(), cancel: vi.fn() }}
        expanded
        onNewAgent={vi.fn()}
        onReopen={vi.fn()}
        onTogglePullRequestPin={async (pr) => setPinnedPullRequests((pins) => [...pins, { ...pr, panel_pinned: true }])}
      />
    )
  }

  render(<MemoryRouter initialEntries={['/pulls/current']}><PinningPreview /></MemoryRouter>)
  await userEvent.click(screen.getByRole('button', { name: 'Pin PR #42 to Holons panel' }))
  const rowOrder = () => screen.getAllByRole('link', { name: /^Open PR #/ }).map((link) => link.getAttribute('href'))
  expect(rowOrder()).toEqual(['/pulls/first', '/pulls/last', '/pulls/current'])

  for (const number of expectedNumbers) {
    fireEvent.keyDown(document.body, { key, altKey: true, shiftKey: true })
    expect(screen.getByRole('link', { name: `Open PR #${number}` })).toHaveAttribute('aria-current', 'page')
    expect(rowOrder()).toEqual(['/pulls/first', '/pulls/current', '/pulls/last'])
  }
})

it.each(['success', 'failure'])('disables all PR pin buttons until a pending update settles with %s', async (outcome) => {
  const user = userEvent.setup()
  const consoleError = vi.spyOn(console, 'error').mockImplementation(() => {})
  const pullRequest = previewPullRequest('active-holon')
  const otherPullRequest = { ...pullRequest, id: 'other-pr', panel_pinned: true, sync_data: { github: { number: 43 } } }
  const holon = previewHolon('active-holon', 'Active PR holon', 'none', '2026-08-26T10:03:00Z', pullRequest.head_branch)
  let resolveToggle: () => void = () => {}
  let rejectToggle: (reason: Error) => void = () => {}
  const onTogglePullRequestPin = vi.fn().mockImplementation(() => new Promise<void>((resolve, reject) => { resolveToggle = resolve; rejectToggle = reject }))
  const props = {
    project: { id: 'preview', name: 'Preview', default_branch: 'main' },
    holons: [holon],
    pullRequests: [otherPullRequest],
    capabilities: [],
    pullRequestByHolon: new Map([[holon.id, pullRequest]]),
    rename: { begin: vi.fn(), change: vi.fn(), commit: vi.fn(), cancel: vi.fn() },
    expanded: true,
    onNewAgent: vi.fn(),
    onReopen: vi.fn(),
    onTogglePullRequestPin,
  }
  render(<MemoryRouter><OperationsPanelView {...props} /></MemoryRouter>)

  const pin = screen.getByRole('button', { name: 'Pin PR #42 to Holons panel' })
  const otherPin = screen.getByRole('button', { name: 'Unpin PR #43 from Holons panel' })
  expect(pin).toBeEnabled()
  expect(otherPin).toBeEnabled()
  expect(pin).toHaveAttribute('aria-pressed', 'false')
  expect(pin.querySelector('svg')).toHaveAttribute('fill', 'none')
  await user.click(pin)
  expect(onTogglePullRequestPin).toHaveBeenCalledWith(pullRequest)
  const unpin = screen.getByRole('button', { name: 'Unpin PR #42 from Holons panel' })
  expect(unpin).toHaveAttribute('aria-pressed', 'true')
  expect(unpin).not.toHaveAttribute('aria-busy')
  expect(unpin).toBeDisabled()
  expect(otherPin).toBeDisabled()
  expect(unpin.querySelector('svg')).toHaveAttribute('fill', 'currentColor')
  await user.click(unpin)
  await user.click(otherPin)
  expect(onTogglePullRequestPin).toHaveBeenCalledTimes(1)

  if (outcome === 'failure') {
    const pinError = new Error('Pin failed.')
    rejectToggle(pinError)
    await waitFor(() => expect(consoleError).toHaveBeenCalledWith('Failed to update PR #42 pin in the Holons panel.', pinError))
    expect(screen.queryByText('Pin failed.')).not.toBeInTheDocument()
    const rolledBackPin = screen.getByRole('button', { name: 'Pin PR #42 to Holons panel' })
    expect(rolledBackPin).toHaveAttribute('aria-pressed', 'false')
    expect(rolledBackPin.querySelector('svg')).toHaveAttribute('fill', 'none')
    await waitFor(() => expect(rolledBackPin).toBeEnabled())
  } else {
    resolveToggle()
    await waitFor(() => expect(unpin).toBeEnabled())
    expect(unpin).toHaveAttribute('aria-pressed', 'true')
  }
  expect(otherPin).toBeEnabled()
  consoleError.mockRestore()
})

it('retains saved pins through stale refreshes, other pin writes and navigation until tracking acknowledges them', async () => {
  const user = userEvent.setup()
  const first = previewPullRequest('')
  const second = { ...first, id: 'second', sync_data: { github: { number: 43 } } }
  const onTogglePullRequestPin = vi.fn().mockResolvedValue(undefined)
  const props = {
    project: { id: 'preview', name: 'Preview', default_branch: 'main' },
    holons: [],
    capabilities: [],
    rename: { begin: vi.fn(), change: vi.fn(), commit: vi.fn(), cancel: vi.fn() },
    expanded: true,
    onNewAgent: vi.fn(),
    onReopen: vi.fn(),
    onTogglePullRequestPin,
  }
  const panel = (selectedPullRequest?: PullRequest, pinnedPullRequests: PullRequest[] = []) => (
    <MemoryRouter>
      <OperationsPanelView
        {...props}
        selectedPullRequestId={selectedPullRequest?.id}
        selectedPullRequest={selectedPullRequest}
        pullRequests={pinnedPullRequests}
      />
    </MemoryRouter>
  )
  const view = render(panel(first))
  await user.click(screen.getByRole('button', { name: 'Pin PR #42 to Holons panel' }))
  // Failed or superseded refreshes resolve without updating the tracking props.
  expect(screen.getByRole('button', { name: 'Unpin PR #42 from Holons panel' })).toHaveAttribute('aria-pressed', 'true')
  view.rerender(panel(second))
  expect(screen.getByRole('link', { name: 'Open PR #42' })).toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'Pin PR #43 to Holons panel' }))
  view.rerender(panel())
  expect(screen.getByRole('button', { name: 'Unpin PR #42 from Holons panel' })).toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Unpin PR #43 from Holons panel' })).toBeInTheDocument()

  // Another toggle must use the saved value even though the source is still stale.
  await user.click(screen.getByRole('button', { name: 'Unpin PR #42 from Holons panel' }))
  expect(onTogglePullRequestPin).toHaveBeenLastCalledWith({ ...first, panel_pinned: true })
  expect(screen.queryByRole('link', { name: 'Open PR #42' })).not.toBeInTheDocument()
  expect(screen.getByRole('link', { name: 'Open PR #43' })).toBeInTheDocument()

  view.rerender(panel(undefined, [{ ...second, panel_pinned: true }]))
  // Once acknowledged, subsequent server changes are authoritative again.
  view.rerender(panel())
  expect(screen.queryByRole('link', { name: 'Open PR #43' })).not.toBeInTheDocument()
})

it('lets merged pull requests be pinned and keeps them in the panel', async () => {
  const user = userEvent.setup()
  const pullRequest = { ...previewPullRequest(''), status: 'merged' as const }
  const onTogglePullRequestPin = vi.fn().mockResolvedValue(undefined)
  render(
    <MemoryRouter>
      <OperationsPanelView
        project={{ id: 'preview', name: 'Preview', default_branch: 'main' }}
        holons={[]}
        capabilities={[]}
        rename={{ begin: vi.fn(), change: vi.fn(), commit: vi.fn(), cancel: vi.fn() }}
        expanded
        onNewAgent={vi.fn()}
        onReopen={vi.fn()}
        onTogglePullRequestPin={onTogglePullRequestPin}
        pullRequests={[{ ...pullRequest, panel_pinned: true }]}
      />
    </MemoryRouter>,
  )
  expect(screen.getByRole('link', { name: 'Open PR #42' })).toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'Unpin PR #42 from Holons panel' }))
  expect(onTogglePullRequestPin).toHaveBeenCalledTimes(1)
})

it('retains a saved unpin through stale tracking and rolls back only a failed subsequent write', async () => {
  const user = userEvent.setup()
  const consoleError = vi.spyOn(console, 'error').mockImplementation(() => {})
  const pullRequest = { ...previewPullRequest(''), panel_pinned: true }
  const onTogglePullRequestPin = vi.fn().mockResolvedValueOnce(undefined).mockRejectedValueOnce(new Error('Pin failed.'))
  const props = {
    project: { id: 'preview', name: 'Preview', default_branch: 'main' },
    holons: [],
    capabilities: [],
    rename: { begin: vi.fn(), change: vi.fn(), commit: vi.fn(), cancel: vi.fn() },
    expanded: true,
    onNewAgent: vi.fn(),
    onReopen: vi.fn(),
    onTogglePullRequestPin,
    pullRequests: [pullRequest],
  }
  const view = render(
    <MemoryRouter>
      <OperationsPanelView {...props} selectedPullRequestId={pullRequest.id} selectedPullRequest={pullRequest} />
    </MemoryRouter>,
  )
  await user.click(screen.getByRole('button', { name: 'Unpin PR #42 from Holons panel' }))
  expect(screen.getByRole('button', { name: 'Pin PR #42 to Holons panel' })).toHaveAttribute('aria-pressed', 'false')
  await user.click(screen.getByRole('button', { name: 'Pin PR #42 to Holons panel' }))
  expect(onTogglePullRequestPin).toHaveBeenLastCalledWith({ ...pullRequest, panel_pinned: false })
  expect(screen.getByRole('button', { name: 'Pin PR #42 to Holons panel' })).toHaveAttribute('aria-pressed', 'false')
  expect(consoleError).toHaveBeenCalledWith('Failed to update PR #42 pin in the Holons panel.', expect.any(Error))
  view.rerender(<MemoryRouter><OperationsPanelView {...props} /></MemoryRouter>)
  expect(screen.queryByRole('link', { name: 'Open PR #42' })).not.toBeInTheDocument()
  view.rerender(<MemoryRouter><OperationsPanelView {...props} pullRequests={[]} /></MemoryRouter>)
  view.rerender(<MemoryRouter><OperationsPanelView {...props} /></MemoryRouter>)
  expect(screen.getByRole('button', { name: 'Unpin PR #42 from Holons panel' })).toBeInTheDocument()
  consoleError.mockRestore()
})

it('keeps an active pull request group visible without a pin and does not duplicate it when pinned', () => {
  const pullRequest = previewPullRequest('active-holon')
  const holon = previewHolon('active-holon', 'Active PR holon', 'none', '2026-08-26T10:03:00Z', pullRequest.head_branch)
  const props = {
    project: { id: 'preview', name: 'Preview', default_branch: 'main' },
    holons: [holon],
    capabilities: [],
    pullRequestByHolon: new Map([[holon.id, pullRequest]]),
    rename: { begin: vi.fn(), change: vi.fn(), commit: vi.fn(), cancel: vi.fn() },
    expanded: true,
    onNewAgent: vi.fn(),
    onReopen: vi.fn(),
  }
  const view = render(<MemoryRouter><OperationsPanelView {...props} /></MemoryRouter>)
  expect(screen.getAllByLabelText('PR #42 · Summarize holons')).toHaveLength(1)
  expect(screen.getByRole('link', { name: /Active PR holon/ })).toBeInTheDocument()

  view.rerender(
    <MemoryRouter>
      <OperationsPanelView {...props} pullRequests={[{ ...pullRequest, panel_pinned: true }]} />
    </MemoryRouter>,
  )
  expect(screen.getAllByLabelText('PR #42 · Summarize holons')).toHaveLength(1)
})

it('hides branch metadata that repeats the group header', async () => {
  const user = userEvent.setup()
  const pullRequest = previewPullRequest('pr-head')
  const independent = previewHolon('independent', 'Independent holon', 'none', '2026-08-26T10:05:00Z', 'pmaviro/independent-work')
  const grouped = previewHolon('grouped', 'Grouped holon', 'none', '2026-08-26T10:04:00Z', 'holark/grouped-work')
  const groupedCompanion = previewHolon('grouped-companion', 'Grouped companion', 'task_complete', '2026-08-26T10:03:00Z', 'holark/grouped-work')
  const prHead = previewHolon('pr-head', 'PR head holon', 'none', '2026-08-26T10:02:00Z', pullRequest.head_branch)
  const prWorker = previewHolon('pr-worker', 'PR worker holon', 'none', '2026-08-26T10:01:30Z', 'holark/worker/two')
  const ended = { ...previewHolon('ended', 'Ended holon', 'none', '2026-08-26T10:01:00Z', 'holark/ended-work'), status: 'failed' as const }

  render(
    <MemoryRouter>
      <OperationsPanelView
        project={{ id: 'preview', name: 'Preview', default_branch: 'main' }}
        holons={[independent, grouped, groupedCompanion, prHead, prWorker, ended]}
        capabilities={[]}
        pullRequestByHolon={new Map([[prHead.id, pullRequest], [prWorker.id, pullRequest]])}
        rename={{ begin: vi.fn(), change: vi.fn(), commit: vi.fn(), cancel: vi.fn() }}
        expanded
        onNewAgent={vi.fn()}
        onReopen={vi.fn()}
        sessionHref={() => '/'}
      />
    </MemoryRouter>,
  )

  const row = (name: RegExp) => within(screen.getByRole('link', { name }))
  expect(row(/Independent holon/).getByText('pmaviro/independent-work')).toBeInTheDocument()
  expect(row(/Grouped holon/).queryByText('holark/grouped-work')).not.toBeInTheDocument()
  expect(row(/PR head holon/).queryByText(pullRequest.head_branch)).not.toBeInTheDocument()
  expect(row(/PR worker holon/).getByText('holark/worker/two')).toBeInTheDocument()

  await user.click(screen.getByRole('button', { name: 'Show ended holons (1)' }))
  expect(row(/Ended holon/).getByText('holark/ended-work')).toBeInTheDocument()
})

it('does not show harness availability in the holons panel', () => {
  render(
    <MemoryRouter>
      <OperationsPanelView
        project={{ id: 'preview', name: 'Preview', default_branch: 'main' }}
        holons={[]}
        capabilities={[{ type: 'opencode', available: true, version: '1.18.26' }]}
        rename={{ begin: vi.fn(), change: vi.fn(), commit: vi.fn(), cancel: vi.fn() }}
        expanded
        onNewAgent={vi.fn()}
        onReopen={vi.fn()}
        sessionHref={() => '/'}
      />
    </MemoryRouter>,
  )

  expect(screen.getByRole('heading', { name: 'Holons' })).toBeInTheDocument()
  expect(screen.queryByRole('heading', { name: 'Harnesses' })).not.toBeInTheDocument()
  expect(screen.queryByText('OpenCode')).not.toBeInTheDocument()
  expect(screen.queryByText('1.18.26')).not.toBeInTheDocument()
})

it('disables reopen when a required local harness is unavailable', async () => {
  const user = userEvent.setup()
  const resumable = {
    ...previewHolon('mixed-resume', 'Mixed resume', 'none', '2026-08-26T10:04:00Z'),
    status: 'cancelled' as const,
    worktree_path: '/tmp/mixed-resume',
    archived_at: '2026-08-26T10:09:00Z',
    agent_sessions: [
      { id: 'codex-agent', agent_type: 'codex' as const, input_state: 'none' as const, activity: 'working' as const, created_at: '2026-08-26T10:04:00Z', updated_at: '2026-08-26T10:04:00Z' },
      { id: 'open-agent', agent_type: 'opencode' as const, input_state: 'none' as const, activity: 'working' as const, created_at: '2026-08-26T10:04:00Z', updated_at: '2026-08-26T10:04:00Z' },
    ],
  }

  render(
    <MemoryRouter>
      <OperationsPanelView
        project={{ id: 'preview', name: 'Preview', default_branch: 'main' }}
        holons={[resumable]}
        capabilities={[{ type: 'codex', available: true }]}
        rename={{ begin: vi.fn(), change: vi.fn(), commit: vi.fn(), cancel: vi.fn() }}
        expanded
        onNewAgent={vi.fn()}
        onReopen={vi.fn()}
        sessionHref={() => '/'}
      />
    </MemoryRouter>,
  )

  await user.click(screen.getByRole('button', { name: 'Show ended holons (1)' }))
  expect(await screen.findByRole('button', { name: 'Reopen Mixed resume' })).toBeDisabled()
})

it('keeps merged activity visible until cancellation, then retains ended history and releases addresses', async () => {
  const user = userEvent.setup()
  const completed = { ...previewHolon('completed-pr', 'Completed work', 'task_complete', '2026-08-26T10:04:00Z'), status: 'completed' as const }
  const stopping = { ...previewHolon('stopping-pr', 'Stopping work', 'none', '2026-08-26T10:04:00Z'), status: 'cancelling' as const }
  const pullRequest = { ...previewPullRequest(completed.id), status: 'merged' as const }
  const byHolon = new Map([[completed.id, pullRequest], [stopping.id, pullRequest]])
  const view = (holons: Holon[], status: PullRequest['status'] = 'merged') => (
    <MemoryRouter>
      <OperationsPanelView
        project={{ id: 'preview', name: 'Preview', default_branch: 'main' }}
        holons={holons}
        capabilities={[]}
        onReopen={vi.fn()}
        pullRequestByHolon={new Map([...byHolon].map(([id, pr]) => [id, { ...pr, status }]))}
        rename={{ begin: vi.fn(), change: vi.fn(), commit: vi.fn(), cancel: vi.fn() }}
        expanded
        onNewAgent={vi.fn()}
        sessionHref={() => '/'}
      />
    </MemoryRouter>
  )
  const { rerender } = render(view([completed, stopping]))
  expect(screen.getByRole('link', { name: /Agent A, Completed work/ })).toBeInTheDocument()
  expect(screen.getByRole('link', { name: /Stopping work/ })).toBeInTheDocument()
  rerender(view([completed, { ...stopping, status: 'cancelled' }]))
  // A merged PR remains visible even when every agent has finished its task.
  expect(screen.getByRole('link', { name: /Completed work/ })).toBeInTheDocument()
  expect(screen.getByRole('link', { name: 'Open PR #42' })).toBeInTheDocument()
  const replacement = previewHolon('replacement', 'Replacement holon', 'none', '2026-08-26T10:05:00Z')
  rerender(view([{ ...completed, status: 'cancelled' }, { ...stopping, status: 'cancelled' }, replacement]))
  expect(screen.queryByRole('link', { name: /Completed work/ })).not.toBeInTheDocument()
  expect(screen.queryByRole('link', { name: /Stopping work/ })).not.toBeInTheDocument()
  expect(screen.queryByRole('link', { name: 'Open PR #42' })).not.toBeInTheDocument()
  expect(screen.getByRole('link', { name: /Agent A, Replacement holon/ })).toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'Show ended holons (2)' }))
  expect(screen.getByRole('link', { name: /^Completed work,/ })).toBeInTheDocument()
  expect(screen.getByRole('link', { name: /^Stopping work,/ })).toBeInTheDocument()
})

it.each(['completed', 'cancelled'] as const)('keeps merged %s holons visible until auxiliary cleanup finishes', (status) => {
  const completed = { ...previewHolon('completed-pr', 'Completed work', 'task_complete', '2026-08-26T10:04:00Z'), status: 'completed' as const }
  const pending = { ...previewHolon('pending-pr', 'Pending cleanup', 'task_complete', '2026-08-26T10:05:00Z'), status }
  const pullRequest = { ...previewPullRequest(completed.id), status: 'merged' as const }
  const editor = {
    id: 'editor', holon_id: pending.id, provider: 'vscode' as const,
    state: 'ready' as const, desired_open: true,
    created_at: pending.created_at, updated_at: pending.created_at,
  }
  const view = (resources: Pick<Holon, 'manual_terminals' | 'ides'>, ended = false) => (
    <MemoryRouter>
      <OperationsPanelView
        project={{ id: 'preview', name: 'Preview', default_branch: 'main' }}
        holons={[{ ...completed, status: ended ? 'cancelled' : completed.status }, { ...pending, ...resources, status: ended ? 'cancelled' : pending.status }]}
        capabilities={[]}
        pullRequestByHolon={new Map([[completed.id, pullRequest], [pending.id, pullRequest]])}
        rename={{ begin: vi.fn(), change: vi.fn(), commit: vi.fn(), cancel: vi.fn() }}
        expanded
        onNewAgent={vi.fn()}
        onReopen={vi.fn()}
      />
    </MemoryRouter>
  )
  const { rerender } = render(view({ manual_terminals: [{
    id: 'shell', terminal_id: 'pty-shell', title: 'Shell', cwd: '/tmp/work',
    created_at: pending.created_at, updated_at: pending.created_at,
  }] }))
  expect(screen.getByRole('link', { name: /Pending cleanup/ })).toBeInTheDocument()
  expect(screen.getByRole('link', { name: /Completed work/ })).toBeInTheDocument()
  expect(screen.getAllByRole('link', { name: 'Open PR #42' }).length).toBeGreaterThan(0)

  for (const state of ['starting', 'ready', 'stopping'] as const) {
    rerender(view({ ides: [{ ...editor, state }] }))
    expect(screen.getByRole('link', { name: /Pending cleanup/ })).toBeInTheDocument()
    expect(screen.getByRole('link', { name: /Completed work/ })).toBeInTheDocument()
  }

  for (const resources of [
    { manual_terminals: [] },
    { ides: [{ ...editor, state: 'suspended' as const }] },
    { ides: [{ ...editor, state: 'closed' as const, desired_open: false }] },
  ]) {
    rerender(view(resources, true))
    expect(screen.queryByRole('link', { name: /Pending cleanup/ })).not.toBeInTheDocument()
    expect(screen.queryByRole('link', { name: /Completed work/ })).not.toBeInTheDocument()
    expect(screen.queryByRole('link', { name: 'Open PR #42' })).not.toBeInTheDocument()
  }
})

it.each(['completed', 'recovery_failed'] as const)('keeps merged %s holon history accessible without previews opening ended holons', async (status) => {
  const terminal = { ...previewHolon('terminal', 'Terminal work', 'task_complete', '2026-08-26T10:04:00Z'), status }
  const cancelled = { ...previewHolon('cancelled', 'Cancelled work', 'none', '2026-08-26T10:05:00Z'), status: 'cancelled' as const }
  const pullRequest = { ...previewPullRequest(terminal.id), status: 'merged' as const }
  const view = (ended: boolean, preview?: { holonId: string }) => (
    <MemoryRouter>
      <OperationsContext.Provider value={{ addresses: new Map(), preview, showPreview: vi.fn(), clearPreview: vi.fn() }}>
        <OperationsPanelView
          project={{ id: 'preview', name: 'Preview', default_branch: 'main' }}
          holons={[{ ...terminal, status: ended ? 'cancelled' : terminal.status }, cancelled]}
          capabilities={[]}
          pullRequestByHolon={new Map([[terminal.id, pullRequest], [cancelled.id, pullRequest]])}
          rename={{ begin: vi.fn(), change: vi.fn(), commit: vi.fn(), cancel: vi.fn() }}
          expanded
          onNewAgent={vi.fn()}
          onReopen={vi.fn()}
        />
      </OperationsContext.Provider>
    </MemoryRouter>
  )
  const { rerender } = render(view(false))
  expect(screen.getByRole('link', { name: /Terminal work/ })).toBeInTheDocument()
  expect(screen.getAllByRole('link', { name: 'Open PR #42' }).length).toBeGreaterThan(0)
  rerender(view(true))
  expect(screen.queryByRole('link', { name: /Terminal work/ })).not.toBeInTheDocument()
  expect(screen.queryByRole('link', { name: 'Open PR #42' })).not.toBeInTheDocument()

  await userEvent.click(screen.getByRole('button', { name: 'Show ended holons (2)' }))
  expect(screen.getAllByRole('link').filter((link) => !link.getAttribute('href')?.startsWith('/pulls/')).map((link) => link.getAttribute('href'))).toEqual([
    '/holons/cancelled',
    '/holons/terminal',
  ])
  await userEvent.click(screen.getByRole('button', { name: 'Hide ended holons' }))
  expect(screen.queryByRole('link', { name: /Terminal work/ })).not.toBeInTheDocument()

  rerender(view(true, { holonId: terminal.id }))
  expect(screen.getByRole('button', { name: 'Show ended holons (2)' })).toBeInTheDocument()
  expect(screen.queryByRole('link', { name: /Terminal work/ })).not.toBeInTheDocument()

  await userEvent.click(screen.getByRole('button', { name: 'Show ended holons (2)' }))
  expect(screen.getByRole('link', { name: /Terminal work/ }).closest('article')).toHaveAttribute('data-previewed', 'true')

  await userEvent.click(screen.getByRole('button', { name: 'Hide ended holons' }))
  expect(screen.queryByRole('link', { name: /Terminal work/ })).not.toBeInTheDocument()
  rerender(view(true))
  expect(screen.getByRole('button', { name: 'Show ended holons (2)' })).toBeInTheDocument()
})

function previewHolon(id: string, title: string, inputState: Holon['input_state'], createdAt: string, branch?: string): Holon {
  return {
    id,
    repository_id: 'preview',
    runtime_id: 'preview-node',
    title,
    prompt: title,
    status: 'running',
    input_state: inputState,
    activity: inputState === 'task_complete' ? 'completed' : inputState === 'none' ? 'working' : 'needs_input',
    worktree_branch: branch,
    agent_session: {
      id: `harness-${id}`,
      holon_id: id,
      agent_type: 'codex',
      input_state: inputState,
    activity: inputState === 'task_complete' ? 'completed' : inputState === 'none' ? 'working' : 'needs_input',
      created_at: createdAt,
      updated_at: createdAt,
    },
    created_at: createdAt,
  }
}

function previewPullRequest(holonId: string): PullRequest {
  return { view_revision: 1,
    id: 'pr-summary',
    title: 'Summarize holons',
    summary: '',
    base_branch: 'main',
    base_commit: 'base',
    head_branch: 'holark/pr-summary',
    head_commit: 'head',
    status: 'open',
    sync_data: { github: { number: 42 } },
    assignee_holark_ids: [],
    requested_reviewer_holark_ids: [],
    linked_session_ids: [holonId],
    created_at: '2026-08-26T10:00:00Z',
    updated_at: '2026-08-26T10:03:00Z',
  }
}
