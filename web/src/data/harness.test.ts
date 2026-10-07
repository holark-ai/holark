import type { AgentSession, HarnessCapabilities, HarnessType, Holon } from './types'
import { defaultHarnessType, harnessDisplayLabel, harnessTypes, requiredHarnessTypes } from './harness'

const now = '2026-08-28T10:00:00Z'

function harness(id: string, harnessType: HarnessType, closed = false): AgentSession {
  return {
    id,
    agent_type: harnessType,
    input_state: 'none',
    created_at: now,
    updated_at: now,
    ...(closed ? { closed_at: now } : {}),
  }
}

function holon(overrides: Partial<Holon>): Holon {
  return {
    id: 'holon-1',
    repository_id: 'project-1',
    runtime_id: 'node-1',
    prompt: 'Work',
    status: 'running',
    input_state: 'none',
    created_at: now,
    ...overrides,
  }
}

describe('requiredHarnessTypes', () => {
  it('returns OpenCode for an OpenCode-only holon', () => {
    expect(requiredHarnessTypes(holon({ agent_sessions: [harness('open-1', 'opencode')] }))).toEqual(['opencode'])
  })

  it('returns mixed harness requirements in canonical order', () => {
    expect(requiredHarnessTypes(holon({
      agent_sessions: [harness('open-1', 'opencode'), harness('codex-1', 'codex'), harness('claude-1', 'claude-code')],
    }))).toEqual(['codex', 'claude-code', 'opencode'])
  })

  it('deduplicates repeated harness types', () => {
    expect(requiredHarnessTypes(holon({
      agent_sessions: [harness('open-1', 'opencode'), harness('open-2', 'opencode')],
    }))).toEqual(['opencode'])
  })

  it('excludes closed harnesses', () => {
    expect(requiredHarnessTypes(holon({
      agent_sessions: [harness('codex-1', 'codex', true), harness('open-1', 'opencode')],
    }))).toEqual(['opencode'])
  })

  it('uses a non-empty harness list instead of the legacy harness', () => {
    expect(requiredHarnessTypes(holon({
      agent_session: harness('legacy-codex', 'codex'),
      agent_sessions: [harness('open-1', 'opencode')],
    }))).toEqual(['opencode'])
  })

  it('falls back to the legacy harness when the list is empty', () => {
    expect(requiredHarnessTypes(holon({
      agent_session: harness('legacy-open', 'opencode'),
      agent_sessions: [],
    }))).toEqual(['opencode'])
  })
})

it('defines exhaustive labels in canonical harness order', () => {
  expect(harnessTypes.map(harnessDisplayLabel)).toEqual(['Codex', 'Claude Code', 'OpenCode'])
})

describe('defaultHarnessType', () => {
  it('preserves an explicitly returned unavailable default', () => {
    const capabilities = [{ type: 'codex', available: true }, { type: 'opencode', available: false }] as HarnessCapabilities
    capabilities.default_harness = 'opencode'
    expect(defaultHarnessType(capabilities)).toBe('opencode')
  })

  it('uses the compatibility fallback order when default metadata is absent', () => {
    expect(defaultHarnessType([{ type: 'opencode', available: true }] as HarnessCapabilities)).toBe('opencode')
    expect(defaultHarnessType([{ type: 'claude-code', available: true }, { type: 'opencode', available: true }] as HarnessCapabilities)).toBe('claude-code')
    expect(defaultHarnessType([{ type: 'codex', available: true }, { type: 'claude-code', available: true }] as HarnessCapabilities)).toBe('codex')
  })
})
