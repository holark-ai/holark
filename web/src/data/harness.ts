import type { AgentSession, HarnessCapabilities, HarnessCapability, HarnessType, InputState, Holon } from './types'

export const primaryHarnessType: HarnessType = 'codex'
export const harnessTypes: HarnessType[] = ['codex', 'claude-code', 'opencode']
const harnessDisplayLabels: Record<HarnessType, string> = {
  codex: 'Codex',
  'claude-code': 'Claude Code',
  opencode: 'OpenCode',
}
const terminalHolonStatuses = new Set<Holon['status']>(['cancelled', 'completed', 'failed', 'lost', 'expired', 'recovery_failed'])

export function availableHarness(capabilities: HarnessCapability[], harnessType: HarnessType = primaryHarnessType) {
  return capabilities.find((harness) => harness.type === harnessType && harness.available)
}

export function hasAvailableHarness(capabilities: HarnessCapability[], harnessType: HarnessType = primaryHarnessType) {
  return Boolean(availableHarness(capabilities, harnessType))
}

export function harnessVersionDetail(harness?: HarnessCapability) {
  if (!harness?.available) return harness?.unavailable_reason || 'Unavailable'
  const version = harness.version || (harness.support_status === 'unknown' ? 'Version unknown' : 'Available')
  if (harness.support_status === 'unsupported') return `${version} · May not work as expected`
  if (harness.support_status === 'unknown' && harness.version) return `${version} · Version unknown`
  return version
}

export function sessionResumeTarget(holon: Holon, harnessType: HarnessType = primaryHarnessType) {
  return primaryAgentSession(holon, harnessType)?.resume_target
}

export function isSessionResumable(holon: Holon) {
  return terminalHolonStatuses.has(holon.status) && (Boolean(holon.archived_at) || holon.kind === 'pr_review') && Boolean(holon.worktree_path) && !holon.read_only
}

export function primaryAgentSession(holon: Holon, harnessType: HarnessType = primaryHarnessType): AgentSession | undefined {
  const fromList = holon.agent_sessions?.find((agent) => !agent.closed_at && (agent.agent_type) === harnessType)
  if (fromList) return fromList
  return (holon.agent_session?.agent_type) === harnessType ? holon.agent_session : undefined
}

export function requiredHarnessTypes(holon: Holon): HarnessType[] {
  const harnesses = holon.agent_sessions?.length
    ? holon.agent_sessions
    : holon.agent_session ? [holon.agent_session] : []
  const required = new Set(harnesses.filter((agent) => !agent.closed_at).map((agent) => agent.agent_type))
  return harnessTypes.filter((harnessType) => required.has(harnessType))
}

export function sessionInputState(holon: Pick<Holon, 'input_state'>): InputState {
  return holon.input_state ?? 'none'
}

export function harnessInputState(holon: Holon, harnessType: HarnessType = primaryHarnessType): InputState {
  const selectedHarness = (holon.agent_session?.agent_type) === harnessType ? holon.agent_session : undefined
  return selectedHarness?.input_state ?? primaryAgentSession(holon, harnessType)?.input_state ?? sessionInputState(holon)
}

export function harnessDisplayLabel(harnessType: HarnessType = primaryHarnessType) {
  return harnessDisplayLabels[harnessType]
}

export function defaultHarnessType(capabilities: HarnessCapabilities): HarnessType {
  if (capabilities.default_harness) return capabilities.default_harness
  return harnessTypes.find((harnessType) => hasAvailableHarness(capabilities, harnessType)) ?? primaryHarnessType
}
