import { act, renderHook } from '@testing-library/react'
import { usePolling } from './usePolling'

afterEach(() => {
  vi.useRealTimers()
})

it('waits for the current refresh before scheduling the next poll', async () => {
  vi.useFakeTimers()
  const pending: Array<(value: number) => void> = []
  const loader = vi.fn(() => new Promise<number>((resolve) => pending.push(resolve)))

  renderHook(() => usePolling(loader, 3000))

  await act(async () => {
    await vi.runOnlyPendingTimersAsync()
  })
  expect(loader).toHaveBeenCalledTimes(1)

  await act(async () => {
    await vi.advanceTimersByTimeAsync(9000)
  })
  expect(loader).toHaveBeenCalledTimes(1)

  await act(async () => {
    pending[0](1)
  })

  await act(async () => {
    await vi.advanceTimersByTimeAsync(2999)
  })
  expect(loader).toHaveBeenCalledTimes(1)

  await act(async () => {
    await vi.advanceTimersByTimeAsync(1)
  })
  expect(loader).toHaveBeenCalledTimes(2)

  await act(async () => {
    await vi.advanceTimersByTimeAsync(6000)
  })
  expect(loader).toHaveBeenCalledTimes(2)
})

it('does not let an older refresh overwrite a newer result', async () => {
  vi.useFakeTimers()
  const pending: Array<(value: number) => void> = []
  const loader = vi.fn(() => new Promise<number>((resolve) => pending.push(resolve)))
  const { result } = renderHook(() => usePolling(loader, 3000))

  await act(async () => {
    await vi.runOnlyPendingTimersAsync()
  })
  expect(loader).toHaveBeenCalledTimes(1)

  act(() => {
    void result.current.refresh()
  })
  expect(loader).toHaveBeenCalledTimes(2)

  await act(async () => {
    pending[1](2)
  })
  expect(result.current.data).toBe(2)

  await act(async () => {
    pending[0](1)
  })
  expect(result.current.data).toBe(2)
})
