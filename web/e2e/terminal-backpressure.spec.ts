import { randomBytes } from 'node:crypto'
import { access, rm, writeFile } from 'node:fs/promises'
import path from 'node:path'
import { expect, test, type APIRequestContext, type Locator, type Page, type TestInfo } from '@playwright/test'

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

type Scenario = {
  runID: string
  triggerPath: string
  progressPath: string
  continuousPath: string
  stopPath: string
  donePath: string
}

test.beforeEach(async ({ page }) => {
  await page.addInitScript(() => {
    let blocked = false
    let waiters: Array<() => void> = []
    window.__HOLARK_TERMINAL_RENDERER_E2E__ = {
      block() {
        blocked = true
      },
      release() {
        blocked = false
        const pending = waiters
        waiters = []
        pending.forEach((resolve) => resolve())
      },
      beforeWrite() {
        if (!blocked) return Promise.resolve()
        return new Promise<void>((resolve) => waiters.push(resolve))
      },
    }
  })
})

test('new terminal reports connecting until its initial screen is ready', async ({ page, request }) => {
  const state = await fixtureState(request)
  const before = new Set((await sessionTerminals(request, state.holon_id)).map((terminal) => terminal.id))
  let terminal: ManualTerminal | undefined

  try {
    await page.goto(`/holons/${state.holon_id}`)
    await blockRenderer(page)
    await page.getByLabel('New tab').click()
    await page.getByRole('button', { name: 'Terminal', exact: true }).click()
    await expect.poll(async () => {
      return (await sessionTerminals(request, state.holon_id))
        .find((candidate) => !before.has(candidate.id) && candidate.terminal_id)
    }, { message: 'manual terminal was not created through the UI' }).not.toBeUndefined()
    terminal = (await sessionTerminals(request, state.holon_id))
      .find((candidate) => !before.has(candidate.id) && candidate.terminal_id)
    if (!terminal) throw new Error('new manual terminal disappeared after creation')

    const pane = activeTerminalPane(page)
    await expect(pane).toHaveAttribute('data-restoring', 'true')
    await expect(pane.getByRole('status')).toHaveText('Connecting...')
    await expect(pane.getByText('Reconnecting...')).toHaveCount(0)

    await releaseRenderer(page)
    await expect(pane).toHaveAttribute('data-restoring', 'false')
    await expect(pane.getByRole('status')).toHaveCount(0)
    await typeShellCommand(page, 'printf "CONNECTED_INPUT\\n"')
    await expect(activeTerminalRows(page)).toContainText('CONNECTED_INPUT')
  } finally {
    await releaseRenderer(page)
    if (terminal) await closeManualTerminal(request, state.holon_id, terminal)
  }
})

test('terminal process continues while browser rendering is blocked', async ({ page, request }, testInfo) => {
  test.setTimeout(45_000)
  await page.setViewportSize({ width: 1280, height: 720 })
  const state = await fixtureState(request)
  const scenario = createScenario(state.worktree_path)
  let terminal: ManualTerminal | undefined
  let testError: Error | undefined
  let cleanupError: Error | undefined

  try {
    await removeScenarioFiles(scenario)
    terminal = await createManualTerminal(page, request, state)
    await typeShellCommand(page, `./terminal-slow-browser-scenario.sh ${scenario.runID}`)
    await expect(activeTerminalRows(page)).toContainText(`READY_${scenario.runID}`)

    await blockRenderer(page)
    await writeFile(scenario.triggerPath, '')
    await expect.poll(() => fileExists(scenario.progressPath), {
      message: `scenario ${scenario.runID} did not reach its progress marker`,
      timeout: 10_000,
    }).toBe(true)
    await expect.poll(() => fileExists(scenario.donePath), {
      message: `scenario ${scenario.runID} did not finish while browser rendering was blocked`,
      timeout: 10_000,
    }).toBe(true)
  } catch (error) {
    testError = await diagnosticError(error, page, scenario, testInfo)
  } finally {
    cleanupError = await cleanupScenario(page, request, state.holon_id, terminal, scenario)
    if (cleanupError) {
      await testInfo.attach('terminal-cleanup-error', { body: cleanupError.message, contentType: 'text/plain' })
    }
  }
  if (testError) throw testError
  if (cleanupError) throw cleanupError
})

