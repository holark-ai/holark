import { expect, test, type APIRequestContext } from '@playwright/test'
import type { Holon } from '../src/data/types'

type FixtureState = {
  holon_id: string
}

type ManualTerminal = {
  id: string
  title: string
}

test('holon tab strip renders a visible window and selects hidden tabs from overflow', async ({ page, request }) => {
  const state = await fixtureState(request)
  const created: ManualTerminal[] = []

  try {
    for (let index = 0; index < 4; index += 1) {
      created.push(await createManualTerminal(request, state.holon_id))
    }

    const response = await request.get(`/api/v1/holons/${state.holon_id}`)
    expect(response.ok()).toBe(true)
    const holon = await response.json() as Holon
    const allTitles = [
      ...(holon.agent_sessions ?? []).filter((tab) => !tab.closed_at).map((tab) => tab.title || 'Agent'),
      ...(holon.manual_terminals ?? []).filter((tab) => !tab.closed_at).map((tab) => tab.title),
      ...(holon.ides ?? []).filter((tab) => tab.desired_open).map(() => 'IDE'),
    ]
    await page.setViewportSize({ width: 260, height: 720 })
    await page.goto(`/holons/${state.holon_id}`)
    await page.getByTestId('holon-tabs').evaluate((element) => {
      const header = element.closest<HTMLElement>('header')
      if (!header) throw new Error('holon header is missing')
      header.style.width = '340px'
      header.style.flex = '0 0 340px'
      header.style.position = 'fixed'
      header.style.left = '0'
      header.style.top = '100px'
      header.style.zIndex = '1000'
      const tabs = element as HTMLElement
      tabs.style.width = '120px'
      tabs.style.flex = '0 0 120px'
    })
    await settleLayout(page)

    const tabs = page.getByRole('tab')
    await expect(tabs).toHaveCount(2)
    const selected = page.getByRole('tab', { selected: true })
    await expect(selected.locator(':scope > button[aria-label^="Close "]')).toBeVisible()
    await expect(selected.locator(':scope > span')).toHaveCount(0)
    const unselected = page.locator('[role="tab"][aria-selected="false"]')
    await expect(unselected.locator(':scope > span')).toHaveCount(1)
    await expect(unselected.locator(':scope > button')).toHaveCount(0)
    const visibleTitles = await tabs.evaluateAll((elements) => elements.map((element) => element.getAttribute('aria-label') ?? ''))
    const hiddenTitles = allTitles.filter((title) => !visibleTitles.includes(title))
    expect(hiddenTitles.length).toBeGreaterThan(0)

    const overflow = page.getByRole('button', { name: `${hiddenTitles.length} hidden ${hiddenTitles.length === 1 ? 'tab' : 'tabs'}` })
    await overflow.click()
    const overflowMenu = overflow.locator('..')
    const overflowOptions = overflowMenu.locator('div').getByRole('button')
    await expect(overflowOptions).toHaveCount(hiddenTitles.length)
    expect((await overflowOptions.allTextContents()).sort()).toEqual([...hiddenTitles].sort())

    const selectedTitle = hiddenTitles.at(-1)!
    await overflowMenu.getByRole('button', { name: selectedTitle }).click()
    await expect(overflow).toHaveAttribute('aria-expanded', 'false')

    const selectedOverflowTab = page.getByRole('tab', { name: selectedTitle })
    await expect(selectedOverflowTab).toHaveAttribute('aria-selected', 'true')
    await expect(page.getByRole('tab')).toHaveCount(2)
    await expect(overflow).toHaveText(`+${hiddenTitles.length}`)
  } finally {
    for (const terminal of created.reverse()) {
      await closeManualTerminal(request, state.holon_id, terminal.id).catch(() => {})
    }
  }
})

