import { expect, test } from '@playwright/test'
import type { CreatePullRequestCommentRequest, PullRequestComment } from '../src/data/types'

test('creates comments and manages threads in Overview and Changes', async ({ page }) => {
  const now = new Date().toISOString()
  const comments: PullRequestComment[] = []
  const creates: CreatePullRequestCommentRequest[] = []
  const workerRequests: Array<{ comment_ids: string[], mode: string }> = []
  const patch = '@@ -1,2 +1,2 @@\n-before\n+after\n context\n'
  // Every PR endpoint is controlled here; this test never publishes to GitHub.
  await page.route('**/api/v1/pull-requests/**', async (route) => {
    const path = new URL(route.request().url()).pathname
    const method = route.request().method()
    let json: unknown = []
    if (path.endsWith('/scoped-pr')) {
      json = { id: 'scoped-pr', title: 'Scoped comments smoke', summary: 'Fixture PR', status: 'open', view_revision: 1, diff_base_commit: 'abc12345', assignee_holark_ids: [], requested_reviewer_holark_ids: [], base_branch: 'main', head_branch: 'feature', base_commit: 'abc12345', head_commit: 'def45678', sync_data: {}, linked_session_ids: [], created_at: now, updated_at: now }
    } else if (path.endsWith('/metadata')) {
      json = { view_revision: 1, pull_request_id: 'scoped-pr', title: 'Scoped comments smoke', description: 'Fixture PR', updated_at: now }
    } else if (path.endsWith('/changes')) {
      json = { branch: 'feature', base_branch: 'main', base_commit: 'abc12345', head_commit: 'def45678', has_changes: true, dirty: false, diff_truncated: false, files: [{ path: 'new.ts', old_path: 'old.ts', status: 'renamed', additions: 1, deletions: 1, binary: false, diff_truncated: false, diff: patch }, { path: 'image.png', status: 'added', additions: 0, deletions: 0, binary: true, diff_truncated: false }] }
      json = { inputs: { head_commit: 'def45678', diff_base_commit: 'abc12345' }, data: json }
    } else if (path.endsWith('/comments')) {
      if (method === 'POST') {
        const body = route.request().postDataJSON() as CreatePullRequestCommentRequest
        creates.push(body)
        const parent = comments.find((comment) => comment.id === body.parent_comment_id)
        const comment: PullRequestComment = { ...parent, id: `comment-${comments.length + 1}`, pull_request_id: 'scoped-pr', scope: parent?.scope ?? 'pull_request', status: 'unresolved', author_type: 'user', original_head_commit: 'def45678', created_at: now, updated_at: now, ...body }
        comments.push(comment)
        json = comment
      } else json = comments
    } else if (path.endsWith('/workers') && method === 'POST') {
      workerRequests.push(route.request().postDataJSON())
    }
    await route.fulfill({ status: method === 'POST' ? 201 : 200, json })
  })
  await page.route('**/api/v1/pull-request-comments/**', async (route) => {
    const path = new URL(route.request().url()).pathname
    const comment = comments.find((item) => path.includes(`/${item.id}/`))!
    expect(comment).toBeTruthy()
    comment.status = path.endsWith('/resolve') ? 'resolved' : 'unresolved'
    await route.fulfill({ json: comment })
  })
  await page.goto('/pulls/scoped-pr')
  await page.getByRole('textbox', { name: 'Comment', exact: true }).fill('General note')
  await page.getByRole('button', { name: 'Comment', exact: true }).click()
  await expect(page.getByText('General note', { exact: true })).toBeVisible()
  expect(creates[0]).toEqual({ body: 'General note' })

  for (const [action, body, location] of [
    ['Comment on file new.ts', 'File note', { scope: 'file', path: 'new.ts', old_path: 'old.ts' }],
    ['Comment on new.ts LEFT line 1', 'Line note', { scope: 'line', path: 'new.ts', old_path: 'old.ts', side: 'LEFT', line: 1, diff_hunk: patch }],
    ['Comment on new.ts RIGHT line 1', 'New line note', { scope: 'line', path: 'new.ts', old_path: 'old.ts', side: 'RIGHT', line: 1, diff_hunk: patch }],
    ['Comment on new.ts RIGHT line 2', 'Context note', { scope: 'line', path: 'new.ts', old_path: 'old.ts', side: 'RIGHT', line: 2, diff_hunk: patch }],
    ['Comment on file image.png', 'Binary note', { scope: 'file', path: 'image.png' }],
  ] as const) {
    await page.getByRole('button', { name: /^Changes/ }).click()
    if (location.path === 'image.png') await page.getByRole('button', { name: /image.png/ }).click()
    await page.getByRole('button', { name: action, exact: true }).focus()
    await page.keyboard.press('Enter')
    await expect(page.getByRole('textbox', { name: /^Comment on / })).toBeFocused()
    await page.getByRole('textbox', { name: /^Comment on / }).fill(body)
    await page.getByRole('button', { name: 'Comment', exact: true }).click()
    await expect(page.getByRole('button', { name: /^Changes/ })).toHaveAttribute('aria-current', 'page')
    await expect(page.getByText(body, { exact: true })).toBeVisible()
    await page.getByRole('button', { name: 'View comment', exact: true }).click()
    await expect(page.getByText(body, { exact: true })).toBeVisible()
    expect(creates.at(-1)).toEqual({ ...location, body })
  }
  await expect(page.locator('article').filter({ has: page.getByText('File note', { exact: true }) }).getByRole('button', { name: 'new.ts' })).toBeVisible()
  const lineThread = page.locator('article').filter({ has: page.getByText('Line note', { exact: true }) })
  await expect(lineThread.getByRole('button', { name: 'new.ts', exact: true })).toBeVisible()
  const excerpt = lineThread.getByLabel('Saved diff excerpt for new.ts')
  await expect(excerpt.getByText('before', { exact: true })).toBeVisible()
  await expect(excerpt.locator('[data-commented="true"]')).toContainText('before')
  await lineThread.getByRole('button', { name: /new\.ts/ }).click()
  await expect(page.getByRole('heading', { name: 'old.ts → new.ts' }).locator('..')).toBeFocused()
  await page.getByRole('button', { name: /^Overview/ }).click()

  for (const [index, body] of ['General note', 'File note', 'Line note'].entries()) {
    await page.getByRole('button', { name: index === 0 ? /^Overview/ : /^Changes/ }).click()
    const article = page.locator('article[id^=comment-]').filter({ has: page.getByText(body, { exact: true }) })
    await expect(article.getByRole('button', { name: 'Reply', exact: true })).toBeDisabled()
    await article.getByRole('textbox').fill(`Reply to ${body}`)
    await article.getByRole('button', { name: 'Reply', exact: true }).click()
    await expect(article.getByText(`Reply to ${body}`, { exact: true })).toBeVisible()
    expect(creates.at(-1)).toEqual({ body: `Reply to ${body}`, parent_comment_id: `comment-${index + 1}` })
    await article.getByRole('button', { name: 'Mark resolved', exact: true }).click()
    await expect(article.getByText('Resolved', { exact: true })).toBeVisible()
    await article.getByRole('button', { name: 'Reopen', exact: true }).click()
    await expect(article.getByText('Resolved', { exact: true })).toHaveCount(0)
  }
  await page.getByRole('button', { name: /^Overview/ }).click()
  await page.getByRole('button', { name: 'Resolve all automatically', exact: true }).click()
  await expect.poll(() => workerRequests).toEqual([{ comment_ids: ['comment-1', 'comment-2', 'comment-3', 'comment-4', 'comment-5', 'comment-6'], mode: 'auto' }])
})
