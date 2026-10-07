import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { Link, MemoryRouter, Route, Routes, useLocation, useNavigate } from 'react-router-dom'
import { api } from '../../data/api'
import { PageCacheProvider } from '../../data/PageCacheProvider'
import type { WorkItemsResult, WorkItemSummary } from '../../data/types'
import { ProjectContext } from '../project/ProjectContext'
import { ProjectMemberStoreProvider } from '../members/ProjectMemberStore'
import { MyWork, PullRequestList } from './WorkItems'

vi.mock('../project/ProjectHeader', () => ({ ProjectHeader: () => null }))
const row: WorkItemSummary = { id: 'pr-1', title: 'Local WIP', kind: 'pull_request', number: 0, status: 'wip', assignee_ids: [], reviewer_ids: [], updated_at: '2026-01-01T12:00:00Z', reasons: [] }
const result: WorkItemsResult = { rows: [row], total: 1, page: 1, per_page: 50, identity: null, sync: [] }
function Location() { const location = useLocation(); return <output data-testid="url">{location.pathname + location.search}</output> }
function renderList(path = '/pulls', personal = false) {
  return render(<MemoryRouter initialEntries={[path]}><ProjectContext.Provider value={{ id: 'repo', name: 'Repo', default_branch: 'main' }}><ProjectMemberStoreProvider projectId="repo"><PageCacheProvider>{personal ? <MyWork /> : <PullRequestList />}<Location /></PageCacheProvider></ProjectMemberStoreProvider></ProjectContext.Provider></MemoryRouter>)
}
beforeEach(() => {
  vi.spyOn(api, 'githubMembers').mockResolvedValue([])
  vi.spyOn(api, 'searchPullRequests').mockResolvedValue(result)
  vi.spyOn(api, 'myWork').mockResolvedValue(result)
  vi.spyOn(api, 'syncMyWork').mockResolvedValue({ synced: true })
  vi.spyOn(api, 'syncPullRequests').mockResolvedValue({ pull_requests: [], imported: 1, updated: 0, synced_at: row.updated_at })
  vi.spyOn(api, 'resolveProjectMembers').mockResolvedValue({ members: [{ id: 'me', login: 'alice', is_me: true, permission: '' }], missing_ids: [] })
})
afterEach(() => vi.restoreAllMocks())

it('searches titles as you type and combines the title with dropdown filters', async () => {
  const user = userEvent.setup(); renderList()
  expect(await screen.findByText('Local WIP')).toBeInTheDocument()
  expect(screen.getByText('Local · PR')).toBeInTheDocument()
  const input = screen.getByRole('textbox', { name: 'Search pull requests' })
  expect(input).toHaveValue('')
  expect(input).toHaveAttribute('placeholder', 'Search PR titles…')
  await user.type(input, 'workspace navigation')
  await waitFor(() => expect(api.searchPullRequests).toHaveBeenLastCalledWith('is:active sort:created-desc', 1, expect.any(AbortSignal), 'workspace navigation'))
  await user.click(screen.getByRole('button', { name: 'State' }))
  await user.click(screen.getByRole('button', { name: 'Clear all' }))
  await user.click(screen.getByRole('checkbox', { name: 'Closed' }))
  await waitFor(() => expect(api.searchPullRequests).toHaveBeenLastCalledWith('sort:created-desc state:closed', 1, expect.any(AbortSignal), 'workspace navigation'))
  await user.keyboard('{Escape}')
  await user.click(screen.getByRole('combobox', { name: 'Author' }))
  await user.click(screen.getByRole('option', { name: 'Me' }))
  await waitFor(() => expect(api.searchPullRequests).toHaveBeenLastCalledWith('sort:created-desc state:closed author:@me', 1, expect.any(AbortSignal), 'workspace navigation'))
  expect(input).toHaveValue('workspace navigation')
})

