import { act, render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { api } from '../../data/api'
import type { PullRequest, PullRequestMetadata } from '../../data/types'
import { PullRequestMetadataCard } from './PullRequestMetadataCard'

afterEach(() => { vi.restoreAllMocks() })

it('keeps the last warning when metadata is returned for a different head', async () => {
  const pullRequest = { view_revision: 1, id: 'metadata-inputs', title: 'Title', summary: 'Saved description', status: 'open', head_commit: 'first', base_commit: 'base' } as PullRequest
  const metadata: PullRequestMetadata = {
    pull_request_id: pullRequest.id, title: pullRequest.title, description: pullRequest.summary, updated_at: '', generation_complete: true,
    head_commit: 'first', diff_base_commit: 'base', view_revision: 1,
    freshness: { generated: { head_commit: 'generated', diff_base_commit: 'base' }, current: { head_commit: 'first', diff_base_commit: 'base' }, outdated: true },
  }
  const load = vi.spyOn(api, 'pullRequestMetadata').mockResolvedValue(metadata)
  const saved = vi.fn(async () => {})
  const { rerender } = render(<MemoryRouter><PullRequestMetadataCard pullRequest={pullRequest} onSaved={saved} /></MemoryRouter>)
  expect(await screen.findByText(/Description may be outdated/)).toBeInTheDocument()
  load.mockResolvedValue({ ...metadata, description: 'Wrong revision description', view_revision: 2 })
  await act(async () => {
    rerender(<MemoryRouter><PullRequestMetadataCard pullRequest={{ ...pullRequest, head_commit: 'second' }} onSaved={saved} /></MemoryRouter>)
  })
  expect(screen.getByText('Saved description')).toBeInTheDocument()
  expect(screen.queryByText('Wrong revision description')).not.toBeInTheDocument()
  expect(screen.getByText(/Description may be outdated/)).toBeInTheDocument()
  expect(saved).not.toHaveBeenCalled()
})
