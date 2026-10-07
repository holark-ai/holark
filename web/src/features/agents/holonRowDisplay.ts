import { holonDisplayStatus } from './holonDisplay'
import { sessionInputState } from '../../data/harness'
import type { Holon } from '../../data/types'
import styles from './HolonStatusIcon.module.css'

export function sessionStatusClassName(status: string, needsInput: boolean) {
  if (needsInput) return styles.statusAttention
  if (status === 'completed') return styles.statusComplete
  if (status === 'recovery_failed' || status === 'failed' || status === 'lost') return styles.statusFailed
  if (status === 'cancelled' || status === 'expired') return styles.statusEnded
  if (['idle', 'unknown', 'starting'].includes(status)) return styles.statusIdle
  return styles.statusActive
}

export function inputStateLabel(holon: Holon) {
  if (holonDisplayStatus(holon) !== 'needs_input') return ''
  const state = sessionInputState(holon)
  if (state === 'permission_required') return 'Permission required'
  if (state === 'user_input_required') return 'Input required'
  return ''
}

export function formatTime(value: string) {
  return new Date(value).toLocaleString([], { dateStyle: 'short', timeStyle: 'short' })
}

export function formatRelativeTime(value: string) {
  const elapsedSeconds = Math.max(0, Math.floor((Date.now() - new Date(value).getTime()) / 1000))
  if (elapsedSeconds < 60) return 'now'
  const elapsedMinutes = Math.floor(elapsedSeconds / 60)
  if (elapsedMinutes < 60) return `${elapsedMinutes}m`
  const elapsedHours = Math.floor(elapsedMinutes / 60)
  if (elapsedHours < 24) return `${elapsedHours}h`
  const elapsedDays = Math.floor(elapsedHours / 24)
  if (elapsedDays < 7) return `${elapsedDays}d`
  return new Date(value).toLocaleDateString([], { month: 'short', day: 'numeric' })
}

export function formatContextTokens(value?: number) {
  if (!Number.isSafeInteger(value) || value === undefined || value < 0) return undefined
  return `${Math.floor(value / 1000)}k`
}

export function displayedContextTokens(holon: Pick<Holon, 'agent_session' | 'agent_sessions'>) {
  const openAgents = (holon.agent_sessions?.length
    ? holon.agent_sessions
    : holon.agent_session ? [holon.agent_session] : []
  ).filter((agent) => !agent.closed_at && agent.status === 'running' && agent.activity === 'working')

  return openAgents.reduce<number | undefined>((highest, agent) => {
    const value = agent.context_tokens
    if (!Number.isSafeInteger(value) || value === undefined || value < 0) return highest
    return highest === undefined ? value : Math.max(highest, value)
  }, undefined)
}
