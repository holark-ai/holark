import { createContext } from 'react'
import type { PullRequest } from '../../data/types'
import type { PollingResult } from '../../data/usePolling'

export type HolonPreview = { holonId: string }

export const OperationsContext = createContext<{
  addresses: ReadonlyMap<string, string>
  pullRequestByHolon?: ReadonlyMap<string, PullRequest>
  pullRequestList?: PollingResult<PullRequest[]>
  markPullRequestMerged?: (id: string) => void
  preview: HolonPreview | undefined
  showPreview: (preview: HolonPreview) => void
  clearPreview: (preview: HolonPreview) => void
} | null>(null)
