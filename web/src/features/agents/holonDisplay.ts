import { sessionInputState } from '../../data/harness'
import type { AgentActivity, Holon, HolonStatus } from '../../data/types'

type SessionDisplayState = Pick<Holon, 'status' | 'exit_code' | 'input_state' | 'activity' | 'agent_sessions' | 'application_phase'>

export function holonDisplayName(holon?: Pick<Holon, 'title' | 'prompt'>) {
  return holon?.title?.trim() || firstNonEmptyLine(holon?.prompt) || 'Agent terminal'
}

export function firstNonEmptyLine(value = '') {
  return value.split(/\r?\n/).map((line) => line.trim()).find(Boolean) || ''
}

export function sessionStatusLabel(status: HolonStatus) {
  if (status === 'restoring') return 'Restoring...'
  if (status === 'recovery_failed') return 'Restoration failed'
  if (status === 'naming') return 'Preparing...'
  if (status === 'queued') return 'Preparing...'
  if (status === 'preparing') return 'Preparing...'
  if (status === 'running') return 'Running'
  if (status === 'cancelling') return 'Cancelling...'
  return status.replace(/[-_]/g, ' ')
}

export function agentDisplayActivity(agent: { status?: string; activity?: AgentActivity }): AgentActivity {
  if (agent.status === 'failed' || agent.status === 'recovery_failed') return 'failed'
  if (['queued', 'naming', 'preparing'].includes(agent.status ?? '')) return 'starting'
  if (agent.status === 'lost') return 'unknown'
  if (['completed', 'cancelled', 'cancelling', 'expired'].includes(agent.status ?? '')) return 'idle'
  return agent.activity || 'unknown'
}

export type HolonDisplayStatus = AgentActivity | 'finalizing' | 'cancelling'

export function holonDisplayStatus(holon: SessionDisplayState): HolonDisplayStatus {
  if (holon.status === 'cancelling') return 'cancelling'
  if (holon.status === 'cancelled') return agentDisplayActivity(holon)
  if (holon.application_phase === 'finalizing') return 'finalizing'
  if (['queued', 'naming', 'preparing'].includes(holon.status)) return 'starting'
  const agents = holon.agent_sessions?.filter((agent) => !agent.closed_at)
  if (agents?.length) {
    const activities = agents.map(agentDisplayActivity)
    const priority: AgentActivity[] = ['needs_input', 'working', 'failed', 'starting', 'unknown', 'completed', 'idle']
    return priority.find((state) => activities.includes(state)) ?? 'unknown'
  }
  return agentDisplayActivity(holon)
}

export function holonDisplayStatusLabel(holon: SessionDisplayState) {
  const status = holonDisplayStatus(holon)
  const labels: Record<HolonDisplayStatus, string> = { finalizing: 'Finalizing', cancelling: 'Cancelling', starting: 'Preparing', working: 'Working', needs_input: 'Needs input', completed: 'Completed', idle: 'Idle', unknown: 'Unknown', failed: 'Failed' }
  if (status === 'finalizing') {
    const input = sessionInputState(holon)
    if (input === 'permission_required') return 'Finalizing · Permission required'
    if (input === 'user_input_required' || holonNeedsInput(holon)) return 'Finalizing · Input required'
  }
  return labels[status] ?? sessionStatusLabel(holon.status)
}

export function holonNeedsInput(holon: SessionDisplayState) {
  return holonDisplayStatus({ ...holon, application_phase: undefined }) === 'needs_input'
}

export function sessionStatusText(holon: SessionDisplayState) {
  if (holonDisplayStatus(holon) === 'finalizing') return holonDisplayStatusLabel(holon) + '.'
  if (holon.status === 'restoring') return 'Restoring the saved conversation...'
  if (holon.status === 'recovery_failed') return 'Conversation restoration failed. Retry to reopen it.'
  if (holon.status === 'naming') return 'Preparing...'
  if (holon.status === 'queued') return 'Preparing...'
  if (holon.status === 'preparing') return 'Preparing...'
  if (holonDisplayStatus(holon) === 'idle') return 'Agent terminal is idle.'
  if (holon.status === 'running' && sessionInputState(holon) === 'permission_required') return 'Agent terminal is waiting for permission.'
  if (holon.status === 'running' && sessionInputState(holon) === 'user_input_required') return 'Agent terminal is waiting for input.'
  if (holon.status === 'running') return holonDisplayStatusLabel(holon) + '.'
  if (holon.status === 'completed') return `Process completed${holon.exit_code === undefined ? '' : ` with exit code ${holon.exit_code}`}.`
  if (holon.status === 'cancelled') return 'Holon cancelled.'
  if (holon.status === 'lost') return 'Runtime connection was lost.'
  if (holon.status === 'expired') return 'Review holon expired after completing.'
  return holon.status === 'cancelling' ? 'Cancelling holon...' : 'Holon failed.'
}
