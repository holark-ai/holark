import { act, render, screen, waitFor, within } from '@testing-library/react'
import { useEffect, type ReactNode } from 'react'
import type { Issue, WorkItemsResult } from '../../data/types'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Route, Routes, useLocation, useNavigate } from 'react-router-dom'
import { ProjectContext } from '../project/ProjectContext'
import { ProjectMemberStoreProvider } from '../members/ProjectMemberStore'
import { api } from '../../data/api'
import { PageCacheContext } from '../../data/pageCache'
import { IssueDetail, IssueList, NewIssueDialog } from './IssueViews'

vi.mock('../../data/api', () => ({
  api: {
    agentCapabilities: vi.fn(),
    issues: vi.fn(),
    searchIssues: vi.fn(),
    searchIssueLabels: vi.fn().mockResolvedValue([]),
    githubMembers: vi.fn().mockResolvedValue([]),
    issue: vi.fn(),
    issueComments: vi.fn().mockResolvedValue({ comments: [], synced_at: null, can_comment: null }),
    syncIssueComments: vi.fn().mockResolvedValue({ comments: [], synced_at: null, can_comment: null }),
    createIssue: vi.fn(),
    issueSnapshot: vi.fn(),
    promptTemplates: vi.fn(),
    prepareHolon: vi.fn(),
    createHolon: vi.fn(),
    startIssueAgent: vi.fn(),
    closeIssue: vi.fn(),
    reopenIssue: vi.fn(),
    replaceIssueAssignees: vi.fn(),
    syncIssues: vi.fn(),
    issueLabels: vi.fn(),
    createIssueLabel: vi.fn(),
    addIssueLabels: vi.fn(),
    removeIssueLabels: vi.fn(),
    projectMemberSearch: vi.fn(),
    resolveProjectMembers: vi.fn(),
  },
}))


const project = { id: 'holark', name: 'Holark', default_branch: 'main' }
const openIssue = {
  id: 'issue-1',
  repository_id: 'holark',
  title: 'Fix issue list',
  body: 'Body text',
  status: 'open' as const,
  sync_data: {},
  labels: [],
  issuer_holark_id: 'member-creator',
  assignee_holark_ids: [],
  linked_pull_request_ids: [],
  created_at: '2026-01-01T10:00:00Z',
  updated_at: '2026-01-01T11:00:00Z',
}

const creatorMember = { id: 'member-creator', login: 'creator', avatar_url: 'https://example.com/creator.png', permission: 'read', is_me: false }
const assigneeMember = { id: 'member-assignee', login: 'assignee', avatar_url: 'https://example.com/assignee.png', permission: 'write', is_me: false }

const capabilities = [{ type: 'codex' as const, available: true, version: '1.0' }]

const issuePromptTemplate = {
  key: 'issue_agent_plan',
  name: 'Issue agent plan prompt',
  use: 'Used when starting an agent from an issue.',
  variables: [
    { name: 'issue_title', description: 'Issue title.' },
    { name: 'issue_body', description: 'Issue body.' },
  ],
  value: 'Plan {{issue_title}}\n\nContext: {{issue_body}}',
  default_value: 'Default {{issue_title}}\n\n{{issue_body}}',
  overridden: true,
}

beforeEach(() => {
  vi.mocked(api.agentCapabilities).mockResolvedValue([])
  vi.mocked(api.searchIssues).mockReset()
  vi.mocked(api.searchIssueLabels).mockReset().mockResolvedValue([])
  vi.mocked(api.githubMembers).mockReset().mockResolvedValue([])
  vi.mocked(api.issue).mockReset()
  vi.mocked(api.createIssue).mockReset()
  vi.mocked(api.issueSnapshot).mockReset()
  vi.mocked(api.promptTemplates).mockReset()
  vi.mocked(api.prepareHolon).mockReset()
  vi.mocked(api.createHolon).mockReset()
  vi.mocked(api.startIssueAgent).mockReset()
  vi.mocked(api.closeIssue).mockReset()
  vi.mocked(api.resolveProjectMembers).mockReset()
  vi.mocked(api.reopenIssue).mockReset()
  vi.mocked(api.replaceIssueAssignees).mockReset()
  vi.mocked(api.syncIssues).mockReset()
  vi.mocked(api.syncIssues).mockResolvedValue({ issues: [], imported: 0, updated: 0, exported: 0, synced_at: '2026-01-01T12:00:00Z' })
  vi.mocked(api.issueLabels).mockReset()
  vi.mocked(api.createIssueLabel).mockReset()
  vi.mocked(api.addIssueLabels).mockReset()
  vi.mocked(api.removeIssueLabels).mockReset()
  vi.mocked(api.projectMemberSearch).mockReset()
  vi.mocked(api.resolveProjectMembers).mockResolvedValue({ members: [], missing_ids: [] })
  vi.mocked(api.projectMemberSearch).mockResolvedValue({ members: [] })
  vi.mocked(api.promptTemplates).mockResolvedValue([issuePromptTemplate])
  vi.mocked(api.prepareHolon).mockResolvedValue({ id: 'base-commit', commit: 'base-commit', ref: 'main' })
  vi.mocked(api.issueSnapshot).mockResolvedValue({
    issue_id: openIssue.id, title: openIssue.title, body: openIssue.body,
  })
})

const bugLabel = { id: 'label-bug', name: 'bug', color: 'd73a4a', description: 'Something is broken' }
const docsLabel = { id: 'label-docs', name: 'documentation', color: '0075ca', description: 'Documentation improvements' }
const githubIssue = {
  ...openIssue,
  sync_provider: 'github',
  sync_external_id: 'github:pmaviro/holark#42',
  sync_data: { github: { number: 42, url: 'https://github.com/pmaviro/holark/issues/42' } },
}

function renderWithProject(route: string, element: ReactNode) {
  return render(
    <MemoryRouter initialEntries={[route]}>
      <ProjectContext.Provider value={project}>
        <ProjectMemberStoreProvider projectId={project.id}>
          {element}
        </ProjectMemberStoreProvider>
      </ProjectContext.Provider>
    </MemoryRouter>,
  )
}

function LocationProbe() {
  const location = useLocation()
  return <span data-testid="location">{location.pathname}</span>
}

function projectionPendingError() {
  return Object.assign(new Error('GitHub accepted the change, but Holark has not stored it yet.'), {
    code: 'issue_projection_pending', status: 202,
  })
}

function labelProjectionPendingError() {
  return Object.assign(new Error('GitHub created the label, but Holark has not stored it yet.'), {
    code: 'label_projection_pending', status: 202,
  })
}


function issueSearchResult(items: Issue[]): WorkItemsResult {
  return { rows: items.map((issue) => ({ id: issue.id, kind: 'issue', title: issue.title, number: issue.sync_data.github?.number ?? 0, status: issue.status,
    author_id: issue.issuer_holark_id, assignee_ids: issue.assignee_holark_ids, reviewer_ids: [], labels: issue.labels,
    linked_pull_request_ids: issue.linked_pull_request_ids, url: issue.sync_data.github?.url, updated_at: issue.updated_at, reasons: [],
  })), total: items.length, page: 1, per_page: 50, identity: null, sync: [] }
}

