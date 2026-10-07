import { createContext, useContext, type SetStateAction } from 'react'
import type { Holon } from '../../data/types'

export type HolonStoreValue = {
  holons: Holon[]
  loading: boolean
  error: Error | undefined
  getHolon: (id: string) => Holon | undefined
  updateHolon: (id: string, update: SetStateAction<Holon | undefined>) => void
  selectHolonTab: (id: string, tabId: string, expectedTabId?: string) => void
  activateHolon: (id: string, cycle: boolean) => void
  dismissSelectionError: (id: string) => void
  selectionErrors: Record<string, Error>
  refresh: () => Promise<void>
}

export const HolonStoreContext = createContext<HolonStoreValue | null>(null)

export function useHolonStore() {
  const store = useContext(HolonStoreContext)
  if (!store) throw new Error('HolonStoreContext is missing')
  return store
}

export function useOptionalHolonStore() {
  return useContext(HolonStoreContext)
}
