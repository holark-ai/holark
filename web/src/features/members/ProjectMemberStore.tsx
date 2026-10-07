import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { api } from '../../data/api'
import type { ProjectMember } from '../../data/types'
import { ProjectMemberContext, type ProjectMemberState, type ProjectMemberStoreValue } from './projectMemberContext'

const MEMBER_RESOLUTION_BATCH_SIZE = 100
const MEMBER_RESOLUTION_RETRY_DELAYS_MS = [1_000, 5_000, 30_000] as const

export function ProjectMemberStoreProvider({ projectId, children }: {
  projectId: string
  children: ReactNode
}) {
  const unavailable = useRef(new Set<string>())
  const members = useRef(new Map<string, ProjectMember>())
  const missing = useRef(new Set<string>())
  const queued = useRef(new Set<string>())
  const inFlight = useRef(new Set<string>())
  const flushScheduled = useRef(false)
  const active = useRef(true)
  const retryAttempts = useRef(new Map<string, number>())
  const retryTimers = useRef(new Map<string, ReturnType<typeof setTimeout>>())
  const flushRef = useRef<() => void>(() => {})
  const [revision, setRevision] = useState(0)

  useEffect(() => {
    active.current = true
    return () => {
      active.current = false
      retryTimers.current.forEach((timer) => clearTimeout(timer))
      retryTimers.current.clear()
    }
  }, [])

  const clearRetry = useCallback((id: string) => {
    const timer = retryTimers.current.get(id)
    if (timer !== undefined) clearTimeout(timer)
    retryTimers.current.delete(id)
    retryAttempts.current.delete(id)
  }, [])

  const mergeMembers = useCallback((loaded: ProjectMember[]) => {
    if (!active.current || loaded.length === 0) return
    for (const member of loaded) {
      members.current.set(member.id, member)
      missing.current.delete(member.id)
      unavailable.current.delete(member.id)
      clearRetry(member.id)
    }
    setRevision((revision) => revision + 1)
  }, [clearRetry])

  const flush = useCallback(async () => {
    flushScheduled.current = false
    const queuedIDs = [...queued.current]
    queued.current.clear()
    if (queuedIDs.length === 0 || !active.current) return
    await Promise.all(Array.from({ length: Math.ceil(queuedIDs.length / MEMBER_RESOLUTION_BATCH_SIZE) }, async (_, index) => {
      const ids = queuedIDs.slice(index * MEMBER_RESOLUTION_BATCH_SIZE, (index + 1) * MEMBER_RESOLUTION_BATCH_SIZE)
      ids.forEach((id) => inFlight.current.add(id))
      try {
        const resolution = await api.resolveProjectMembers(projectId, ids)
        if (!active.current) return
        for (const id of ids) {
          clearRetry(id)
          unavailable.current.delete(id)
        }
        const resolvedIDs = new Set(resolution.members.map((member) => member.id))
        mergeMembers(resolution.members)
        for (const id of resolution.missing_ids) {
          if (!members.current.has(id)) missing.current.add(id)
        }
        for (const id of ids) {
          if (!resolvedIDs.has(id) && !members.current.has(id)) missing.current.add(id)
        }
        setRevision((revision) => revision + 1)
      } catch {
        if (!active.current) return
        for (const id of ids) {
          if (!members.current.has(id)) unavailable.current.add(id)
          const attempt = retryAttempts.current.get(id) ?? 0
          const delay = MEMBER_RESOLUTION_RETRY_DELAYS_MS[attempt]
          if (delay === undefined || retryTimers.current.has(id)) continue
          retryAttempts.current.set(id, attempt + 1)
          const timer = setTimeout(() => {
            retryTimers.current.delete(id)
            if (!active.current || members.current.has(id) || missing.current.has(id)) return
            queued.current.add(id)
            if (flushScheduled.current) return
            flushScheduled.current = true
            queueMicrotask(() => { flushRef.current() })
          }, delay)
          retryTimers.current.set(id, timer)
        }
        setRevision((revision) => revision + 1)
      } finally {
        ids.forEach((id) => inFlight.current.delete(id))
      }
    }))
  }, [clearRetry, mergeMembers, projectId])

  useEffect(() => {
    flushRef.current = flush
  }, [flush])

  const requestMembers = useCallback((memberIds: string[]) => {
    for (const rawID of memberIds) {
      const id = rawID.trim()
      if (!id || members.current.has(id) || missing.current.has(id) || inFlight.current.has(id)) continue
      if (unavailable.current.has(id)) {
        if (retryTimers.current.has(id)) continue
        retryAttempts.current.delete(id)
      }
      queued.current.add(id)
    }
    if (queued.current.size === 0 || flushScheduled.current) return
    flushScheduled.current = true
    queueMicrotask(() => { void flush() })
  }, [flush])

  const searchMembers = useCallback(async (query: string, limit = 20) => {
    const response = await api.projectMemberSearch(projectId, query.trim(), limit)
    mergeMembers(response.members)
    return response.members
  }, [mergeMembers, projectId])

  const getMemberState = useCallback((rawID: string): ProjectMemberState => {
    const id = rawID.trim()
    const member = members.current.get(id)
    if (member) return { status: 'resolved', member }
    if (!id || missing.current.has(id)) return { status: 'missing' }
    if (unavailable.current.has(id)) return { status: 'unavailable' }
    return { status: 'loading' }
  }, [])

  const value = useMemo<ProjectMemberStoreValue>(() => ({
    revision,
    getMemberState,
    requestMembers,
    searchMembers,
  }), [getMemberState, requestMembers, revision, searchMembers])

  return <ProjectMemberContext.Provider value={value}>{children}</ProjectMemberContext.Provider>
}
