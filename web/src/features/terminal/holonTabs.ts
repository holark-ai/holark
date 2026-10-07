import type { AgentSession, ManualTerminal, Holon, HolonIDE, HolonStatus } from '../../data/types'
import { agentDisplayActivity } from '../agents/holonDisplay'

export type HolonTerminalTab =
  | { kind: 'agent'; id: string; title: string; harness: AgentSession }
  | { kind: 'terminal'; id: string; title: string; terminal: ManualTerminal }
  | { kind: 'ide'; id: string; title: string; ide: HolonIDE }

export function visibleManualTerminals(holon?: Holon) {
  return (holon?.manual_terminals ?? []).filter((tab) => !tab.closed_at).sort(compareManualTabs)
}

export function visibleHolonIDEs(holon?: Holon) {
  return (holon?.ides ?? []).filter((tab) => tab.desired_open).sort(compareIDETabs)
}

export function orderedSessionTabs(agentTabs: AgentSession[], manualTabs: ManualTerminal[], ideTabs: HolonIDE[]): HolonTerminalTab[] {
  const tabs: HolonTerminalTab[] = [
    ...agentTabs.map((harness) => ({ kind: 'agent' as const, id: harness.id, title: harness.title || 'Agent', harness })),
    ...manualTabs.map((terminal) => ({ kind: 'terminal' as const, id: terminal.id, title: terminal.title, terminal })),
    ...ideTabs.map((ide) => ({ kind: 'ide' as const, id: ide.id, title: 'IDE', ide })),
  ]
  return tabs.sort(compareSessionTabs)
}

function compareSessionTabs(left: HolonTerminalTab, right: HolonTerminalTab) {
  return compareTabOrder(tabContent(left), tabContent(right), left.kind, right.kind)
}

export function compareHarnessTabs(left: AgentSession, right: AgentSession) {
  return compareTabOrder(left, right, left.agent_type ?? 'codex', right.agent_type ?? 'codex')
}

export function compareManualTabs(left: ManualTerminal, right: ManualTerminal) {
  return compareTabOrder(left, right, 'terminal', 'terminal')
}

export function compareIDETabs(left: HolonIDE, right: HolonIDE) {
  return compareTabOrder(left, right, 'ide', 'ide')
}

type OrderedTab = Pick<AgentSession, 'id' | 'tab_order' | 'created_at'>

function compareTabOrder(left: OrderedTab, right: OrderedTab, leftKind: string, rightKind: string) {
  const leftOrder = left.tab_order ?? 0
  const rightOrder = right.tab_order ?? 0
  if (leftOrder > 0 && rightOrder > 0 && leftOrder !== rightOrder) return leftOrder - rightOrder
  return new Date(left.created_at).getTime() - new Date(right.created_at).getTime()
    || leftKind.localeCompare(rightKind) || left.id.localeCompare(right.id)
}

function tabContent(tab: HolonTerminalTab) {
  return tab.kind === 'agent' ? tab.harness : tab.kind === 'ide' ? tab.ide : tab.terminal
}

export function visibleAgentSessions(holon?: Holon) {
  const hasAgentSessions = Array.isArray(holon?.agent_sessions)
  const harnesses = hasAgentSessions ? (holon?.agent_sessions ?? []) : holon?.agent_session ? [holon.agent_session] : []
  const visible = harnesses.filter((harness) => !harness.closed_at)
  if (visible.length || !holon || hasAgentSessions) return [...visible].sort(compareHarnessTabs)
  return [{
    id: `harness-${holon.id}`,
    holon_id: holon.id,
    agent_type: 'codex' as const,
    title: 'Agent',
    prompt: holon.prompt,
    status: holon.status,
    input_state: holon.input_state,
    activity: holon.activity,
    resume_target: holon.agent_session?.resume_target,
    tab_order: 1,
    created_at: holon.created_at,
    updated_at: holon.finished_at ?? holon.started_at ?? holon.created_at,
  }]
}

export const terminalStatuses = new Set<HolonStatus>(['cancelled', 'completed', 'failed', 'lost', 'expired', 'recovery_failed'])

export function selectedHolonTab(holon?: Holon) {
  const agents = visibleAgentSessions(holon)
  const terminals = visibleManualTerminals(holon)
  const ides = visibleHolonIDEs(holon)
  const tabs = orderedSessionTabs(agents, terminals, ides)
  if (tabs.some((tab) => tab.id === holon?.last_selected_tab_id)) return holon?.last_selected_tab_id
  return agents.find((agent) => !terminalStatuses.has(agent.status ?? holon?.status ?? 'queued'))?.id
    ?? terminals[0]?.id ?? ides[0]?.id ?? tabs[0]?.id
}

// Legacy display placeholders must never become durable selections.
export function isSelectableHolonTab(holon: Holon, id: string) {
  const agents = holon.agent_sessions ?? (holon.agent_session ? [holon.agent_session] : [])
  return agents.some((agent) => agent.id === id && !agent.closed_at)
    || (holon.manual_terminals ?? []).some((terminal) => terminal.id === id && !terminal.closed_at)
    || (holon.ides ?? []).some((ide) => ide.id === id && ide.desired_open)
}

export function attentionTab(holon: Holon, cycle: boolean) {
  const waiting = orderedSessionTabs(visibleAgentSessions(holon), visibleManualTerminals(holon), visibleHolonIDEs(holon)).filter((tab) => tab.kind === 'agent'
    && !['restoring', 'recovery_failed'].includes(tab.harness.status ?? holon.status)
    && agentDisplayActivity({ ...tab.harness, status: tab.harness.status ?? holon.status }) === 'needs_input')
  if (!waiting.length) return selectedHolonTab(holon)
  const remembered = cycle ? selectedHolonTab(holon) : holon.last_selected_tab_id
  const index = waiting.findIndex((tab) => tab.id === remembered)
  return waiting[index < 0 ? 0 : cycle ? (index + 1) % waiting.length : index].id
}
