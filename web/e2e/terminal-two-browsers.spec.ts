import { randomBytes } from 'node:crypto'
import { expect, test, type APIRequestContext, type Locator, type Page } from '@playwright/test'

type FixtureState = {
  holon_id: string
}

type ManualTerminal = {
  id: string
  terminal_id?: string
  title: string
}

type HolonResponse = {
  manual_terminals?: ManualTerminal[] | null
}

test('a second browser becomes interactive and fences input from the first browser', async ({ browser, request }) => {
  test.setTimeout(60_000)
  const state = await fixtureState(request)
  const runID = randomBytes(8).toString('hex')
  const firstContext = await browser.newContext({ viewport: { width: 1280, height: 720 } })
  const secondContext = await browser.newContext({ viewport: { width: 1440, height: 800 } })
  const first = await firstContext.newPage()
  const second = await secondContext.newPage()
  let terminal: ManualTerminal | undefined

  try {
    terminal = await createManualTerminal(first, request, state)
    await typeTerminalLine(first, `./terminal-ownership-scenario.sh ${runID}`)
    await expect(activeTerminalRows(first)).toContainText(`OWNER_READY_${runID}`)

    const firstInput = `first_${runID}`
    await typeTerminalLine(first, firstInput)
    await expect(activeTerminalRows(first)).toContainText(`OWNER_INPUT_${runID}:${firstInput}`)

    await second.goto(`/holons/${state.holon_id}`)
    await second.getByRole('tab', { name: terminal.title }).click()
    await waitForRestoredTerminal(second)
    await expect(activeTerminalRows(second)).toContainText(`OWNER_INPUT_${runID}:${firstInput}`)
    await expect(first.getByRole('alert')).toContainText('This terminal was attached in another view.')

    const secondInput = `second_${runID}`
    await typeTerminalLine(second, secondInput)
    const secondAccepted = `OWNER_INPUT_${runID}:${secondInput}`
    await expect(activeTerminalRows(second)).toContainText(secondAccepted)
    expect(occurrences(await renderedTerminalText(activeTerminalRows(second)), secondAccepted)).toBe(1)

    const fencedInput = `fenced_${runID}`
    await activeTerminalPane(first).locator('.xterm').filter({ visible: true }).click()
    await first.keyboard.type(fencedInput)
    await first.keyboard.press('Enter')

    const afterFence = `after_fence_${runID}`
    await typeTerminalLine(second, afterFence)
    await expect(activeTerminalRows(second)).toContainText(`OWNER_INPUT_${runID}:${afterFence}`)
    const currentText = await renderedTerminalText(activeTerminalRows(second))
    expect(currentText).not.toContain(`OWNER_INPUT_${runID}:${fencedInput}`)
    await expect(first.getByRole('alert')).toContainText('This terminal was attached in another view.')
  } finally {
    if (terminal) await closeManualTerminal(request, state.holon_id, terminal).catch(() => {})
    await firstContext.close()
    await secondContext.close()
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

function occurrences(value: string, expected: string) {
  return value.split(expected).length - 1
}