it.each(['workspace', ''])('clears the applied or pending title while preserving filters (title: %s)', async (title) => {
  const user = userEvent.setup()
  const filters = 'state:open author:alice sort:updated-desc'
  renderList(`/pulls?q=${encodeURIComponent(filters)}&page=2&title=${title}`)
  await screen.findByText('Local WIP')
  const input = screen.getByRole('textbox', { name: 'Search pull requests' })
  await user.type(input, ' pending')
  await user.click(screen.getByRole('button', { name: 'Clear search' }))
  expect(input).toHaveValue('')
  expect(input).toHaveFocus()
  await waitFor(() => expect(api.searchPullRequests).toHaveBeenLastCalledWith(filters, 1, expect.any(AbortSignal), ''))
  expect(screen.getByRole('button', { name: 'State' })).toHaveTextContent('Open')
  expect(screen.getByRole('combobox', { name: 'Author' })).toHaveTextContent('alice')
  expect(screen.getByRole('combobox', { name: 'Sort' })).toHaveTextContent('Recently updated')
  await user.type(input, 'replacement')
  await waitFor(() => expect(api.searchPullRequests).toHaveBeenLastCalledWith(filters, 1, expect.any(AbortSignal), 'replacement'))
})

it('combines checked states, clears and selects all while retaining other filters', async () => {
  const user = userEvent.setup(); renderList('/pulls?q=state%3Aopen%2Cmerged+author%3Aalice+sort%3Aupdated-desc&page=2')
  await screen.findByText('Local WIP')
  await user.click(screen.getByRole('button', { name: 'State' }))
  expect(screen.getByRole('checkbox', { name: 'Open' })).toBeChecked()
  expect(screen.getByRole('checkbox', { name: 'Merged' })).toBeChecked()
  expect(screen.getByRole('checkbox', { name: 'Draft' })).not.toBeChecked()
  await user.click(screen.getByRole('checkbox', { name: 'Closed' }))
  await waitFor(() => expect(api.searchPullRequests).toHaveBeenLastCalledWith('author:alice sort:updated-desc state:open,merged,closed', 1, expect.any(AbortSignal), ''))
  await user.click(screen.getByRole('checkbox', { name: 'Merged' }))
  await waitFor(() => expect(api.searchPullRequests).toHaveBeenLastCalledWith('author:alice sort:updated-desc state:open,closed', 1, expect.any(AbortSignal), ''))
  await user.click(screen.getByRole('button', { name: 'Clear all' }))
  await waitFor(() => expect(api.searchPullRequests).toHaveBeenLastCalledWith('author:alice sort:updated-desc state:none', 1, expect.any(AbortSignal), ''))
  expect(screen.getAllByRole('checkbox').every((checkbox) => !(checkbox as HTMLInputElement).checked)).toBe(true)
  await user.click(screen.getByRole('button', { name: 'Select all' }))
  await waitFor(() => expect(api.searchPullRequests).toHaveBeenLastCalledWith('author:alice sort:updated-desc is:all', 1, expect.any(AbortSignal), ''))
  expect(screen.getAllByRole('checkbox').every((checkbox) => (checkbox as HTMLInputElement).checked)).toBe(true)
})

it('preserves a focused search draft when the initial response canonicalizes the URL', async () => {
  let finish!: (value: WorkItemsResult) => void
  vi.mocked(api.searchPullRequests).mockImplementationOnce(() => new Promise((resolve) => { finish = resolve }))
  const user = userEvent.setup(); renderList()
  await waitFor(() => expect(api.searchPullRequests).toHaveBeenCalledTimes(1))
  const input = screen.getByRole('textbox', { name: 'Search pull requests' })
  await user.clear(input); await user.type(input, 'unfinished search')

  await act(async () => finish({ ...result, query: 'is:active sort:created-desc' }))

  await waitFor(() => expect(screen.getByTestId('url')).toHaveTextContent('/pulls?q=is%3Aactive+sort%3Acreated-desc&page=1'))
  expect(input).toHaveValue('unfinished search')
  expect(input).toHaveFocus()
  expect(screen.getByRole('textbox', { name: 'Search pull requests' })).toBe(input)
  expect(api.searchPullRequests).toHaveBeenCalledTimes(1)
})