describe('IssueList', () => {
  it('keeps the newer cached list when an earlier visit finishes syncing after unmount', async () => {
    const user = userEvent.setup()
    const snapshots = new Map<string, unknown>()
    const renderList = () => renderWithProject('/issues', (
      <PageCacheContext.Provider value={snapshots}>
        <IssueList />
      </PageCacheContext.Provider>
    ))
    let finishSync!: (value: Awaited<ReturnType<typeof api.syncIssues>>) => void
    vi.mocked(api.syncIssues).mockReturnValueOnce(new Promise((resolve) => { finishSync = resolve }))
    vi.mocked(api.searchIssues).mockResolvedValue(issueSearchResult([openIssue]))
    const firstVisit = renderList()
    expect(await screen.findByText(openIssue.title)).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Sync' }))
    await waitFor(() => expect(api.syncIssues).toHaveBeenCalledTimes(1))
    firstVisit.unmount()

    const newerIssue = { ...openIssue, title: 'Updated issue title' }
    vi.mocked(api.searchIssues).mockResolvedValue(issueSearchResult([newerIssue]))
    const secondVisit = renderList()
    expect(await screen.findByText(newerIssue.title)).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Sync' }))
    expect(await screen.findByText('Synced 0 imported, 0 updated, 0 exported.')).toBeInTheDocument()

    // The first visit's delayed sync must not start a refresh with older data.
    vi.mocked(api.searchIssues).mockResolvedValue(issueSearchResult([openIssue]))
    const requestsBeforeSync = vi.mocked(api.searchIssues).mock.calls.length
    await act(async () => {
      finishSync({ issues: [], imported: 0, updated: 0, exported: 0, synced_at: '2026-01-01T12:00:00Z' })
    })
    expect(api.searchIssues).toHaveBeenCalledTimes(requestsBeforeSync)
    secondVisit.unmount()

    vi.mocked(api.searchIssues).mockReturnValue(new Promise(() => {}))
    renderList()
    expect(screen.getByText(newerIssue.title)).toBeInTheDocument()
  })

  it('renders loading and empty states', async () => {
    vi.mocked(api.searchIssues).mockResolvedValue(issueSearchResult([]))

    renderWithProject('/issues', <IssueList />)

    expect(screen.getByText('Loading issues...')).toBeInTheDocument()
    expect(await screen.findByText('No issues match this search.')).toBeInTheDocument()
  })

  it('renders populated issues', async () => {
    vi.mocked(api.searchIssues).mockResolvedValue(issueSearchResult([{ ...openIssue, linked_pull_request_ids: ['pr-1'] }]))

    renderWithProject('/issues', <IssueList />)

    expect(await screen.findByRole('link', { name: /Fix issue list/ })).toHaveAttribute('href', '/issues/issue-1?returnTo=%2Fissues')
    expect(screen.getByText(/1 PRs/)).toBeInTheDocument()

  })
  it('shows participant avatars with name and role tooltips', async () => {
    vi.mocked(api.searchIssues).mockResolvedValue(issueSearchResult([{ ...openIssue, assignee_holark_ids: [assigneeMember.id] }]))
    vi.mocked(api.resolveProjectMembers).mockResolvedValue({ members: [creatorMember, assigneeMember], missing_ids: [] })
    renderWithProject('/issues', <IssueList />)
    expect(await screen.findByText('creator · Author')).toHaveAttribute('role', 'tooltip')
    expect(await screen.findByText('assignee · Assignee')).toHaveAttribute('role', 'tooltip')
    expect(screen.getByLabelText('creator').querySelector('img')).toHaveAttribute('src', creatorMember.avatar_url)
  })

  it('uses issue states, server ordering, people and sort filters', async () => {
    const user = userEvent.setup()
    vi.mocked(api.searchIssues).mockResolvedValue(issueSearchResult([openIssue]))
    renderWithProject('/issues', <IssueList />)
    await screen.findByText(openIssue.title)
    expect(api.searchIssues).toHaveBeenCalledWith('is:open sort:created-desc', 1, expect.any(AbortSignal), '')
    expect(screen.queryByRole('combobox', { name: 'Reviewer' })).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'State' }))
    expect(screen.getAllByRole('checkbox')).toHaveLength(2)
    expect(screen.getByRole('checkbox', { name: 'Open' })).toBeChecked()
    await user.click(screen.getByRole('checkbox', { name: 'Closed' }))
    await waitFor(() => expect(api.searchIssues).toHaveBeenLastCalledWith('sort:created-desc is:all', 1, expect.any(AbortSignal), ''))
    await user.keyboard('{Escape}')
    await user.click(screen.getByRole('combobox', { name: 'Assignee' }))
    await user.click(screen.getByRole('option', { name: 'Me' }))
    await waitFor(() => expect(api.searchIssues).toHaveBeenLastCalledWith('sort:created-desc is:all assignee:@me', 1, expect.any(AbortSignal), ''))
    vi.mocked(api.searchIssues).mockResolvedValue(issueSearchResult([{ ...openIssue, id: 'closed', title: 'Closed first', status: 'closed' }, openIssue]))
    await user.click(screen.getByRole('combobox', { name: 'Sort' }))
    await user.click(screen.getByRole('option', { name: 'Least recently updated' }))
    const closed = await screen.findByText('Closed first')
    expect(closed.compareDocumentPosition(screen.getByText(openIssue.title)) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(api.searchIssues).toHaveBeenLastCalledWith('is:all assignee:@me sort:updated-asc', 1, expect.any(AbortSignal), '')
  })

  it('syncs issues and renders GitHub links', async () => {
    const syncedIssue = {
      ...openIssue,
      sync_provider: 'github',
      sync_external_id: 'github:pmaviro/holark#42',
      sync_data: { github: { number: 42, url: 'https://github.com/pmaviro/holark/issues/42' } },
    }
    vi.mocked(api.searchIssues).mockResolvedValue(issueSearchResult([syncedIssue]))
    vi.mocked(api.syncIssues).mockResolvedValue({
      issues: [syncedIssue],
      imported: 1,
      updated: 2,
      exported: 3,
      synced_at: '2026-01-01T12:00:00Z',
    })

    renderWithProject('/issues', <IssueList />)

    expect(await screen.findByText('Fix issue list')).toBeInTheDocument()

    expect(api.syncIssues).not.toHaveBeenCalled()
    await userEvent.setup().click(screen.getByRole('button', { name: 'Sync' }))
    await waitFor(() => expect(api.syncIssues).toHaveBeenCalledWith('holark'))
    expect(await screen.findByText('Synced 1 imported, 2 updated, 3 exported.')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'GitHub #42' })).toHaveAttribute('href', 'https://github.com/pmaviro/holark/issues/42')
    expect(api.searchIssues).toHaveBeenCalledTimes(2)
    const user = userEvent.setup()
    const message = screen.getByText('Synced 1 imported, 2 updated, 3 exported.')
    expect(message.parentElement?.parentElement).toContainElement(screen.getByRole('button', { name: 'Sync' }))
    await user.click(screen.getByRole('button', { name: 'Dismiss sync result' }))

    let rejectSync!: (error: Error) => void
    vi.mocked(api.syncIssues).mockImplementationOnce(() => new Promise((_, reject) => { rejectSync = reject }))
    await user.click(screen.getByRole('button', { name: 'Sync' }))
    expect(screen.getByRole('button', { name: 'Syncing with GitHub…' })).toBeDisabled()
    await act(async () => rejectSync(new Error('GitHub unavailable')))
    const warning = await screen.findByRole('alert')
    expect(warning).toHaveTextContent('GitHub unavailable')
    expect(warning.parentElement).toContainElement(screen.getByRole('button', { name: 'Sync' }))
    await user.click(screen.getByRole('button', { name: 'Dismiss sync result' }))
    expect(screen.getByRole('button', { name: 'Sync' })).toBeEnabled()
  })

  it('starts an agent from the list without opening the issue detail page', async () => {
    const user = userEvent.setup()
    vi.mocked(api.searchIssues).mockResolvedValue(issueSearchResult([openIssue]))
    vi.mocked(api.agentCapabilities).mockResolvedValue(capabilities)
    vi.mocked(api.startIssueAgent).mockResolvedValue({
      id: 'holon-1',
      repository_id: 'holark',
      runtime_id: 'node-1',
      prompt: 'Fix issue list',
      status: 'queued',
      input_state: 'none',
      created_at: new Date().toISOString(),
    })

    render(
      <MemoryRouter initialEntries={['/issues']}>
        <ProjectContext.Provider value={project}>
          <ProjectMemberStoreProvider projectId={project.id}>
            <Routes>
              <Route path="/issues" element={<><IssueList /><LocationProbe /></>} />
              <Route path="/holons/:holonId" element={<LocationProbe />} />
            </Routes>
          </ProjectMemberStoreProvider>
        </ProjectContext.Provider>
      </MemoryRouter>,
    )

    const issueCard = await screen.findByRole('link', { name: /Fix issue list/ })
    expect(issueCard).toHaveAttribute('href', '/issues/issue-1?returnTo=%2Fissues')
    await user.click(screen.getByRole('button', { name: 'Start agent' }))

    await waitFor(() => expect(api.startIssueAgent).toHaveBeenCalledWith('issue-1'))
    expect(await screen.findByTestId('location')).toHaveTextContent('/holons/holon-1')
  })

  it('creates an issue and navigates to detail', async () => {
    const user = userEvent.setup()
    vi.mocked(api.searchIssues).mockResolvedValue(issueSearchResult([]))
    vi.mocked(api.createIssue).mockResolvedValue(openIssue)

    render(
      <MemoryRouter initialEntries={['/issues']}>
        <ProjectContext.Provider value={project}>
          <Routes>
            <Route path="/issues" element={<><IssueList /><LocationProbe /></>} />
            <Route path="/issues/:issueId" element={<LocationProbe />} />
          </Routes>
        </ProjectContext.Provider>
      </MemoryRouter>,
    )

    await user.click(await screen.findByRole('button', { name: 'New issue' }))
    await user.type(screen.getByLabelText('Title'), 'Fix issue list')
    await user.type(screen.getByLabelText('Body'), 'Body text')
    await user.click(screen.getByRole('button', { name: 'Create' }))

    await waitFor(() => expect(api.createIssue).toHaveBeenCalledWith('holark', 'Fix issue list', 'Body text'))
    expect(await screen.findByTestId('location')).toHaveTextContent('/issues/issue-1')
  })

  it('keeps the list route when issue creation is accepted but projection is pending', async () => {
    const user = userEvent.setup()
    vi.mocked(api.searchIssues).mockResolvedValue(issueSearchResult([]))
    vi.mocked(api.createIssue).mockRejectedValue(projectionPendingError())

    render(
      <MemoryRouter initialEntries={['/issues']}>
        <ProjectContext.Provider value={project}>
          <Routes>
            <Route path="/issues" element={<><IssueList /><LocationProbe /></>} />
            <Route path="/issues/:issueId" element={<LocationProbe />} />
          </Routes>
        </ProjectContext.Provider>
      </MemoryRouter>,
    )

    await user.click(await screen.findByRole('button', { name: 'New issue' }))
    await user.type(screen.getByLabelText('Title'), 'Fix issue list')
    await user.type(screen.getByLabelText('Body'), 'Body text')
    await user.click(screen.getByRole('button', { name: 'Create' }))

    await waitFor(() => expect(api.createIssue).toHaveBeenCalledWith('holark', 'Fix issue list', 'Body text'))
    expect(await screen.findByText('GitHub accepted the change, but Holark has not stored it yet.')).toBeInTheDocument()
    expect(screen.getByTestId('location')).toHaveTextContent('/issues')
  })
})

