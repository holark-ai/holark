import { randomBytes } from 'node:crypto'
import { expect, test, type APIRequestContext, type Locator, type Page } from '@playwright/test'

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

// Live terminal traffic must depend only on the in-memory process state and
// terminal host. The fixture occupies SQLite's sole connection until an
// explicit release, proving output, input, and resize do not wait on durable
// metadata while the browser remains on the same live attachment.
test('live output, input, and resize continue while SQLite is unavailable', async ({ page, request }) => {
  test.setTimeout(60_000)
  await page.setViewportSize({ width: 1280, height: 720 })
  const state = await fixtureState(request)
  const before = new Set((await sessionTerminals(request, state.holon_id)).map((terminal) => terminal.id))
  const runID = randomBytes(8).toString('hex')
  let created: ManualTerminal | undefined
  let databaseBlocked = false

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

    await expect(activeTerminalPane(page)).toHaveAttribute('data-restoring', 'false')
    await typeShellCommand(page, `printf 'DB_BEFORE_${runID}:%s\\n' "$(stty size)"; while [ ! -f .terminal-database-${runID}-blocked ]; do :; done; printf 'DB_OUTPUT_${runID}\\n'; IFS= read -r line; printf 'DB_ACCEPTED_${runID}:%s\\n' "$line"`)
    const beforeDimensions = await waitForTerminalDimensions(page, `DB_BEFORE_${runID}`)

    const blocked = await request.post(`/__e2e/database/block/${runID}`)
    expect(blocked.ok()).toBe(true)
    databaseBlocked = true

    await expect(activeTerminalRows(page), 'PTY output must continue while SQLite is occupied')
      .toContainText(`DB_OUTPUT_${runID}`)
    await expect(activeTerminalPane(page)).toHaveAttribute('data-restoring', 'false')

    const inputMarker = `input${runID}`
    await typeShellCommand(page, inputMarker)
    await expect(activeTerminalRows(page), 'typed input must reach the PTY while SQLite is occupied')
      .toContainText(`DB_ACCEPTED_${runID}:${inputMarker}`)
    expect(occurrences(await renderedTerminalText(activeTerminalRows(page)), `DB_ACCEPTED_${runID}:${inputMarker}`)).toBe(1)

    await page.setViewportSize({ width: 1500, height: 900 })
    await typeShellCommand(page, `printf 'DB_AFTER_${runID}:%s\\n' "$(stty size)"`)
    const afterDimensions = await waitForTerminalDimensions(
      page,
      `DB_AFTER_${runID}`,
      'PTY resize must complete while SQLite is occupied',
    )
    expect(afterDimensions).not.toEqual(beforeDimensions)
    await expect(activeTerminalPane(page)).toHaveAttribute('data-restoring', 'false')
  } finally {
    if (databaseBlocked) {
      await request.post(`/__e2e/database/release/${runID}`).catch(() => {})
    }
    if (created) await closeManualTerminal(request, state.holon_id, created).catch(() => {})
  }
})

async function fixtureState(request: APIRequestContext) {
  const response = await request.get('/__e2e/state')
  expect(response.ok()).toBe(true)
  return response.json() as Promise<FixtureState>
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

async function typeShellCommand(page: Page, command: string) {
  await activeTerminalPane(page).locator('.xterm').filter({ visible: true }).click()
  await page.keyboard.type(command)
  await page.keyboard.press('Enter')
}

async function renderedTerminalText(rows: Locator) {
  return rows.evaluate((container) => Array.from(container.children)
    .map((line) => line.textContent ?? '')
    .join('\n'))
}

async function waitForTerminalDimensions(page: Page, marker: string, message?: string) {
  let dimensions: { rows: number, columns: number } | null = null
  await expect.poll(async () => {
    dimensions = terminalDimensions(await renderedTerminalText(activeTerminalRows(page)), marker)
    return dimensions
  }, { message }).not.toBeNull()
  if (!dimensions) throw new Error(`terminal did not render dimensions for ${marker}`)
  return dimensions
}

function terminalDimensions(rendered: string, marker: string) {
  const match = rendered.match(new RegExp(`${marker}:(\\d+) (\\d+)`))
  if (!match) return null
  return { rows: Number(match[1]), columns: Number(match[2]) }
}

function occurrences(value: string, expected: string) {
  return value.split(expected).length - 1
}

async function closeManualTerminal(request: APIRequestContext, holonID: string, terminal: ManualTerminal) {
  const response = await request.post(`/api/v1/holons/${encodeURIComponent(holonID)}/terminals/${encodeURIComponent(terminal.id)}/close`)
  if (!response.ok()) throw new Error(`failed to close manual terminal ${terminal.id}: ${response.status()} ${await response.text()}`)
}