it('preserves edits and focus while a submitted query gains default qualifiers', async () => {
  const user = userEvent.setup(); renderList('/pulls?q=state:open')
  await screen.findByText('Local WIP')
  let finish!: (value: WorkItemsResult) => void
  const canonical = { ...result, query: 'state:open sort:created-desc' }
  vi.mocked(api.searchPullRequests).mockResolvedValue(canonical).mockImplementationOnce(() => new Promise((resolve) => { finish = resolve }))
  const input = screen.getByRole('textbox', { name: 'Search pull requests' })
  await user.clear(input); await user.type(input, 'fix{Enter}')
  await waitFor(() => expect(api.searchPullRequests).toHaveBeenLastCalledWith('state:open', 1, expect.any(AbortSignal), 'fix'))
  expect(screen.queryByText('Local WIP')).not.toBeInTheDocument()
  expect(input).toHaveFocus()
  await user.keyboard(' next')

  await act(async () => finish(canonical))

  await waitFor(() => expect(screen.getByTestId('url')).toHaveTextContent('q=state%3Aopen+sort%3Acreated-desc&page=1&title=fix'))
  expect(input).toHaveValue('fix next')
  expect(input).toHaveFocus()
  expect(screen.getByRole('textbox', { name: 'Search pull requests' })).toBe(input)
})

it('resolves all participants on a full page in batches within the endpoint limit', async () => {
  const rows = Array.from({ length: 50 }, (_, index) => ({
    ...row,
    id: `pr-${index}`,
    author_id: `author-${index}`,
    assignee_ids: [`assignee-${index}`],
    reviewer_ids: [`reviewer-${index}`],
  }))
  vi.mocked(api.searchPullRequests).mockResolvedValue({ ...result, rows, total: rows.length })
  vi.mocked(api.resolveProjectMembers).mockImplementation(async (_, ids) => {
    if (ids.length > 100) throw new Error('At most 100 member IDs may be resolved at once.')
    return { members: ids.map((id) => ({ id, login: id, is_me: false, permission: '' })), missing_ids: [] }
  })

  renderList('/pulls?q=is%3Aall&page=1')

  await waitFor(() => expect(screen.getAllByLabelText(/^(author|assignee|reviewer)-/)).toHaveLength(150))
  const batches = vi.mocked(api.resolveProjectMembers).mock.calls.map(([, ids]) => ids)
  expect(batches.map((ids) => ids.length)).toEqual([100, 50])
  expect(batches.flat().sort()).toEqual(rows.flatMap((item) => [item.author_id, ...item.assignee_ids, ...item.reviewer_ids]).sort())
})

it('selects an imported bot author from the Author dropdown', async () => {
  vi.mocked(api.githubMembers).mockResolvedValue([{ id: 'bot', login: 'dependabot[bot]', is_me: false, permission: '' }])
  const user = userEvent.setup(); renderList()
  await screen.findByText('Local WIP')
  await user.click(screen.getByRole('combobox', { name: 'Author' }))
  await user.click(await screen.findByRole('option', { name: 'dependabot[bot]' }))
  await waitFor(() => expect(api.searchPullRequests).toHaveBeenLastCalledWith('is:active sort:created-desc author:dependabot[bot]', 1, expect.any(AbortSignal), ''))
  expect(screen.getByRole('combobox', { name: 'Author' })).toHaveTextContent('dependabot[bot]')
})

