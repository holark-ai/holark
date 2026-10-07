import { randomBytes } from 'node:crypto'
import { access, rm } from 'node:fs/promises'
import path from 'node:path'
import { expect, test, type APIRequestContext, type Locator, type Page } from '@playwright/test'

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

test('reload restores full-screen cells, attributes, cursor, modes, and input behavior', async ({ page, request }) => {
  test.setTimeout(60_000)
  await page.addInitScript(() => localStorage.setItem('holark.color-theme.shared-tree', 'dark'))
  await page.setViewportSize({ width: 1280, height: 720 })
  const state = await fixtureState(request)
  const runID = randomBytes(8).toString('hex')
  const readyPath = path.join(state.worktree_path, `.terminal-semantic-${runID}-ready`)
  let terminal: ManualTerminal | undefined

  try {
    terminal = await createManualTerminal(page, request, state)
    await typeTerminalLine(page, `./terminal-semantic-restore-scenario.sh ${runID}`)
    await expect.poll(() => fileExists(readyPath), { message: 'semantic terminal scenario did not reach its stable state' }).toBe(true)
    await expectSemanticScreen(page, runID)
    expect(await cursorPosition(page)).toEqual({ row: 5, column: 6 })

    await page.reload()
    await waitForRestoredTerminal(page)
    await expectSemanticScreen(page, runID)
    expect(await cursorPosition(page)).toEqual({ row: 5, column: 6 })

    await page.keyboard.press('ArrowUp')
    await expect(activeTerminalRows(page)).toContainText(`KEY_${runID}:1b4f41`)
    await expect(activeTerminalRows(page)).toContainText(`SEMANTIC_FOOTER_${runID}`)

    const input = `after_restore_${runID}`
    await typeTerminalLine(page, input)
    const accepted = `SEMANTIC_INPUT_${runID}:${input}`
    await expect(activeTerminalRows(page)).toContainText(accepted)
    expect(occurrences(await renderedTerminalText(activeTerminalRows(page)), accepted)).toBe(1)
  } finally {
    if (terminal) await closeManualTerminal(request, state.holon_id, terminal).catch(() => {})
    await rm(readyPath, { force: true })
  }
})

async function expectSemanticScreen(page: Page, runID: string) {
  const rows = activeTerminalRows(page)
  await expect(rows).toContainText(`SEMANTIC_HEADER_${runID}`)
  await expect(rows).toContainText(`COLOR_${runID}_é`)
  await expect(rows).toContainText(`SEMANTIC_FOOTER_${runID}`)
  await expect.poll(async () => rows.locator('span').evaluateAll((spans, marker) => {
    const span = spans.find((candidate) => candidate.textContent?.includes(marker as string))
    return span ? getComputedStyle(span).color : ''
  }, `COLOR_${runID}_é`), { message: 'restored true-color cell attributes did not match the emitted screen' }).toBe('rgb(12, 200, 90)')
}

async function cursorPosition(page: Page) {
  await activeTerminalPane(page).locator('.xterm').filter({ visible: true }).click()
  return activeTerminalRows(page).evaluate((rows) => {
    const lines = [...rows.children] as HTMLElement[]
    for (let row = 0; row < lines.length; row += 1) {
      const cursor = lines[row].querySelector('.xterm-cursor') as HTMLElement | null
      if (!cursor) continue
      const lineBox = lines[row].getBoundingClientRect()
      const cursorBox = cursor.getBoundingClientRect()
      return { row, column: Math.round((cursorBox.left - lineBox.left) / cursorBox.width) }
    }
    throw new Error('xterm cursor was not rendered')
  })
}

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

function occurrences(value: string, expected: string) {
  return value.split(expected).length - 1
}
