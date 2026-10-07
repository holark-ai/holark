import { randomBytes } from 'node:crypto'
import { expect, test, type APIRequestContext, type Locator, type Page } from '@playwright/test'
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

type Dimensions = {
  columns: number
  rows: number
}

type ResizeChurnState = {
  callbacks: number
  startedAt: number
  animation: Animation
  observer: ResizeObserver
}

declare global {
  interface Window {
    __HOLARK_RESIZE_CHURN__?: ResizeChurnState
  }
}

test('terminal delivers its latest grid while ResizeObserver activity continues', async ({ page, request }) => {
  test.setTimeout(60_000)
  await page.setViewportSize({ width: 1280, height: 720 })
  const state = await fixtureState(request)
  const runID = `r${randomBytes(8).toString('hex')}`
  let terminal: ManualTerminal | undefined

  try {
    terminal = await createManualTerminal(page, request, state)
    if (!terminal.terminal_id) throw new Error('manual terminal has no canonical terminal ID')
    const terminalID = terminal.terminal_id
    await typeTerminalLine(page, `./terminal-resize-delivery-scenario.sh ${runID}`)
    await expect(activeTerminalRows(page)).toContainText(`RESIZE_READY_${runID}`)

    const baseline = requiredGrid(await terminalSnapshot(page, terminalID))
    const layout = await chooseResizeLayout(page, terminalID, baseline)
    await startResizeChurn(page, layout)

    let changed: Dimensions | undefined
    await expect.poll(async () => {
      const current = requiredGrid(await terminalSnapshot(page, terminalID))
      if (!sameDimensions(current, baseline)) changed = current
      return changed
    }, { message: `browser grid did not change from ${formatDimensions(baseline)}` }).not.toBeUndefined()

    const stable = await sampleGridDuringChurn(page, terminalID, 250)
    expect(stable.grids, 'browser grid must remain stable during observer churn').toEqual([formatDimensions(changed!)])
    expect(stable.callbackDelta, 'the mounted terminal target must continue producing ResizeObserver callbacks').toBeGreaterThanOrEqual(3)
    expect(stable.animationRunning, 'observer churn animation must still be active after the grid stabilizes').toBe(true)

    const deadline = performance.now() + 1_000
    let applied: Dimensions | undefined
    while (performance.now() < deadline) {
      applied = latestAppliedDimensions(await renderedTerminalText(activeTerminalRows(page)), runID)
      if (applied) break
      await page.waitForTimeout(20)
    }
    if (!applied) {
      const diagnostic = await resizeDiagnostic(page, terminalID, baseline, changed!)
      throw new Error(`PTY resize was not delivered within the bounded delay: ${JSON.stringify(diagnostic)}`)
    }

    expect(applied, 'autonomous SIGWINCH dimensions must match the stable browser grid').toEqual(changed)
    const delivered = await resizeDiagnostic(page, terminalID, baseline, changed!)
    expect(delivered.animationRunning, 'PTY resize must arrive before observer churn ends').toBe(true)
    expect(delivered.elapsedMilliseconds).toBeLessThan(1_000)
  } finally {
    await page.evaluate(() => {
      const churn = window.__HOLARK_RESIZE_CHURN__
      churn?.animation.cancel()
      churn?.observer.disconnect()
      delete window.__HOLARK_RESIZE_CHURN__
    }).catch(() => {})
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

async function renderedTerminalText(rows: Locator) {
  return rows.locator('div').evaluateAll((lines) => lines.map((line) => line.textContent ?? '').join('\n'))
}

async function terminalSnapshot(page: Page, terminalID: string) {
  return page.evaluate((id) => {
    const diagnostics = window.__HOLARK_TERMINAL_TEST_SNAPSHOTS__
    if (!diagnostics) throw new Error('terminal E2E diagnostics are not installed')
    return diagnostics.snapshots().find((snapshot) => snapshot.terminal_id === id)
  }, terminalID) as Promise<TerminalTestSnapshot | undefined>
}

function requiredGrid(snapshot: TerminalTestSnapshot | undefined): Dimensions {
  if (!snapshot) throw new Error('terminal diagnostic snapshot is unavailable')
  return { columns: snapshot.columns, rows: snapshot.rows }
}

async function chooseResizeLayout(page: Page, terminalID: string, baseline: Dimensions) {
  const snapshot = await terminalSnapshot(page, terminalID)
  if (!snapshot?.container_rectangle || !snapshot.screen_rectangle) {
    throw new Error('terminal layout rectangles are unavailable')
  }
  const cellWidth = snapshot.screen_rectangle.width / baseline.columns
  for (let inset = 72; inset <= 160; inset += 1) {
    const available = snapshot.container_rectangle.width - inset
    const columns = Math.floor(available / cellWidth)
    const slack = available - columns * cellWidth
    if (columns !== baseline.columns && slack > 1 && cellWidth - slack > 1) {
      return { inset, oscillation: 0.5 }
    }
  }
  throw new Error(`could not choose a stable sub-cell resize interval from cell width ${cellWidth}`)
}

async function startResizeChurn(page: Page, layout: { inset: number; oscillation: number }) {
  await page.evaluate(({ inset, oscillation }) => {
    const target = document.querySelector<HTMLElement>('[data-terminal-pane][data-active="true"] [data-presented="true"]')
    if (!target) throw new Error('mounted terminal target is unavailable')
    const state: ResizeChurnState = {
      callbacks: 0,
      startedAt: performance.now(),
      animation: undefined as unknown as Animation,
      observer: undefined as unknown as ResizeObserver,
    }
    state.observer = new ResizeObserver(() => { state.callbacks += 1 })
    state.observer.observe(target)
    target.style.right = `${inset}px`
    state.animation = target.animate([
      { right: `${inset}px` },
      { right: `${inset + oscillation}px` },
      { right: `${inset}px` },
    ], { duration: 2_000, easing: 'linear' })
    window.__HOLARK_RESIZE_CHURN__ = state
  }, layout)
}

async function sampleGridDuringChurn(page: Page, terminalID: string, durationMilliseconds: number) {
  return page.evaluate(async ({ id, duration }) => {
    const churn = window.__HOLARK_RESIZE_CHURN__
    if (!churn) throw new Error('resize churn is not installed')
    const callbacksBefore = churn.callbacks
    const grids = new Set<string>()
    const until = performance.now() + duration
    while (performance.now() < until) {
      await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()))
      const snapshot = window.__HOLARK_TERMINAL_TEST_SNAPSHOTS__?.snapshots()
        .find((candidate) => candidate.terminal_id === id)
      if (!snapshot) throw new Error('terminal diagnostic snapshot is unavailable during churn')
      grids.add(`${snapshot.columns}x${snapshot.rows}`)
    }
    return {
      grids: [...grids],
      callbackDelta: churn.callbacks - callbacksBefore,
      animationRunning: churn.animation.playState === 'running',
    }
  }, { id: terminalID, duration: durationMilliseconds })
}

async function resizeDiagnostic(page: Page, terminalID: string, baseline: Dimensions, current: Dimensions) {
  return page.evaluate(({ id, initial, changed }) => {
    const churn = window.__HOLARK_RESIZE_CHURN__
    const snapshot = window.__HOLARK_TERMINAL_TEST_SNAPSHOTS__?.snapshots()
      .find((candidate) => candidate.terminal_id === id)
    return {
      baseline: initial,
      expected: changed,
      browser: snapshot ? { columns: snapshot.columns, rows: snapshot.rows } : undefined,
      observerCallbacks: churn?.callbacks ?? 0,
      animationRunning: churn?.animation.playState === 'running',
      elapsedMilliseconds: churn ? performance.now() - churn.startedAt : Number.POSITIVE_INFINITY,
    }
  }, { id: terminalID, initial: baseline, changed: current })
}

function latestAppliedDimensions(rendered: string, runID: string): Dimensions | undefined {
  const matches = [...rendered.matchAll(new RegExp(`RESIZE_APPLIED_${runID}:(\\d+)x(\\d+)`, 'g'))]
  const match = matches.at(-1)
  return match ? { columns: Number.parseInt(match[1], 10), rows: Number.parseInt(match[2], 10) } : undefined
}

function sameDimensions(left: Dimensions, right: Dimensions) {
  return left.columns === right.columns && left.rows === right.rows
}

function formatDimensions(dimensions: Dimensions) {
  return `${dimensions.columns}x${dimensions.rows}`
}

async function closeManualTerminal(request: APIRequestContext, holonID: string, terminal: ManualTerminal) {
  const response = await request.post(
    `/api/v1/holons/${encodeURIComponent(holonID)}/terminals/${encodeURIComponent(terminal.id)}/close`,
  )
  if (!response.ok()) throw new Error(`failed to close manual terminal ${terminal.id}: ${response.status()} ${await response.text()}`)
}