it('keeps issue body across harmless parent rerenders', async () => {
  const user = userEvent.setup()
  const onCreate = vi.fn(async () => undefined)
  const view = render(
    <MemoryRouter>
      <NewIssueDialog open onClose={() => undefined} onCreate={onCreate} />
    </MemoryRouter>,
  )

  const bodyInput = screen.getByLabelText('Body')
  await user.click(bodyInput)
  await user.type(bodyInput, 'Detailed reproduction')

  view.rerender(
    <MemoryRouter>
      <NewIssueDialog open onClose={() => undefined} onCreate={onCreate} />
    </MemoryRouter>,
  )

  expect(screen.getByLabelText('Body')).toHaveFocus()
  expect(screen.getByLabelText('Body')).toHaveValue('Detailed reproduction')
})

it('renders detail state and calls close and reopen actions', async () => {
  const user = userEvent.setup()
  const closedIssue = { ...openIssue, status: 'closed' as const, closed_at: '2026-01-01T12:00:00Z' }
  vi.mocked(api.issue).mockResolvedValue({
    ...openIssue,
    sync_provider: 'github',
    sync_external_id: 'github:pmaviro/holark#42',
    sync_data: { github: { number: 42, url: 'https://github.com/pmaviro/holark/issues/42' } },
  })
  vi.mocked(api.closeIssue).mockResolvedValue(closedIssue)
  vi.mocked(api.reopenIssue).mockResolvedValue(openIssue)

  renderWithProject('/issues/issue-1', (
    <Routes>
      <Route path="/issues/:issueId" element={<IssueDetail />} />
    </Routes>
  ))

  expect(await screen.findByRole('heading', { name: 'Fix issue list' })).toBeInTheDocument()
  expect(screen.getByText('Body text')).toBeInTheDocument()
  expect(screen.getByRole('link', { name: 'GitHub #42' })).toHaveAttribute('href', 'https://github.com/pmaviro/holark/issues/42')

  await user.click(screen.getByRole('button', { name: 'Close issue' }))
  await waitFor(() => expect(api.closeIssue).toHaveBeenCalledWith('issue-1'))
  expect(await screen.findByRole('button', { name: 'Reopen issue' })).toBeInTheDocument()

  await user.click(screen.getByRole('button', { name: 'Reopen issue' }))
  await waitFor(() => expect(api.reopenIssue).toHaveBeenCalledWith('issue-1'))
  expect(await screen.findByRole('button', { name: 'Close issue' })).toBeInTheDocument()
})

it('renders creator and assignees with the same avatar-and-login identity in issue detail', async () => {
  vi.mocked(api.issue).mockResolvedValue({ ...openIssue, assignee_holark_ids: [assigneeMember.id] })
  vi.mocked(api.resolveProjectMembers).mockResolvedValue({ members: [creatorMember, assigneeMember], missing_ids: [] })

  const { container } = renderWithProject('/issues/issue-1', (
    <Routes>
      <Route path="/issues/:issueId" element={<IssueDetail />} />
    </Routes>
  ))

  expect(await screen.findByText('creator')).toBeInTheDocument()
  expect(await screen.findByText('assignee')).toBeInTheDocument()
  expect(container.querySelector('img[src="https://example.com/creator.png"]')).toBeInTheDocument()
  expect(container.querySelector('img[src="https://example.com/assignee.png"]')).toBeInTheDocument()
})

it('keeps issue detail state when close is accepted but projection is pending', async () => {
  const user = userEvent.setup()
  vi.mocked(api.issue).mockResolvedValue(openIssue)
  vi.mocked(api.closeIssue).mockRejectedValue(projectionPendingError())

  renderWithProject('/issues/issue-1', (
    <Routes>
      <Route path="/issues/:issueId" element={<IssueDetail />} />
    </Routes>
  ))

  await user.click(await screen.findByRole('button', { name: 'Close issue' }))
  await waitFor(() => expect(api.closeIssue).toHaveBeenCalledWith('issue-1'))
  expect(await screen.findByText('GitHub accepted the change, but Holark has not stored it yet.')).toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Close issue' })).toBeInTheDocument()
  expect(screen.queryByRole('button', { name: 'Reopen issue' })).not.toBeInTheDocument()
  expect(screen.getByRole('heading', { name: 'Fix issue list' })).toBeInTheDocument()
})

it('keeps issue detail state when reopen is accepted but projection is pending', async () => {
  const user = userEvent.setup()
  const closedIssue = { ...openIssue, status: 'closed' as const, closed_at: '2026-01-01T12:00:00Z' }
  vi.mocked(api.issue).mockResolvedValue(closedIssue)
  vi.mocked(api.reopenIssue).mockRejectedValue(projectionPendingError())

  renderWithProject('/issues/issue-1', (
    <Routes>
      <Route path="/issues/:issueId" element={<IssueDetail />} />
    </Routes>
  ))

  await user.click(await screen.findByRole('button', { name: 'Reopen issue' }))
  await waitFor(() => expect(api.reopenIssue).toHaveBeenCalledWith('issue-1'))
  expect(await screen.findByText('GitHub accepted the change, but Holark has not stored it yet.')).toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Reopen issue' })).toBeInTheDocument()
  expect(screen.queryByRole('button', { name: 'Close issue' })).not.toBeInTheDocument()
  expect(screen.getByRole('heading', { name: 'Fix issue list' })).toBeInTheDocument()
})

