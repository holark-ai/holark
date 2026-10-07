import { act, fireEvent, render, screen, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { api } from '../../data/api'
import type { Holon } from '../../data/types'
import { OperationsPanelView } from '../operations/OperationsPanelView'
import { OperationsProvider } from '../operations/OperationsProvider'
import { HolonStoreContext } from '../project/holonStoreContext'
import { HolonStoreProvider } from '../project/HolonStore'
import { HolonTile } from './HolonTile'

const holons: Holon[] = [
  { id: 'source', title: 'Source work', prompt: '', status: 'running', created_at: '2026-09-05T10:00:00Z', worktree_branch: 'feature' },
  { id: 'metadata', title: 'Generate PR summary', prompt: '', status: 'running', input_state: 'permission_required', activity: 'needs_input', created_at: '2026-09-05T10:01:00Z', worktree_branch: 'feature' },
  { id: 'failed', title: 'Failed review', prompt: '', status: 'failed', created_at: '2026-09-05T10:02:00Z' },
]

function Preview({ tileId = 'metadata', showTile = true, entries = holons }: { tileId?: string, showTile?: boolean, entries?: Holon[] }) {
  return (
    <MemoryRouter>
      <HolonStoreContext.Provider value={{ selectHolonTab: vi.fn(), activateHolon: vi.fn(), selectionErrors: {}, dismissSelectionError: () => {}, holons: entries, loading: false, error: undefined, getHolon: (id) => entries.find((holon) => holon.id === id), updateHolon: vi.fn(), refresh: vi.fn() }}>
        <OperationsProvider>
          {showTile && <HolonTile holonId={tileId} label="Metadata holon" />}
          <OperationsPanelView
            project={{ id: 'project', name: 'Project', default_branch: 'main' }}
            holons={entries}
            capabilities={[]}
            rename={{ begin: vi.fn(), change: vi.fn(), commit: vi.fn(), cancel: vi.fn() }}
            expanded={false}
            onNewAgent={vi.fn()}
            onReopen={vi.fn()}
          />
        </OperationsProvider>
      </HolonStoreContext.Provider>
    </MemoryRouter>
  )
}

beforeEach(() => { vi.useFakeTimers() })
afterEach(() => { vi.restoreAllMocks(); vi.useRealTimers() })

it('shows a neutral skeleton until the missing Holon loads without waiting for the next list poll', async () => {
  let finishLoading!: (holon: Holon) => void
  vi.spyOn(api, 'holons').mockResolvedValue([holons[0]])
  const loadHolon = vi.spyOn(api, 'holon').mockImplementation(() => new Promise((resolve) => { finishLoading = resolve }))
  render(
    <MemoryRouter>
      <HolonStoreProvider projectId="project">
        <HolonTile holonId="metadata" label="Metadata holon" />
      </HolonStoreProvider>
    </MemoryRouter>,
  )
  await act(async () => { await vi.advanceTimersByTimeAsync(0) })

  const tile = screen.getByRole('link', { name: 'Metadata holon: Loading holon' })
  expect(tile).toHaveAttribute('aria-busy', 'true')
  expect(tile.textContent).toBe('')
  expect(loadHolon).toHaveBeenCalledWith('metadata')

  await act(async () => { finishLoading(holons[1]) })
  expect(tile).toHaveAttribute('aria-busy', 'false')
  expect(within(tile).getByText(holons[1].title!)).toBeInTheDocument()
  expect(within(tile).getByText('Permission required')).toBeInTheDocument()
})

it('waits for hover intent, scrolls only the operations list, and preserves focus and scroll position on leave', () => {
  render(<Preview />)
  const panel = screen.getByRole('complementary', { name: 'Holons' })
  const tile = screen.getByRole('link', { name: /Metadata holon/ })
  const rowLink = within(panel).getByRole('link', { name: /Agent B, Generate PR summary, Permission required/ })
  const row = rowLink.closest('article')!
  const list = within(panel).getByRole('region', { name: 'Holon list' })
  const scrollTo = vi.fn(({ top }: ScrollToOptions) => { list.scrollTop = top! })
  Object.defineProperty(list, 'scrollTo', { value: scrollTo })
  vi.spyOn(list, 'getBoundingClientRect').mockReturnValue({ top: 50, bottom: 300 } as DOMRect)
  vi.spyOn(row, 'getBoundingClientRect').mockReturnValue({ top: 600, bottom: 652 } as DOMRect)
  const scrollIntoView = vi.spyOn(Element.prototype, 'scrollIntoView')
  const focused = document.activeElement

  expect(within(tile).getByText('B')).toBeInTheDocument()
  expect(within(tile).getByText('Permission required')).toBeInTheDocument()
  expect(tile).toHaveAttribute('href', '/holons/metadata')
  fireEvent.mouseEnter(tile)
  act(() => vi.advanceTimersByTime(199))
  expect(scrollTo).not.toHaveBeenCalled()
  act(() => vi.advanceTimersByTime(1))
  expect(scrollTo).toHaveBeenCalled()
  expect(list.scrollTop).toBeGreaterThan(0)
  const revealedPosition = list.scrollTop
  expect(row).toHaveAttribute('data-previewed', 'true')
  expect(panel).toHaveAttribute('data-expanded', 'false')
  expect(document.activeElement).toBe(focused)
  expect(scrollIntoView).not.toHaveBeenCalled()

  fireEvent.mouseLeave(tile)
  expect(row).not.toHaveAttribute('data-previewed')
  expect(list.scrollTop).toBe(revealedPosition)
})

it('ignores passing hover and clears the highlight when the tile unmounts', () => {
  const view = render(<Preview />)
  const tile = screen.getByRole('link', { name: /Metadata holon/ })
  fireEvent.mouseEnter(tile)
  act(() => vi.advanceTimersByTime(100))
  fireEvent.mouseLeave(tile)
  act(() => vi.advanceTimersByTime(200))
  expect(document.querySelector('[data-previewed]')).toBeNull()

  fireEvent.mouseEnter(tile)
  act(() => vi.advanceTimersByTime(200))
  expect(document.querySelector('[data-previewed]')).not.toBeNull()
  view.rerender(<Preview showTile={false} />)
  expect(document.querySelector('[data-previewed]')).toBeNull()
})

it('highlights the status row on keyboard focus and leaves it available after blur', () => {
  render(<Preview />)
  const panel = screen.getByRole('complementary', { name: 'Holons' })
  expect(within(panel).getByRole('link', { name: /Generate PR summary/ })).toBeInTheDocument()
  const tile = screen.getByRole('link', { name: /Metadata holon/ })
  act(() => tile.focus())
  act(() => vi.advanceTimersByTime(200))
  expect(within(panel).getByRole('link', { name: /Generate PR summary/ }).closest('article')).toHaveAttribute('data-previewed', 'true')
  expect(panel).toHaveAttribute('data-expanded', 'false')
  expect(tile).toHaveFocus()
  act(() => tile.blur())
  expect(within(panel).getByRole('link', { name: /Generate PR summary/ })).toBeInTheDocument()
  expect(document.querySelector('[data-previewed]')).toBeNull()
})

it('keeps ended holons closed when hovering a failed holon tile', () => {
  render(<Preview tileId="failed" />)
  const panel = screen.getByRole('complementary', { name: 'Holons' })
  expect(within(panel).queryByRole('link', { name: /Failed review/ })).toBeNull()
  const tile = screen.getByRole('link', { name: /Metadata holon/ })
  fireEvent.mouseEnter(tile)
  act(() => vi.advanceTimersByTime(200))
  expect(within(panel).queryByRole('link', { name: /Failed review/ })).toBeNull()
  expect(within(panel).getByRole('button', { name: 'Show ended holons (1)' })).toBeInTheDocument()
  expect(panel).toHaveAttribute('data-expanded', 'false')
  fireEvent.mouseLeave(tile)
  expect(within(panel).getByRole('button', { name: 'Show ended holons (1)' })).toBeInTheDocument()
})

it('shares preparation and finalization precedence with the sidebar and restores settled display', () => {
  const base: Holon = { ...holons[0], id: 'phase', title: 'Phase work', worktree_branch: undefined, status: 'preparing', agent_sessions: [], manual_terminals: [] }
  const view = render(<Preview tileId="phase" entries={[base]} />)
  const tile = () => screen.getByRole('link', { name: /Metadata holon/ })
  const sidebar = () => within(screen.getByRole('complementary', { name: 'Holons' }))
  expect(tile()).toHaveTextContent('Preparing')
  expect(sidebar().getByRole('link', { name: /Phase work, Preparing/ })).toBeInTheDocument()

  const initializing = { ...base, status: 'running' as const, activity: 'starting' as const }
  view.rerender(<Preview tileId="phase" entries={[initializing]} />)
  expect(tile()).toHaveTextContent('Preparing')
  const finalizing: Holon = { ...initializing, application_phase: 'finalizing', activity: 'needs_input', input_state: 'permission_required', reason: 'Publication needs retry.' }
  view.rerender(<Preview tileId="phase" entries={[finalizing]} />)
  expect(tile()).toHaveTextContent('Finalizing · Permission required')
  expect(tile()).toHaveTextContent('Publication needs retry.')
  expect(sidebar().getByRole('link', { name: /Phase work, Finalizing · Permission required/ })).toBeInTheDocument()

  view.rerender(<Preview tileId="phase" entries={[{ ...finalizing, status: 'cancelling' }]} />)
  expect(tile()).toHaveTextContent('Cancelling')
  expect(tile()).not.toHaveTextContent('Finalizing')
  const retiring: Holon = { ...finalizing, status: 'expired', activity: 'idle', input_state: 'none', archived_at: base.created_at, worktree_path: '/tmp/work' }
  view.rerender(<Preview tileId="phase" entries={[retiring]} />)
  expect(tile()).toHaveTextContent('Finalizing')
  expect(sidebar().getByRole('link', { name: /Phase work, Finalizing/ })).toBeInTheDocument()
  view.rerender(<Preview tileId="phase" entries={[{ ...retiring, application_phase: undefined }]} />)
  expect(tile()).toHaveTextContent('Workspace archived.')
  expect(sidebar().queryByRole('link', { name: /Phase work/ })).toBeNull()
})
