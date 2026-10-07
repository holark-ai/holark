import { expect, test } from '@playwright/test'

test('Settings saves only edited launch commands from stale windows', async ({ context, page }) => {
  await context.route('**/api/v1/agent-capabilities', (route) => route.fulfill({ json: {
    capabilities: [{ type: 'codex', available: true, automated_workflows: true }],
  } }))
  await context.route('**/api/v1/prompt-templates', (route) => route.fulfill({ json: [] }))
  let commands = { codex: '', 'claude-code': '', opencode: '' }
  const updates: unknown[] = []
  await context.route('**/api/v1/settings/agent-launch-commands', async (route) => {
    if (route.request().method() === 'PUT') {
      const changes = route.request().postDataJSON().commands
      updates.push(changes)
      commands = { ...commands, ...changes }
      await route.fulfill({ status: 204 })
    } else {
      await route.fulfill({ json: { commands } })
    }
  })
  const other = await context.newPage()
  for (const window of [page, other]) {
    await window.goto('/settings')
    await window.getByText('Launch commands', { exact: true }).click()
    await expect(window.getByRole('textbox', { name: 'Codex', exact: true })).toBeEnabled()
  }
  await page.getByRole('textbox', { name: 'Codex', exact: true }).fill('custom-codex')
  await page.getByRole('button', { name: 'Save', exact: true }).click()
  await expect(page.getByRole('status')).toHaveText('Launch commands saved.')

  await other.getByRole('textbox', { name: 'Claude Code', exact: true }).fill('npx claude')
  await other.getByRole('button', { name: 'Save', exact: true }).click()
  await expect(other.getByRole('status')).toHaveText('Launch commands saved.')
  expect(updates).toEqual([{ codex: 'custom-codex' }, { 'claude-code': 'npx claude' }])
  expect(commands).toEqual({ codex: 'custom-codex', 'claude-code': 'npx claude', opencode: '' })

  await page.getByRole('textbox', { name: 'Codex', exact: true }).fill('')
  await page.getByRole('button', { name: 'Save', exact: true }).click()
  await expect(page.getByRole('status')).toHaveText('Launch commands saved.')
  expect(updates.at(-1)).toEqual({ codex: '' })
  expect(commands).toEqual({ codex: '', 'claude-code': 'npx claude', opencode: '' })
  await expect(page.getByRole('button', { name: 'Save', exact: true })).toBeDisabled()
})
