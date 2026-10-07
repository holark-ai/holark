import type { ReactNode } from 'react'
import { AgentCapabilitiesContext, useCapabilityState } from './useAgentCapabilities'

export function AgentCapabilitiesProvider({ children }: { children: ReactNode }) {
  const state = useCapabilityState()
  return <AgentCapabilitiesContext.Provider value={state}>{children}</AgentCapabilitiesContext.Provider>
}
