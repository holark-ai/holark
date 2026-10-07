import { randomBytes } from 'node:crypto'
import { access, rm, writeFile } from 'node:fs/promises'
import path from 'node:path'
import { expect, test, type APIRequestContext, type Locator, type Page } from '@playwright/test'

type FixtureState = {
  holon_id: string
  worktree_path: string
}

type ManualTerminal = {
  id: string
  terminal_id?: string
  title: string
}

type HolonResponse = {
  manual_terminals?: ManualTerminal[] | null
}

// Screen tracking is auxiliary and must never block terminal operation. While a
// tracking worker is deliberately paused, an existing terminal must continue
// displaying output, accept input exactly once, resize, and remain alive. A
// second terminal must also be able to start. After tracking resumes, its state
// must again be available after a browser reload.
test('hung screen tracking does not affect live terminals', async ({ page, request }) => {
  test.setTimeout(60_000)
  await page.setViewportSize({ width: 1280, height: 720 })
  const state = await fixtureState(request)
  const terminals: ManualTerminal[] = []
  const runID = `t${randomBytes(8).toString('hex')}`
  const scenarioPath = path.join(state.worktree_path, `.terminal-tracking-${runID}`)
  const progressPath = `${scenarioPath}-progress`
  const triggerPath = `${scenarioPath}-trigger`

  try {
    await rm(progressPath, { force: true })
    await rm(triggerPath, { force: true })
    terminals.push(await createManualTerminal(page, request, state))
    await typeShellCommand(page, `./terminal-tracking-failure-scenario.sh ${runID}`)
    await expect(activeTerminalRows(page)).toContainText(`READY_${runID}`)

    await setTrackingPaused(request, true)
    await writeFile(triggerPath, '')
    await expect.poll(() => fileExists(progressPath), {
      message: 'the PTY command did not run while tracking was hung',
    }).toBe(true)
    await expect(activeTerminalRows(page), 'live output stopped behind the tracking worker').toContainText(`PROGRESS_${runID}`)

    const exactInput = `INPUT_${runID}`
    await typeTerminalText(page, exactInput)
    await page.keyboard.press('Enter')
    const accepted = `ACCEPTED_${runID}:${exactInput}`
    await expect(activeTerminalRows(page)).toContainText(accepted)
    expect(occurrences(await renderedTerminalText(activeTerminalRows(page)), accepted)).toBe(1)

    await page.setViewportSize({ width: 1500, height: 900 })
    const resizeMarker = `TRACKING_DIMS_${runID}`
    await typeShellCommand(page, `set -- $(stty size); printf '${resizeMarker}:%sx%s\\n' "$2" "$1"`)
    await expect(activeTerminalRows(page), 'PTY resize stopped behind the tracking worker')
      .toContainText(new RegExp(`${resizeMarker}:\\d+x\\d+`))

    terminals.push(await createManualTerminal(page, request, state))
    const secondMarker = `SECOND_${runID}`
    await typeShellCommand(page, `printf '${secondMarker}\\n'`)
    await expect(activeTerminalRows(page)).toContainText(secondMarker)

    await setTrackingPaused(request, false)
    const recovered = `RECOVERED_${runID}`
    await typeShellCommand(page, `printf '${recovered}\\n'`)
    await expect(activeTerminalRows(page)).toContainText(recovered)
    await page.reload()
    await page.getByRole('tab', { name: terminals[1].title }).click()
    await waitForRestoredTerminal(page)
    await expect(activeTerminalRows(page)).toContainText(recovered)
  } finally {
    await setTrackingPaused(request, false)
    for (const terminal of terminals.reverse()) {
      await closeManualTerminal(request, state.holon_id, terminal).catch(() => {})
    }
    await rm(progressPath, { force: true })
    await rm(triggerPath, { force: true })
  }
})