it.each([
  ['Author', 'author'],
  ['Assignee', 'assignee'],
  ['Reviewer', 'user-review-requested'],
])('shows one current-user option with avatar and handle in %s', async (label, qualifier) => {
  const me = { id: 'me', login: 'tt10612', avatar_url: 'https://avatars.example/me', is_me: true, permission: '' }
  const other = { id: 'other', login: 'alice', avatar_url: 'https://avatars.example/alice', is_me: false, permission: '' }
  vi.mocked(api.githubMembers).mockResolvedValue([me, other])
  const user = userEvent.setup()
  renderList(`/pulls?q=${qualifier}:tt10612`)
  await screen.findByText('Local WIP')
  const filter = screen.getByRole('combobox', { name: label })
  expect(filter).toHaveTextContent('tt10612 (Me)')
  expect(filter.querySelector('img')).toHaveAttribute('src', me.avatar_url)
  await user.click(filter)
  expect(screen.getAllByRole('option')).toHaveLength(3)
  const self = screen.getByRole('option', { name: 'tt10612 (Me)' })
  expect(self).toHaveAttribute('aria-selected', 'true')
  expect(self.querySelector('img')).toHaveAttribute('src', me.avatar_url)
  const teammate = screen.getByRole('option', { name: 'alice' })
  expect(teammate.querySelector('img')).toHaveAttribute('src', other.avatar_url)
  await user.keyboard('alice{Enter}')
  await waitFor(() => expect(api.searchPullRequests).toHaveBeenLastCalledWith(`${qualifier}:alice`, 1, expect.any(AbortSignal), ''))
  await user.click(filter)
  await user.click(screen.getByRole('option', { name: 'tt10612 (Me)' }))
  await waitFor(() => expect(api.searchPullRequests).toHaveBeenLastCalledWith(`${qualifier}:@me`, 1, expect.any(AbortSignal), ''))
  expect(filter).toHaveTextContent('tt10612 (Me)')
})

it('uses the authenticated identity for Me before the member list includes it', async () => {
  vi.mocked(api.searchPullRequests).mockResolvedValue({ ...result, identity: { id: 'me', login: 'tt10612', avatar_url: 'https://avatars.example/me', is_me: true, permission: '' } })
  const user = userEvent.setup()
  renderList('/pulls?q=author:tt10612')
  await screen.findByText('Local WIP')
  await user.click(screen.getByRole('combobox', { name: 'Author' }))
  expect(screen.getAllByRole('option')).toHaveLength(2)
  expect(screen.getByRole('option', { name: 'tt10612 (Me)' })).toHaveAttribute('aria-selected', 'true')
})

it('treats punctuation as title text and shows search failures', async () => {
  const user = userEvent.setup(); renderList()
  await screen.findByText('Local WIP')
  vi.mocked(api.searchPullRequests).mockRejectedValue(new Error('Could not read cached pull requests.'))
  const input = screen.getByRole('textbox', { name: 'Search pull requests' }); await user.clear(input); await user.type(input, 'label:bug{Enter}')
  expect(await screen.findByRole('alert')).toHaveTextContent('Could not read cached pull requests.')
  expect(api.searchPullRequests).toHaveBeenLastCalledWith('is:active sort:created-desc', 1, expect.any(AbortSignal), 'label:bug')
  expect(screen.queryByText('Local WIP')).not.toBeInTheDocument()
})

it('keeps query and page in return links and clears rows while a new page loads', async () => {
  vi.mocked(api.searchPullRequests).mockResolvedValue({ ...result, total: 51 })
  const user = userEvent.setup(); renderList('/pulls?q=is%3Aall&page=1&title=local')
  const link = await screen.findByRole('link', { name: 'Local WIP' })
  expect(link).toHaveAttribute('href', '/pulls/pr-1?returnTo=%2Fpulls%3Fq%3Dis%253Aall%26page%3D1%26title%3Dlocal')
  let finish!: (value: WorkItemsResult) => void
  vi.mocked(api.searchPullRequests).mockImplementation(() => new Promise((resolve) => { finish = resolve }))
  await user.click(screen.getByRole('button', { name: 'Next' }))
  await waitFor(() => expect(api.searchPullRequests).toHaveBeenLastCalledWith('is:all', 2, expect.any(AbortSignal), 'local'))
  expect(screen.queryByText('Local WIP')).not.toBeInTheDocument()
  await act(async () => finish({ ...result, page: 2, total: 51, rows: [{ ...row, title: 'Second page' }] }))
  expect(await screen.findByText('Second page')).toBeInTheDocument()
  expect(screen.getByTestId('url')).toHaveTextContent('page=2')
  vi.mocked(api.searchPullRequests).mockRejectedValueOnce(new Error('Return refresh failed'))
  await user.click(screen.getByRole('button', { name: 'Previous' }))
  expect(screen.getByText('Local WIP')).toBeInTheDocument()
  expect(screen.queryByText('Second page')).not.toBeInTheDocument()
  expect(await screen.findByRole('alert')).toHaveTextContent('Return refresh failed')
  await user.click(screen.getByRole('combobox', { name: 'Sort' }))
  await user.click(screen.getByRole('option', { name: 'Least recently updated' }))
  await waitFor(() => expect(api.searchPullRequests).toHaveBeenLastCalledWith('is:all sort:updated-asc', 1, expect.any(AbortSignal), 'local'))
})

