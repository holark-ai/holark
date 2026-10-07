import { randomBytes } from 'node:crypto'
import { expect, test, type APIRequestContext, type Page } from '@playwright/test'
import type { TerminalTestSnapshot } from '../src/features/terminal/terminalTestSnapshots'

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

type HostProgress = {
  last_sequence: number
  checkpoint_sequence: number
}

test('manual terminal erases a cell after a two-cell Unicode glyph live and after reload', async ({ page, request }) => {
  test.setTimeout(60_000)
  await page.setViewportSize({ width: 1280, height: 720 })
  const state = await fixtureState(request)
  const runID = `u${randomBytes(8).toString('hex')}`
  const readyMarker = `WIDTH_REDRAW_READY_${runID}`
  let terminal: ManualTerminal | undefined

  try {
    terminal = await createManualTerminal(page, request, state)
    if (!terminal.terminal_id) throw new Error('manual terminal has no canonical terminal ID')
    const terminalID = terminal.terminal_id
    const initialProgress = await hostProgress(request, terminalID)

    await typeTerminalLine(page, `./terminal-unicode-width-scenario.sh ${runID}`)
    await expect(activeTerminalRows(page)).toContainText(readyMarker)
    await waitForTerminalPaint(page)
    await expectCleanFirstRow(page, terminalID)

    let stableProgress: HostProgress | undefined
    await expect.poll(async () => {
      const progress = await hostProgress(request, terminalID)
      if (progress.last_sequence <= initialProgress.last_sequence) return undefined
      stableProgress = progress
      return progress.last_sequence
    }, { message: 'terminal host did not record the width-sensitive redraw output' }).toBeGreaterThan(initialProgress.last_sequence)
    if (!stableProgress) throw new Error('terminal host progress is unavailable after the redraw')
    const stableSequence = stableProgress.last_sequence
    await expect.poll(async () => {
      return (await hostProgress(request, terminalID)).checkpoint_sequence
    }, { message: 'checkpoint did not cover the stable width-sensitive redraw output' }).toBeGreaterThanOrEqual(stableSequence)

    await page.reload()
    await waitForRestoredTerminal(page)
    await expect(activeTerminalRows(page)).toContainText(readyMarker)
    await waitForTerminalPaint(page)
    await expectCleanFirstRow(page, terminalID)
  } finally {
    if (terminal) await closeManualTerminal(request, state.holon_id, terminal).catch(() => {})
  }
})

async function fixtureState(request: APIRequestContext) {
  const response = await request.get('/__e2e/state')
  expect(response.ok()).toBe(true)
  return response.json() as Promise<FixtureState>
}

async function createManualTerminal(page: Page, request: APIRequestContext, state: FixtureState) {
  const before = new Set((await sessionTerminals(request, state.holon_id)).map((candidate) => candidate.id))
  await page.goto(`/holons/${state.holon_id}`)
  await page.getByLabel('New tab').click()
  await page.getByRole('button', { name: 'Terminal', exact: true }).click()
  await expect.poll(async () => {
    return (await sessionTerminals(request, state.holon_id))
      .find((candidate) => !before.has(candidate.id) && candidate.terminal_id)
  }, { message: 'manual terminal was not created through the UI' }).not.toBeUndefined()
  const terminal = (await sessionTerminals(request, state.holon_id))
    .find((candidate) => !before.has(candidate.id) && candidate.terminal_id)
  if (!terminal) throw new Error('new manual terminal disappeared after creation')
  await waitForRestoredTerminal(page)
  return terminal
}

async function sessionTerminals(request: APIRequestContext, holonID: string) {
  const response = await request.get(`/api/v1/holons/${encodeURIComponent(holonID)}`)
  expect(response.ok()).toBe(true)
  const session = await response.json() as HolonResponse
  return session.manual_terminals ?? []
}

async function hostProgress(request: APIRequestContext, terminalID: string) {
  const response = await request.get(`/__e2e/terminal-progress/${encodeURIComponent(terminalID)}`)
  expect(response.ok()).toBe(true)
  return response.json() as Promise<HostProgress>
}

function activeTerminalPane(page: Page) {
  return page.locator('[data-terminal-pane][data-active="true"]')
}

function activeTerminalRows(page: Page) {
  return activeTerminalPane(page).locator('.xterm-rows').filter({ visible: true })
}

async function waitForRestoredTerminal(page: Page) {
  await expect(activeTerminalPane(page)).toHaveAttribute('data-restoring', 'false')
}

async function typeTerminalLine(page: Page, value: string) {
  await activeTerminalPane(page).locator('.xterm').filter({ visible: true }).click()
  await page.keyboard.type(value)
  await page.keyboard.press('Enter')
}

async function waitForTerminalPaint(page: Page) {
  await page.evaluate(() => new Promise<void>((resolve) => {
    requestAnimationFrame(() => requestAnimationFrame(() => resolve()))
  }))
}

async function expectCleanFirstRow(page: Page, terminalID: string) {
  await expect.poll(async () => {
    const firstDOMRow = await activeTerminalRows(page).locator('div').first().textContent()
    const snapshot = await terminalSnapshot(page, terminalID)
    const presented = snapshot?.runtimes.find((runtime) => runtime.kind === 'presented')
    return {
      dom: (firstDOMRow ?? '').trimEnd(),
      model: presented?.viewport[0]?.trimEnd(),
    }
  }, { message: 'the width-sensitive redraw must erase the stale x from the first row' }).toEqual({
    dom: '🐹',
    model: '🐹',
  })
}

async function terminalSnapshot(page: Page, terminalID: string) {
  return page.evaluate((id) => {
    const diagnostics = window.__HOLARK_TERMINAL_TEST_SNAPSHOTS__
    if (!diagnostics) throw new Error('terminal E2E diagnostics are not installed')
    return diagnostics.snapshots().find((snapshot) => snapshot.terminal_id === id)
  }, terminalID) as Promise<TerminalTestSnapshot | undefined>
}

async function closeManualTerminal(request: APIRequestContext, holonID: string, terminal: ManualTerminal) {
  const response = await request.post(`/api/v1/holons/${encodeURIComponent(holonID)}/terminals/${encodeURIComponent(terminal.id)}/close`)
  if (!response.ok()) throw new Error(`failed to close manual terminal ${terminal.id}: ${response.status()} ${await response.text()}`)
}
