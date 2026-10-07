import { createContext, useContext, useMemo } from 'react'
import { usePolling, type PollingCache } from './usePolling'

export const PageCacheContext = createContext<Map<string, unknown> | null>(null)

// Bound memory while retaining small page snapshots for return navigation.
const maxSnapshots = 24

export function useRetainedPolling<T>(key: string, loader: (signal: AbortSignal) => Promise<T>, interval = 3000) {
  const snapshots = useContext(PageCacheContext)
  const cache = useMemo<PollingCache<T> | undefined>(() => snapshots ? {
    read: () => snapshots.get(key) as T | undefined,
    write: (data) => {
      snapshots.delete(key)
      snapshots.set(key, data)
      if (snapshots.size > maxSnapshots) snapshots.delete(snapshots.keys().next().value!)
    },
  } : undefined, [snapshots, key])
  return usePolling(loader, interval, cache, true)
}
