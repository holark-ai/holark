import { act, fireEvent, render, screen, within } from '@testing-library/react'
import { api } from '../../data/api'
import type { RepositoryCommit } from '../../data/types'
import { ProjectContext } from '../project/ProjectContext'
import { RepositoryCommitsView } from './RepositoryCommitsView'

const commits: RepositoryCommit[] = Array.from({ length: 65 }, (_, i) => ({
  sha: (1000 - i).toString(16).padStart(40, '0'), message: `Change ${i}`,
  author_name: 'Ada', authored_at: '2026-10-01T12:00:00Z',
}))

afterEach(() => { vi.useRealTimers(); vi.restoreAllMocks() })

it('appends older pages in the same day, retries failures, and preserves the selected commit and scroll position', async () => {
  vi.useFakeTimers()
  const request = vi.spyOn(api, 'repositoryCommits')
    .mockResolvedValueOnce({ commits: commits.slice(0, 50), next_cursor: 'page-two' })
    .mockRejectedValueOnce(new Error('offline'))
  let finishPage!: (value: { commits: RepositoryCommit[] }) => void
  request.mockImplementationOnce(() => new Promise(resolve => { finishPage = resolve }))
  render(<ProjectContext.Provider value={{ id: 'repo', name: 'Repo', default_branch: 'main' }}>
    <RepositoryCommitsView refName="main" selectedCommit={commits[12].sha} onSelectCommit={() => {}} />
  </ProjectContext.Provider>)
  await act(async () => { await vi.advanceTimersByTimeAsync(1) })
  const history = screen.getByLabelText('Commit history')
  expect(within(history).getAllByRole('button')).toHaveLength(50)
  Object.defineProperties(history, {
    clientHeight: { configurable: true, value: 400 },
    scrollHeight: { configurable: true, get: () => within(history).getAllByRole('button').length * 52 },
  })
  history.scrollTop = 2050
  await act(async () => { fireEvent.scroll(history) })
  expect(screen.getByText('Could not load older commits.')).toBeInTheDocument()
  fireEvent.scroll(history)
  expect(request).toHaveBeenCalledTimes(2)
  fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
  expect(screen.getByText('Loading older commits…')).toBeInTheDocument()
  fireEvent.scroll(history)
  expect(request).toHaveBeenCalledTimes(3)
  await act(async () => { finishPage({ commits: commits.slice(50) }) })
  expect(within(history).getAllByRole('button')).toHaveLength(65)
  expect(within(history).getAllByRole('heading')).toHaveLength(1)
  expect(within(history).getByRole('heading')).toHaveTextContent('65')
  expect(within(history).getByRole('button', { pressed: true })).toHaveTextContent('Change 12')
  expect(history.scrollTop).toBe(2050)
  expect(screen.getByText('End of history')).toBeInTheDocument()
})

it('offers new commits without replacing the history until the user refreshes', async () => {
  vi.useFakeTimers()
  const arrived = { ...commits[0], sha: 'f'.repeat(40), message: 'New arrival' }
  const request = vi.spyOn(api, 'repositoryCommits').mockResolvedValueOnce({ commits: commits.slice(0, 50), next_cursor: 'older' })
    .mockResolvedValue({ commits: [arrived, ...commits.slice(0, 49)], next_cursor: 'new-older' })
  const select = vi.fn()
  render(<ProjectContext.Provider value={{ id: 'repo', name: 'Repo', default_branch: 'main' }}>
    <RepositoryCommitsView refName="main" onSelectCommit={select} />
  </ProjectContext.Provider>)
  await act(async () => { await vi.advanceTimersByTimeAsync(1) })
  const history = screen.getByLabelText('Commit history')
  history.scrollTop = 100
  await act(async () => { await vi.advanceTimersByTimeAsync(7000) })
  expect(request).toHaveBeenCalledTimes(2)
  expect(within(history).queryByText('New arrival')).not.toBeInTheDocument()
  expect(history.scrollTop).toBe(100)
  fireEvent.click(screen.getByRole('button', { name: 'New commits · Refresh history' }))
  expect(within(history).getByText('New arrival')).toBeInTheDocument()
  expect(history.scrollTop).toBe(0)
  expect(select).toHaveBeenCalledWith(arrived.sha)
})