it('starts an agent from issue detail through the durable issue workflow', async () => {
  const user = userEvent.setup()
  vi.mocked(api.issue).mockResolvedValue(openIssue)
  vi.mocked(api.agentCapabilities).mockResolvedValue(capabilities)
  vi.mocked(api.startIssueAgent).mockResolvedValue({
    id: 'holon-1',
    repository_id: 'holark',
    runtime_id: 'node-1',
    prompt: 'Fix issue list',
    status: 'queued',
    input_state: 'none',
    created_at: new Date().toISOString(),
  })

  render(
    <MemoryRouter initialEntries={['/issues/issue-1']}>
      <ProjectContext.Provider value={project}>
        <ProjectMemberStoreProvider projectId={project.id}>
          <Routes>
            <Route path="/issues/:issueId" element={<><IssueDetail /><LocationProbe /></>} />
            <Route path="/holons/:holonId" element={<LocationProbe />} />
          </Routes>
        </ProjectMemberStoreProvider>
      </ProjectContext.Provider>
    </MemoryRouter>,
  )

  await user.click(await screen.findByRole('button', { name: 'Start agent' }))
  expect(screen.queryByRole('dialog', { name: 'Start agent from issue' })).not.toBeInTheDocument()

  await waitFor(() => expect(api.startIssueAgent).toHaveBeenCalledWith('issue-1'))
  expect(await screen.findByTestId('location')).toHaveTextContent('/holons/holon-1')
})

it('delegates issue prompt rendering to the durable issue workflow', async () => {
  const user = userEvent.setup()
  vi.mocked(api.issue).mockResolvedValue({ ...openIssue, body: '' })
  vi.mocked(api.agentCapabilities).mockResolvedValue(capabilities)
  vi.mocked(api.startIssueAgent).mockResolvedValue({
    id: 'holon-1', repository_id: 'holark', runtime_id: 'node-1', prompt: '',
    status: 'queued', input_state: 'none', created_at: new Date().toISOString(),
  })

  renderWithProject('/issues/issue-1', (
    <Routes><Route path="/issues/:issueId" element={<IssueDetail />} /></Routes>
  ))
  await user.click(await screen.findByRole('button', { name: 'Start agent' }))

  await waitFor(() => expect(api.startIssueAgent).toHaveBeenCalledWith('issue-1'))
})

it('does not navigate when the durable issue workflow fails', async () => {
  const user = userEvent.setup()
  vi.mocked(api.issue).mockResolvedValue(openIssue)
  vi.mocked(api.agentCapabilities).mockResolvedValue(capabilities)
  vi.mocked(api.startIssueAgent).mockRejectedValue(new Error('Start failed.'))

  renderWithProject('/issues/issue-1', (
    <Routes><Route path="/issues/:issueId" element={<IssueDetail />} /></Routes>
  ))
  await user.click(await screen.findByRole('button', { name: 'Start agent' }))

  expect(await screen.findByText(/failed/i)).toBeInTheDocument()
  expect(api.startIssueAgent).toHaveBeenCalledWith('issue-1')
})

it('does not render an issue Sessions section', async () => {
  vi.mocked(api.issue).mockResolvedValue(openIssue)
  renderWithProject('/issues/issue-1', (
    <Routes><Route path="/issues/:issueId" element={<IssueDetail />} /></Routes>
  ))

  expect(await screen.findByRole('heading', { name: 'Fix issue list' })).toBeInTheDocument()
  expect(screen.queryByRole('heading', { name: 'Sessions' })).not.toBeInTheDocument()
})

function renderIssueDetail() {
  return renderWithProject('/issues/issue-1', (
    <Routes><Route path="/issues/:issueId" element={<IssueDetail />} /></Routes>
  ))
}

describe('issue assignee management', () => {
  const alice = { id: 'member-alice', login: 'alice', avatar_url: 'https://example.com/alice.png', permission: 'write', is_me: false }
  const bob = { id: 'member-bob', login: 'bob', avatar_url: 'https://example.com/bob.png', permission: 'write', is_me: false }

  function stubMembers(...members: typeof alice[]) {
    vi.mocked(api.resolveProjectMembers).mockImplementation(async (_projectId, ids) => ({
      members: members.filter((member) => ids.includes(member.id)),
      missing_ids: ids.filter((id) => !members.some((member) => member.id === id)),
    }))
    vi.mocked(api.projectMemberSearch).mockImplementation(async (_projectId, query) => ({
      members: members.filter((member) => member.login.includes(query.toLowerCase())),
    }))
  }

  it.each([
    ['open GitHub issue', githubIssue, true],
    ['closed GitHub issue', { ...githubIssue, status: 'closed' as const }, false],
    ['local issue', openIssue, false],
    ['other-provider issue', { ...openIssue, sync_provider: 'gitlab' }, false],
  ])('exposes editing only for an %s', async (_name, issue, editable) => {
    vi.mocked(api.issue).mockResolvedValue(issue)
    renderIssueDetail()

    await screen.findByRole('heading', { name: 'Assignees' })
    const edit = screen.queryByRole('button', { name: 'Edit assignees' })
    if (editable) expect(edit).toBeInTheDocument()
    else expect(edit).not.toBeInTheDocument()
  })

  it('searches and edits a draft, saves once optimistically, and adopts the server issue', async () => {
    const user = userEvent.setup()
    const initialIssue = { ...githubIssue, assignee_holark_ids: [alice.id] }
    const canonicalIssue = { ...githubIssue, assignee_holark_ids: [bob.id, alice.id], updated_at: '2026-01-02T12:00:00Z' }
    let resolveMutation: (issue: typeof canonicalIssue) => void = () => undefined
    vi.mocked(api.issue).mockResolvedValue(initialIssue)
    stubMembers(alice, bob)
    vi.mocked(api.replaceIssueAssignees).mockReturnValue(new Promise((resolve) => { resolveMutation = resolve }))
    renderIssueDetail()

    const trigger = await screen.findByRole('button', { name: 'Edit assignees' })
    const section = screen.getByRole('heading', { name: 'Assignees' }).closest('section') as HTMLElement
    const displayedMembers = section.children[1] as HTMLElement
    expect(await within(section).findByText('alice')).toBeInTheDocument()
    await user.click(trigger)

    const search = screen.getByRole('searchbox', { name: 'Search project members' })
    expect(search).toHaveFocus()
    expect(screen.getByLabelText('Selected members')).toHaveTextContent('alice')
    await user.type(search, 'bob')
    await user.click(await screen.findByRole('option', { name: /bob/i }))
    expect(within(displayedMembers).queryByText('bob')).not.toBeInTheDocument()
    await user.clear(search)
    await user.click(await screen.findByRole('option', { name: /alice/i }))
    await user.click(screen.getByRole('button', { name: 'Done' }))

    expect(api.replaceIssueAssignees).toHaveBeenCalledTimes(1)
    expect(api.replaceIssueAssignees).toHaveBeenCalledWith('issue-1', [bob.id])
    expect(within(section).getByText('bob')).toBeInTheDocument()
    expect(within(section).queryByText('alice')).not.toBeInTheDocument()
    expect(trigger).toHaveAttribute('aria-disabled', 'true')
    expect(trigger).toHaveFocus()
    await user.click(trigger)
    expect(screen.queryByRole('searchbox', { name: 'Search project members' })).not.toBeInTheDocument()

    resolveMutation(canonicalIssue)
    await waitFor(() => expect(trigger).not.toHaveAttribute('aria-disabled'))
    expect(within(section).getByText('bob')).toBeInTheDocument()
    expect(within(section).getByText('alice')).toBeInTheDocument()
  })

  it('skips unchanged saves and discards changed drafts on Escape', async () => {
    const user = userEvent.setup()
    vi.mocked(api.issue).mockResolvedValue({ ...githubIssue, assignee_holark_ids: [alice.id] })
    stubMembers(alice)
    renderIssueDetail()

    const trigger = await screen.findByRole('button', { name: 'Edit assignees' })
    await user.click(trigger)
    await user.click(screen.getByRole('button', { name: 'Done' }))
    expect(api.replaceIssueAssignees).not.toHaveBeenCalled()
    expect(trigger).toHaveFocus()

    await user.click(trigger)
    await user.click(await screen.findByRole('option', { name: /alice/i }))
    await user.keyboard('{Escape}')
    expect(api.replaceIssueAssignees).not.toHaveBeenCalled()
    expect(trigger).toHaveFocus()
    expect(await screen.findByText('alice')).toBeInTheDocument()
  })

  it('saves a changed draft on outside click', async () => {
    const user = userEvent.setup()
    const changedIssue = { ...githubIssue, assignee_holark_ids: [alice.id] }
    vi.mocked(api.issue).mockResolvedValue(githubIssue)
    vi.mocked(api.replaceIssueAssignees).mockResolvedValue(changedIssue)
    stubMembers(alice)
    renderIssueDetail()

    await user.click(await screen.findByRole('button', { name: 'Edit assignees' }))
    await user.click(await screen.findByRole('option', { name: /alice/i }))
    await user.click(document.body)

    await waitFor(() => expect(api.replaceIssueAssignees).toHaveBeenCalledWith('issue-1', [alice.id]))
    expect(screen.queryByRole('searchbox', { name: 'Search project members' })).not.toBeInTheDocument()
    expect(await screen.findByText('alice')).toBeInTheDocument()
  })

  it('locks while saving and rolls back an ordinary failure with an accessible alert', async () => {
    const user = userEvent.setup()
    let rejectMutation: (error: Error) => void = () => undefined
    vi.mocked(api.issue).mockResolvedValue({ ...githubIssue, assignee_holark_ids: [alice.id] })
    stubMembers(alice, bob)
    vi.mocked(api.replaceIssueAssignees).mockReturnValue(new Promise((_resolve, reject) => { rejectMutation = reject }))
    renderIssueDetail()

    const trigger = await screen.findByRole('button', { name: 'Edit assignees' })
    await user.click(trigger)
    await user.click(await screen.findByRole('option', { name: /bob/i }))
    await user.click(screen.getByRole('button', { name: 'Done' }))
    expect(trigger).toHaveAttribute('aria-disabled', 'true')
    expect(await screen.findByText('bob')).toBeInTheDocument()

    rejectMutation(new Error('GitHub rejected the assignees.'))
    expect(await screen.findByRole('alert')).toHaveTextContent('Couldn’t update assignees. GitHub’s selection was restored.')
    await waitFor(() => expect(trigger).not.toHaveAttribute('aria-disabled'))
    expect(screen.getByText('alice')).toBeInTheDocument()
    expect(screen.queryByText('bob')).not.toBeInTheDocument()
  })

  it('reconciles projection-pending changes through issue sync', async () => {
    const user = userEvent.setup()
    const reconciledIssue = { ...githubIssue, assignee_holark_ids: [bob.id] }
    vi.mocked(api.issue).mockResolvedValue({ ...githubIssue, assignee_holark_ids: [alice.id] })
    stubMembers(alice, bob)
    vi.mocked(api.replaceIssueAssignees).mockRejectedValue(projectionPendingError())
    vi.mocked(api.syncIssues).mockResolvedValue({ issues: [reconciledIssue], imported: 0, updated: 1, exported: 0, synced_at: '2026-01-02T12:00:00Z' })
    renderIssueDetail()

    await user.click(await screen.findByRole('button', { name: 'Edit assignees' }))
    await user.click(await screen.findByRole('option', { name: /bob/i }))
    await user.click(screen.getByRole('button', { name: 'Done' }))

    await waitFor(() => expect(api.syncIssues).toHaveBeenCalledWith('holark'))
    expect(await screen.findByText('bob')).toBeInTheDocument()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })

  it('rolls back and explains the uncertain GitHub state when reconciliation fails', async () => {
    const user = userEvent.setup()
    vi.mocked(api.issue).mockResolvedValue({ ...githubIssue, assignee_holark_ids: [alice.id] })
    stubMembers(alice, bob)
    vi.mocked(api.replaceIssueAssignees).mockRejectedValue(projectionPendingError())
    vi.mocked(api.syncIssues).mockRejectedValue(new Error('Sync failed.'))
    renderIssueDetail()

    await user.click(await screen.findByRole('button', { name: 'Edit assignees' }))
    await user.click(await screen.findByRole('option', { name: /bob/i }))
    await user.click(screen.getByRole('button', { name: 'Done' }))

    expect(await screen.findByRole('alert')).toHaveTextContent('GitHub accepted the assignee change, but Holark must be refreshed.')
    expect(screen.getByText('alice')).toBeInTheDocument()
    expect(screen.queryByText('bob')).not.toBeInTheDocument()
  })
})

