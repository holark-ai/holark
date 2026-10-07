import { expect, test, type APIRequestContext, type Page } from '@playwright/test'

type FixtureState = { holon_id: string }
type ManualTerminal = { id: string; terminal_id?: string }
type Holon = { manual_terminals?: ManualTerminal[] }

test('Holark restart clears a stale PTY binding and requires explicit relaunch', async ({ page, request }) => {
  const state = await fixtureState(request)
  const terminal = await createManualTerminal(page, request, state.holon_id)
  const oldTerminalID = terminal.terminal_id
  expect(oldTerminalID).toBeTruthy()

  await typeTerminalLine(page, 'printf BEFORE_RESTART\\n')
  await expect(activeRows(page)).toContainText('BEFORE_RESTART')

  const restart = await request.post('/__e2e/restart-holark')
  expect(restart.ok()).toBe(true)
  await page.reload()

  const stale = await manualTerminal(request, state.holon_id, terminal.id)
  expect(stale.terminal_id ?? '').toBe('')
  await expect(page.getByText('Terminal process ended')).toBeVisible()

  await page.getByRole('button', { name: 'Relaunch terminal' }).click()
  await expect.poll(async () => (await manualTerminal(request, state.holon_id, terminal.id)).terminal_id ?? '').not.toBe('')
  const replacement = await manualTerminal(request, state.holon_id, terminal.id)
  expect(replacement.terminal_id).toBeTruthy()
  expect(replacement.terminal_id).not.toBe(oldTerminalID)

  await page.reload()
  await expect(activePane(page)).toHaveAttribute('data-restoring', 'false')
  await typeTerminalLine(page, 'printf AFTER_RELAUNCH\\n')
  await expect(activeRows(page)).toContainText('AFTER_RELAUNCH')
})

async function fixtureState(request: APIRequestContext) {
  const response = await request.get('/__e2e/state')
  expect(response.ok()).toBe(true)
  return response.json() as Promise<FixtureState>
}

async function createManualTerminal(page: Page, request: APIRequestContext, holonID: string) {
  const before = new Set((await terminals(request, holonID)).map((terminal) => terminal.id))
  await page.goto(`/holons/${holonID}`)
  await page.getByLabel('New tab').click()
  await page.getByRole('button', { name: 'Terminal', exact: true }).click()
  await expect.poll(async () => (await terminals(request, holonID)).find((terminal) => !before.has(terminal.id) && terminal.terminal_id)).not.toBeUndefined()
  return (await terminals(request, holonID)).find((terminal) => !before.has(terminal.id))!
}

async function terminals(request: APIRequestContext, holonID: string) {
  const response = await request.get(`/api/v1/holons/${encodeURIComponent(holonID)}`)
  expect(response.ok()).toBe(true)
  return ((await response.json()) as Holon).manual_terminals ?? []
}

async function manualTerminal(request: APIRequestContext, holonID: string, recordID: string) {
  const terminal = (await terminals(request, holonID)).find((candidate) => candidate.id === recordID)
  if (!terminal) throw new Error('durable terminal tab disappeared across Holark restart')
  return terminal
}

function activePane(page: Page) { return page.locator('[data-terminal-pane][data-active="true"]') }
function activeRows(page: Page) { return activePane(page).locator('.xterm-rows').filter({ visible: true }) }
async function typeTerminalLine(page: Page, value: string) {
  await activePane(page).locator('.xterm').filter({ visible: true }).click()
  await page.keyboard.type(value)
  await page.keyboard.press('Enter')
}