it('syncs full history only on request and retains cached rows after failure', async () => {
  const user = userEvent.setup(); renderList()
  await screen.findByText('Local WIP'); expect(api.syncPullRequests).not.toHaveBeenCalled()
  vi.mocked(api.syncPullRequests).mockRejectedValue(new Error('GitHub unavailable'))
  await user.click(screen.getByRole('button', { name: 'Sync' }))
  expect(await screen.findByText('GitHub unavailable')).toBeInTheDocument()
  expect(screen.getByText('Local WIP')).toBeInTheDocument()
  expect(api.syncPullRequests).toHaveBeenCalledWith('repo', 'history')
})

it.each([false, true])('shows dismissible sync success beside the button and clears it on retry (personal: %s)', async (personal) => {
  const user = userEvent.setup()
  if (personal) vi.mocked(api.myWork).mockResolvedValue({ ...result, identity: { id: 'me', login: 'alice', is_me: true, permission: '' } })
  renderList(personal ? '/my-work' : '/pulls', personal)
  await screen.findByText('Local WIP')
  const outcome = personal ? 'My work synchronized.' : 'Synced 1 imported, 0 updated.'
  await user.click(screen.getByRole('button', { name: 'Sync' }))
  const message = await screen.findByText(outcome)
  expect(message.closest('[role="status"]')).toContainElement(screen.getByRole('button', { name: 'Dismiss sync result' }))
  expect(message.parentElement?.parentElement).toContainElement(screen.getByRole('button', { name: 'Sync' }))
  await user.click(screen.getByRole('button', { name: 'Dismiss sync result' }))
  expect(screen.queryByText(outcome)).not.toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'Sync' }))
  await screen.findByText(outcome)
  let rejectSync!: (error: Error) => void
  if (personal) vi.mocked(api.syncMyWork).mockImplementationOnce(() => new Promise((_, reject) => { rejectSync = reject }))
  else vi.mocked(api.syncPullRequests).mockImplementationOnce(() => new Promise((_, reject) => { rejectSync = reject }))
  await user.click(screen.getByRole('button', { name: 'Sync' }))
  expect(screen.getByRole('button', { name: 'Syncing with GitHub…' })).toBeDisabled()
  expect(screen.queryByText(outcome)).not.toBeInTheDocument()
  await act(async () => rejectSync(new Error('GitHub unavailable')))
  const warning = await screen.findByRole('alert')
  expect(warning).toHaveTextContent('GitHub unavailable')
  expect(warning.parentElement).toContainElement(screen.getByRole('button', { name: 'Sync' }))
  await user.click(within(warning).getByRole('button', { name: 'Dismiss sync result' }))
  expect(screen.queryByText('GitHub unavailable')).not.toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Sync' })).toBeEnabled()
})

it('keeps the current search when a sync started under an earlier query finishes', async () => {
  let finish!: (value: Awaited<ReturnType<typeof api.syncPullRequests>>) => void
  vi.mocked(api.syncPullRequests).mockImplementationOnce(() => new Promise((resolve) => { finish = resolve }))
  const user = userEvent.setup(); renderList()
  await screen.findByText('Local WIP')
  await user.click(screen.getByRole('button', { name: 'Sync' }))
  expect(screen.getByRole('button', { name: 'Syncing with GitHub…' })).toBeDisabled()
  vi.mocked(api.searchPullRequests).mockResolvedValue({ ...result, rows: [{ ...row, title: 'Current search' }] })
  const input = screen.getByRole('textbox', { name: 'Search pull requests' })
  await user.clear(input); await user.type(input, 'current{Enter}')
  await screen.findByText('Current search')

  await act(async () => finish({ pull_requests: [], imported: 1, updated: 0, synced_at: row.updated_at }))

  expect(api.searchPullRequests).toHaveBeenCalledTimes(2)
  expect(api.searchPullRequests).toHaveBeenLastCalledWith('is:active sort:created-desc', 1, expect.any(AbortSignal), 'current')
  expect(screen.getByText('Synced 1 imported, 0 updated.')).toBeInTheDocument()
  expect(input).toHaveValue('current')
  expect(screen.getByText('Current search')).toBeInTheDocument()
})