// A temporary tracking outage does not create a restoration gap while all output
// since the last trusted checkpoint remains in the retained tail. Reloading must
// reconstruct the current screen from that checkpoint plus the retained output,
// without showing a degraded-restoration warning. Tracking and normal restoration
// must continue after the worker becomes available again.
test('reload restores a trusted checkpoint plus retained tail while tracking is unavailable', async ({ page, request }) => {
  test.setTimeout(60_000)
  await page.setViewportSize({ width: 1280, height: 720 })
  const state = await fixtureState(request)
  const runID = `r${randomBytes(8).toString('hex')}`
  const scenarioPath = path.join(state.worktree_path, `.terminal-tracking-${runID}`)
  const progressPath = `${scenarioPath}-progress`
  const triggerPath = `${scenarioPath}-trigger`
  let terminal: ManualTerminal | undefined

  try {
    await rm(progressPath, { force: true })
    await rm(triggerPath, { force: true })
    terminal = await createManualTerminal(page, request, state)
    const checkpointMarker = `READY_${runID}`
    await typeShellCommand(page, `./terminal-tracking-failure-scenario.sh ${runID}`)
    await expect(activeTerminalRows(page)).toContainText(checkpointMarker)
    await page.reload()
    await waitForRestoredTerminal(page)
    await expect(activeTerminalRows(page)).toContainText(checkpointMarker)

    await setTrackingPaused(request, true)
    const tailMarker = `PROGRESS_${runID}`
    await writeFile(triggerPath, '')
    await expect.poll(() => fileExists(progressPath), {
      message: 'the PTY did not produce tail output while tracking was paused',
    }).toBe(true)
    await expect(activeTerminalRows(page)).toContainText(tailMarker)

    await page.reload()
    await waitForRestoredTerminal(page)
    await expect(activeTerminalRows(page)).toContainText(checkpointMarker)
    await expect(activeTerminalRows(page)).toContainText(tailMarker)
    await expect(page.getByText('Restored after a capture failure. Some earlier screen state may be missing.')).toHaveCount(0)

    await setTrackingPaused(request, false)
    await typeTerminalText(page, `finish_${runID}`)
    await page.keyboard.press('Enter')
    await expect(activeTerminalRows(page)).toContainText(`ACCEPTED_${runID}:finish_${runID}`)
    const recovered = `TRACKING_RECOVERED_${runID}`
    await typeShellCommand(page, `printf '${recovered}\\n'`)
    await expect(activeTerminalRows(page)).toContainText(recovered)
    await page.reload()
    await waitForRestoredTerminal(page)
    await expect(activeTerminalRows(page)).toContainText(recovered)
  } finally {
    await setTrackingPaused(request, false)
    if (terminal) await closeManualTerminal(request, state.holon_id, terminal).catch(() => {})
    await rm(progressPath, { force: true })
    await rm(triggerPath, { force: true })
  }
})

