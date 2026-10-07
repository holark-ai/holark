import { useCallback, useRef, useState } from 'react'
import { api } from '../../data/api'
import type { Holon } from '../../data/types'
import { useHolonStore } from '../project/holonStoreContext'
import { holonDisplayName } from './holonDisplay'
import { limitHolonTitle } from './holonTitle'

type SessionRenameState = {
  holonId: string
  originalValue: string
  value: string
}

export function useSessionRename() {
  const { updateHolon } = useHolonStore()
  const [renaming, setRenaming] = useState<SessionRenameState>()
  const [error, setError] = useState('')
  const finalizing = useRef(false)

  const begin = useCallback((holon?: Holon) => {
    if (!holon) return false
    const currentName = holonDisplayName(holon)
    finalizing.current = false
    setError('')
    setRenaming({
      holonId: holon.id,
      originalValue: currentName,
      value: currentName,
    })
    return true
  }, [])

  const change = useCallback((value: string) => {
    setRenaming((current) => current ? { ...current, value: limitHolonTitle(value) } : current)
  }, [])

  const cancel = useCallback(() => {
    finalizing.current = true
    setRenaming(undefined)
  }, [])

  const commit = useCallback(async () => {
    const pending = renaming
    if (!pending || finalizing.current) return
    finalizing.current = true
    setRenaming(undefined)
    const title = pending.value.trim()
    if (title === pending.originalValue.trim()) return

    setError('')
    try {
      const holon = await api.updateSessionTitle(pending.holonId, title)
      updateHolon(pending.holonId, (current) => current ? { ...current, title: holon.title } : holon)
    } catch (value) {
      setError(value instanceof Error ? value.message : 'Holon could not be renamed.')
    }
  }, [renaming, updateHolon])

  return { renaming, error, begin, change, cancel, commit }
}
