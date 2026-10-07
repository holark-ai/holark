import { expect, test, type APIRequestContext, type Locator, type Page } from '@playwright/test'
import type { TerminalTestSnapshot } from '../src/features/terminal/terminalTestSnapshots'

type FixtureState = {
  holon_id: string
}

type HolonResponse = {
  manual_terminals?: ManualTerminal[] | null
}

type ManualTerminal = {
  id: string
  terminal_id?: string
}

test('manual terminal renders output, keeps resize geometry stable, and restores after reload', async ({ page, request }) => {
  test.setTimeout(90_000)
  await page.setViewportSize({ width: 1280, height: 720 })
  const state = await fixtureState(request)
  const runID = Date.now()
  const first = `SMOKE_SCROLL_${runID}_0001`
  const middle = `SMOKE_SCROLL_${runID}_3000`
  const last = `SMOKE_SCROLL_${runID}_6000`
  const after = `SMOKE_AFTER_REFRESH_${Date.now()}`
  const before = new Set((await sessionTerminals(request, state.holon_id)).map((terminal) => terminal.id))
  let created: ManualTerminal | undefined

  try {
    await page.goto(`/holons/${state.holon_id}`)
    await page.getByLabel('New tab').click()
    await page.getByRole('button', { name: 'Terminal', exact: true }).click()
    await expect.poll(async () => {
      return (await sessionTerminals(request, state.holon_id))
        .find((terminal) => !before.has(terminal.id) && terminal.terminal_id)
    }, { message: 'manual terminal was not created through the UI' }).not.toBeUndefined()
    created = (await sessionTerminals(request, state.holon_id))
      .find((terminal) => !before.has(terminal.id) && terminal.terminal_id)
    if (!created?.terminal_id) throw new Error('new manual terminal disappeared after creation')

    await waitForRestoredTerminal(page)
    const terminalRows = activeTerminalRows(page)
    await typeShellCommand(page, `i=1; while [ "$i" -le 6000 ]; do printf 'SMOKE_SCROLL_${runID}_%04d\\n' "$i"; i=$((i+1)); done`)
    await expect(terminalRows).toContainText(last, { timeout: 30_000 })

    const initialPTY = await reportPTYDimensions(page, terminalRows, `SMOKE_DIMS_INITIAL_${runID}`)
    await page.setViewportSize({ width: 1500, height: 900 })

    const resizedMarker = `SMOKE_DIMS_RESIZED_${runID}`
    const resizedPTY = await reportPTYDimensions(page, terminalRows, resizedMarker)
    expect(resizedPTY).not.toEqual(initialPTY)
    await expectDimensionAgreement(page, created.terminal_id, resizedPTY)

    const outputMarker = `SMOKE_OUTPUT_COMMIT_${runID}`
    await typeShellCommand(page, `printf '${outputMarker}\\n'`)
    await expect(terminalRows).toContainText(outputMarker)
    await waitForTerminalPaint(page)
    const stableMarker = `SMOKE_DIMS_STABLE_${runID}`
    const stablePTY = await reportPTYDimensions(page, terminalRows, stableMarker)
    expect(stablePTY, 'output-driven fitting must keep unchanged PTY geometry').toEqual(resizedPTY)
    await expectDimensionAgreement(page, created.terminal_id, stablePTY)

    await page.reload()
    await waitForRestoredTerminal(page)
    const restoredRows = activeTerminalRows(page)
    await expect(restoredRows).toContainText(last)
    await expectRestoredScrollback(page, [first, middle, last])
    await scrollTerminalToBottom(page, last)
    await expect(restoredRows).toContainText(last)

    await waitForTerminalPaint(page)
    const reloadMarker = `SMOKE_DIMS_RELOAD_${runID}`
    const reloadPTY = await reportPTYDimensions(page, restoredRows, reloadMarker)
    expect(reloadPTY, 'same-size reload fitting must keep unchanged PTY geometry').toEqual(stablePTY)
    await expectDimensionAgreement(page, created.terminal_id, reloadPTY)

    await typeShellCommand(page, `printf '${after}\\n'`)
    await expect(restoredRows).toContainText(after)
    const rendered = await renderedTerminalText(restoredRows)
    expect(rendered.indexOf(reloadMarker)).toBeGreaterThanOrEqual(0)
    expect(rendered.indexOf(after)).toBeGreaterThan(rendered.indexOf(reloadMarker))
  } finally {
    if (created) await closeManualTerminal(request, state.holon_id, created).catch(() => {})
  }
})

