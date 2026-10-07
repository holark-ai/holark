import { expect, test, type Page } from '@playwright/test'

test('expands short tabs after closing a long tab and cancelling a rename', async ({ page, request }) => {
  const stateResponse = await request.get('/__e2e/state')
  expect(stateResponse.ok()).toBe(true)
  const { holon_id: holonId } = await stateResponse.json() as { holon_id: string }
  const terminalsURL = `/api/v1/holons/${holonId}/terminals`
  const longTitle = 'A very long terminal title that makes the other tabs collapse'
  const created: string[] = []

  try {
    for (const title of ['A', 'B', longTitle]) {
      const response = await request.post(terminalsURL, { data: { title } })
      expect(response.ok()).toBe(true)
      created.push((await response.json() as { id: string }).id)
    }
    await page.setViewportSize({ width: 1800, height: 900 })
    await page.goto(`/holons/${holonId}`)
    const viewport = page.getByTestId('tab-viewport')
    const tablist = page.getByRole('tablist', { name: 'Holon terminals' })
    await setStripWidth(page, 1400)
    await expect(viewport).toHaveAttribute('data-natural-width', 'true')
    await page.getByRole('tab', { name: longTitle, exact: true }).click()

    await setStripWidth(page, 200)
    await expect(tablist).toHaveAttribute('data-compact', 'true')
    await page.getByRole('button', { name: `Close ${longTitle}`, exact: true }).click()
    await expect(page.getByRole('tab', { name: longTitle, exact: true })).toHaveCount(0)
    await expect(viewport).toHaveAttribute('data-natural-width', 'true')
    await expect(tablist.getByText('A', { exact: true })).toBeVisible()
    await expect(tablist.getByText('B', { exact: true })).toBeVisible()

    await page.getByRole('tab', { name: 'A', exact: true }).dblclick()
    const rename = page.getByRole('textbox', { name: 'Rename A', exact: true })
    await expect(rename).toBeVisible()
    await expect(viewport).not.toHaveAttribute('data-natural-width')
    await rename.press('Escape')
    await expect(rename).toHaveCount(0)
    await expect(viewport).toHaveAttribute('data-natural-width', 'true')
    await expect(tablist.getByText('A', { exact: true })).toBeVisible()
    await expect(tablist.getByText('B', { exact: true })).toBeVisible()
  } finally {
    for (const id of created) await request.post(`${terminalsURL}/${id}/close`)
  }
})

async function setStripWidth(page: Page, width: number) {
  await page.getByTestId('tab-viewport').locator('..').evaluate((element, nextWidth) => {
    const strip = element as HTMLElement
    strip.style.width = `${nextWidth}px`
    strip.style.flex = `0 0 ${nextWidth}px`
  }, width)
}
