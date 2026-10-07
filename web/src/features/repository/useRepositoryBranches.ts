import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { api } from '../../data/api'
import type { RepositoryRefsResponse } from '../../data/types'

export function useRepositoryBranches(projectId: string, defaultBranch: string, enabled = true) {
  const [response, setResponse] = useState<RepositoryRefsResponse>()
  const refs = response?.refs
  const [loading, setLoading] = useState(false)
  const [refreshing, setRefreshing] = useState(false)
  const [error, setError] = useState('')
  const [refreshError, setRefreshError] = useState('')
  const refreshController = useRef<AbortController | undefined>(undefined)

  const applyResponse = useCallback((response: RepositoryRefsResponse) => {
    if (!Array.isArray(response.refs)) return
    setResponse(response)
  }, [])

  useEffect(() => {
    if (!enabled) return undefined
    let cancelled = false
    const controller = new AbortController()
    const request = api.repositoryRefs(projectId, controller.signal)
    void Promise.resolve().then(() => {
      if (cancelled) return
      setLoading(true)
      setError('')
    })
    void request.then(
      (response) => {
        if (!cancelled) applyResponse(response)
      },
      (value) => {
        if (!cancelled) setError(errorMessage(value, 'Branches could not be loaded.'))
      },
    ).finally(() => {
      if (!cancelled) setLoading(false)
    })
    return () => { cancelled = true; controller.abort() }
  }, [applyResponse, enabled, projectId])

  const refresh = useCallback(async () => {
    if (!enabled || refreshController.current) return
    const controller = new AbortController()
    refreshController.current = controller
    setRefreshing(true)
    setRefreshError('')
    try {
      const response = await api.refreshRepositoryRefs(projectId, controller.signal)
      if (controller.signal.aborted) return
      applyResponse(response)
      if (response.refresh_error) setRefreshError('Couldn’t refresh branches. Showing previously loaded branches.')
    } catch {
      if (!controller.signal.aborted) setRefreshError('Couldn’t refresh branches. Try again.')
    } finally {
      if (refreshController.current === controller) {
        refreshController.current = undefined
        setRefreshing(false)
      }
    }
  }, [applyResponse, enabled, projectId])

  useEffect(() => {
    return () => {
      refreshController.current?.abort()
      refreshController.current = undefined
      setRefreshing(false)
    }
  }, [enabled, projectId])

  const branches = useMemo(() => {
    const byName = new Map((refs ?? []).filter((ref) => ref.kind === 'branch').map((ref) => [ref.short_name, ref]))
    if (!byName.has(defaultBranch)) {
      byName.set(defaultBranch, {
        name: `refs/heads/${defaultBranch}`,
        short_name: defaultBranch,
        kind: 'branch',
        target: '',
        committed_at: '',
      })
    }
    return [...byName.values()]
  }, [defaultBranch, refs])

  return { response, branches, loading, refreshing, error: refreshError || error, refresh }
}

function errorMessage(value: unknown, fallback: string) {
  return value instanceof Error ? value.message : fallback
}