test('stale terminal restores atomically and preserves unfinished input exactly once', async ({ page, request }, testInfo) => {
  test.setTimeout(45_000)
  await page.setViewportSize({ width: 1280, height: 720 })
  const state = await fixtureState(request)
  const scenario = createScenario(state.worktree_path)
  let terminal: ManualTerminal | undefined
  let testError: Error | undefined
  let cleanupError: Error | undefined

  try {
    await removeScenarioFiles(scenario)
    terminal = await createManualTerminal(page, request, state)
    await typeShellCommand(page, `./terminal-slow-browser-scenario.sh ${scenario.runID}`)
    await expect(activeTerminalRows(page)).toContainText(`READY_${scenario.runID}`)

    const unfinished = `unfinished_${scenario.runID}`
    await typeTerminalText(page, unfinished)
    await expect(activeTerminalRows(page)).toContainText(unfinished)
    await observeVisibleTerminalHistory(page)

    await blockRenderer(page)
    await writeFile(scenario.triggerPath, '')
    await expect.poll(() => fileExists(scenario.progressPath), {
      message: `scenario ${scenario.runID} did not reach its progress marker`,
      timeout: 10_000,
    }).toBe(true)
    await expect.poll(() => fileExists(scenario.donePath), {
      message: `scenario ${scenario.runID} did not finish while browser rendering was blocked`,
      timeout: 10_000,
    }).toBe(true)

    const pane = activeTerminalPane(page)
    await expect(pane).toHaveAttribute('data-restoring', 'true')
    await expect(pane.getByRole('status')).toHaveText('Reconnecting...')
    await expect(activeTerminalRows(page), 'the last committed screen must remain visible during recovery')
      .toContainText(unfinished)
    await page.keyboard.type(`IGNORED_${scenario.runID}`)

    await releaseRenderer(page)
    await expect(pane).toHaveAttribute('data-restoring', 'false')
    await expect(activeTerminalRows(page)).toContainText(`FINAL_${scenario.runID}`)

    const visibleHistory = await terminalVisibleHistory(page)
    expect(visibleHistory.filter((visible) => visible.includes(`INTERMEDIATE_${scenario.runID}_`)),
      'visible terminal must not replay obsolete intermediate states').toEqual([])

    await page.keyboard.press('Enter')
    const accepted = `INPUT_${scenario.runID}:${unfinished}`
    await expect(activeTerminalRows(page)).toContainText(accepted)
    const rendered = await renderedTerminalText(activeTerminalRows(page))
    expect(occurrences(rendered, accepted)).toBe(1)
    expect(rendered).not.toContain(`IGNORED_${scenario.runID}`)

    const after = `AFTER_RESTORE_${scenario.runID}`
    await typeShellCommand(page, `printf '${after}\\n'`)
    await expect(activeTerminalRows(page)).toContainText(after)
  } catch (error) {
    testError = await diagnosticError(error, page, scenario, testInfo)
  } finally {
    cleanupError = await cleanupScenario(page, request, state.holon_id, terminal, scenario)
    if (cleanupError) {
      await testInfo.attach('terminal-cleanup-error', { body: cleanupError.message, contentType: 'text/plain' })
    }
  }
  if (testError) throw testError
  if (cleanupError) throw cleanupError
})

