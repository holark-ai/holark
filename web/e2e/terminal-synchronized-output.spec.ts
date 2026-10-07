import { randomBytes } from 'node:crypto'
import { rm, writeFile } from 'node:fs/promises'
import path from 'node:path'
import { expect, test, type APIRequestContext, type Page } from '@playwright/test'

type FixtureState = {
  holon_id: string
  worktree_path: string
}

type ManualTerminal = {
  id: string
  terminal_id?: string
}

type HolonResponse = {
  manual_terminals?: ManualTerminal[] | null
}

type TerminalFrame = {
  sequence: number
  pendingRenderBytes: number
  pendingRenderCompletion: boolean
  model: string[]
  dom: string[]
}

test('synchronized output publishes the settled xterm model to the visible rows', async ({ page, request }, testInfo) => {
  await page.setViewportSize({ width: 1280, height: 720 })
  const state = await fixtureState(request)
  const runID = `s${randomBytes(8).toString('hex')}`
  const triggerPath = path.join(state.worktree_path, `.terminal-synchronized-output-${runID}-trigger`)
  const modelMarker = `MODEL_FRAME_${runID}`
  let terminal: ManualTerminal | undefined

  try {
    terminal = await createManualTerminal(page, request, state)
    if (!terminal.terminal_id) throw new Error('manual terminal has no canonical terminal ID')

    await typeTerminalLine(page, `./terminal-synchronized-output-race-scenario.sh ${runID}`)
    await expect(activeTerminalRows(page)).toContainText(`BASELINE_FRAME_${runID}`)

    await writeFile(triggerPath, '')
    await page.waitForFunction(({ terminalID, marker }) => {
      const snapshot = window.__HOLARK_TERMINAL_TEST_SNAPSHOTS__?.snapshots()
        .find((candidate) => candidate.terminal_id === terminalID)
      const presented = snapshot?.runtimes.find((runtime) => runtime.kind === 'presented')
      return snapshot?.pending_render_bytes === 0 && !snapshot?.pending_render_completion &&
        presented?.viewport.some((row) => row.includes(marker))
    }, { terminalID: terminal.terminal_id, marker: modelMarker }, { polling: 10, timeout: 800 })

    await waitForTwoAnimationFrames(page)
    const frame = await terminalFrame(page, terminal.terminal_id)
    await testInfo.attach('synchronized-output-frame', {
      body: JSON.stringify(frame, null, 2),
      contentType: 'application/json',
    })

    expect(frame.pendingRenderBytes).toBe(0)
    expect(frame.pendingRenderCompletion).toBe(false)
    expect(frame.model.join('\n')).toContain(modelMarker)
    expect(frame.dom, 'visible xterm rows must publish the settled synchronized-output frame').toEqual(frame.model)
  } finally {
    if (terminal) await closeManualTerminal(request, state.holon_id, terminal).catch(() => {})
    await rm(triggerPath, { force: true }).catch(() => {})
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
  await expect(activeTerminalPane(page)).toHaveAttribute('data-restoring', 'false')
  return terminal
}

async function sessionTerminals(request: APIRequestContext, holonID: string) {
  const response = await request.get(`/api/v1/holons/${encodeURIComponent(holonID)}`)
  expect(response.ok()).toBe(true)
  const session = await response.json() as HolonResponse
  return session.manual_terminals ?? []
}

function activeTerminalPane(page: Page) {
  return page.locator('[data-terminal-pane][data-active="true"]')
}

function activeTerminalRows(page: Page) {
  return activeTerminalPane(page).locator('.xterm-rows').filter({ visible: true })
}

async function typeTerminalLine(page: Page, value: string) {
  await activeTerminalPane(page).locator('.xterm').filter({ visible: true }).click()
  await page.keyboard.type(value)
  await page.keyboard.press('Enter')
}

async function waitForTwoAnimationFrames(page: Page) {
  await page.evaluate(() => new Promise<void>((resolve) => {
    requestAnimationFrame(() => requestAnimationFrame(() => resolve()))
  }))
}

async function terminalFrame(page: Page, terminalID: string): Promise<TerminalFrame> {
  return page.evaluate((id) => {
    const snapshot = window.__HOLARK_TERMINAL_TEST_SNAPSHOTS__?.snapshots()
      .find((candidate) => candidate.terminal_id === id)
    const presented = snapshot?.runtimes.find((runtime) => runtime.kind === 'presented')
    const rows = document.querySelector<HTMLElement>('[data-terminal-pane][data-active="true"] .xterm-rows')
    if (!snapshot || !presented || !rows) throw new Error('active terminal frame is unavailable')
    return {
      sequence: snapshot.sequence,
      pendingRenderBytes: snapshot.pending_render_bytes,
      pendingRenderCompletion: snapshot.pending_render_completion,
      model: presented.viewport.map((row) => row.trimEnd()),
      dom: [...rows.children].map((row) => (row.textContent ?? '').trimEnd()),
    }
  }, terminalID)
}

async function closeManualTerminal(request: APIRequestContext, holonID: string, terminal: ManualTerminal) {
  const response = await request.post(
    `/api/v1/holons/${encodeURIComponent(holonID)}/terminals/${encodeURIComponent(terminal.id)}/close`,
  )
  if (!response.ok()) throw new Error(`failed to close manual terminal ${terminal.id}: ${response.status()} ${await response.text()}`)
}
