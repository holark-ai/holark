import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { StrictMode, useState } from 'react'
import { api } from '../../data/api'
import { MemberAvatarStack } from './MemberAvatarStack'
import { MemberIdentity } from './MemberIdentity'
import { MemberPicker } from './MemberPicker'
import { ProjectMemberStoreProvider } from './ProjectMemberStore'

vi.mock('../../data/api', () => ({
  api: {
    projectMemberSearch: vi.fn(),
    resolveProjectMembers: vi.fn(),
  },
}))

const alice = {
  id: 'member-alice',
  login: 'alice',
  avatar_url: 'https://example.com/alice.png',
  profile_url: 'https://example.com/alice',
  permission: 'write',
  is_me: false,
}
const bob = {
  id: 'member-bob',
  login: 'bob',
  permission: 'read',
  is_me: true,
}

beforeEach(() => {
  vi.mocked(api.projectMemberSearch).mockReset()
  vi.mocked(api.resolveProjectMembers).mockReset()
  vi.mocked(api.projectMemberSearch).mockResolvedValue({ members: [] })
  vi.mocked(api.resolveProjectMembers).mockResolvedValue({ members: [], missing_ids: [] })
})

function Store({ children }: { children: React.ReactNode }) {
  return <ProjectMemberStoreProvider projectId="project">{children}</ProjectMemberStoreProvider>
}

it('batches member resolution during one render tick and reuses cached members', async () => {
  vi.mocked(api.resolveProjectMembers).mockResolvedValue({ members: [alice, bob], missing_ids: [] })
  const view = render(<StrictMode><Store><MemberIdentity memberId={alice.id} /><MemberIdentity memberId={bob.id} /></Store></StrictMode>)

  expect(screen.getAllByText('Loading member')).toHaveLength(2)
  expect(await screen.findByText('alice')).toBeInTheDocument()
  expect(screen.getByText('bob')).toBeInTheDocument()
  expect(api.resolveProjectMembers).toHaveBeenCalledTimes(1)
  expect(api.resolveProjectMembers).toHaveBeenCalledWith('project', [alice.id, bob.id])

  view.rerender(<StrictMode><Store><MemberIdentity memberId={alice.id} /><MemberIdentity memberId={bob.id} /></Store></StrictMode>)
  await waitFor(() => expect(api.resolveProjectMembers).toHaveBeenCalledTimes(1))
})

it('renders resolved, missing, and empty avatar-stack states', async () => {
  vi.mocked(api.resolveProjectMembers).mockResolvedValue({ members: [alice, bob], missing_ids: ['member-stale'] })
  const { container } = render(
    <Store>
      <MemberAvatarStack memberIds={[]} />
      <MemberAvatarStack memberIds={[bob.id, alice.id, 'member-stale']} />
    </Store>,
  )

  const stack = await screen.findByRole('list', { name: 'Assignees' })
  expect(within(stack).getAllByRole('listitem')).toHaveLength(3)
  expect(await within(stack).findByLabelText('bob')).toBeInTheDocument()
  expect(within(stack).getByLabelText('alice')).toBeInTheDocument()
  expect(within(stack).getByLabelText('Unknown member member-stale')).toBeInTheDocument()
  expect(container.querySelectorAll('[aria-label="Assignees"]')).toHaveLength(1)
})

it('searches and emits only Holark IDs for multiple selection', async () => {
  vi.mocked(api.projectMemberSearch).mockResolvedValue({ members: [alice, bob] })

  function ControlledPicker() {
    const [selected, setSelected] = useState<string[]>([bob.id])
    return (
      <>
        <MemberPicker selectedIds={selected} multiple onChange={setSelected} />
        <output data-testid="selected-member-ids">{selected.join(',')}</output>
      </>
    )
  }

  render(<Store><ControlledPicker /></Store>)
  const user = userEvent.setup()
  await user.type(screen.getByRole('searchbox', { name: 'Search project members' }), 'ali')

  await waitFor(() => expect(api.projectMemberSearch).toHaveBeenLastCalledWith('project', 'ali', 20))
  const listbox = await screen.findByRole('listbox', { name: 'Project members' })
  expect(within(listbox).getByRole('option', { name: /alice/ })).toBeInTheDocument()
  expect(within(listbox).getByRole('option', { name: /bob/ })).toHaveAttribute('aria-selected', 'true')

  await user.click(within(listbox).getByRole('option', { name: /alice/ }))
  expect(within(listbox).getByRole('option', { name: /alice/ })).toHaveAttribute('aria-selected', 'true')
  expect(screen.getByTestId('selected-member-ids')).toHaveTextContent('member-bob,member-alice')
})

it('names selected-member removal controls with the resolved login', async () => {
  vi.mocked(api.resolveProjectMembers).mockResolvedValue({ members: [alice], missing_ids: [] })

  render(<Store><MemberPicker selectedIds={[alice.id]} onChange={vi.fn()} /></Store>)

  expect(await screen.findByRole('button', { name: 'Remove alice' })).toBeInTheDocument()
})

it('caches missing IDs without repeated resolution', async () => {
  vi.mocked(api.resolveProjectMembers).mockResolvedValue({ members: [], missing_ids: ['member-stale'] })
  const view = render(<Store><MemberIdentity memberId="member-stale" /></Store>)

  expect(await screen.findByLabelText('Unknown member member-stale')).toBeInTheDocument()
  view.rerender(<Store><MemberIdentity memberId="member-stale" /></Store>)
  await waitFor(() => expect(api.resolveProjectMembers).toHaveBeenCalledTimes(1))
})

it('retries a failed resolution and renders the member when it recovers', async () => {
  vi.useFakeTimers()
  vi.mocked(api.resolveProjectMembers)
    .mockRejectedValueOnce(new Error('Members unavailable.'))
    .mockResolvedValueOnce({ members: [alice], missing_ids: [] })

  try {
    render(<Store><MemberIdentity memberId={alice.id} /></Store>)
    await act(async () => { await vi.advanceTimersByTimeAsync(0) })

    expect(screen.getByLabelText(`Member unavailable ${alice.id}`)).toBeInTheDocument()

    await act(async () => { await vi.advanceTimersByTimeAsync(1_000) })

    expect(screen.getByText('alice')).toBeInTheDocument()
    expect(api.resolveProjectMembers).toHaveBeenCalledTimes(2)
  } finally {
    vi.useRealTimers()
  }
})
