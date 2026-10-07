import { useCallback, useMemo, useState, type ReactNode } from 'react'
import { useHolonStore } from '../project/holonStoreContext'
import { OperationsContext, type HolonPreview } from './operationsContext'
import { useHolonAddresses } from './useHolonAddresses'
import { usePolling } from '../../data/usePolling'
import { buildPullRequestTracking } from './pullRequestTracking'
import { api } from '../../data/api'
import type { PullRequest } from '../../data/types'

export function OperationsProvider({ children, projectId = 'repository' }: { children: ReactNode, projectId?: string }) {
  const { holons } = useHolonStore()
  const loadPullRequests = useCallback(() => api.pullRequests(projectId), [projectId])
  const pullRequests = usePolling(loadPullRequests)
  const loadLinks = useCallback(() => api.pullRequestSessionLinks(projectId), [projectId])
  const links = usePolling(loadLinks)
  const refreshPullRequests = useCallback(() => {
    // Refresh sidebar links too, but let the list finish independently.
    void links.refresh()
    return pullRequests.refresh()
  }, [links.refresh, pullRequests.refresh])
  const [mergedPullRequestIds, setMergedPullRequestIds] = useState<ReadonlySet<string>>(() => new Set())
  const markPullRequestMerged = useCallback((id: string) => {
    setMergedPullRequestIds((current) => current.has(id) ? current : new Set(current).add(id))
  }, [])
  const tracking = useMemo(() => {
    const data = pullRequests.data && buildPullRequestTracking(pullRequests.data, links.data ?? [])
    if (!data || mergedPullRequestIds.size === 0) return data
    // Merge is terminal: a sidebar poll started before confirmation must not
    // briefly restore Open while Holon links are still loading.
    const update = (pr: PullRequest): PullRequest => mergedPullRequestIds.has(pr.id) ? { ...pr, status: 'merged' } : pr
    return {
      pullRequests: data.pullRequests.map(update),
      pullRequestByHolon: new Map([...data.pullRequestByHolon].map(([id, pr]) => [id, update(pr)])),
    }
  }, [pullRequests.data, links.data, mergedPullRequestIds])
  // Keep the list available across route changes and share the sidebar's poll.
  const pullRequestList = useMemo(() => ({
    data: tracking?.pullRequests,
    loading: pullRequests.loading,
    error: pullRequests.error,
    refresh: refreshPullRequests,
  }), [tracking, pullRequests.loading, pullRequests.error, refreshPullRequests])
  const pullRequestByHolon = useMemo(() => {
    const result = new Map(tracking?.pullRequestByHolon ?? [])
    const byId = new Map((tracking?.pullRequests ?? []).map((pullRequest) => [pullRequest.id, pullRequest]))
    for (const holon of holons) {
      if (!holon.pull_request_id) continue
      const pullRequest = byId.get(holon.pull_request_id)
      if (pullRequest) result.set(holon.id, pullRequest)
    }
    return result
  }, [holons, tracking])
  const addresses = useHolonAddresses(holons, pullRequestByHolon)
  const [preview, showPreview] = useState<HolonPreview>()
  const clearPreview = useCallback((previous: HolonPreview) => {
    showPreview((current) => current === previous ? undefined : current)
  }, [])
  const value = useMemo(() => ({ addresses, pullRequestByHolon, pullRequestList, markPullRequestMerged, preview, showPreview, clearPreview }), [addresses, pullRequestByHolon, pullRequestList, markPullRequestMerged, preview, clearPreview])
  return <OperationsContext.Provider value={value}>{children}</OperationsContext.Provider>
}
