import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, expect, it, vi } from 'vitest'
import { setApiRequestTransport } from '../../data/api'
import type { WorkspaceInspection } from '../../data/types'
import { HolonDiffPanel } from './HolonDiffPanel'

vi.mock('./TextComparison', () => ({ default: ({ original, modified }: { original: string; modified: string }) => <pre>{original} → {modified}</pre> }))

let restore: (() => void) | undefined
afterEach(() => { cleanup(); restore?.() })

it('saves the base, resets commit selection, and reloads the comparison and contents', async () => {
  const user = userEvent.setup()
  let base = 'main'
  const requests: string[] = []
  const onHolonChange = vi.fn()
  const onOpenChange = vi.fn()
  restore = setApiRequestTransport(async (path, init) => {
    requests.push(`${init?.method || 'GET'} ${path}`)
    if (path.includes('/repository/refs')) return Response.json({ refs: ['main', 'develop'].map((name) => ({ name: `refs/heads/${name}`, short_name: name, kind: 'branch', target: '', committed_at: '' })) })
    if (path.endsWith('/base-branch')) {
      base = JSON.parse(init!.body as string).base_branch
      return Response.json({ id: 'h', base_branch: base })
    }
    const inspection: WorkspaceInspection = {
      branch: 'feature', base_branch: base, base_commit: base === 'main' ? 'aaa' : 'bbb', head_commit: 'ccc', dirty: true, has_changes: true, diff_truncated: false,
      branch_base_commit: base === 'main' ? 'aaa' : 'bbb', workspace_head_commit: 'ccc', work_session_start_commit: 'aaa',
      commits: base === 'main' ? [{ sha: 'bbb', parent_commit: 'aaa', subject: 'First change' }] : [],
      files: [{ path: base === 'main' ? 'old.txt' : 'new.txt', status: 'M', additions: 1, deletions: 1, binary: false, diff: '', diff_truncated: false, content_status: 'ready', contents: { original: `${base} original`, modified: 'working copy' } }],
    }
    return Response.json(inspection)
  })
  render(<HolonDiffPanel holonId="h" enabled readOnly={false} open onOpenChange={onOpenChange} onHolonChange={onHolonChange} />)
  await user.click(await screen.findByRole('button', { name: /First change/ }))
  expect(screen.getByRole('button', { name: 'View all' })).toBeEnabled()
  await user.click(screen.getByRole('combobox', { name: 'Change base branch, currently main' }))
  await screen.findByRole('combobox', { name: 'Search branches' })
  await user.keyboard('{Escape}')
  expect(onOpenChange).not.toHaveBeenCalled()
  expect(screen.getByRole('combobox', { name: 'Change base branch, currently main' })).toHaveAttribute('aria-expanded', 'false')
  await user.click(screen.getByRole('combobox', { name: 'Change base branch, currently main' }))
  await user.type(await screen.findByRole('combobox', { name: 'Search branches' }), 'develop')
  await user.click(await screen.findByRole('option', { name: 'develop' }))
  await screen.findByRole('combobox', { name: 'Change base branch, currently develop' })
  expect(onHolonChange).toHaveBeenCalledWith({ id: 'h', base_branch: 'develop' })
  expect(screen.getByRole('button', { name: 'View all' })).toBeDisabled()
  expect(screen.queryByRole('button', { name: /First change/ })).not.toBeInTheDocument()
  await screen.findByText('develop original → working copy')
  expect(requests.some((request) => request.startsWith('PUT ') && request.endsWith('/base-branch'))).toBe(true)
  expect(requests.some((request) => request.includes('new.txt') && request.includes('contents=true'))).toBe(true)
})

it('keeps the existing comparison when saving fails and allows retry', async () => {
  const user = userEvent.setup()
  let attempts = 0
  restore = setApiRequestTransport(async (path) => {
    if (path.includes('/repository/refs')) return Response.json({ refs: ['main', 'develop'].map((name) => ({ name, short_name: name, kind: 'branch', target: '', committed_at: '' })) })
    if (path.endsWith('/base-branch')) {
      attempts++
      return Response.json({ message: 'Branch no longer exists.' }, { status: 404 })
    }
    return Response.json({ branch: 'feature', base_branch: 'main', base_commit: 'aaa', head_commit: 'bbb', has_changes: false, dirty: false, diff_truncated: false, files: [] })
  })
  render(<HolonDiffPanel holonId="h" enabled readOnly={false} open onOpenChange={() => {}} />)
  for (let attempt = 1; attempt <= 2; attempt++) {
    await user.click(await screen.findByRole('combobox', { name: 'Change base branch, currently main' }))
    await user.click(await screen.findByRole('option', { name: 'develop' }))
    await waitFor(() => expect(attempts).toBe(attempt))
    expect(await screen.findByRole('alert')).toHaveTextContent('Branch no longer exists.')
    expect(screen.getByRole('combobox', { name: 'Change base branch, currently main' })).toBeEnabled()
  }
})
