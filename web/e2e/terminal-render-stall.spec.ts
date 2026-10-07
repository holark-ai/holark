import { randomBytes } from 'node:crypto'
import { access, readFile, rm, writeFile } from 'node:fs/promises'
import path from 'node:path'
import { expect, test, type APIRequestContext, type Locator, type Page, type TestInfo } from '@playwright/test'
import type { TerminalTestSnapshot } from '../src/features/terminal/terminalTestSnapshots'

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

type HostProgress = {
  terminal_id: string
  dimensions: { columns: number; rows: number }
  last_sequence: number
  checkpoint_sequence: number
}

type StallScenario = {
  runID: string
  firstPath: string
  progressPath: string
  finalPath: string
  donePath: string
}

test.beforeEach(async ({ page }) => {
  await page.addInitScript(() => {
    let blocked = false
    let stallArmed = false
    let stallActive = false
    let writeWaiters: Array<() => void> = []
    let completionWaiters: Array<() => void> = []
    let stalledWaiters: Array<() => void> = []
    window.__HOLARK_TERMINAL_RENDERER_E2E__ = {
      block() {
        blocked = true
      },
      release() {
        blocked = false
        stallArmed = false
        stallActive = false
        const pending = [...writeWaiters, ...completionWaiters, ...stalledWaiters]
        writeWaiters = []
        completionWaiters = []
        stalledWaiters = []
        pending.forEach((resolve) => resolve())
      },
      beforeWrite() {
        if (!blocked) return Promise.resolve()
        return new Promise<void>((resolve) => writeWaiters.push(resolve))
      },
      afterWrite() {
        if (!stallArmed) return Promise.resolve()
        stallArmed = false
        stallActive = true
        const entered = stalledWaiters
        stalledWaiters = []
        entered.forEach((resolve) => resolve())
        return new Promise<void>((resolve) => completionWaiters.push(resolve))
      },
      stallNextCompletion() {
        if (stallArmed || stallActive) throw new Error('a render completion is already armed or stalled')
        stallArmed = true
      },
      stalled() {
        if (stallActive) return Promise.resolve()
        return new Promise<void>((resolve) => stalledWaiters.push(resolve))
      },
    }
  })
})

test('terminal keeps processing restored output while background animation frames are suspended', async ({ page, request }, testInfo) => {
  const state = await fixtureState(request)
  const scenario = createScenario(state.worktree_path)
  let terminal: ManualTerminal | undefined
  let testError: Error | undefined
  let cleanupError: Error | undefined

  try {
    terminal = await createManualTerminal(page, request, state)
    const terminalID = terminal.terminal_id!
    await typeTerminalLine(page, `./terminal-render-stall-scenario.sh ${scenario.runID}`)
    await expect(activeTerminalRows(page)).toContainText(`STALL_READY_${scenario.runID}`)

    // Headless browsers do not reliably suspend background tabs. Hold only
    // animation frames, leaving real xterm writes and watchdog timers running.
    await page.addInitScript(() => {
      const requestFrame = window.requestAnimationFrame.bind(window)
      const cancelFrame = window.cancelAnimationFrame.bind(window)
      const frames = new Map<number, FrameRequestCallback>()
      let frameID = 0
      Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'hidden' })
      window.requestAnimationFrame = (callback) => {
        frames.set(--frameID, callback)
        return frameID
      }
      window.cancelAnimationFrame = (id) => { frames.delete(id) }
      window.addEventListener('resume-background-frames', () => {
        Reflect.deleteProperty(document, 'visibilityState')
        window.requestAnimationFrame = requestFrame
        window.cancelAnimationFrame = cancelFrame
        document.dispatchEvent(new Event('visibilitychange'))
        frames.forEach((callback) => requestFrame(callback))
        frames.clear()
      }, { once: true })
    })
    await page.reload()
    await expect(activeTerminalPane(page)).toHaveAttribute('data-terminal-id', terminalID)
    await expect.poll(async () => (await terminalSnapshot(page, terminalID))?.sequence)
      .toBe((await hostProgress(request, terminalID)).last_sequence)
    const restored = await terminalSnapshot(page, terminalID)
    expect(restored?.restoring).toBe(true)

    await writeFile(scenario.firstPath, '')
    await writeFile(scenario.finalPath, '')
    await expect.poll(() => fileExists(scenario.donePath)).toBe(true)

    // Span more than two watchdog intervals while presentation remains paused.
    await page.evaluate(() => new Promise((resolve) => setTimeout(resolve, 3_500)))
    const host = await hostProgress(request, terminalID)
    const background = await terminalSnapshot(page, terminalID)
    expect(background).toMatchObject({
      sequence: host.last_sequence,
      render_epoch: restored!.render_epoch,
      pending_render_completion: false,
      pending_render_bytes: 0,
      connected: true,
      consumer_stale: false,
      restoring: true,
    })

    await page.evaluate(() => window.dispatchEvent(new Event('resume-background-frames')))
    await expect(activeTerminalPane(page)).toHaveAttribute('data-restoring', 'false')
    await expect(activeTerminalRows(page)).toContainText(`STALL_FINAL_${scenario.runID}`)
    const input = `after_background_${scenario.runID}`
    await typeTerminalLine(page, input)
    await expect(activeTerminalRows(page)).toContainText(`STALL_INPUT_${scenario.runID}:${input}`)
  } catch (error) {
    testError = await diagnosticError(error, page, request, terminal?.terminal_id, scenario, testInfo)
  } finally {
    await page.evaluate(() => window.dispatchEvent(new Event('resume-background-frames'))).catch(() => {})
    cleanupError = await cleanupScenario(page, request, state.holon_id, terminal, scenario)
  }
  if (testError) throw testError
  if (cleanupError) throw cleanupError
})

