import { useState, type ReactNode } from 'react'
import { PageCacheContext } from './pageCache'

export function PageCacheProvider({ children }: { children: ReactNode }) {
  const [snapshots] = useState(() => new Map<string, unknown>())
  return <PageCacheContext.Provider value={snapshots}>{children}</PageCacheContext.Provider>
}