// If a tracking outage lasts long enough to exceed the retained-tail limit, exact
// restoration is no longer possible. Reloading must show the available screen
// with an honest degraded-restoration warning while keeping the terminal usable.
// After the terminal emits a genuine parsed full reset, subsequent state can be
// trusted again, the warning disappears, and a later reload restores normally.
test('degraded restore is disclosed and a parsed full reset restores trust', async ({ page, request }) => {
  test.setTimeout(90_000)
  await page.setViewportSize({ width: 1280, height: 720 })
  const state = await fixtureState(request)
  const runID = `q${randomBytes(8).toString('hex')}`
  const triggerPath = path.join(state.worktree_path, `.terminal-quality-${runID}-trigger`)
  const progressPath = path.join(state.worktree_path, `.terminal-quality-${runID}-progress`)
  const warning = 'Restored after a capture failure. Some earlier screen state may be missing. Click to dismiss.'
  let terminal: ManualTerminal | undefined

  try {
    await rm(triggerPath, { force: true })
    await rm(progressPath, { force: true })
    terminal = await createManualTerminal(page, request, state)
    await typeShellCommand(page, `./terminal-tracking-quality-scenario.sh ${runID}`)
    await expect(activeTerminalRows(page)).toContainText(`QUALITY_READY_${runID}`)
    await page.reload()
    await waitForRestoredTerminal(page)

    await setTrackingPaused(request, true)
    await writeFile(triggerPath, '')
    await expect.poll(() => fileExists(progressPath), {
      message: 'the PTY did not cross the hard capture limit', timeout: 30_000,
    }).toBe(true)
    await expect(activeTerminalRows(page)).toContainText(`DEGRADED_SCREEN_${runID}`)
    await setTrackingPaused(request, false)

    await page.reload()
    await waitForRestoredTerminal(page)
    const degradedWarning = page.getByRole('button', { name: warning })
    await expect(activeTerminalRows(page)).toContainText(`DEGRADED_SCREEN_${runID}`)
    await expect(degradedWarning).toBeVisible()
    await degradedWarning.click()
    await expect(degradedWarning).toHaveCount(0)
    const exactInput = `FOCUS_${runID}`
    await page.keyboard.type(exactInput)
    await page.keyboard.press('Enter')
    await expect(activeTerminalRows(page)).toContainText(`DEGRADED_INPUT_${runID}:${exactInput}`)

    await page.reload()
    await waitForRestoredTerminal(page)
    await expect(page.getByRole('button', { name: warning })).toBeVisible()
    await typeTerminalText(page, `RESET_${runID}`)
    await page.keyboard.press('Enter')
    await expect(activeTerminalRows(page)).toContainText(`TRUSTED_SCREEN_${runID}`)
    await expect(page.getByRole('button', { name: warning })).toHaveCount(0)

    await page.reload()
    await waitForRestoredTerminal(page)
    await expect(activeTerminalRows(page)).toContainText(`TRUSTED_SCREEN_${runID}`)
    await expect(page.getByRole('button', { name: warning })).toHaveCount(0)
  } finally {
    await setTrackingPaused(request, false)
    if (terminal) await closeManualTerminal(request, state.holon_id, terminal).catch(() => {})
    await rm(triggerPath, { force: true })
    await rm(progressPath, { force: true })
  }
})

async function fixtureState(request: APIRequestContext) {
  const response = await request.get('/__e2e/state')
  expect(response.ok()).toBe(true)
  return response.json() as Promise<FixtureState>
}

async function createManualTerminal(page: Page, request: APIRequestContext, state: FixtureState) {
  const before = new Set((await sessionTerminals(request, state.holon_id)).map((terminal) => terminal.id))
  if (!page.url().includes(`/agents/${state.holon_id}`)) {
    await page.goto(`/holons/${state.holon_id}`)
  }
  await page.getByLabel('New tab').click()
  await page.getByRole('button', { name: 'Terminal', exact: true }).click()
  await expect.poll(async () => (await sessionTerminals(request, state.holon_id))
    .filter((terminal) => !before.has(terminal.id) && terminal.terminal_id).length,
  { message: 'manual terminal was not created through the UI' }).toBe(1)
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

async function closeManualTerminal(request: APIRequestContext, holonID: string, terminal: ManualTerminal) {
  const response = await request.post(`/api/v1/holons/${encodeURIComponent(holonID)}/terminals/${encodeURIComponent(terminal.id)}/close`)
  if (!response.ok()) throw new Error(`failed to close manual terminal ${terminal.id}: ${response.status()} ${await response.text()}`)
}

async function setTrackingPaused(request: APIRequestContext, paused: boolean) {
  const response = await request.post(`/__e2e/tracking/${paused ? 'pause' : 'release'}`)
  expect(response.ok()).toBe(true)
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

async function typeShellCommand(page: Page, command: string) {
  await typeTerminalText(page, command)
  await page.keyboard.press('Enter')
}

async function typeTerminalText(page: Page, text: string) {
  await activeTerminalPane(page).locator('.xterm').filter({ visible: true }).click()
  await page.keyboard.type(text)
}

async function renderedTerminalText(rows: Locator) {
  return rows.locator('div').evaluateAll((lines) => lines.map((line) => line.textContent ?? '').join('\n'))
}

async function fileExists(filePath: string) {
  try {
    await access(filePath)
    return true
  } catch {
    return false
  }
}

function occurrences(value: string, needle: string) {
  return value.split(needle).length - 1
}