test('terminal autonomously recovers from a permanently stalled render completion', async ({ page, request }, testInfo) => {
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
    if (!terminal.terminal_id) throw new Error('manual terminal has no canonical terminal ID')
    const terminalID = terminal.terminal_id
    await typeTerminalLine(page, `./terminal-render-stall-scenario.sh ${scenario.runID}`)
    await expect(activeTerminalRows(page)).toContainText(`STALL_READY_${scenario.runID}`)

    const initialHost = await hostProgress(request, terminalID)
    const initialBrowser = await terminalSnapshot(page, terminalID)
    if (!initialBrowser) throw new Error('browser terminal diagnostic snapshot is missing')

    await armCompletionStall(page)
    await writeFile(scenario.firstPath, '')
    await waitForCompletionStall(page)
    const stalledAt = Date.now()
    await expect.poll(() => fileExists(scenario.progressPath), {
      message: 'render-stall scenario did not emit its first update',
    }).toBe(true)

    await writeFile(scenario.finalPath, '')
    await expect.poll(() => fileExists(scenario.donePath), {
      message: 'PTY did not finish while browser render completion was stalled',
    }).toBe(true)
    await expect.poll(() => readText(scenario.progressPath), {
      message: 'external progress marker did not reach the final update',
    }).toBe('final\n')

    let progressedHost: HostProgress | undefined
    await expect.poll(async () => {
      progressedHost = await hostProgress(request, terminalID)
      return progressedHost.last_sequence > initialHost.last_sequence &&
        progressedHost.checkpoint_sequence > initialHost.checkpoint_sequence
    }, { message: 'host output and checkpoint did not advance behind the stalled browser' }).toBe(true)
    if (!progressedHost) throw new Error('host progress snapshot is missing')

    const stalledBrowser = await terminalSnapshot(page, terminalID)
    if (!stalledBrowser) throw new Error('stalled browser terminal diagnostic snapshot is missing')
    expect(stalledBrowser.sequence, 'browser completion must remain behind host progress').toBeLessThan(progressedHost.last_sequence)
    expect(stalledBrowser.sequence, 'the stalled completion must not advance browser progress').toBe(initialBrowser.sequence)
    await expect(activeTerminalRows(page)).not.toContainText(`STALL_FINAL_${scenario.runID}`)

    await page.setViewportSize({ width: 1500, height: 900 })
    const afterResizeBrowser = await terminalSnapshot(page, terminalID)
    expect(afterResizeBrowser?.sequence).toBe(stalledBrowser.sequence)

    const detectionRemaining = Math.max(1, 2_000 - (Date.now() - stalledAt))
    await expect(activeTerminalPane(page), 'browser must detect stalled render progress within two seconds')
      .toHaveAttribute('data-restoring', 'true', { timeout: detectionRemaining })
    expect(Date.now() - stalledAt).toBeLessThanOrEqual(2_000)

    const recoveryRemaining = Math.max(1, 10_000 - (Date.now() - stalledAt))
    await expect(activeTerminalRows(page), 'latest checkpoint must become visible after autonomous recovery')
      .toContainText(`STALL_FINAL_${scenario.runID}`, { timeout: recoveryRemaining })
    await expect(activeTerminalPane(page)).toHaveAttribute('data-restoring', 'false')

    const dimensionMarker = `dimensions_${scenario.runID}`
    await typeTerminalLine(page, dimensionMarker)
    const ptyDimensions = await readPTYDimensions(page, scenario.runID)
    await expectRecoveredGeometry(page, request, terminalID, ptyDimensions)

    const acceptedInput = `after_stall_${scenario.runID}`
    await typeTerminalLine(page, acceptedInput)
    const accepted = `STALL_INPUT_${scenario.runID}:${acceptedInput}`
    await expect(activeTerminalRows(page)).toContainText(accepted)
    expect(occurrences(await renderedTerminalText(activeTerminalRows(page)), accepted)).toBe(1)

    const current = (await sessionTerminals(request, state.holon_id)).find((candidate) => candidate.id === terminal?.id)
    expect(current?.terminal_id, 'recovery must retain logical terminal identity').toBe(terminalID)
  } catch (error) {
    testError = await diagnosticError(error, page, request, terminal?.terminal_id, scenario, testInfo)
  } finally {
    cleanupError = await cleanupScenario(page, request, state.holon_id, terminal, scenario)
  }
  if (testError) throw testError
  if (cleanupError) throw cleanupError
})

