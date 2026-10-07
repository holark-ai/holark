import { act, renderHook } from '@testing-library/react'
import { api } from './api'
import { AgentCapabilitiesProvider } from './AgentCapabilitiesProvider'
import { useAgentCapabilities } from './useAgentCapabilities'
import type { HarnessCapabilities } from './types'

afterEach(() => {
  vi.restoreAllMocks()
  vi.useRealTimers()
})

it('shares one load across consumers, keeps saved defaults in sync, and never polls', async () => {
  vi.useFakeTimers()
  const capabilities: HarnessCapabilities = Object.assign([], { default_harness: 'codex' as const })
  const request = vi.spyOn(api, 'agentCapabilities').mockResolvedValue(capabilities)
  const { result, rerender } = renderHook(() => [useAgentCapabilities(), useAgentCapabilities()], {
    wrapper: AgentCapabilitiesProvider,
  })
  await act(async () => { await result.current[0].load() })
  expect(request).toHaveBeenCalledTimes(1)
  expect(result.current[0].data).toBe(capabilities)
  expect(result.current[1].data).toBe(capabilities)
  act(() => result.current[0].setDefaultHarness('opencode'))
  expect(result.current[1].data?.default_harness).toBe('opencode')
  rerender()
  await act(async () => { await vi.advanceTimersByTimeAsync(120_000) })
  expect(request).toHaveBeenCalledTimes(1)
})