describe('issue label management', () => {
  it('always renders labels and only offers management for GitHub-backed issues', async () => {
    vi.mocked(api.issue).mockResolvedValue(openIssue)
    const view = renderIssueDetail()

    expect(await screen.findByRole('heading', { name: 'Labels' })).toBeInTheDocument()
    expect(screen.getByText('No labels assigned.')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Manage labels' })).not.toBeInTheDocument()

    vi.mocked(api.issue).mockResolvedValue(githubIssue)
    view.unmount()
    renderIssueDetail()
    expect(await screen.findByRole('button', { name: 'Manage labels' })).toBeInTheDocument()
  })

  it('loads lazily, searches names and descriptions, and restores focus after dismissal', async () => {
    const user = userEvent.setup()
    vi.mocked(api.issue).mockResolvedValue(githubIssue)
    vi.mocked(api.issueLabels).mockResolvedValue([bugLabel, docsLabel])
    renderIssueDetail()

    const trigger = await screen.findByRole('button', { name: 'Manage labels' })
    expect(api.issueLabels).not.toHaveBeenCalled()
    await user.click(trigger)

    expect(api.issueLabels).toHaveBeenCalledWith('holark')
    const search = screen.getByRole('searchbox', { name: 'Search labels' })
    expect(search).toHaveFocus()
    expect(await screen.findByRole('checkbox', { name: /bug/i })).toBeInTheDocument()

    await user.type(search, 'improvements')
    expect(screen.getByRole('checkbox', { name: /documentation/i })).toBeInTheDocument()
    expect(screen.queryByRole('checkbox', { name: /bug/i })).not.toBeInTheDocument()
    await user.clear(search)
    await user.type(search, 'missing')
    expect(screen.getByText('No matching labels.')).toBeInTheDocument()

    await user.keyboard('{Escape}')
    expect(screen.queryByRole('searchbox', { name: 'Search labels' })).not.toBeInTheDocument()
    expect(trigger).toHaveFocus()
  })

  it('shows empty catalog and transport-error states and dismisses on outside click', async () => {
    const user = userEvent.setup()
    vi.mocked(api.issue).mockResolvedValue(githubIssue)
    vi.mocked(api.issueLabels).mockResolvedValueOnce([]).mockRejectedValueOnce(new Error('Labels unavailable.'))
    renderIssueDetail()

    const trigger = await screen.findByRole('button', { name: 'Manage labels' })
    await user.click(trigger)
    expect(await screen.findByText('No repository labels yet.')).toBeInTheDocument()
    await user.click(document.body)
    expect(screen.queryByText('No repository labels yet.')).not.toBeInTheDocument()
    expect(trigger).toHaveFocus()

    await user.click(trigger)
    expect(await screen.findByRole('alert')).toHaveTextContent('Labels unavailable.')
  })

  it('reflects assignments and immediately replaces the issue after add and remove', async () => {
    const user = userEvent.setup()
    vi.mocked(api.issue).mockResolvedValue({ ...githubIssue, labels: [bugLabel] })
    vi.mocked(api.issueLabels).mockResolvedValue([bugLabel, docsLabel])
    vi.mocked(api.addIssueLabels).mockResolvedValue({ ...githubIssue, labels: [bugLabel, docsLabel] })
    vi.mocked(api.removeIssueLabels).mockResolvedValue({ ...githubIssue, labels: [docsLabel] })
    renderIssueDetail()

    await user.click(await screen.findByRole('button', { name: 'Manage labels' }))
    const bug = await screen.findByRole('checkbox', { name: /bug/i })
    const docs = screen.getByRole('checkbox', { name: /documentation/i })
    expect(bug).toBeChecked()
    expect(docs).not.toBeChecked()

    await user.click(docs)
    await waitFor(() => expect(api.addIssueLabels).toHaveBeenCalledWith('issue-1', ['documentation']))
    expect(docs).toBeChecked()
    expect(screen.getByLabelText('Labels')).toHaveTextContent('documentation')

    await user.click(bug)
    await waitFor(() => expect(api.removeIssueLabels).toHaveBeenCalledWith('issue-1', ['bug']))
    expect(bug).not.toBeChecked()
    expect(screen.getByLabelText('Labels')).not.toHaveTextContent('bug')
  })

  it('locks assignment controls during a mutation and reports mutation errors inline', async () => {
    const user = userEvent.setup()
    let rejectMutation: (reason: Error) => void = () => undefined
    vi.mocked(api.issue).mockResolvedValue(githubIssue)
    vi.mocked(api.issueLabels).mockResolvedValue([bugLabel, docsLabel])
    vi.mocked(api.addIssueLabels).mockReturnValue(new Promise((_resolve, reject) => { rejectMutation = reject }))
    renderIssueDetail()

    await user.click(await screen.findByRole('button', { name: 'Manage labels' }))
    const bug = await screen.findByRole('checkbox', { name: /bug/i })
    const docs = screen.getByRole('checkbox', { name: /documentation/i })
    await user.click(bug)
    expect(bug).toBeDisabled()
    expect(docs).toBeDisabled()

    rejectMutation(new Error('Could not add label.'))
    expect(await screen.findByRole('alert')).toHaveTextContent('Could not add label.')
    await waitFor(() => expect(bug).not.toBeDisabled())
    expect(bug).not.toBeChecked()
  })

  it('validates, previews, creates, and immediately adds a repository label', async () => {
    const user = userEvent.setup()
    const feature = { id: 'label-feature', name: 'feature', color: 'aabbcc', description: 'Planned work' }
    vi.mocked(api.issue).mockResolvedValue(githubIssue)
    vi.mocked(api.issueLabels).mockResolvedValue([])
    vi.mocked(api.createIssueLabel).mockResolvedValue(feature)
    vi.mocked(api.addIssueLabels).mockResolvedValue({ ...githubIssue, labels: [feature] })
    renderIssueDetail()

    await user.click(await screen.findByRole('button', { name: 'Manage labels' }))
    await user.click(await screen.findByRole('button', { name: 'Create new label' }))
    expect(screen.queryByRole('searchbox', { name: 'Search labels' })).not.toBeInTheDocument()

    const submit = screen.getByRole('button', { name: 'Create and add' })
    expect(submit).toBeDisabled()
    await user.type(screen.getByLabelText('Label name'), 'feature')
    await user.clear(screen.getByLabelText('Hex color'))
    await user.type(screen.getByLabelText('Hex color'), 'nothex')
    expect(submit).toBeDisabled()
    await user.clear(screen.getByLabelText('Hex color'))
    await user.type(screen.getByLabelText('Hex color'), 'aabbcc')
    await user.type(screen.getByLabelText('Description (optional)'), 'Planned work')
    expect(screen.getByLabelText('Description (optional)')).toHaveAttribute('maxlength', '100')
    expect(screen.getByLabelText('Label preview')).toHaveTextContent('feature')
    expect(submit).toBeEnabled()

    await user.click(submit)
    await waitFor(() => expect(api.createIssueLabel).toHaveBeenCalledWith('holark', {
      name: 'feature', color: 'aabbcc', description: 'Planned work',
    }))
    expect(api.addIssueLabels).toHaveBeenCalledWith('issue-1', ['feature'])
    expect(screen.queryByRole('dialog', { name: 'Create new label' })).not.toBeInTheDocument()
    expect(screen.getByLabelText('Labels')).toHaveTextContent('feature')
  })

  it('retries only assignment when creation succeeded but assignment failed', async () => {
    const user = userEvent.setup()
    const feature = { id: 'label-feature', name: 'feature', color: 'aabbcc', description: '' }
    vi.mocked(api.issue).mockResolvedValue(githubIssue)
    vi.mocked(api.issueLabels).mockResolvedValue([])
    vi.mocked(api.createIssueLabel).mockResolvedValue(feature)
    vi.mocked(api.addIssueLabels)
      .mockRejectedValueOnce(new Error('Assignment failed.'))
      .mockResolvedValueOnce({ ...githubIssue, labels: [feature] })
    renderIssueDetail()

    await user.click(await screen.findByRole('button', { name: 'Manage labels' }))
    await user.click(await screen.findByRole('button', { name: 'Create new label' }))
    await user.type(screen.getByLabelText('Label name'), 'feature')
    await user.click(screen.getByRole('button', { name: 'Create and add' }))

    expect(await screen.findByRole('alert')).toHaveTextContent('was created, but it was not added')
    await user.click(screen.getByRole('button', { name: 'Try adding again' }))
    await waitFor(() => expect(api.addIssueLabels).toHaveBeenCalledTimes(2))
    expect(api.createIssueLabel).toHaveBeenCalledTimes(1)
    expect(screen.getByLabelText('Labels')).toHaveTextContent('feature')
  })

  it('treats projection-pending label creation as complete before retrying assignment', async () => {
    const user = userEvent.setup()
    const feature = { id: 'label-feature', name: 'feature', color: '0969da', description: '' }
    vi.mocked(api.issue).mockResolvedValue(githubIssue)
    vi.mocked(api.issueLabels).mockResolvedValueOnce([]).mockResolvedValueOnce([])
    vi.mocked(api.createIssueLabel).mockRejectedValue(labelProjectionPendingError())
    vi.mocked(api.addIssueLabels).mockResolvedValue({ ...githubIssue, labels: [feature] })
    renderIssueDetail()

    await user.click(await screen.findByRole('button', { name: 'Manage labels' }))
    await user.click(await screen.findByRole('button', { name: 'Create new label' }))
    await user.type(screen.getByLabelText('Label name'), 'feature')
    await user.click(screen.getByRole('button', { name: 'Create and add' }))

    expect(await screen.findByRole('alert')).toHaveTextContent('was created on GitHub')
    expect(api.issueLabels).toHaveBeenCalledTimes(2)
    expect(screen.getByLabelText('Label name')).toBeDisabled()

    await user.click(screen.getByRole('button', { name: 'Try adding again' }))
    await waitFor(() => expect(api.addIssueLabels).toHaveBeenCalledWith('issue-1', ['feature']))
    expect(api.createIssueLabel).toHaveBeenCalledTimes(1)
    expect(screen.getByLabelText('Labels')).toHaveTextContent('feature')
  })
})

it('returns from an issue to the originating My work filter', async () => {
 vi.mocked(api.issue).mockResolvedValue(openIssue)
 renderWithProject('/issues/issue-1?returnTo=%2Fmy-work%3Fview%3Dassigned_issues%26page%3D2', <IssueDetail />)
 expect(await screen.findByRole('link', { name: 'Back to My work' })).toHaveAttribute('href', '/my-work?view=assigned_issues&page=2')
})


it('matches any label group and requires all its labels immediately', async () => {
  const user = userEvent.setup()
  vi.mocked(api.searchIssues).mockResolvedValue(issueSearchResult([openIssue]))
  vi.mocked(api.searchIssueLabels).mockResolvedValue(['backend', 'data', 'api', 'bug', 'regression'].map((name) => ({ ...bugLabel, id: name, name })))
  renderWithProject('/issues', <IssueList />)
  await screen.findByText(openIssue.title)
  expect(api.searchIssueLabels).not.toHaveBeenCalled()
  await user.click(screen.getByRole('button', { name: 'Labels' }))
  expect(screen.getByText('Match any group')).toBeInTheDocument()
  expect(screen.queryByRole('button', { name: 'Apply labels' })).not.toBeInTheDocument()
  expect(screen.queryByRole('button', { name: 'All' })).not.toBeInTheDocument()
  expect(screen.queryByRole('button', { name: 'Any' })).not.toBeInTheDocument()
  await user.click(await screen.findByRole('checkbox', { name: 'backend' }))
  await waitFor(() => expect(api.searchIssues).toHaveBeenLastCalledWith('is:open sort:created-desc label:"backend"', 1, expect.any(AbortSignal), ''))
  const searches = vi.mocked(api.searchIssues).mock.calls.length
  await user.click(screen.getByRole('button', { name: 'Add group' }))
  expect(api.searchIssues).toHaveBeenCalledTimes(searches)
  expect(screen.getAllByRole('searchbox', { name: 'Search filter labels' })).toHaveLength(1)
  let second = screen.getByRole('group', { name: 'Label group 2' })
  expect(within(second).getByRole('searchbox')).toHaveFocus()
  await user.click(within(second).getByRole('checkbox', { name: 'data' }))
  await waitFor(() => expect(api.searchIssues).toHaveBeenLastCalledWith('is:open sort:created-desc label:("backend" OR "data")', 1, expect.any(AbortSignal), ''))
  await user.click(within(second).getByRole('checkbox', { name: 'api' }))
  await waitFor(() => expect(api.searchIssues).toHaveBeenLastCalledWith('is:open sort:created-desc label:("backend" OR ("data" AND "api"))', 1, expect.any(AbortSignal), ''))
  expect(within(second).getByText('All of')).toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'Add group' }))
  const third = screen.getByRole('group', { name: 'Label group 3' })
  await user.click(within(third).getByRole('checkbox', { name: 'bug' }))
  await user.click(within(third).getByRole('checkbox', { name: 'regression' }))
  const applied = 'is:open sort:created-desc label:("backend" OR ("data" AND "api") OR ("bug" AND "regression"))'
  await waitFor(() => expect(api.searchIssues).toHaveBeenLastCalledWith(applied, 1, expect.any(AbortSignal), ''))
  await user.keyboard('{Escape}')
  await user.click(screen.getByRole('button', { name: 'Labels' }))
  expect(screen.getAllByRole('group', { name: /Label group/ })).toHaveLength(3)
  expect(screen.queryByRole('searchbox', { name: 'Search filter labels' })).not.toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Labels' })).toHaveTextContent('3 groups')
  second = screen.getByRole('group', { name: 'Label group 2' })
  await user.click(within(second).getByRole('button', { name: 'Add label' }))
  await user.click(within(second).getByRole('checkbox', { name: 'api' }))
  await waitFor(() => expect(api.searchIssues).toHaveBeenLastCalledWith('is:open sort:created-desc label:("backend" OR "data" OR ("bug" AND "regression"))', 1, expect.any(AbortSignal), ''))
  await user.click(screen.getByRole('button', { name: 'Remove group 2' }))
  await waitFor(() => expect(api.searchIssues).toHaveBeenLastCalledWith('is:open sort:created-desc label:("backend" OR ("bug" AND "regression"))', 1, expect.any(AbortSignal), ''))
  await user.click(screen.getByRole('button', { name: 'Remove label regression' }))
  await waitFor(() => expect(api.searchIssues).toHaveBeenLastCalledWith('is:open sort:created-desc label:("backend" OR "bug")', 1, expect.any(AbortSignal), ''))
  await user.click(screen.getByRole('button', { name: 'Remove label bug' }))
  await waitFor(() => expect(api.searchIssues).toHaveBeenLastCalledWith('is:open sort:created-desc label:"backend"', 1, expect.any(AbortSignal), ''))
  await user.click(screen.getByRole('button', { name: 'Add group' }))
  expect(screen.getAllByRole('group', { name: /Label group/ })).toHaveLength(2)
  expect(within(screen.getByRole('group', { name: 'Label group 2' })).getByRole('searchbox')).toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'Clear' }))
  await waitFor(() => expect(api.searchIssues).toHaveBeenLastCalledWith('is:open sort:created-desc', 1, expect.any(AbortSignal), ''))
  expect(screen.getByRole('checkbox', { name: 'backend' })).not.toBeChecked()
  expect(api.issueLabels).not.toHaveBeenCalled()
  expect(api.syncIssues).not.toHaveBeenCalled()
})