async function fixtureState(request: APIRequestContext) {
  const response = await request.get('/__e2e/state')
  expect(response.ok()).toBe(true)
  return response.json() as Promise<FixtureState>
}

function createScenario(worktreePath: string): StallScenario {
  const runID = `s${randomBytes(8).toString('hex')}`
  return {
    runID,
    firstPath: path.join(worktreePath, `.terminal-render-stall-${runID}-first`),
    progressPath: path.join(worktreePath, `.terminal-render-stall-${runID}-progress`),
    finalPath: path.join(worktreePath, `.terminal-render-stall-${runID}-final`),
    donePath: path.join(worktreePath, `.terminal-render-stall-${runID}-done`),
  }
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
  await expect(activeTerminalPane(page)).toHaveAttribute('data-terminal-id', terminal.terminal_id!)
  await expect(activeTerminalPane(page)).toHaveAttribute('data-restoring', 'false')
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

async function typeTerminalLine(page: Page, value: string) {
  await activeTerminalPane(page).locator('.xterm').filter({ visible: true }).click()
  await page.keyboard.type(value)
  await page.keyboard.press('Enter')
}

async function armCompletionStall(page: Page) {
  await page.evaluate(() => {
    const gate = window.__HOLARK_TERMINAL_RENDERER_E2E__
    if (!gate?.stallNextCompletion) throw new Error('render completion stall gate is not installed')
    gate.stallNextCompletion()
  })
}

async function waitForCompletionStall(page: Page) {
  await page.evaluate(async () => {
    const gate = window.__HOLARK_TERMINAL_RENDERER_E2E__
    if (!gate?.stalled) throw new Error('render completion stall barrier is not installed')
    await Promise.race([
      gate.stalled(),
      new Promise<never>((_, reject) => setTimeout(() => reject(new Error('render completion did not stall')), 5_000)),
    ])
  })
}

async function terminalSnapshot(page: Page, terminalID: string) {
  return page.evaluate((id) => {
    const diagnostics = window.__HOLARK_TERMINAL_TEST_SNAPSHOTS__
    if (!diagnostics) throw new Error('terminal E2E diagnostics are not installed')
    return diagnostics.snapshots().find((snapshot) => snapshot.terminal_id === id)
  }, terminalID) as Promise<TerminalTestSnapshot | undefined>
}

async function readPTYDimensions(page: Page, runID: string) {
  const marker = `STALL_DIMS_${runID}:`
  await expect(activeTerminalRows(page)).toContainText(marker)
  const match = new RegExp(`STALL_DIMS_${runID}:(\\d+)x(\\d+)`).exec(await renderedTerminalText(activeTerminalRows(page)))
  if (!match) throw new Error('recovered PTY dimension marker was not rendered')
  return { columns: Number.parseInt(match[1], 10), rows: Number.parseInt(match[2], 10) }
}

async function expectRecoveredGeometry(
  page: Page,
  request: APIRequestContext,
  terminalID: string,
  pty: { columns: number; rows: number },
) {
  await expect.poll(async () => {
    const browser = await terminalSnapshot(page, terminalID)
    const host = await hostProgress(request, terminalID)
    return {
      browser: { columns: browser?.columns, rows: browser?.rows },
      proposed: browser?.proposed_dimensions,
      host: host.dimensions,
    }
  }, { message: 'browser, host, stable fit, and PTY dimensions must agree after recovery' }).toEqual({
    browser: pty,
    proposed: pty,
    host: pty,
  })
}

async function renderedTerminalText(rows: Locator) {
  return rows.locator('div').evaluateAll((lines) => lines.map((line) => line.textContent ?? '').join('\n'))
}

async function cleanupScenario(
  page: Page,
  request: APIRequestContext,
  holonID: string,
  terminal: ManualTerminal | undefined,
  scenario: StallScenario,
) {
  const errors: string[] = []
  if (!page.isClosed()) {
    await page.evaluate(() => window.__HOLARK_TERMINAL_RENDERER_E2E__?.release()).catch(() => {})
  }
  if (terminal) {
    const response = await request.post(
      `/api/v1/holons/${encodeURIComponent(holonID)}/terminals/${encodeURIComponent(terminal.id)}/close`,
    ).catch(() => undefined)
    if (!response?.ok()) errors.push(`failed to close manual terminal ${terminal.id}`)
  }
  try {
    await removeScenarioFiles(scenario)
  } catch (error) {
    errors.push(`remove scenario files: ${error instanceof Error ? error.message : String(error)}`)
  }
  return errors.length ? new Error(errors.join('\n')) : undefined
}

async function diagnosticError(
  error: unknown,
  page: Page,
  request: APIRequestContext,
  terminalID: string | undefined,
  scenario: StallScenario,
  testInfo: TestInfo,
) {
  const diagnostic = {
    browser: terminalID && !page.isClosed() ? await terminalSnapshot(page, terminalID).catch((failure) => String(failure)) : undefined,
    host: terminalID ? await hostProgress(request, terminalID).catch((failure) => String(failure)) : undefined,
    external: {
      first: await fileExists(scenario.firstPath),
      progress: await readText(scenario.progressPath),
      final: await fileExists(scenario.finalPath),
      done: await fileExists(scenario.donePath),
    },
    restoring: !page.isClosed() ? await activeTerminalPane(page).getAttribute('data-restoring').catch(() => undefined) : undefined,
    rendered: !page.isClosed() ? await renderedTerminalText(activeTerminalRows(page)).catch((failure) => String(failure)) : undefined,
  }
  await testInfo.attach('terminal-render-stall-diagnostic', {
    body: JSON.stringify(diagnostic, null, 2),
    contentType: 'application/json',
  })
  return error instanceof Error ? error : new Error(String(error))
}

async function removeScenarioFiles(scenario: StallScenario) {
  await Promise.all([scenario.firstPath, scenario.progressPath, scenario.finalPath, scenario.donePath]
    .map((file) => rm(file, { force: true })))
}

async function fileExists(file: string) {
  try {
    await access(file)
    return true
  } catch {
    return false
  }
}

async function readText(file: string) {
  try {
    return await readFile(file, 'utf8')
  } catch {
    return ''
  }
}

function occurrences(value: string, expected: string) {
  return value.split(expected).length - 1
}
