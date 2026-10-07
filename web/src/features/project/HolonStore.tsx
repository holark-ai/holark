import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode, type SetStateAction } from 'react'
import { api } from '../../data/api'
import type { Holon } from '../../data/types'
import { attentionTab, isSelectableHolonTab } from '../terminal/holonTabs'
import { HolonStoreContext, type HolonStoreValue } from './holonStoreContext'

type SelectionSave = { tabId: string; version: number; pending: boolean; saving: boolean; dismissedVersion?: number }

export function HolonStoreProvider({ projectId, children, interval = 3000 }: {
  projectId: string
  children: ReactNode
  interval?: number
}) {
  void projectId
  const [holonsById, setHolonsById] = useState<Map<string, Holon>>(() => new Map())
  // Event handlers and async responses need the latest state even within a React batch.
  const currentHolons = useRef(holonsById)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<Error>()
  const [selectionErrors, setSelectionErrors] = useState<Record<string, Error>>({})
  const selections = useRef(new Map<string, SelectionSave>())
  const revisions = useRef(new Map<string, number>())
  const refreshSequence = useRef(0)

  const publish = useCallback((next: Map<string, Holon>) => {
    const discarded: string[] = []
    for (const [id, save] of selections.current) {
      const holon = next.get(id)
      if (holon && isSelectableHolonTab(holon, save.tabId)) continue
      selections.current.delete(id)
      discarded.push(id)
    }
    if (discarded.length) setSelectionErrors((current) => {
      const remaining = { ...current }
      for (const id of discarded) delete remaining[id]
      return remaining
    })
    currentHolons.current = next
    setHolonsById(next)
  }, [])

  const saveSelection = useCallback(async (id: string) => {
    const save = selections.current.get(id)
    if (!save || save.saving || !save.pending) return
    save.saving = true
    try {
      while (save.pending) {
        const version = save.version
        const tabId = save.tabId
        try {
          await api.selectHolonTab(id, tabId)
        } catch (value) {
          if (selections.current.get(id) !== save) return
          // A newer choice should still be attempted if an older request failed.
          if (version !== save.version) continue
          if (save.dismissedVersion === version) return
          setSelectionErrors((current) => ({ ...current, [id]: value instanceof Error ? value : new Error('Request failed') }))
          return
        }
        if (selections.current.get(id) !== save) return
        delete save.dismissedVersion
        setSelectionErrors((current) => {
          const next = { ...current }
          delete next[id]
          return next
        })
        if (version === save.version) {
          save.pending = false
          // Invalidate polls that started before the write was acknowledged.
          save.version += 1
        }
      }
    } finally {
      save.saving = false
    }
  }, [])

  const selectHolonTab = useCallback((id: string, tabId: string, expectedTabId?: string) => {
    const holon = currentHolons.current.get(id)
    if (!holon || (expectedTabId !== undefined && holon.last_selected_tab_id !== expectedTabId)
      || !isSelectableHolonTab(holon, tabId)) return
    if (holon.last_selected_tab_id !== tabId) {
      const save = selections.current.get(id) ?? { tabId, version: 0, pending: false, saving: false }
      save.tabId = tabId
      save.version += 1
      save.pending = true
      selections.current.set(id, save)
      revisions.current.set(id, (revisions.current.get(id) ?? 0) + 1)
      const next = new Map(currentHolons.current)
      next.set(id, { ...holon, last_selected_tab_id: tabId })
      publish(next)
    }
    void saveSelection(id)
  }, [publish, saveSelection])

  const dismissSelectionError = useCallback((id: string) => {
    const save = selections.current.get(id)
    // Keep retrying the write, but do not resurface a dismissed failure on each poll.
    if (save) save.dismissedVersion = save.version
    setSelectionErrors((current) => {
      const next = { ...current }
      delete next[id]
      return next
    })
  }, [])

  const activateHolon = useCallback((id: string, cycle: boolean) => {
    const holon = currentHolons.current.get(id)
    if (!holon) return
    const tabId = attentionTab(holon, cycle)
    if (tabId) selectHolonTab(id, tabId)
  }, [selectHolonTab])

  const refresh = useCallback(async () => {
    const sequence = ++refreshSequence.current
    const revisionsAtStart = new Map(revisions.current)
    const selectionVersions = new Map([...selections.current].map(([id, save]) => [id, save.version]))
    try {
      const loaded = await api.holons()
      if (sequence !== refreshSequence.current) return
      const protectedSelections = loaded.map((holon) => {
        const save = selections.current.get(holon.id)
        if (save && isSelectableHolonTab(holon, save.tabId)
          && (save.pending || save.version !== selectionVersions.get(holon.id))) {
          return { ...holon, last_selected_tab_id: currentHolons.current.get(holon.id)?.last_selected_tab_id }
        }
        return holon
      })
      publish(reconcileHolons(currentHolons.current, protectedSelections, revisionsAtStart, revisions.current))
      setError(undefined)
      for (const id of selections.current.keys()) void saveSelection(id)
    } catch (value) {
      if (sequence !== refreshSequence.current) return
      setError(value instanceof Error ? value : new Error('Request failed'))
    } finally {
      if (sequence === refreshSequence.current) setLoading(false)
    }
  }, [publish, saveSelection])

  useEffect(() => {
    let active = true
    let timer: number | undefined
    const poll = async () => {
      await refresh()
      if (active) timer = window.setTimeout(poll, interval)
    }
    timer = window.setTimeout(poll)
    return () => {
      active = false
      refreshSequence.current += 1
      if (timer !== undefined) window.clearTimeout(timer)
    }
  }, [interval, refresh])

  const updateHolon = useCallback((id: string, update: SetStateAction<Holon | undefined>) => {
    revisions.current.set(id, (revisions.current.get(id) ?? 0) + 1)
    const previous = currentHolons.current.get(id)
    let nextHolon = typeof update === 'function' ? update(previous) : update
    if (nextHolon === previous) return
    // Lifecycle responses may contain a selection read before the user's latest click.
    if (nextHolon && previous) nextHolon = { ...nextHolon, last_selected_tab_id: previous.last_selected_tab_id }
    const next = new Map(currentHolons.current)
    next.delete(id)
    if (nextHolon) next.set(nextHolon.id, nextHolon)
    publish(next)
  }, [publish])

  const holons = useMemo(() => [...holonsById.values()], [holonsById])
  const getHolon = useCallback((id: string) => holonsById.get(id), [holonsById])
  const value = useMemo<HolonStoreValue>(() => ({
    holons, loading, error, getHolon, updateHolon, selectHolonTab, activateHolon, dismissSelectionError, selectionErrors, refresh,
  }), [error, getHolon, loading, refresh, holons, updateHolon, selectHolonTab, activateHolon, dismissSelectionError, selectionErrors])

  return <HolonStoreContext.Provider value={value}>{children}</HolonStoreContext.Provider>
}

function reconcileHolons(
  current: Map<string, Holon>,
  polled: Holon[],
  revisionsAtStart: Map<string, number>,
  currentRevisions: Map<string, number>,
) {
  const polledById = new Map(polled.map((holon) => [holon.id, holon]))
  const next = new Map(current)

  for (const id of current.keys()) {
    if (!polledById.has(id) && currentRevisions.get(id) === revisionsAtStart.get(id)) next.delete(id)
  }
  for (const holon of polled) {
    if (currentRevisions.get(holon.id) === revisionsAtStart.get(holon.id)) next.set(holon.id, holon)
  }
  return next
}
