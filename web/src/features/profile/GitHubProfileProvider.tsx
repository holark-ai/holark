import type { ReactNode } from 'react'
import { api } from '../../data/api'
import { usePolling } from '../../data/usePolling'
import { GitHubProfileContext } from './githubProfileContext'

export function GitHubProfileProvider({ children }: { children: ReactNode }) {
  const profile = usePolling(api.githubProfile, 5 * 60_000)
  return <GitHubProfileContext.Provider value={profile}>{children}</GitHubProfileContext.Provider>
}