it('closes the label picker on history navigation without restoring stale selections', async () => {
  const user = userEvent.setup()
  vi.mocked(api.searchIssues).mockResolvedValue(issueSearchResult([openIssue]))
  vi.mocked(api.searchIssueLabels).mockResolvedValue([bugLabel, docsLabel, { ...bugLabel, id: 'frontend', name: 'frontend' }])
  let navigate!: ReturnType<typeof useNavigate>
  function HistoryProbe() {
    const routerNavigate = useNavigate()
    useEffect(() => { navigate = routerNavigate }, [routerNavigate])
    return null
  }
  renderWithProject('/issues', <><HistoryProbe /><IssueList /></>)
  await screen.findByText(openIssue.title)
  await user.click(screen.getByRole('button', { name: 'Labels' }))
  await user.click(await screen.findByRole('checkbox', { name: 'bug' }))
  await user.click(screen.getByRole('checkbox', { name: 'documentation' }))
  expect(screen.getByRole('checkbox', { name: 'documentation' })).toBeChecked()

  await act(async () => navigate(-1))
  await waitFor(() => expect(api.searchIssues).toHaveBeenLastCalledWith('is:open sort:created-desc label:"bug"', 1, expect.any(AbortSignal), ''))
  await waitFor(() => expect(screen.queryByRole('group', { name: 'Label group 1' })).not.toBeInTheDocument())
  await user.click(screen.getByRole('button', { name: 'Labels' }))
  await user.click(screen.getByRole('button', { name: 'Add label' }))
  expect(screen.getByRole('checkbox', { name: 'bug' })).toBeChecked()
  expect(screen.getByRole('checkbox', { name: 'documentation' })).not.toBeChecked()

  await act(async () => navigate(1))
  await waitFor(() => expect(screen.queryByRole('group', { name: 'Label group 1' })).not.toBeInTheDocument())
  await user.click(screen.getByRole('button', { name: 'Labels' }))
  expect(screen.getByRole('button', { name: 'Remove label documentation' })).toBeInTheDocument()
  await act(async () => navigate(-1))
  await waitFor(() => expect(screen.queryByRole('group', { name: 'Label group 1' })).not.toBeInTheDocument())
  await user.click(screen.getByRole('button', { name: 'Labels' }))
  await user.click(screen.getByRole('button', { name: 'Add label' }))
  await user.click(screen.getByRole('checkbox', { name: 'frontend' }))
  await waitFor(() => expect(api.searchIssues).toHaveBeenLastCalledWith('is:open sort:created-desc label:("bug" AND "frontend")', 1, expect.any(AbortSignal), ''))
  expect(screen.getByRole('checkbox', { name: 'documentation' })).not.toBeChecked()
})

