import { expect, test, type APIRequestContext, type Page } from '@playwright/test'

type FixtureState = {
  holon_id: string
}

type ManualTerminal = {
  id: string
  terminal_id?: string
}

type HolonResponse = {
  manual_terminals?: ManualTerminal[] | null
}

// An old Firefox tab can keep trying to connect to a terminal that the server
// no longer knows about. This test removes the terminal while the tab keeps its
// old view, then checks that an HTTP availability probe tells the tab the
// terminal is gone without admitting another WebSocket attempt. It then checks
// that a new terminal can connect.
//
// We do not test the reported 10-15 second delay directly. That delay comes
// from Firefox's internal WebSocket scheduling and varies between headless
// runs and machines. Keeping stale attempts out of Firefox's WebSocket queue is
// the stable behavior that prevents the delay.
test('a stale Firefox terminal is rejected before WebSocket reconnect without blocking a new terminal', async ({ page, request }) => {
  test.setTimeout(60_000)
  await page.setViewportSize({ width: 1280, height: 720 })
  const state = await fixtureState(request)
  let currentTerminal: ManualTerminal | undefined

  try {
    const staleTerminal = await createManualTerminal(page, request, state)
    if (!staleTerminal.terminal_id) throw new Error('new manual terminal has no live terminal binding')
    const staleTerminalID = staleTerminal.terminal_id
    const attachedAt = await terminalAttachmentCount(request, staleTerminalID)

    // Keep the old tab's last session view while the backend record disappears,
    // as happens when a durable tab is retained across a Holark restart.
    await page.route('**/api/v1/holons', (route) => route.abort())
    const staleAttachPath = terminalAttachPath(state.holon_id, staleTerminalID)
    const missingResponse = page.waitForResponse((response) => {
      return response.request().method() === 'HEAD' && new URL(response.url()).pathname === staleAttachPath
    })
    await restartHolark(request)

    expect((await missingResponse).status(), 'the stale terminal availability probe must report it missing').toBe(404)
    await expect(activeTerminalPane(page).getByRole('alert')).toHaveText('Terminal not found.')
    await expect(activeTerminalPane(page)).toHaveAttribute('data-restoring', 'false')

    expect(await terminalAttachmentCount(request, staleTerminalID),
      'the missing terminal must not enter Firefox\'s WebSocket queue').toBe(attachedAt)
    await page.waitForTimeout(1_000)
    expect(await terminalAttachmentCount(request, staleTerminalID),
      'the stale terminal must not schedule another WebSocket attempt').toBe(attachedAt)

    currentTerminal = await createManualTerminal(page, request, state)
    if (!currentTerminal.terminal_id) throw new Error('replacement manual terminal has no live terminal binding')
    const replacementTerminalID = currentTerminal.terminal_id
    await expect.poll(() => terminalAttachmentCount(request, replacementTerminalID), {
      message: 'the replacement terminal did not open a WebSocket attachment',
    }).toBeGreaterThan(0)
  } finally {
    if (currentTerminal) await closeManualTerminal(request, state.holon_id, currentTerminal).catch(() => {})
  }
})

async function fixtureState(request: APIRequestContext) {
  const response = await request.get('/__e2e/state')
  expect(response.ok()).toBe(true)
  return response.json() as Promise<FixtureState>
}

async function createManualTerminal(page: Page, request: APIRequestContext, state: FixtureState) {
  const before = new Set((await sessionTerminals(request, state.holon_id)).map((terminal) => terminal.id))
  if (page.url() === 'about:blank') await page.goto(`/holons/${state.holon_id}`)
  await page.getByLabel('New tab').click()
  await page.getByRole('button', { name: 'Terminal', exact: true }).click()
  await expect.poll(async () => {
    return (await sessionTerminals(request, state.holon_id))
      .find((terminal) => !before.has(terminal.id) && terminal.terminal_id)
  }, { message: 'manual terminal was not created through the UI' }).not.toBeUndefined()
  const terminal = (await sessionTerminals(request, state.holon_id))
    .find((candidate) => !before.has(candidate.id) && candidate.terminal_id)
  if (!terminal) throw new Error('new manual terminal disappeared after creation')
  await expect(activeTerminalPane(page)).toHaveAttribute('data-restoring', 'false')
  return terminal
}

async function sessionTerminals(request: APIRequestContext, holonID: string) {
  const response = await request.get(`/api/v1/holons/${encodeURIComponent(holonID)}`)
  expect(response.ok()).toBe(true)
  const session = await response.json() as HolonResponse
  return session.manual_terminals ?? []
}

async function closeManualTerminal(request: APIRequestContext, holonID: string, terminal: ManualTerminal) {
  const response = await request.post(`/api/v1/holons/${encodeURIComponent(holonID)}/terminals/${encodeURIComponent(terminal.id)}/close`)
  if (!response.ok()) throw new Error(`failed to close manual terminal ${terminal.id}: ${response.status()} ${await response.text()}`)
}

async function restartHolark(request: APIRequestContext) {
  const response = await request.post('/__e2e/restart-holark')
  if (!response.ok()) throw new Error('failed to restart Holark: ' + response.status() + ' ' + await response.text())
}

async function terminalAttachmentCount(request: APIRequestContext, terminalID: string) {
  const response = await request.get(`/__e2e/terminal-attachments/${encodeURIComponent(terminalID)}`)
  expect(response.ok()).toBe(true)
  return Number.parseInt(await response.text(), 10)
}

function terminalAttachPath(holonID: string, terminalID: string) {
  return `/api/v1/holons/${encodeURIComponent(holonID)}/terminals/${encodeURIComponent(terminalID)}/attach`
}

function activeTerminalPane(page: Page) {
  return page.locator('[data-terminal-pane][data-active="true"]')
}
