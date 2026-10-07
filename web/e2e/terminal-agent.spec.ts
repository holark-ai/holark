import { expect, test, type APIRequestContext } from '@playwright/test'

type FixtureState = { holon_id: string }
type AgentSession = { id: string; terminal_id?: string; title: string }

test('agent tab presentation remains bound to durable agent-session state', async ({ page, request }) => {
  const state = await fixtureState(request)
  const response = await request.post(`/api/v1/holons/${encodeURIComponent(state.holon_id)}/agent-sessions`, {
    data: { prompt: 'browser fixture agent', agent_type: 'codex' },
  })
  expect(response.ok()).toBe(true)
  let created = await response.json() as AgentSession

  const rename = await request.patch(`/api/v1/holons/${encodeURIComponent(state.holon_id)}/agent-sessions/${encodeURIComponent(created.id)}`, {
    data: { title: 'Fixture agent' },
  })
  expect(rename.ok()).toBe(true)
  created = await rename.json() as AgentSession

  try {
    // Local composition launches the agent and projects its canonical terminal binding.
    expect(created).toMatchObject({ title: 'Fixture agent' })
    expect(created.terminal_id).toMatch(/^terminal-/)
    await page.goto(`/holons/${state.holon_id}`)
    await expect(page.getByRole('tab', { name: 'Fixture agent' })).toBeVisible()
  } finally {
    await request.post(`/api/v1/holons/${encodeURIComponent(state.holon_id)}/agent-sessions/${encodeURIComponent(created.id)}/close`)
  }
})

async function fixtureState(request: APIRequestContext) {
  const response = await request.get('/__e2e/state')
  expect(response.ok()).toBe(true)
  return response.json() as Promise<FixtureState>
}