async function fixtureState(request: APIRequestContext) {
  const response = await request.get('/__e2e/state')
  expect(response.ok()).toBe(true)
  return response.json() as Promise<FixtureState>
}

function activeTerminalRows(page: Page) {
  return page.locator('[data-terminal-pane][data-active="true"] .xterm-rows').filter({ visible: true })
}

async function waitForRestoredTerminal(page: Page) {
  await expect(page.locator('[data-terminal-pane][data-active="true"]')).toHaveAttribute('data-restoring', 'false')
}

async function expectRestoredScrollback(page: Page, expected: string[]) {
  const rows = activeTerminalRows(page)
  const missing = new Set(expected)
  await page.locator('[data-terminal-pane][data-active="true"] .xterm').filter({ visible: true }).click()
  for (let pageUp = 0; pageUp < 320 && missing.size > 0; pageUp += 1) {
    const visible = await renderedTerminalText(rows)
    for (const text of missing) {
      if (visible.includes(text)) missing.delete(text)
    }
    if (missing.size === 0) return
    await page.keyboard.press('Shift+PageUp')
    await waitForTerminalPaint(page)
  }
  const visible = await renderedTerminalText(rows)
  throw new Error(`restored terminal scrollback missed ${JSON.stringify([...missing])}; visible=${JSON.stringify(visible)}`)
}

async function scrollTerminalToBottom(page: Page, expected: string) {
  const rows = activeTerminalRows(page)
  await page.locator('[data-terminal-pane][data-active="true"] .xterm').filter({ visible: true }).click()
  for (let pageDown = 0; pageDown < 320; pageDown += 1) {
    if ((await renderedTerminalText(rows)).includes(expected)) return
    await page.keyboard.press('Shift+PageDown')
    await waitForTerminalPaint(page)
  }
  throw new Error(`terminal did not return to its latest scrollback containing ${JSON.stringify(expected)}`)
}

async function waitForTerminalPaint(page: Page) {
  await page.evaluate(() => new Promise<void>((resolve) => {
    requestAnimationFrame(() => requestAnimationFrame(() => resolve()))
  }))
}

async function typeShellCommand(page: Page, command: string) {
  await page.locator('[data-terminal-pane][data-active="true"] .xterm').filter({ visible: true }).click()
  await page.keyboard.type(command)
  await page.keyboard.press('Enter')
}

async function renderedTerminalText(rows: Locator) {
  return rows.locator('div').evaluateAll((lines) => lines.map((line) => line.textContent ?? '').join('\n'))
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

async function reportPTYDimensions(page: Page, rows: Locator, marker: string) {
  await typeShellCommand(page, `set -- $(stty size); printf '${marker}:%sx%s\\n' "$2" "$1"`)
  let dimensions: { columns: number; rows: number } | undefined
  await expect.poll(async () => {
    const match = new RegExp(`${marker}:(\\d+)x(\\d+)`).exec(await renderedTerminalText(rows))
    dimensions = match ? { columns: Number.parseInt(match[1], 10), rows: Number.parseInt(match[2], 10) } : undefined
    return dimensions
  }, { message: `PTY dimension marker ${marker} was not rendered` }).not.toBeUndefined()
  return dimensions!
}

async function expectDimensionAgreement(
  page: Page,
  terminalID: string,
  pty: { columns: number; rows: number },
) {
  await expect.poll(async () => {
    const browser = await terminalSnapshot(page, terminalID)
    return {
      browser: { columns: browser?.columns, rows: browser?.rows },
      proposed: browser?.proposed_dimensions,
    }
  }, { message: `browser, stable fit, and PTY must agree at ${pty.columns}x${pty.rows}` }).toEqual({
    browser: pty,
    proposed: pty,
  })
}

async function terminalSnapshot(page: Page, terminalID: string) {
  return page.evaluate((id) => {
    const diagnostics = window.__HOLARK_TERMINAL_TEST_SNAPSHOTS__
    if (!diagnostics) throw new Error('terminal E2E diagnostics are not installed')
    return diagnostics.snapshots().find((snapshot) => snapshot.terminal_id === id)
  }, terminalID) as Promise<TerminalTestSnapshot | undefined>
}