test('large stale restore becomes usable while terminal output continues', async ({ page, request }, testInfo) => {
  test.setTimeout(45_000)
  await page.setViewportSize({ width: 1280, height: 720 })
  const state = await fixtureState(request)
  const scenario = createScenario(state.worktree_path)
  let terminal: ManualTerminal | undefined
  let testError: Error | undefined
  let cleanupError: Error | undefined

  try {
    await removeScenarioFiles(scenario)
    terminal = await createManualTerminal(page, request, state)
    await typeShellCommand(page, `./terminal-slow-browser-scenario.sh ${scenario.runID} continuous`)
    await expect(activeTerminalRows(page)).toContainText(`READY_${scenario.runID}`)

    await blockRenderer(page)
    await writeFile(scenario.triggerPath, '')
    await expect.poll(() => fileExists(scenario.continuousPath), {
      message: `scenario ${scenario.runID} did not begin continuous output after its large backlog`,
      timeout: 10_000,
    }).toBe(true)

    const pane = activeTerminalPane(page)
    await expect(pane).toHaveAttribute('data-restoring', 'true')
    await releaseRenderer(page)
    await expect(pane, 'terminal must finish restoring before continuous process output stops')
      .toHaveAttribute('data-restoring', 'false', { timeout: 10_000 })
    expect(await fileExists(scenario.donePath), 'process output stopped before the terminal restored').toBe(false)
    await expect(activeTerminalRows(page)).toContainText(`LIVE_${scenario.runID}_`)

    await writeFile(scenario.stopPath, '')
    await expect.poll(() => fileExists(scenario.donePath), {
      message: `scenario ${scenario.runID} did not stop continuous output`,
      timeout: 10_000,
    }).toBe(true)
    await expect(activeTerminalRows(page)).toContainText(`FINAL_${scenario.runID}`)

    const acceptedInput = `AFTER_LIVE_RESTORE_${scenario.runID}`
    await typeShellCommand(page, acceptedInput)
    await expect(activeTerminalRows(page)).toContainText(`INPUT_${scenario.runID}:${acceptedInput}`)
  } catch (error) {
    testError = await diagnosticError(error, page, scenario, testInfo)
  } finally {
    cleanupError = await cleanupScenario(page, request, state.holon_id, terminal, scenario)
    if (cleanupError) {
      await testInfo.attach('terminal-cleanup-error', { body: cleanupError.message, contentType: 'text/plain' })
    }
  }
  if (testError) throw testError
  if (cleanupError) throw cleanupError
})

async function fixtureState(request: APIRequestContext) {
  const response = await request.get('/__e2e/state')
  expect(response.ok()).toBe(true)
  return response.json() as Promise<FixtureState>
}

function createScenario(worktreePath: string): Scenario {
  const runID = `b${randomBytes(8).toString('hex')}`
  if (!/^[A-Za-z0-9]+$/.test(runID)) throw new Error(`unsafe scenario run ID: ${runID}`)
  return {
    runID,
    triggerPath: path.join(worktreePath, `.terminal-e2e-${runID}-trigger`),
    progressPath: path.join(worktreePath, `.terminal-e2e-${runID}-progress`),
    continuousPath: path.join(worktreePath, `.terminal-e2e-${runID}-continuous`),
    stopPath: path.join(worktreePath, `.terminal-e2e-${runID}-stop`),
    donePath: path.join(worktreePath, `.terminal-e2e-${runID}-done`),
  }
}

async function createManualTerminal(page: Page, request: APIRequestContext, state: FixtureState) {
  const before = new Set((await sessionTerminals(request, state.holon_id)).map((terminal) => terminal.id))
  await page.goto(`/holons/${state.holon_id}`)
  await page.getByLabel('New tab').click()
  await page.getByRole('button', { name: 'Terminal', exact: true }).click()

  await expect.poll(async () => {
    const added = (await sessionTerminals(request, state.holon_id))
      .filter((terminal) => !before.has(terminal.id) && terminal.terminal_id)
    return added.length
  }, { message: 'manual terminal was not created through the UI' }).toBe(1)

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
  const response = await request.post(
    `/api/v1/holons/${encodeURIComponent(holonID)}/terminals/${encodeURIComponent(terminal.id)}/close`,
  )
  if (!response.ok()) {
    const body = await response.text()
    throw new Error(
      `failed to close manual terminal ${terminal.id} (${terminal.terminal_id ?? 'no canonical ID'}): ${response.status()} ${body}`,
    )
  }
}

