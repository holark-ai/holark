import type { AgentSession, Holon } from '../../data/types'
import { holonDisplayName } from './holonDisplay'

const now = '2026-08-06T12:00:00.000Z'
const primaryHarness: AgentSession = {
  id: 'harness-1',
  holon_id: 'holon-1',
  agent_type: 'codex',
  title: 'Agent',
  input_state: 'none',
  created_at: now,
  updated_at: now,
}

function holon(overrides: Partial<Holon> = {}): Holon {
  return {
    id: 'holon-1',
    repository_id: 'holark',
    runtime_id: 'node-1',
    prompt: 'Prompt-derived name\nMore detail',
    status: 'running',
    input_state: 'none',
    created_at: now,
    ...overrides,
  }
}

describe('holonDisplayName', () => {
  it('prefers the persisted holon title without using the agent title', () => {
    expect(holonDisplayName(holon({ title: 'Persisted holon name', agent_session: primaryHarness }))).toBe('Persisted holon name')
  })

  it('falls back to the first non-empty prompt line', () => {
    expect(holonDisplayName(holon({
      title: '   ',
      prompt: '\n  Prompt-derived name  \nMore detail',
      agent_session: { ...primaryHarness, title: 'Unrelated agent title' },
    }))).toBe('Prompt-derived name')
  })
})