it('round-trips typed label groups through people, state, sort and the label editor', async () => {
  const user = userEvent.setup()
  vi.mocked(api.searchIssues).mockResolvedValue(issueSearchResult([openIssue]))
  vi.mocked(api.searchIssueLabels).mockResolvedValue([bugLabel, { ...docsLabel, name: 'help wanted' }])
  const labelQuery = 'label:(bug OR ("help wanted" AND unknown))'
  renderWithProject(`/issues?q=${encodeURIComponent(labelQuery)}&page=2`, <IssueList />)
  await screen.findByText(openIssue.title)
  await user.click(screen.getByRole('combobox', { name: 'Sort' }))
  await user.click(screen.getByRole('option', { name: 'Recently updated' }))
  await waitFor(() => expect(api.searchIssues).toHaveBeenLastCalledWith(`${labelQuery} sort:updated-desc`, 1, expect.any(AbortSignal), ''))
  await user.click(screen.getByRole('combobox', { name: 'Assignee' }))
  await user.click(screen.getByRole('option', { name: 'Me' }))
  await user.click(screen.getByRole('button', { name: 'State' }))
  await user.click(screen.getByRole('checkbox', { name: 'Closed' }))
  await user.keyboard('{Escape}')
  const prefix = `${labelQuery} sort:updated-desc assignee:@me is:all`
  await waitFor(() => expect(api.searchIssues).toHaveBeenLastCalledWith(prefix, 1, expect.any(AbortSignal), ''))
  await user.click(screen.getByRole('button', { name: 'Labels' }))
  const group = screen.getByRole('group', { name: 'Label group 2' })
  expect(within(group).getByRole('button', { name: 'Remove label help wanted' })).toBeInTheDocument()
  expect(within(group).getByRole('button', { name: 'Remove label unknown' })).toBeInTheDocument()
  await user.click(within(group).getByRole('button', { name: 'Remove label unknown' }))
  await waitFor(() => expect(api.searchIssues).toHaveBeenLastCalledWith('sort:updated-desc assignee:@me is:all label:("bug" OR "help wanted")', 1, expect.any(AbortSignal), ''))
})

