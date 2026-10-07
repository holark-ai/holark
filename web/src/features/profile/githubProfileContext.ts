import { createContext, useContext } from 'react'
import type { GitHubProfile } from '../../data/types'
import type { PollingResult } from '../../data/usePolling'

export const GitHubProfileContext = createContext<PollingResult<GitHubProfile>>({
  data: undefined,
  loading: false,
  error: undefined,
  refresh: async () => {},
})

export function useGitHubProfile() {
  return useContext(GitHubProfileContext)
}
