import { expect, test } from '@playwright/test'

test('Settings model selection supports keyboard use and narrow layouts', async ({ page }) => {
  await page.route('**/api/v1/agent-capabilities', (route) => route.fulfill({ json: {
    capabilities: [{ type: 'codex', available: true, automated_workflows: true }],
    harness_defaults: { default: { harness_type: 'codex', explicit: true, model: 'saved-model' } },
  } }))
  await page.route('**/api/v1/prompt-templates', (route) => route.fulfill({ json: [] }))
  await page.route('**/api/v1/agents/codex/models', (route) => route.fulfill({ json: [{ id: 'discovered-model', label: 'Discovered model' }] }))
  await page.route('**/api/v1/settings/agent-harness-defaults/default', async (route) => {
    await route.fulfill({ json: { ...route.request().postDataJSON(), explicit: true } })
  })
  await page.goto('/settings')
  const model = page.getByRole('combobox', { name: 'Default harness model', exact: true })
  await expect(model).toContainText('saved-model')
  await model.focus()
  await page.keyboard.press('Enter')
  await expect(page.getByRole('option', { name: 'Discovered model', exact: true })).toBeVisible()
  await page.keyboard.press('End')
  await expect(page.getByRole('option', { name: 'Custom model…', exact: true })).toBeFocused()
  await page.keyboard.press('Enter')
  const input = page.getByRole('textbox', { name: 'Default harness custom model ID' })
  await expect(input).toBeFocused()
  await input.fill('provider/exact-version')
  await page.keyboard.press('Tab')
  await expect(page.getByRole('button', { name: 'Save', exact: true })).toBeFocused()
  await page.keyboard.press('Enter')
  await expect(model).toContainText('provider/exact-version')
  await expect(page.getByLabel('Work on an issue model', { exact: true })).toContainText('provider/exact-version')
  for (const width of [1280, 390]) {
    await page.setViewportSize({ width, height: 900 })
    await expect(model).toBeVisible()
    const controls = await page.getByRole('combobox').all()
    for (const control of controls) {
      const bounds = await control.boundingBox()
      expect(bounds).not.toBeNull()
      expect(bounds!.x).toBeGreaterThanOrEqual(0)
      expect(bounds!.x + bounds!.width).toBeLessThanOrEqual(width)
    }
    await page.screenshot({ path: `/tmp/settings-models-${width}.png`, fullPage: true })
  }
})