it('shows deduplicated personal rows, reason badges and counts after a background sync failure', async () => {
  const personal = { ...result, identity: { id: 'me', login: 'alice', is_me: true, permission: '' }, counts: { all: 1, review_requested: 1, assigned_issues: 0, assigned_pull_requests: 1 }, rows: [{ ...row, status: 'open' as const, reasons: ['Assigned PR', 'Review requested'] }], sync: [{ section: 'pull_requests', attempted_at: row.updated_at, synced_at: row.updated_at, error: 'Offline' }] }
  vi.mocked(api.myWork).mockResolvedValue(personal)
  const user = userEvent.setup(); renderList('/my-work?view=all&page=1', true)
  expect(await screen.findByText('Local WIP')).toBeInTheDocument()
  expect(screen.getAllByRole('link', { name: 'Local WIP' })).toHaveLength(1)
  expect(screen.queryByText('GitHub identity')).not.toBeInTheDocument()
  expect(screen.queryByText('alice')).not.toBeInTheDocument()
  expect(screen.getByText('Assigned PR')).toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Review requested 1' })).toBeInTheDocument()
  expect(screen.getByRole('link', { name: 'Local WIP' }).getAttribute('href')).toContain('returnTo=%2Fmy-work')
  await user.click(screen.getByRole('button', { name: 'Assigned issues 0' }))
  await waitFor(() => expect(api.myWork).toHaveBeenLastCalledWith('assigned_issues', 1, expect.any(AbortSignal)))
  expect(api.syncMyWork).not.toHaveBeenCalled()
  await user.click(screen.getByRole('button', { name: 'Sync' }))
  await waitFor(() => expect(api.syncMyWork).toHaveBeenCalledTimes(1))
})

it.each([false, true])('keeps My work syncing until completion and allows retry (failure: %s)', async (failed) => {
  let complete!: () => void
  vi.mocked(api.syncMyWork).mockImplementationOnce(() => new Promise((resolve, reject) => {
    complete = () => failed ? reject(new Error('Some sections could not be refreshed.')) : resolve({ synced: true })
  }))
  const user = userEvent.setup()
  renderList('/my-work', true)
  await screen.findByText(/GitHub identity unavailable/)
  await user.click(screen.getByRole('button', { name: 'Sync' }))
  expect(screen.getByRole('button', { name: 'Syncing with GitHub…' })).toBeDisabled()
  expect(screen.queryByText('My work synchronized.')).not.toBeInTheDocument()
  await act(async () => complete())
  expect(await screen.findByText(failed ? 'Some sections could not be refreshed.' : 'My work synchronized.')).toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Sync' })).toBeEnabled()
  if (failed) expect(screen.queryByText('My work synchronized.')).not.toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'Sync' }))
  expect(await screen.findByText('My work synchronized.')).toBeInTheDocument()
})

it('distinguishes missing identity from a successful empty queue', async () => {
  vi.mocked(api.myWork).mockResolvedValue({ ...result, rows: [], total: 0 })
  const view = renderList('/my-work', true)
  expect(await screen.findByText(/GitHub identity unavailable/)).toBeInTheDocument()
  expect(screen.queryByText(/No matching work/)).not.toBeInTheDocument()
  view.unmount()
  vi.mocked(api.myWork).mockResolvedValue({ ...result, rows: [], total: 0, identity: { id: 'me', login: 'alice', permission: '', is_me: true }, sync: ['identity', 'issues', 'pull_requests'].map((section) => ({ section, attempted_at: row.updated_at, synced_at: row.updated_at })) })
  renderList('/my-work', true)
  expect(await screen.findByText('No matching work in the current cache.')).toBeInTheDocument()
})


