import { createContext, useCallback, useContext, useEffect, useRef, useState } from 'react'
import { api } from './api'
import type { HarnessCapabilities, HarnessType, HarnessWorkflow } from './types'

// Share capability requests across consumers and refresh on window activation or settings saves.
export function useCapabilityState(enabled = true) {
  const [data, setData] = useState<HarnessCapabilities>()
  const [error, setError] = useState<Error>()
  const [loading, setLoading] = useState(true)
  const request = useRef<Promise<HarnessCapabilities> | undefined>(undefined)
  const generation = useRef(0)
  const load = useCallback((refresh = false) => {
    if (refresh) request.current = undefined
    if (!request.current) {
      const currentGeneration = ++generation.current
      setLoading(true)
      setError(undefined)
      request.current = api.agentCapabilities().then((value) => {
        if (generation.current === currentGeneration) {
          setData(value)
          setLoading(false)
        }
        return value
      }, (value: unknown) => {
        if (generation.current === currentGeneration) {
          request.current = undefined
          setError(value instanceof Error ? value : new Error('Agent capabilities are unavailable.'))
          setLoading(false)
        }
        throw value
      })
    }
    return request.current
  }, [])
  useEffect(() => {
    if (!enabled) return
    void load().catch(() => {})
    // Shared launch commands may have changed in another repository's window.
    const refreshIfVisible = () => {
      if (document.visibilityState !== 'hidden') void load(true).catch(() => {})
    }
    window.addEventListener('focus', refreshIfVisible)
    document.addEventListener('visibilitychange', refreshIfVisible)
    return () => {
      window.removeEventListener('focus', refreshIfVisible)
      document.removeEventListener('visibilitychange', refreshIfVisible)
    }
  }, [enabled, load])
  const setDefaultHarness = useCallback((harnessType: HarnessType, model = '', permissions = '') => {
    setData((current) => {
      if (!current) return current
      const defaults = { ...current.harness_defaults }
      defaults.default = { harness_type: harnessType, model, permissions, explicit: true }
      for (const [workflow, preference] of Object.entries(defaults)) {
        if (workflow !== 'default' && !preference?.explicit) defaults[workflow as HarnessWorkflow] = { harness_type: harnessType, model, permissions, explicit: false }
      }
      return Object.assign([...current], { default_harness: harnessType, default_harness_explicit: true, harness_defaults: defaults })
    })
  }, [])
  const setHarnessDefault = useCallback((workflow: HarnessWorkflow, harnessType?: HarnessType, model = '', permissions = '') => {
    if (workflow === 'default' && harnessType) {
      setDefaultHarness(harnessType, model, permissions)
      return
    }
    setData((current) => {
      if (!current) return current
      const defaults = { ...current.harness_defaults }
      const fallback = defaults.default?.harness_type ?? current.default_harness ?? 'codex'
      defaults[workflow] = { harness_type: harnessType ?? fallback, model: harnessType ? model : defaults.default?.model, permissions: harnessType ? permissions : defaults.default?.permissions, explicit: Boolean(harnessType) }
      return Object.assign([...current], {
        default_harness: current.default_harness,
        default_harness_explicit: current.default_harness_explicit,
        harness_defaults: defaults,
      })
    })
  }, [setDefaultHarness])
  return { data, error, loading, load, setDefaultHarness, setHarnessDefault }
}

export const AgentCapabilitiesContext = createContext<ReturnType<typeof useCapabilityState> | undefined>(undefined)

export function useAgentCapabilities(enabled = true) {
  const shared = useContext(AgentCapabilitiesContext)
  const local = useCapabilityState(!shared && enabled)
  return shared ?? local
}