async function cleanupScenario(
  page: Page,
  request: APIRequestContext,
  holonID: string,
  terminal: ManualTerminal | undefined,
  scenario: Scenario,
) {
  const errors: string[] = []
  await releaseRenderer(page)
  if (await fileExists(scenario.continuousPath) && !await fileExists(scenario.donePath)) {
    try {
      await writeFile(scenario.stopPath, '')
    } catch (error) {
      errors.push(`stop scenario ${scenario.runID}: ${error instanceof Error ? error.message : String(error)}`)
    }
  }
  if (await fileExists(scenario.triggerPath) && !await waitForFile(scenario.donePath, 10_000)) {
    errors.push(`scenario ${scenario.runID} did not finish within 10s after releasing the renderer`)
  }
  if (terminal) {
    try {
      await closeManualTerminal(request, holonID, terminal)
    } catch (error) {
      errors.push(error instanceof Error ? error.message : String(error))
    }
  }
  try {
    await removeScenarioFiles(scenario)
  } catch (error) {
    errors.push(`remove scenario files: ${error instanceof Error ? error.message : String(error)}`)
  }
  return errors.length > 0 ? new Error(errors.join('\n')) : undefined
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

async function blockRenderer(page: Page) {
  await page.evaluate(() => {
    const gate = window.__HOLARK_TERMINAL_RENDERER_E2E__
    if (!gate) throw new Error('terminal renderer E2E gate is not installed')
    gate.block()
  })
}

async function releaseRenderer(page: Page) {
  if (page.isClosed()) return
  await page.evaluate(() => window.__HOLARK_TERMINAL_RENDERER_E2E__?.release()).catch(() => {})
}

async function observeVisibleTerminalHistory(page: Page) {
  await page.evaluate(() => {
    const visibleText = () => {
      const pane = document.querySelector<HTMLElement>('[data-terminal-pane][data-active="true"]')
      if (!pane) return ''
      return [...pane.querySelectorAll<HTMLElement>('.xterm-rows')]
        .filter((rows) => getComputedStyle(rows).visibility !== 'hidden')
        .flatMap((rows) => [...rows.querySelectorAll('div')])
        .map((row) => row.textContent ?? '')
        .join('\n')
    }
    const record = () => {
      const text = visibleText()
      const history = window.__HOLARK_TERMINAL_VISIBLE_HISTORY__ ?? []
      if (text && history[history.length - 1] !== text) history.push(text.slice(-3_000))
      window.__HOLARK_TERMINAL_VISIBLE_HISTORY__ = history.slice(-60)
    }
    window.__HOLARK_TERMINAL_VISIBLE_HISTORY__ = []
    record()
    new MutationObserver(record).observe(document.body, {
      attributes: true,
      characterData: true,
      childList: true,
      subtree: true,
    })
  })
}

async function terminalVisibleHistory(page: Page) {
  return page.evaluate(() => window.__HOLARK_TERMINAL_VISIBLE_HISTORY__ ?? [])
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

async function waitForFile(filePath: string, timeout: number) {
  const deadline = Date.now() + timeout
  while (Date.now() < deadline) {
    if (await fileExists(filePath)) return true
    await new Promise((resolve) => setTimeout(resolve, 50))
  }
  return fileExists(filePath)
}

async function removeScenarioFiles(scenario: Scenario) {
  await Promise.all([
    rm(scenario.triggerPath, { force: true }),
    rm(scenario.progressPath, { force: true }),
    rm(scenario.continuousPath, { force: true }),
    rm(scenario.stopPath, { force: true }),
    rm(scenario.donePath, { force: true }),
  ])
}

function occurrences(value: string, needle: string) {
  return value.split(needle).length - 1
}

async function diagnosticError(error: unknown, page: Page, scenario: Scenario, testInfo: TestInfo) {
  const diagnostics = {
    run_id: scenario.runID,
    trigger_exists: await fileExists(scenario.triggerPath),
    progress_exists: await fileExists(scenario.progressPath),
    done_exists: await fileExists(scenario.donePath),
    restoring: await activeTerminalPane(page).getAttribute('data-restoring').catch(() => null),
    rendered: await renderedTerminalText(activeTerminalRows(page)).catch(() => ''),
    visible_history: await terminalVisibleHistory(page).catch(() => []),
  }
  const body = JSON.stringify(diagnostics, null, 2)
  await testInfo.attach('terminal-diagnostics', { body, contentType: 'application/json' })
  const message = error instanceof Error ? error.message : String(error)
  return new Error(`${message}\nterminal diagnostics: ${body}`)
}