it.each([
  { path: '/pulls?q=is%3Aall&page=1', personal: false },
  { path: '/my-work?view=review_requested&page=1', personal: true },
])('retains results when returning to $path and its refresh fails', async ({ path, personal }) => {
  const identity = { id: 'me', login: 'alice', permission: '', is_me: true }
  const load = vi.mocked(personal ? api.myWork : api.searchPullRequests)
  load.mockResolvedValue({ ...result, identity })
  const user = userEvent.setup()
  render(
    <MemoryRouter initialEntries={[path]}>
      <ProjectContext.Provider value={{ id: 'repo', name: 'Repo', default_branch: 'main' }}>
        <ProjectMemberStoreProvider projectId="repo">
          <PageCacheProvider>
            <Routes>
              <Route path={personal ? '/my-work' : '/pulls'} element={personal ? <MyWork /> : <PullRequestList />} />
              <Route path="/pulls/:id" element={<Link to={path}>Back to list</Link>} />
            </Routes>
          </PageCacheProvider>
        </ProjectMemberStoreProvider>
      </ProjectContext.Provider>
    </MemoryRouter>,
  )
  await user.click(await screen.findByRole('link', { name: 'Local WIP' }))
  load.mockRejectedValue(new Error('Return refresh failed'))
  await user.click(await screen.findByRole('link', { name: 'Back to list' }))
  expect(screen.getByText('Local WIP')).toBeInTheDocument()
  expect(screen.queryByText(/Loading cached/)).not.toBeInTheDocument()
  expect(await screen.findByRole('alert')).toHaveTextContent('Return refresh failed')
  expect(screen.getByText('Local WIP')).toBeInTheDocument()
})


it('applies advanced PR filters and cancels pending title edits on back and forward navigation', async () => {
  const user = userEvent.setup()
  function History() {
    const navigate = useNavigate()
    return <><button onClick={() => navigate(-1)}>Back</button><button onClick={() => navigate(1)}>Forward</button></>
  }
  const previous = '/pulls?q=is:closed&title=fix&page=2'
  const current = '/pulls?q=is:open&title=fix&page=3'
  render(<MemoryRouter initialEntries={[previous, current]} initialIndex={1}><ProjectContext.Provider value={{ id: 'repo', name: 'Repo', default_branch: 'main' }}><ProjectMemberStoreProvider projectId="repo"><PullRequestList /><History /><Location /></ProjectMemberStoreProvider></ProjectContext.Provider></MemoryRouter>)
  await screen.findByText('Local WIP')
  await user.click(screen.getByText('Advanced filters'))
  const editor = screen.getByRole('textbox', { name: 'Query expression' })
  await user.clear(editor)
  await user.type(editor, 'draft:true')
  const title = screen.getByRole('textbox', { name: 'Search pull requests' })
  await user.type(title, ' pending')
  await user.click(screen.getByRole('button', { name: 'Back' }))
  await act(async () => { await new Promise(resolve => setTimeout(resolve, 300)) })
  expect(screen.getByTestId('url')).toHaveTextContent(previous)
  expect(title).toHaveValue('fix')
  expect(editor).toHaveValue('is:closed')
  expect(api.searchPullRequests).toHaveBeenLastCalledWith('is:closed', 2, expect.any(AbortSignal), 'fix')
  await user.click(screen.getByRole('button', { name: 'Forward' }))
  expect(screen.getByTestId('url')).toHaveTextContent(current)
  expect(editor).toHaveValue('is:open')
  await user.clear(editor)
  await user.type(editor, 'is:all draft:true')
  expect(api.searchPullRequests).not.toHaveBeenCalledWith('is:all draft:true', expect.anything(), expect.anything(), expect.anything())
  await user.click(screen.getByRole('button', { name: 'Apply' }))
  await waitFor(() => expect(api.searchPullRequests).toHaveBeenLastCalledWith('is:all draft:true', 1, expect.any(AbortSignal), 'fix'))
})
