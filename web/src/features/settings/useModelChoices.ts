import { useCallback, useRef, useState } from 'react'
import { api } from '../../data/api'
import type { HarnessType, ModelChoice } from '../../data/types'

export type ModelDiscovery = { models?: ModelChoice[]; loading?: boolean; error?: boolean }

// The cache belongs to one Settings visit, including failed requests until Retry.
export function useModelChoices() {
  const requests = useRef(new Map<HarnessType, Promise<void>>())
  const [choices, setChoices] = useState<Partial<Record<HarnessType, ModelDiscovery>>>({})
  const load = useCallback((agent: HarnessType, retry = false) => {
    if (retry) requests.current.delete(agent)
    if (requests.current.has(agent)) return
    setChoices((current) => ({ ...current, [agent]: { ...current[agent], loading: true, error: false } }))
    const request = api.agentModels(agent).then((models) => {
      if (requests.current.get(agent) === request) setChoices((current) => ({ ...current, [agent]: { models } }))
    }, () => {
      if (requests.current.get(agent) === request) setChoices((current) => ({ ...current, [agent]: { ...current[agent], loading: false, error: true } }))
    })
    requests.current.set(agent, request)
  }, [])
  const reset = useCallback(() => {
    requests.current.clear()
    setChoices({})
  }, [])
  return { choices, load, reset }
}
