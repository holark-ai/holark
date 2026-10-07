import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { api } from '../../data/api'
import type { IssueComment, IssueDiscussion as Discussion } from '../../data/types'
import { IssueDiscussion } from './IssueDiscussion'
import { quoteComment } from './commentBody'
import { IssueMarkdown } from './IssueMarkdown'

vi.mock('../../data/api', () => ({ api: {
  issueComments: vi.fn(), syncIssueComments: vi.fn(), createIssueComment: vi.fn(), updateIssueComment: vi.fn(), deleteIssueComment: vi.fn(), resolveGitHubImage: vi.fn(),
} }))
const comment: IssueComment = { id: 'c1', issue_id: 'i1', body: 'Original comment', author: { login: 'outsider', avatar_url: '', url: '' }, github_id: '1', github_node_id: 'node1', url: 'https://github.com/o/r/issues/1#issuecomment-1', created_at: '2026-01-01T00:00:00Z', updated_at: '2026-01-02T00:00:00Z', can_edit: true, can_delete: true }
const discussion: Discussion = { comments: [comment], synced_at: '2026-01-02T00:00:00Z', can_comment: true }
function deferred<T>() { let resolve!: (value: T) => void; const promise = new Promise<T>((r) => { resolve = r }); return { promise, resolve } }
function show(id = 'i1') { return render(<MemoryRouter><IssueDiscussion issueId={id} githubURL="https://github.com/o/r/issues/1" /></MemoryRouter>) }
beforeEach(() => {
  vi.resetAllMocks()
  vi.mocked(api.issueComments).mockResolvedValue(discussion)
  vi.mocked(api.syncIssueComments).mockResolvedValue(discussion)
  vi.mocked(api.createIssueComment).mockResolvedValue({ ...comment, id: 'new', body: 'New comment' })
  vi.mocked(api.updateIssueComment).mockResolvedValue({ ...comment, body: 'Edited canonical' })
  vi.mocked(api.deleteIssueComment).mockResolvedValue(undefined)
})
it('renders sanitized Markdown with quotes, links, code and existing image URLs', () => {
  const { container } = render(<IssueMarkdown body={'<script>alert(1)</script>\n\n[unsafe](javascript:alert%281%29)\n\n[link](https://example.com)\n\n> quote\n\n`code`\n\n![image](https://example.com/image.png)'} />)
  expect(container.querySelector('script')).toBeNull()
  expect(container.querySelector('a[href^="javascript:"]')).toBeNull()
  expect(screen.getByRole('link', { name: 'link' })).toHaveAttribute('href', 'https://example.com')
  expect(container.querySelector('blockquote')).toHaveTextContent('quote')
  expect(container.querySelector('code')).toHaveTextContent('code')
  expect(screen.getByRole('img', { name: 'image' })).toHaveAttribute('src', 'https://example.com/image.png')
})
const legacyImageURL = 'https://github.com/owner/repo.name/assets/668535/5c3a4439-40e3-4868-8bf5-1069efef670e'
it.each([
  ['Markdown', `![Legacy image](${legacyImageURL})`],
  ['HTML', `<img alt="Legacy image" src="${legacyImageURL}">`],
])('resolves legacy images in synced issue comments and preserves saved %s', async (_format, body) => {
  const user = userEvent.setup()
  const synced = { ...discussion, comments: [{ ...comment, body }] }
  vi.mocked(api.issueComments).mockResolvedValue(synced)
  vi.mocked(api.syncIssueComments).mockResolvedValue(synced)
  const displayURL = 'https://private-user-images.githubusercontent.com/668535/image.png?jwt=signed'
  vi.mocked(api.resolveGitHubImage).mockResolvedValue({ url: displayURL })
  vi.mocked(api.updateIssueComment).mockResolvedValue({ ...comment, body })
  show()
  await waitFor(() => expect(screen.getByRole('img', { name: 'Legacy image' })).toHaveAttribute('src', displayURL))
  expect(api.resolveGitHubImage).toHaveBeenCalledWith(legacyImageURL, expect.any(AbortSignal))
  await user.click(screen.getByRole('button', { name: 'Comment actions' }))
  await user.click(screen.getByRole('menuitem', { name: 'Edit' }))
  expect(screen.getByRole('textbox', { name: 'Edit comment' })).toHaveValue(body)
  await user.click(screen.getByRole('button', { name: 'Save comment' }))
  expect(api.updateIssueComment).toHaveBeenCalledWith('c1', body)
})
it('keeps picture images beside their sources while resolving synced issue attachments', async () => {
  const body = `<picture><source media="(prefers-color-scheme: dark)" srcset="https://example.com/dark.png"><img alt="Theme image" src="${legacyImageURL}"></picture>`
  const synced = { ...discussion, comments: [{ ...comment, body }] }
  vi.mocked(api.issueComments).mockResolvedValue(synced)
  vi.mocked(api.syncIssueComments).mockResolvedValue(synced)
  const resolution = deferred<{ url: string }>()
  vi.mocked(api.resolveGitHubImage).mockReturnValue(resolution.promise)
  const { container } = show()
  const image = await screen.findByRole('img', { name: 'Theme image' })
  const picture = container.querySelector('picture')
  expect(image.parentElement).toBe(picture)
  expect(picture?.querySelector('source')).toHaveAttribute('srcset', 'https://example.com/dark.png')
  expect(picture?.querySelector('source')).toHaveAttribute('media', '(prefers-color-scheme: dark)')
  expect(screen.getByText('Loading image…').parentElement).toBe(picture)
  const displayURL = 'https://private-user-images.githubusercontent.com/668535/image.png?jwt=signed'
  await act(async () => resolution.resolve({ url: displayURL }))
  expect(image).toHaveAttribute('src', displayURL)
  expect(image.parentElement).toBe(picture)
  expect(screen.queryByText('Loading image…')).not.toBeInTheDocument()
})
const sourceImageURL = 'https://github.com/user-attachments/assets/11111111-1111-1111-1111-111111111111'
it.each([
  ['single', sourceImageURL, false],
  ['density', `${sourceImageURL} 1x, ${legacyImageURL} 2x, https://example.com/public.png 3x`, true],
  ['width', `${sourceImageURL} 480w,\n${legacyImageURL} 960w`, true],
  ['descriptor-free', `${sourceImageURL}, ${legacyImageURL}`, true],
  ['commas in public URLs', `https://example.com/image,${legacyImageURL} 1x, ${sourceImageURL} 2x`, false],
])('resolves %s picture sources in synced issue comments without changing saved text', async (_kind, srcSet, includesLegacyCandidate) => {
  const user = userEvent.setup()
  const body = `<picture><source media="(prefers-color-scheme: dark)" srcset="${srcSet}"><img alt="Theme image" src="https://example.com/fallback.png"></picture>`
  const synced = { ...discussion, comments: [{ ...comment, body }] }
  vi.mocked(api.issueComments).mockResolvedValue(synced)
  vi.mocked(api.syncIssueComments).mockResolvedValue(synced)
  vi.mocked(api.updateIssueComment).mockResolvedValue({ ...comment, body })
  const resolution = deferred<void>()
  const displayURL = 'https://private-user-images.githubusercontent.com/1/theme.png?jwt=signed&expires=123'
  const legacyDisplayURL = 'https://private-user-images.githubusercontent.com/2/theme.png?jwt=signed&expires=123'
  vi.mocked(api.resolveGitHubImage).mockImplementation(async (url) => {
    await resolution.promise
    return { url: url === sourceImageURL ? displayURL : legacyDisplayURL }
  })
  const { container } = show()
  const image = await screen.findByRole('img', { name: 'Theme image' })
  const source = container.querySelector('source')
  expect(source).not.toHaveAttribute('srcset')
  expect(source).toHaveAttribute('media', '(prefers-color-scheme: dark)')
  expect(source?.parentElement).toBe(image.parentElement)
  expect(image).toHaveAttribute('src', 'https://example.com/fallback.png')
  await act(async () => resolution.resolve())
  const expectedSrcSet = includesLegacyCandidate ? srcSet.replace(sourceImageURL, displayURL).replace(legacyImageURL, legacyDisplayURL) : srcSet.replace(sourceImageURL, displayURL)
  expect(source).toHaveAttribute('srcset', expectedSrcSet)
  expect(api.resolveGitHubImage).toHaveBeenCalledWith(sourceImageURL, expect.any(AbortSignal))
  expect(api.resolveGitHubImage).toHaveBeenCalledTimes(includesLegacyCandidate ? 2 : 1)
  if (includesLegacyCandidate) expect(api.resolveGitHubImage).toHaveBeenCalledWith(legacyImageURL, expect.any(AbortSignal))
  await user.click(screen.getByRole('button', { name: 'Comment actions' }))
  await user.click(screen.getByRole('menuitem', { name: 'Edit' }))
  expect(screen.getByRole('textbox', { name: 'Edit comment' })).toHaveValue(body)
  await user.click(screen.getByRole('button', { name: 'Save comment' }))
  expect(api.updateIssueComment).toHaveBeenCalledWith('c1', body)
})
it('keeps public picture sources usable when authenticated resolution fails', async () => {
  const body = `<picture><source srcset="${sourceImageURL} 1x, https://example.com/public.png 2x"><img alt="Fallback" src="https://example.com/fallback.png"></picture>`
  vi.mocked(api.resolveGitHubImage).mockRejectedValue(new Error('GitHub unavailable'))
  const { container } = render(<IssueMarkdown body={body} />)
  await waitFor(() => expect(container.querySelector('source')).toHaveAttribute('srcset', `${sourceImageURL} 1x, https://example.com/public.png 2x`))
  expect(screen.getByRole('img', { name: 'Fallback' })).toHaveAttribute('src', 'https://example.com/fallback.png')
})
it('ignores an old picture source resolution after a synced comment changes', async () => {
  const user = userEvent.setup()
  const body = `<picture><source srcset="${sourceImageURL}"><img alt="Theme image" src="https://example.com/fallback.png"></picture>`
  const synced = { ...discussion, comments: [{ ...comment, body }] }
  vi.mocked(api.issueComments).mockResolvedValue(synced)
  vi.mocked(api.syncIssueComments).mockResolvedValue(synced)
  const oldResolution = deferred<{ url: string }>()
  const displayURL = 'https://private-user-images.githubusercontent.com/2/theme.png?jwt=signed'
  vi.mocked(api.resolveGitHubImage).mockReturnValueOnce(oldResolution.promise).mockResolvedValue({ url: displayURL })
  const { container } = show()
  await screen.findByRole('img', { name: 'Theme image' })
  const oldSignal = vi.mocked(api.resolveGitHubImage).mock.calls[0][1]
  vi.mocked(api.syncIssueComments).mockResolvedValue({ ...synced, comments: [{ ...comment, body: body.replace(sourceImageURL, legacyImageURL) }] })
  await user.click(screen.getByRole('button', { name: 'Refresh' }))
  await waitFor(() => expect(container.querySelector('source')).toHaveAttribute('srcset', displayURL))
  expect(oldSignal?.aborted).toBe(true)
  await act(async () => oldResolution.resolve({ url: 'https://example.com/old.png' }))
  expect(container.querySelector('source')).toHaveAttribute('srcset', displayURL)
})
it('shows the cache before sync completes and keeps it when sync fails', async () => {
  const sync = deferred<Discussion>()
  vi.mocked(api.syncIssueComments).mockReturnValueOnce(sync.promise).mockRejectedValueOnce(new Error('GitHub unavailable'))
  show()
  expect(await screen.findByText('Original comment')).toBeVisible()
  expect(screen.getByRole('button', { name: 'Refreshing…' })).toBeDisabled()
  await act(async () => sync.resolve(discussion))
  await userEvent.click(screen.getByRole('button', { name: 'Refresh' }))
  expect(await screen.findByRole('alert')).toHaveTextContent('GitHub unavailable')
  expect(screen.getByText('Original comment')).toBeVisible()
})
it('appends a flat Markdown quotation to the current draft and focuses it', async () => {
  const user = userEvent.setup(); show()
  await screen.findByText('Original comment')
  await user.type(screen.getByRole('textbox', { name: 'Add a comment' }), 'My draft')
  await user.click(screen.getByRole('button', { name: 'Comment actions' }))
  await user.click(screen.getByRole('menuitem', { name: 'Quote reply' }))
  expect(screen.getByRole('textbox', { name: 'Add a comment' })).toHaveValue('My draft\n\n> Original comment\n\n')
  expect(screen.getByRole('textbox', { name: 'Add a comment' })).toHaveFocus()
  expect(screen.queryByRole('menu')).toBeNull()
  expect(quoteComment('first\r\n\r\n> nested')).toBe('> first\n> \n> > nested\n\n')
})
it('preserves drafts on refresh and rejected writes, and prevents duplicate submission', async () => {
  const user = userEvent.setup();show();await screen.findByText('Original comment')
  const input = screen.getByRole('textbox', { name: 'Add a comment' })
  await user.type(input, 'My draft')
  await user.click(screen.getByRole('button', { name: 'Refresh' }))
  expect(input).toHaveValue('My draft')
  vi.mocked(api.createIssueComment).mockRejectedValueOnce(Object.assign(new Error('Permission changed'), { status: 403 }))
  await user.click(screen.getByRole('button', { name: 'Comment' }))
  expect(await screen.findByRole('alert')).toHaveTextContent('Permission changed')
  expect(input).toHaveValue('My draft')
  const write = deferred<IssueComment>();vi.mocked(api.createIssueComment).mockReturnValueOnce(write.promise)
  await user.click(screen.getByRole('button', { name: 'Comment' }))
  expect(screen.getByRole('button', { name: 'Saving…' })).toBeDisabled()
  expect(input).toBeDisabled()
  await act(async () => write.resolve({ ...comment, id: 'new', body: 'Canonical' }))
  expect(input).toHaveValue('')
  expect(screen.getByText('Canonical')).toBeVisible()
  expect(api.createIssueComment).toHaveBeenCalledTimes(2)
})
it.each(['issue_comment_projection_pending', 'issue_comment_outcome_uncertain'])('retains the draft and requires explicit recovery for %s', async (code) => {
  const user = userEvent.setup();show();await screen.findByText('Original comment')
  vi.mocked(api.createIssueComment).mockRejectedValueOnce(Object.assign(new Error('Sync to recover'), { code, status: code.endsWith('pending') ? 202 : 502 }))
  const input = screen.getByRole('textbox', { name: 'Add a comment' });await user.type(input, 'Retained')
  await user.click(screen.getByRole('button', { name: 'Comment' }))
  expect(await screen.findByRole('status')).toHaveTextContent('Your draft has been kept')
  expect(input).toHaveValue('Retained')
  expect(screen.getByRole('button', { name: 'Comment' })).toBeDisabled()
  await user.click(screen.getByRole('button', { name: 'Refresh' }))
  expect(api.createIssueComment).toHaveBeenCalledTimes(1)
  expect(input).toHaveValue('Retained')
})
it('edits inline, preserves an edit during refresh, and confirms remote deletion', async () => {
  const user = userEvent.setup();show();await screen.findByText('Original comment')
  await user.click(screen.getByRole('button', { name: 'Comment actions' }))
  await user.click(screen.getByRole('menuitem', { name: 'Edit' }))
  const edit = screen.getByRole('textbox', { name: 'Edit comment' });await user.clear(edit);await user.type(edit, 'My edit')
  await user.click(screen.getByRole('button', { name: 'Refresh' }))
  expect(edit).toHaveValue('My edit')
  await user.click(screen.getByRole('button', { name: 'Save comment' }))
  expect(await screen.findByText('Edited canonical')).toBeVisible()
  expect(api.updateIssueComment).toHaveBeenCalledWith('c1', 'My edit')
  await user.click(screen.getByRole('button', { name: 'Comment actions' }))
  await user.click(screen.getByRole('menuitem', { name: 'Delete' }))
  expect(api.deleteIssueComment).not.toHaveBeenCalled()
  const dialog = screen.getByRole('dialog', { name: 'Delete comment?' })
  await user.click(within(dialog).getByRole('button', { name: 'Delete comment' }))
  await waitFor(() => expect(screen.queryByText('Edited canonical')).not.toBeInTheDocument())
  expect(api.deleteIssueComment).toHaveBeenCalledWith('c1')
})
it('shows deleted authors and respects refreshed viewer capabilities', async () => {
  const restricted = { ...discussion, can_comment: false, comments: [{ ...comment, author: { login: '', url: '', avatar_url: '' }, can_edit: false, can_delete: false }] }
  vi.mocked(api.syncIssueComments).mockResolvedValue(restricted)
  show()
  expect(await screen.findByText('Unknown author')).toBeVisible()
  await userEvent.click(screen.getByRole('button', { name: 'Comment actions' }))
  expect(screen.queryByRole('menuitem', { name: 'Edit' })).toBeNull()
  expect(screen.queryByRole('menuitem', { name: 'Delete' })).toBeNull()
  expect(screen.getByRole('menuitem', { name: 'Quote reply' })).toBeDisabled()
})
it('ignores late sync and mutation responses after navigation', async () => {
  const sync = deferred<Discussion>();const write = deferred<IssueComment>()
  vi.mocked(api.syncIssueComments).mockReturnValueOnce(sync.promise)
  vi.mocked(api.createIssueComment).mockReturnValueOnce(write.promise)
  const user = userEvent.setup();const view = show();await screen.findByText('Original comment')
  await user.type(screen.getByRole('textbox', { name: 'Add a comment' }), 'Old draft')
  await user.click(screen.getByRole('button', { name: 'Comment' }))
  const next = { ...discussion, comments: [{ ...comment, id: 'c2', issue_id: 'i2', body: 'Second issue' }] }
  vi.mocked(api.issueComments).mockResolvedValue(next);vi.mocked(api.syncIssueComments).mockResolvedValue(next)
  view.rerender(<MemoryRouter><IssueDiscussion issueId="i2" /></MemoryRouter>)
  expect(await screen.findByText('Second issue')).toBeVisible()
  await act(async () => { sync.resolve(discussion);write.resolve({ ...comment, body: 'Late write' }) })
  expect(screen.queryByText('Original comment')).toBeNull()
  expect(screen.queryByText('Late write')).toBeNull()
  expect(screen.getByRole('textbox', { name: 'Add a comment' })).toHaveValue('')
})

it('keeps refreshed comments when a post starts during sync', async () => {
  const sync = deferred<Discussion>()
  const write = deferred<IssueComment>()
  vi.mocked(api.syncIssueComments).mockReturnValueOnce(sync.promise)
  vi.mocked(api.createIssueComment).mockReturnValueOnce(write.promise)
  const user = userEvent.setup()
  show()
  await screen.findByText('Original comment')
  await user.type(screen.getByRole('textbox', { name: 'Add a comment' }), 'New comment')
  await user.click(screen.getByRole('button', { name: 'Comment' }))
  const remote = { ...comment, id: 'remote', github_id: '2', body: 'Remote comment discovered by sync' }
  await act(async () => sync.resolve({ ...discussion, comments: [comment, remote] }))
  await act(async () => write.resolve({ ...comment, id: 'new', github_id: '3', body: 'New comment' }))
  expect(await screen.findByText('New comment')).toBeVisible()
  expect(screen.getByText('Remote comment discovered by sync')).toBeVisible()
})