it('normalizes the query while retaining title, pagination and detail return state', async () => {
  const user = userEvent.setup()
  const canonical = 'label:"bug" is:open sort:created-desc'
  vi.mocked(api.searchIssues).mockResolvedValue({ ...issueSearchResult([openIssue]), total: 51, query: canonical })
  renderWithProject('/issues?q=label:bug&title=fix', <IssueList />)
  await screen.findByText(openIssue.title)
  expect(screen.getByRole('textbox', { name: 'Search issues' })).toHaveValue('fix')
  await user.click(screen.getByText('Advanced filters'))
  expect(screen.getByRole('textbox', { name: 'Query expression' })).toHaveValue(canonical)
  await user.click(await screen.findByRole('button', { name: 'Next' }))
  await waitFor(() => expect(api.searchIssues).toHaveBeenLastCalledWith(canonical, 2, expect.any(AbortSignal), 'fix'))
  const link = screen.getByRole('link', { name: /Fix issue list/ })
  const returnTo = new URLSearchParams(new URL(link.getAttribute('href')!, 'http://localhost').search).get('returnTo')!
  const params = new URL(returnTo, 'http://localhost').searchParams
  expect(params.get('q')).toBe(canonical)
  expect(params.get('title')).toBe('fix')
  expect(params.get('page')).toBe('2')
})

it('returns from issue details to the originating issue search', async () => {
  vi.mocked(api.issue).mockResolvedValue(openIssue)
  const origin = '/issues?q=label%3Abug&title=fix&page=2'
  renderWithProject(`/issues/issue-1?returnTo=${encodeURIComponent(origin)}`, <Routes><Route path="/issues/:issueId" element={<IssueDetail />} /></Routes>)
  expect(await screen.findByRole('link', { name: 'Back to issues' })).toHaveAttribute('href', origin)
})

it('keeps selected labels editable when the cached catalog fails and supports retry', async () => {
  const user = userEvent.setup()
  vi.mocked(api.searchIssues).mockResolvedValue(issueSearchResult([openIssue]))
  vi.mocked(api.searchIssueLabels).mockRejectedValueOnce(new Error('Catalog unavailable')).mockResolvedValue([bugLabel, docsLabel])
  renderWithProject('/issues?q=label%3Abug', <IssueList />)
  await screen.findByText(openIssue.title)
  await user.click(screen.getByRole('button', { name: 'Labels' }))
  expect(await screen.findByRole('alert')).toHaveTextContent('Catalog unavailable')
  expect(screen.getByRole('button', { name: 'Remove label bug' })).toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'Add label' }))
  await user.click(screen.getByRole('button', { name: 'Retry' }))
  expect(await screen.findByRole('checkbox', { name: 'documentation' })).not.toBeChecked()
  expect(screen.getByRole('checkbox', { name: 'bug' })).toBeChecked()
  await user.click(screen.getByRole('checkbox', { name: 'documentation' }))
  await waitFor(() => expect(api.searchIssues).toHaveBeenLastCalledWith('label:("bug" AND "documentation")', 1, expect.any(AbortSignal), ''))
})


it('opens nested alternatives as separate groups with all required labels', async () => {
  const user = userEvent.setup()
  vi.mocked(api.searchIssues).mockResolvedValue(issueSearchResult([openIssue]))
  const query = 'label:(backend OR (data OR (api OR (bug AND bug AND regression))))'
  renderWithProject(`/issues?q=${encodeURIComponent(query)}`, <IssueList />)
  await screen.findByText(openIssue.title)
  await user.click(screen.getByRole('button', { name: 'Labels' }))
  expect(screen.getAllByRole('group', { name: /Label group/ })).toHaveLength(4)
  const requirements = screen.getByRole('group', { name: 'Label group 4' })
  expect(within(requirements).getByRole('button', { name: 'Remove label bug' })).toBeInTheDocument()
  expect(within(requirements).getByRole('button', { name: 'Remove label regression' })).toBeInTheDocument()
  expect(api.searchIssues).toHaveBeenCalledTimes(1)
  await user.click(within(requirements).getByRole('button', { name: 'Remove label regression' }))
  await waitFor(() => expect(api.searchIssues).toHaveBeenLastCalledWith('label:("backend" OR "data" OR "api" OR "bug")', 1, expect.any(AbortSignal), ''))
})

it('preserves a custom expression that cannot be displayed as label groups until cleared', async () => {
  const user = userEvent.setup()
  vi.mocked(api.searchIssues).mockResolvedValue(issueSearchResult([openIssue]))
  const query = 'is:closed label:(backend AND (data OR api))'
  renderWithProject(`/issues?q=${encodeURIComponent(query)}`, <IssueList />)
  await screen.findByText(openIssue.title)
  await user.click(screen.getByRole('button', { name: 'Labels' }))
  expect(await screen.findByRole('alert')).toHaveTextContent('cannot be edited as a list of label groups')
  expect(screen.getByRole('button', { name: 'Labels' })).toHaveTextContent('Custom search')
  await user.click(screen.getByRole('button', { name: 'Edit advanced filters' }))
  await waitFor(() => expect(screen.getByRole('textbox', { name: 'Query expression' })).toHaveFocus())
  expect(screen.getByRole('textbox', { name: 'Query expression' })).toHaveValue(query)
  await user.click(screen.getByRole('button', { name: 'Labels' }))
  expect(api.searchIssues).toHaveBeenCalledTimes(1)
  await user.click(screen.getByRole('button', { name: 'Clear' }))
  await waitFor(() => expect(api.searchIssues).toHaveBeenLastCalledWith('is:closed', 1, expect.any(AbortSignal), ''))
  expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  expect(screen.getByRole('group', { name: 'Label group 1' })).toBeInTheDocument()
})


it('keeps repeated label qualifiers as requirements within one group', async () => {
  const user = userEvent.setup()
  vi.mocked(api.searchIssues).mockResolvedValue(issueSearchResult([openIssue]))
  const query = 'label:backend label:(data AND api)'
  renderWithProject(`/issues?q=${encodeURIComponent(query)}`, <IssueList />)
  await screen.findByText(openIssue.title)
  await user.click(screen.getByRole('button', { name: 'Labels' }))
  expect(screen.getAllByRole('group', { name: /Label group/ })).toHaveLength(1)
  expect(screen.getByRole('button', { name: 'Remove label backend' })).toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Remove label data' })).toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Remove label api' })).toBeInTheDocument()
  expect(api.searchIssues).toHaveBeenCalledTimes(1)
  await user.click(screen.getByRole('button', { name: 'Remove label api' }))
  await waitFor(() => expect(api.searchIssues).toHaveBeenLastCalledWith('label:("backend" AND "data")', 1, expect.any(AbortSignal), ''))
})

it('submits advanced filters explicitly, preserves a closed draft, and displays invalid queries unchanged', async () => {
  const user = userEvent.setup()
  vi.mocked(api.searchIssues).mockResolvedValue(issueSearchResult([openIssue]))
  renderWithProject('/issues?q=is:open&title=fix&page=2', <IssueList />)
  await screen.findByText(openIssue.title)
  expect(screen.getByText('Advanced filters').closest('details')).not.toHaveAttribute('open')
  await user.click(screen.getByText('Advanced filters'))
  const editor = screen.getByRole('textbox', { name: 'Query expression' })
  const query = 'is:all label:(bug AND (frontend OR backend))'
  await user.clear(editor)
  await user.type(editor, query)
  await user.click(screen.getByText('Advanced filters'))
  await act(async () => { await new Promise(resolve => setTimeout(resolve, 300)) })
  expect(api.searchIssues).toHaveBeenCalledTimes(1)
  await user.click(screen.getByText('Advanced filters'))
  expect(editor).toHaveValue(query)
  await user.click(screen.getByRole('button', { name: 'Apply' }))
  await waitFor(() => expect(api.searchIssues).toHaveBeenLastCalledWith(query, 1, expect.any(AbortSignal), 'fix'))
  vi.mocked(api.searchIssues).mockRejectedValue(new Error('Unclosed label group'))
  await user.clear(editor)
  await user.type(editor, 'label:(bug OR')
  await user.click(screen.getByRole('button', { name: 'Apply' }))
  expect(await screen.findByRole('alert')).toHaveTextContent('Unclosed label group')
  expect(editor).toHaveValue('label:(bug OR')
  expect(screen.getByRole('textbox', { name: 'Search issues' })).toHaveValue('fix')
  expect(screen.queryByText(openIssue.title)).not.toBeInTheDocument()
})
