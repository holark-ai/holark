import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { useState } from 'react'
import type { Holon } from '../../data/types'
import { HolonStoreProvider } from './HolonStore'
import { useHolonStore } from './holonStoreContext'

const runningHolon: Holon = {
  id: 'holon-1',
  repository_id: 'holark',
  runtime_id: 'node-1',
  prompt: 'Live task',
  status: 'running',
  input_state: 'none',
  created_at: '2026-08-02T10:00:00.000Z',
}

afterEach(() => vi.unstubAllGlobals())

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((next) => { resolve = next })
  return { promise, resolve }
}

function response(holons: Holon[]) {
  return new Response(JSON.stringify(holons), { status: 200, headers: { 'Content-Type': 'application/json' } })
}

function StoreProbe() {
  const store = useHolonStore()
  const [, renderAgain] = useState(0)
  return (
    <section aria-label="Holon store">
      <span data-testid="loading">{String(store.loading)}</span>
      <span data-testid="error">{store.error?.message ?? ''}</span>
      <span data-testid="selection">{store.getHolon('holon-1')?.last_selected_tab_id ?? ''}</span>
      <button type="button" onClick={() => store.selectHolonTab('holon-1', 'terminal-1')}>Select tab</button>
      <button type="button" onClick={() => store.selectHolonTab('holon-1', 'terminal-2')}>Select second tab</button>
      <ul>
        {store.holons.map((holon) => (
          <li key={holon.id}>{holon.id}:{holon.status}:{holon.input_state}</li>
        ))}
      </ul>
      <button type="button" onClick={() => store.updateHolon('holon-1', (holon) => holon ? { ...holon, input_state: 'permission_required' } : holon)}>Live update</button>
      <button type="button" onClick={() => { void store.refresh() }}>Refresh</button>
      <button type="button" onClick={() => renderAgain((value) => value + 1)}>{store.getHolon('holon-1')?.status ?? 'missing'}</button>
    </section>
  )
}

it('protects live holon revisions from an older in-flight poll while reconciling other holons', async () => {
  const stalePoll = deferred<Response>()
  const secondHolon = { ...runningHolon, id: 'holon-2', prompt: 'Background task', input_state: 'task_complete' as const }
  const discoveredHolon = { ...runningHolon, id: 'holon-3', prompt: 'Discovered task', status: 'queued' as const }
  let request = 0
  const fetchMock = vi.fn(async () => {
    request += 1
    if (request === 1) {
      return response([
        runningHolon,
        secondHolon,
        { ...runningHolon, id: 'other-project', repository_id: 'elsewhere' },
      ])
    }
    if (request === 2) return stalePoll.promise
    return response([{ ...runningHolon, status: 'completed', input_state: 'task_complete' }, discoveredHolon])
  })
  vi.stubGlobal('fetch', fetchMock)

  render(<HolonStoreProvider projectId="holark" interval={60_000}><StoreProbe /></HolonStoreProvider>)

  const store = screen.getByRole('region', { name: 'Holon store' })
  expect(await within(store).findByText('holon-1:running:none')).toBeInTheDocument()
  expect(within(store).getByText('holon-2:running:task_complete')).toBeInTheDocument()
  expect(within(store).getByText(/other-project/)).toBeInTheDocument()
  expect(screen.getByTestId('loading')).toHaveTextContent('false')

  fireEvent.click(within(store).getByRole('button', { name: 'Refresh' }))
  await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2))
  fireEvent.click(within(store).getByRole('button', { name: 'Live update' }))
  expect(within(store).getByText('holon-1:running:permission_required')).toBeInTheDocument()

  await act(async () => {
    stalePoll.resolve(response([
      { ...runningHolon, status: 'queued' },
      { ...secondHolon, status: 'completed', input_state: 'task_complete' },
    ]))
    await stalePoll.promise
  })

  expect(within(store).getByText('holon-1:running:permission_required')).toBeInTheDocument()
  expect(within(store).getByText('holon-2:completed:task_complete')).toBeInTheDocument()

  fireEvent.click(within(store).getByRole('button', { name: 'Refresh' }))
  expect(await within(store).findByText('holon-1:completed:task_complete')).toBeInTheDocument()
  expect(within(store).getByText('holon-3:queued:none')).toBeInTheDocument()
  expect(within(store).queryByText(/holon-2:/)).not.toBeInTheDocument()
})

it('exposes polling failures and recovers through manual refresh', async () => {
  let attempts = 0
  const fetchMock = vi.fn(async () => {
    if (++attempts === 1) throw new Error('Sessions unavailable.')
    return response([runningHolon])
  })
  vi.stubGlobal('fetch', fetchMock)

  render(<HolonStoreProvider projectId="holark" interval={60_000}><StoreProbe /></HolonStoreProvider>)

  await waitFor(() => expect(screen.getByTestId('loading')).toHaveTextContent('false'))
  expect(screen.getByTestId('error')).toHaveTextContent('Sessions unavailable.')

  fireEvent.click(screen.getByRole('button', { name: 'Refresh' }))

  expect(await screen.findByText('holon-1:running:none')).toBeInTheDocument()
  expect(screen.getByTestId('error')).toBeEmptyDOMElement()
})

it('saves the latest tab choice without letting an older poll overwrite it', async () => {
  const holon: Holon = {
    ...runningHolon,
    manual_terminals: ['terminal-1', 'terminal-2'].map((id) => ({
      id, title: id, cwd: '/tmp', created_at: runningHolon.created_at, updated_at: runningHolon.created_at,
    })),
  }
  const firstSave = deferred<Response>()
  const latestSave = deferred<Response>()
  const stalePoll = deferred<Response>()
  const save = vi.fn<(init?: RequestInit) => Promise<Response>>()
    .mockReturnValueOnce(firstSave.promise)
    .mockReturnValueOnce(latestSave.promise)
  const poll = vi.fn().mockResolvedValueOnce(response([holon])).mockReturnValueOnce(stalePoll.promise)
  vi.stubGlobal('fetch', vi.fn((url: string, init?: RequestInit) => url.endsWith('/selected-tab') ? save(init) : poll()))
  render(<HolonStoreProvider projectId="holark" interval={60_000}><StoreProbe /></HolonStoreProvider>)
  await screen.findByText('holon-1:running:none')

  fireEvent.click(screen.getByRole('button', { name: 'Select tab' }))
  fireEvent.click(screen.getByRole('button', { name: 'Select second tab' }))
  expect(screen.getByTestId('selection')).toHaveTextContent('terminal-2')
  expect(save).toHaveBeenCalledTimes(1)
  // Start the poll after both clicks, so it must be protected even after the saves finish.
  fireEvent.click(screen.getByRole('button', { name: 'Refresh' }))
  await act(async () => { firstSave.resolve(new Response(null, { status: 204 })) })
  expect(save).toHaveBeenCalledTimes(2)
  expect(save).toHaveBeenLastCalledWith(expect.objectContaining({ method: 'PUT', body: JSON.stringify({ tab_id: 'terminal-2' }) }))
  await act(async () => { latestSave.resolve(new Response(null, { status: 204 })) })
  await act(async () => { stalePoll.resolve(response([{ ...holon, input_state: 'permission_required' }])) })
  expect(screen.getByTestId('selection')).toHaveTextContent('terminal-2')
  expect(screen.getByText('holon-1:running:permission_required')).toBeInTheDocument()
})
