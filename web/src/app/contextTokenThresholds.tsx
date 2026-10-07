import { createContext, useContext } from 'react'

export type ContextTokenThresholds = {
  warning: number
  danger: number
}

export const defaultContextTokenThresholds: ContextTokenThresholds = {
  warning: 100_000,
  danger: 150_000,
}

export const contextTokenThresholdsStorageKey = 'holark.context-token-thresholds'

type ContextTokenThresholdsContextValue = {
  thresholds: ContextTokenThresholds
  setThresholds: (thresholds: ContextTokenThresholds) => void
}

export const ContextTokenThresholdsContext = createContext<ContextTokenThresholdsContextValue>({
  thresholds: defaultContextTokenThresholds,
  setThresholds: () => undefined,
})

export function validContextTokenThresholds(value: unknown): value is ContextTokenThresholds {
  if (!value || typeof value !== 'object') return false
  const candidate = value as Partial<ContextTokenThresholds>
  return Number.isSafeInteger(candidate.warning)
    && Number.isSafeInteger(candidate.danger)
    && (candidate.warning ?? -1) >= 0
    && (candidate.danger ?? -1) > (candidate.warning ?? -1)
}

export function readStoredContextTokenThresholds(): ContextTokenThresholds {
  try {
    const stored = window.localStorage.getItem(contextTokenThresholdsStorageKey)
    if (!stored) return defaultContextTokenThresholds
    const parsed: unknown = JSON.parse(stored)
    return validContextTokenThresholds(parsed) ? parsed : defaultContextTokenThresholds
  } catch {
    return defaultContextTokenThresholds
  }
}

export function useContextTokenThresholds() {
  return useContext(ContextTokenThresholdsContext)
}
