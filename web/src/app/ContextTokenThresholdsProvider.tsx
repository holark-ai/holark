import { useCallback, useEffect, useMemo, useState, type ReactNode } from 'react'
import {
  ContextTokenThresholdsContext,
  contextTokenThresholdsStorageKey,
  defaultContextTokenThresholds,
  readStoredContextTokenThresholds,
  validContextTokenThresholds,
  type ContextTokenThresholds,
} from './contextTokenThresholds'

export function ContextTokenThresholdsProvider({ children }: { children: ReactNode }) {
  const [thresholds, setThresholdState] = useState<ContextTokenThresholds>(readStoredContextTokenThresholds)

  const setThresholds = useCallback((next: ContextTokenThresholds) => {
    if (!validContextTokenThresholds(next)) return
    setThresholdState(next)
    try {
      window.localStorage.setItem(contextTokenThresholdsStorageKey, JSON.stringify(next))
    } catch {
      // The preference still applies for this page when browser storage is unavailable.
    }
  }, [])

  useEffect(() => {
    const synchronizeThresholds = (event: StorageEvent) => {
      if (event.key !== contextTokenThresholdsStorageKey) return
      if (event.newValue === null) {
        setThresholdState(defaultContextTokenThresholds)
        return
      }
      try {
        const parsed: unknown = JSON.parse(event.newValue)
        if (validContextTokenThresholds(parsed)) setThresholdState(parsed)
      } catch {
        // Ignore malformed preferences written by another tab.
      }
    }
    window.addEventListener('storage', synchronizeThresholds)
    return () => window.removeEventListener('storage', synchronizeThresholds)
  }, [])

  const value = useMemo(() => ({ thresholds, setThresholds }), [setThresholds, thresholds])
  return <ContextTokenThresholdsContext.Provider value={value}>{children}</ContextTokenThresholdsContext.Provider>
}