test('holon header menus stay in the viewport, layer above rails, and accept pointer clicks', async ({ page }) => {
  await page.setViewportSize({ width: 900, height: 300 })
  await page.goto('/__dev/holon')
  await page.getByText('Preview controls', { exact: true }).click()
  await page.getByRole('spinbutton', { name: 'Tabs' }).fill('6')
  await page.getByText('Preview controls', { exact: true }).locator('..').evaluate((element) => { (element as HTMLElement).style.display = 'none' })
  const header = page.getByTestId('holon-tabs').locator('..')
  await header.evaluate((element) => {
    const target = element as HTMLElement
    target.style.position = 'fixed'
    target.style.left = '0'
    target.style.top = '210px'
    target.style.width = '850px'
    const tabs = target.querySelector<HTMLElement>('[data-testid="holon-tabs"]')
    if (!tabs) throw new Error('holon tabs are missing')
    tabs.style.width = '120px'
    tabs.style.flex = '0 0 120px'
  })
  await settleLayout(page)

  const rail = page.getByRole('navigation', { name: 'Primary navigation' })
  expect(Number(await header.evaluate((element) => getComputedStyle(element).zIndex))).toBeGreaterThan(Number(await rail.evaluate((element) => getComputedStyle(element).zIndex)))

  const addTab = page.getByRole('button', { name: 'New tab' })
  await addTab.click()
  await expectHeaderPopoverOnScreen(page)

  const hiddenTabs = page.getByRole('button', { name: /hidden tabs?/ })
  await hiddenTabs.click()
  await expect(addTab).toHaveAttribute('aria-expanded', 'false')
  await expectHeaderPopoverOnScreen(page)
  await page.keyboard.press('Escape')
  await expect(hiddenTabs).toHaveAttribute('aria-expanded', 'false')
  await expect(hiddenTabs).toBeFocused()

  await addTab.click()
  const addMenu = page.locator('[data-holon-header-popover]:visible')
  await addMenu.getByRole('button', { name: 'Terminal' }).click()
  await expect(addTab).toHaveAttribute('aria-expanded', 'false')

  const moreActions = page.getByRole('button', { name: 'More holon actions' })
  await moreActions.click()
  await expectHeaderPopoverOnScreen(page)
  await page.getByRole('button', { name: 'End holon' }).click()
  await expect(moreActions).toHaveAttribute('aria-expanded', 'false')

  await moreActions.click()
  const detailsPopover = await expectHeaderPopoverOnScreen(page)
  await detailsPopover.getByRole('heading', { name: 'Task', exact: true }).click()
  await expect(moreActions).toHaveAttribute('aria-expanded', 'true')
  await page.mouse.click(400, 100)
  await expect(moreActions).toHaveAttribute('aria-expanded', 'false')
})

async function fixtureState(request: APIRequestContext) {
  const response = await request.get('/__e2e/state')
  expect(response.ok()).toBe(true)
  return response.json() as Promise<FixtureState>
}

async function createManualTerminal(request: APIRequestContext, holonID: string) {
  const response = await request.post(`/api/v1/holons/${encodeURIComponent(holonID)}/terminals`, { data: { title: '' } })
  expect(response.ok()).toBe(true)
  return response.json() as Promise<ManualTerminal>
}

async function closeManualTerminal(request: APIRequestContext, holonID: string, terminalID: string) {
  const response = await request.post(`/api/v1/holons/${encodeURIComponent(holonID)}/terminals/${encodeURIComponent(terminalID)}/close`)
  if (!response.ok()) throw new Error(`failed to close manual terminal ${terminalID}: ${response.status()} ${await response.text()}`)
}

async function settleLayout(page: import('@playwright/test').Page) {
  await page.evaluate(() => new Promise<void>((resolve) => {
    requestAnimationFrame(() => requestAnimationFrame(() => resolve()))
  }))
}

async function expectHeaderPopoverOnScreen(page: import('@playwright/test').Page) {
  const popover = page.locator('[data-holon-header-popover]:visible')
  await expect(popover).toHaveCount(1)
  await expect.poll(async () => {
    const bounds = await popover.boundingBox()
    const viewport = page.viewportSize()
    if (!bounds || !viewport) return false
    return bounds.x >= 7.5 && bounds.y >= 7.5
      && bounds.x + bounds.width <= viewport.width - 7.5
      && bounds.y + bounds.height <= viewport.height - 7.5
  }).toBe(true)
  return popover
}
