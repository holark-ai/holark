import { randomBytes } from 'node:crypto'
import { access, readFile, rm, writeFile } from 'node:fs/promises'
import path from 'node:path'
import { expect, test, type APIRequestContext, type Locator, type Page } from '@playwright/test'
import type { TerminalTestSnapshot } from '../src/features/terminal/terminalTestSnapshots'

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

test('terminal keeps progressing across Holark tab and route navigation', async ({ page, request }) => {
  test.setTimeout(60_000)
  await page.setViewportSize({ width: 1280, height: 720 })
  const state = await fixtureState(request)
  const runID = randomBytes(8).toString('hex')
  const triggerPath = path.join(state.worktree_path, `.terminal-navigation-${runID}-trigger`)
  const progressPath = path.join(state.worktree_path, `.terminal-navigation-${runID}-progress`)
  const donePath = path.join(state.worktree_path, `.terminal-navigation-${runID}-done`)
  const created: ManualTerminal[] = []

  try {
    created.push(await createManualTerminal(page, request, state))
    const original = created[0]
    if (!original.terminal_id) throw new Error('original manual terminal has no canonical terminal ID')
    await typeTerminalLine(page, `./terminal-navigation-scenario.sh ${runID}`)
    await expect(activeTerminalRows(page)).toContainText(`NAV_READY_${runID}`)
    await waitForTerminalPaint(page)
    const initialPTY = await reportNavigationDimensions(page, `initial${runID}`)
    await expectNavigationGeometry(page, original.terminal_id, initialPTY)

    created.push(await createManualTerminal(page, request, state))
    expect(created[1].terminal_id).not.toBe(original.terminal_id)
    await writeFile(triggerPath, '')
    await expect.poll(() => numericFile(progressPath), {
      message: 'the hidden terminal did not progress while another terminal tab was active',
    }).toBeGreaterThanOrEqual(5)

    await page.getByRole('tab', { name: original.title }).click()
    await waitForRestoredTerminal(page)
    await waitForTerminalPaint(page)
    const tabPTY = await reportNavigationDimensions(page, `tab${runID}`)
    expect(tabPTY, 'terminal-tab activation must retain unchanged PTY geometry').toEqual(initialPTY)
    await expectNavigationGeometry(page, original.terminal_id, tabPTY)
    await expectOriginalTerminalIdentity(request, state.holon_id, original)

    await page.getByRole('link', { name: 'Repository', exact: true }).click()
    await expect(page).toHaveURL('/')
    await expect.poll(() => fileExists(donePath), {
      message: 'the terminal did not finish its background work while another route was active',
    }).toBe(true)

    await page.goto(`/holons/${state.holon_id}`)
    await page.getByRole('tab', { name: original.title }).click()
    await waitForRestoredTerminal(page)
    await expect(activeTerminalRows(page)).toContainText(`NAV_FINAL_${runID}`)

    await waitForTerminalPaint(page)
    const routePTY = await reportNavigationDimensions(page, `route${runID}`)
    expect(routePTY, 'route remount must retain unchanged PTY geometry').toEqual(initialPTY)
    await expectNavigationGeometry(page, original.terminal_id, routePTY)

    const input = `after_navigation_${runID}`
    await typeTerminalLine(page, input)
    const accepted = `NAV_INPUT_${runID}:${input}`
    await expect(activeTerminalRows(page)).toContainText(accepted)
    expect(occurrences(await renderedTerminalText(activeTerminalRows(page)), accepted)).toBe(1)

    await expectOriginalTerminalIdentity(request, state.holon_id, original)
  } finally {
    for (const terminal of created.reverse()) {
      await closeManualTerminal(request, state.holon_id, terminal).catch(() => {})
    }
    await Promise.all([triggerPath, progressPath, donePath].map((file) => rm(file, { force: true })))
  }
})

async function fixtureState(request: APIRequestContext) {
  const response = await request.get('/__e2e/state')
  expect(response.ok()).toBe(true)
  return response.json() as Promise<FixtureState>
}

async function createManualTerminal(page: Page, request: APIRequestContext, state: FixtureState) {
  const before = new Set((await sessionTerminals(request, state.holon_id)).map((terminal) => terminal.id))
  if (!page.url().includes(`/holons/${state.holon_id}`)) {
    await page.goto(`/holons/${state.holon_id}`)
  }
  await page.getByLabel('New tab').click()
  await page.getByRole('button', { name: 'Terminal', exact: true }).click()
  await expect.poll(async () => {
    return (await sessionTerminals(request, state.holon_id))
      .find((terminal) => !before.has(terminal.id) && terminal.terminal_id)
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

async function closeManualTerminal(request: APIRequestContext, holonID: string, terminal: ManualTerminal) {
  const response = await request.post(`/api/v1/holons/${encodeURIComponent(holonID)}/terminals/${encodeURIComponent(terminal.id)}/close`)
  if (!response.ok()) throw new Error(`failed to close manual terminal ${terminal.id}: ${response.status()} ${await response.text()}`)
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

async function renderedTerminalText(rows: Locator) {
  return rows.locator('div').evaluateAll((lines) => lines.map((line) => line.textContent ?? '').join('\n'))
}

async function fileExists(file: string) {
  try {
    await access(file)
    return true
  } catch {
    return false
  }
}

async function numericFile(file: string) {
  try {
    return Number.parseInt(await readFile(file, 'utf8'), 10)
  } catch {
    return 0
  }
}

function occurrences(value: string, expected: string) {
  return value.split(expected).length - 1
}

async function waitForTerminalPaint(page: Page) {
  await page.evaluate(() => new Promise<void>((resolve) => {
    requestAnimationFrame(() => requestAnimationFrame(() => resolve()))
  }))
}

async function reportNavigationDimensions(page: Page, marker: string) {
  await typeTerminalLine(page, `dimensions_${marker}`)
  const expected = `NAV_DIMS_${marker}:`
  await expect(activeTerminalRows(page)).toContainText(expected)
  const match = new RegExp(`NAV_DIMS_${marker}:(\\d+)x(\\d+)`).exec(await renderedTerminalText(activeTerminalRows(page)))
  if (!match) throw new Error(`PTY dimension marker ${marker} was not rendered`)
  return { columns: Number.parseInt(match[1], 10), rows: Number.parseInt(match[2], 10) }
}

async function expectNavigationGeometry(
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

async function expectOriginalTerminalIdentity(request: APIRequestContext, holonID: string, original: ManualTerminal) {
  const current = (await sessionTerminals(request, holonID)).find((terminal) => terminal.id === original.id)
  expect(current?.terminal_id, 'navigation must retain the original PTY identity').toBe(original.terminal_id)
}
