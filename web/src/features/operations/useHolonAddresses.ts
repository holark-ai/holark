import { useState } from 'react'
import { holonDisplayStatus } from '../agents/holonDisplay'
import type { Holon, PullRequest } from '../../data/types'

export const hiddenHolonStatuses = new Set(['cancelled', 'failed', 'lost', 'expired'])

export function activeSidebarHolons(holons: Holon[], pullRequestByHolon: ReadonlyMap<string, PullRequest>) {
  return holons.filter((holon) => {
    if (holonDisplayStatus(holon) === 'finalizing') return true
    // Keep unfinished cleanup visible, but never infer cancellation from PR status.
    if (pullRequestByHolon.get(holon.id)?.status === 'merged' && hasPendingAuxiliaryCleanup(holon)) return true
    // Ending preserves the agent outcome, so completed and recovery-failed
    // holons leave the active list once their workspace has been archived.
    return !holon.archived_at && !hiddenHolonStatuses.has(holon.status)
  })
}

function hasPendingAuxiliaryCleanup(holon: Holon) {
  // The API omits closed terminals; suspended IDEs have already stopped.
  return Boolean(holon.manual_terminals?.length
    || holon.ides?.some((ide) => ide.desired_open && ide.state !== 'suspended'))
}

export function useHolonAddresses(holons: Holon[], pullRequestByHolon: ReadonlyMap<string, PullRequest>) {
  const current = activeSidebarHolons(holons, pullRequestByHolon)
  const signature = current.map((holon) => holon.id).sort().join('\u0000')
  const [state, setState] = useState<{ signature?: string, addresses: ReadonlyMap<string, string> }>(() => ({ addresses: new Map() }))
  if (state.signature !== signature) {
    const addresses = reconcileSessionAddresses(state.addresses, current)
    setState({ signature, addresses })
    return addresses
  }
  return state.addresses
}

function reconcileSessionAddresses(addresses: ReadonlyMap<string, string>, holons: Holon[]) {
  const nextAddresses = new Map(addresses)
  let changed = false
  const currentSessionIds = new Set(holons.map((holon) => holon.id))
  for (const holonId of nextAddresses.keys()) {
    if (!currentSessionIds.has(holonId)) {
      nextAddresses.delete(holonId)
      changed = true
    }
  }

  const usedAddresses = new Set(nextAddresses.values())
  const unaddressedSessions = holons
    .filter((holon) => !nextAddresses.has(holon.id))
    .sort((left, right) => new Date(left.created_at).getTime() - new Date(right.created_at).getTime() || left.id.localeCompare(right.id))
  for (const holon of unaddressedSessions) {
    const address = firstAvailableSessionAddress(usedAddresses)
    if (!address) break
    nextAddresses.set(holon.id, address)
    usedAddresses.add(address)
    changed = true
  }
  return changed ? nextAddresses : addresses
}

function firstAvailableSessionAddress(usedAddresses: ReadonlySet<string>) {
  for (let index = 0; index < 26; index += 1) {
    const address = String.fromCharCode(65 + index)
    if (!usedAddresses.has(address)) return address
  }
  return undefined
}
