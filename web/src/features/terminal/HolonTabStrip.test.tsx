import { act, fireEvent, render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { DismissiblePopoverGroup } from '../../components/DismissiblePopover'
import { HolonTabStrip, type HolonTabStripItem } from './HolonTabStrip'

afterEach(() => {
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

function tabs(count = 2): HolonTabStripItem[] {
  return Array.from({ length: count }, (_, index) => ({
    id: `tab-${index + 1}`,
    layoutKey: `layout-${index + 1}`,
    title: `Tab ${index + 1}`,
    icon: <span data-testid={`icon-${index + 1}`} />,
    iconLabel: 'Terminal',
    tone: index === 0 ? 'running' : 'idle',
  }))
}

function resizeHarness() {
  let callback: ResizeObserverCallback | undefined
  vi.stubGlobal('ResizeObserver', class ResizeObserver {
    constructor(next: ResizeObserverCallback) { callback = next }
    observe() {}
    unobserve() {}
    disconnect() {}
  })
  let width = 0
  const setWidth = (nextWidth: number) => {
    width = nextWidth
    const viewport = screen.getByTestId('tab-viewport')
    vi.spyOn(viewport.parentElement!, 'getBoundingClientRect').mockImplementation(() => rect(0, width))
    act(() => callback?.([], {} as ResizeObserver))
  }
  return { setWidth }
}

function rect(left: number, right: number) {
  return {
    x: left,
    y: 0,
    left,
    right,
    top: 0,
    bottom: 35,
    width: right - left,
    height: 35,
    toJSON: () => ({}),
  } as DOMRect
}

function addOptions() {
  return <button type="button">Terminal</button>
}

it.each([734, 205, 160])('opens the launcher with the shortcut at %ipx, navigates choices, and restores focus on Escape', async (width) => {
  const resize = resizeHarness()
  const launch = vi.fn()
  render(<><textarea aria-label="Terminal input" /><HolonTabStrip tabs={tabs(5)} selectedTabId="tab-1" onSelect={vi.fn()} onClose={vi.fn()} addOptions={<>
    <button disabled>Codex</button><button>Claude Code</button>
    <button data-default-tab-option onClick={launch}>Terminal</button><button>IDE</button>
  </>} /></>, { wrapper: DismissiblePopoverGroup })
  resize.setWidth(width)
  if (width < 206) expect(screen.queryByRole('button', { name: 'New tab' })).not.toBeInTheDocument()
  const input = screen.getByRole('textbox')
  input.focus()
  fireEvent.keyDown(input, { code: 'KeyT', shiftKey: true, altKey: true })
  expect(screen.getByRole('button', { name: 'Terminal' })).toHaveFocus()
  if (width < 206) expect(screen.queryByRole('button', { name: 'New tab' })).not.toBeInTheDocument()
  expect(launch).not.toHaveBeenCalled()
  await userEvent.keyboard('{ArrowDown}')
  expect(screen.getByRole('button', { name: 'IDE' })).toHaveFocus()
  await userEvent.keyboard('{ArrowDown}')
  expect(screen.getByRole('button', { name: 'Claude Code' })).toHaveFocus()
  await userEvent.keyboard('{Escape}')
  expect(input).toHaveFocus()
  expect(screen.queryByRole('button', { name: 'Terminal' })).not.toBeInTheDocument()
  fireEvent.keyDown(input, { code: 'KeyT', shiftKey: true, altKey: true })
  await userEvent.keyboard('{Enter}')
  expect(launch).toHaveBeenCalledOnce()
  expect(screen.queryByRole('button', { name: 'Terminal' })).not.toBeInTheDocument()
})

it('reveals choices on hover without moving focus and allows crossing into the menu', () => {
  vi.useFakeTimers()
  try {
    render(<><textarea aria-label="Terminal input" /><HolonTabStrip tabs={tabs()} selectedTabId="tab-1" onSelect={vi.fn()} onClose={vi.fn()} addOptions={addOptions()} /></>, { wrapper: DismissiblePopoverGroup })
    const input = screen.getByRole('textbox')
    input.focus()
    const trigger = screen.getByRole('button', { name: 'New tab' })
    fireEvent.pointerEnter(trigger, { pointerType: 'mouse' })
    expect(screen.queryByRole('button', { name: 'Terminal' })).not.toBeInTheDocument()
    act(() => vi.advanceTimersByTime(200))
    expect(screen.getByRole('button', { name: 'Terminal' })).toBeInTheDocument()
    expect(input).toHaveFocus()
    fireEvent.pointerLeave(trigger)
    act(() => vi.advanceTimersByTime(100))
    fireEvent.pointerEnter(screen.getByRole('button', { name: 'Terminal' }), { pointerType: 'mouse' })
    act(() => vi.advanceTimersByTime(250))
    expect(screen.getByRole('button', { name: 'Terminal' })).toBeInTheDocument()
    fireEvent.pointerLeave(screen.getByRole('button', { name: 'Terminal' }))
    act(() => vi.advanceTimersByTime(250))
    expect(screen.queryByRole('button', { name: 'Terminal' })).not.toBeInTheDocument()
  } finally {
    vi.useRealTimers()
  }
})

it('reveals an unselected tab close button during keyboard traversal', async () => {
  render(
    <HolonTabStrip
      tabs={tabs()}
      selectedTabId="tab-1"
      onSelect={vi.fn()}
      onClose={vi.fn()}
    />,
    { wrapper: DismissiblePopoverGroup },
  )

  await userEvent.tab()
  await userEvent.tab()
  await userEvent.tab()
  await userEvent.tab()

  const closeSecond = screen.getByRole('button', { name: 'Close Tab 2' })
  expect(closeSecond).toHaveFocus()
  expect(closeSecond).toHaveStyle({ opacity: '1' })
})

it('collapses every tab to its icon as soon as full tabs do not fit', () => {
  const resize = resizeHarness()
  render(
    <HolonTabStrip
      tabs={tabs(3)}
      selectedTabId="tab-1"
      addOptions={addOptions()}
      onSelect={vi.fn()}
      onClose={vi.fn()}
    />,
    { wrapper: DismissiblePopoverGroup },
  )

  resize.setWidth(454)
  expect(screen.getByRole('tablist')).not.toHaveAttribute('data-compact')
  expect(screen.getByText('Tab 2')).toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Close Tab 2' })).toBeInTheDocument()

  resize.setWidth(453)
  expect(screen.getByRole('tablist')).toHaveAttribute('data-compact', 'true')
  expect(screen.getAllByRole('tab')).toHaveLength(3)
  expect(screen.queryByRole('button', { name: /hidden tabs?/ })).not.toBeInTheDocument()
  expect(screen.queryByText('Tab 2')).not.toBeInTheDocument()
  expect(screen.queryByRole('button', { name: 'Close Tab 2' })).not.toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Close Tab 1' })).toBeInTheDocument()
  expect(screen.getByRole('tab', { name: 'Tab 2' })).toHaveAttribute('title', 'Tab 2')

  resize.setWidth(454)
  expect(screen.getByRole('tablist')).not.toHaveAttribute('data-compact')
  expect(screen.getByText('Tab 2')).toBeInTheDocument()
})

it('hides the new tab button once tabs need overflow and restores it when they fit with it', () => {
  const resize = resizeHarness()
  render(
    <HolonTabStrip tabs={tabs(5)} selectedTabId="tab-1" addOptions={addOptions()} onSelect={vi.fn()} onClose={vi.fn()} />,
    { wrapper: DismissiblePopoverGroup },
  )

  resize.setWidth(206)
  expect(screen.getByRole('button', { name: 'New tab' })).toBeInTheDocument()
  expect(screen.getAllByRole('tab')).toHaveLength(5)

  resize.setWidth(205)
  expect(screen.queryByRole('button', { name: 'New tab' })).not.toBeInTheDocument()
  expect(screen.getAllByRole('tab')).toHaveLength(5)

  resize.setWidth(160)
  expect(screen.queryByRole('button', { name: 'New tab' })).not.toBeInTheDocument()
  expect(screen.getByRole('button', { name: '2 hidden tabs' })).toBeInTheDocument()
})

it('keeps every tab visible at exactly the 24px minimum', () => {
  const resize = resizeHarness()
  render(
    <HolonTabStrip
      tabs={tabs(5)}
      selectedTabId="tab-5"
      addOptions={addOptions()}
      onSelect={vi.fn()}
      onClose={vi.fn()}
    />,
    { wrapper: DismissiblePopoverGroup },
  )

  resize.setWidth(172)

  expect(screen.getAllByRole('tab')).toHaveLength(5)
  expect(screen.queryByRole('button', { name: /hidden tabs?/ })).not.toBeInTheDocument()
  for (const tab of screen.getAllByRole('tab')) expect(tab).toHaveStyle({ width: tab.getAttribute('aria-selected') === 'true' ? '76px' : '24px' })
})

it('adds overflow below minimum capacity, retains selection, and moves the visible window', async () => {
  const resize = resizeHarness()
  const allTabs = tabs(5)
  let selected = 'tab-5'
  const onClose = vi.fn()
  const { rerender } = render(
    <HolonTabStrip
      tabs={allTabs}
      selectedTabId={selected}
      addOptions={addOptions()}
      onSelect={(tab) => {
        selected = tab.id
        rerender(
          <HolonTabStrip tabs={allTabs} selectedTabId={selected} addOptions={addOptions()} onSelect={vi.fn()} onClose={onClose} />,
        )
      }}
      onClose={onClose}
    />,
    { wrapper: DismissiblePopoverGroup },
  )
  resize.setWidth(160)

  expect(screen.getAllByRole('tab')).toHaveLength(3)
  expect(screen.getByRole('tab', { name: 'Tab 5' })).toHaveAttribute('aria-selected', 'true')
  expect(screen.queryByRole('tab', { name: 'Tab 1' })).not.toBeInTheDocument()
  const overflow = screen.getByRole('button', { name: '2 hidden tabs' })
  await userEvent.click(overflow)
  const menu = overflow.parentElement!
  expect(within(menu).getAllByRole('button')).toHaveLength(3)
  await userEvent.click(within(menu).getByRole('button', { name: 'Tab 1' }))

  expect(screen.getByRole('tab', { name: 'Tab 1' })).toHaveAttribute('aria-selected', 'true')
  expect(screen.getByRole('tab', { name: 'Tab 3' })).toBeInTheDocument()
  expect(screen.queryByRole('tab', { name: 'Tab 5' })).not.toBeInTheDocument()
  expect(screen.getByRole('button', { name: '2 hidden tabs' })).toHaveAttribute('aria-expanded', 'false')
})

it('renders icons, with actions only on the selected tab, and limits rename and drag behavior', () => {
  const resize = resizeHarness()
  const onDoubleClick = vi.fn()
  const onReorder = vi.fn()
  const allTabs = tabs(2)
  const { rerender } = render(
    <HolonTabStrip
      tabs={allTabs}
      selectedTabId="tab-1"
      onSelect={vi.fn()}
      onClose={vi.fn()}
      onDoubleClick={onDoubleClick}
      onReorder={onReorder}
    />,
    { wrapper: DismissiblePopoverGroup },
  )
  resize.setWidth(100)

  const first = screen.getByRole('tab', { name: 'Tab 1' })
  const second = screen.getByRole('tab', { name: 'Tab 2' })
  expect(within(first).getByRole('button', { name: 'Close Tab 1' })).toBeInTheDocument()
  expect(within(first).getByTestId('icon-1')).toBeInTheDocument()
  expect(within(second).getByTestId('icon-2')).toBeInTheDocument()
  expect(within(second).queryByRole('button')).not.toBeInTheDocument()
  fireEvent.doubleClick(second)
  expect(onDoubleClick).not.toHaveBeenCalled()

  const tablist = screen.getByRole('tablist')
  vi.spyOn(first, 'getBoundingClientRect').mockReturnValue(rect(0, 50))
  vi.spyOn(second, 'getBoundingClientRect').mockReturnValue(rect(50, 100))
  vi.spyOn(tablist, 'getBoundingClientRect').mockReturnValue(rect(0, 100))
  fireEvent.pointerDown(first, { button: 0, pointerId: 1, clientX: 25, clientY: 10 })
  fireEvent.pointerMove(first, { pointerId: 1, clientX: 75, clientY: 10 })
  fireEvent.pointerUp(first, { pointerId: 1, clientX: 75, clientY: 10 })
  expect(onReorder).not.toHaveBeenCalled()

  fireEvent.pointerDown(second, { button: 0, pointerId: 2, clientX: 75, clientY: 10 })
  fireEvent.pointerMove(second, { pointerId: 2, clientX: 1, clientY: 10 })
  fireEvent.pointerUp(second, { pointerId: 2, clientX: 1, clientY: 10 })
  expect(onReorder).toHaveBeenCalledWith('layout-2', 0)

  rerender(
    <HolonTabStrip
      tabs={[{ ...allTabs[0], renaming: true, label: <input aria-label="Rename Tab 1" /> }, allTabs[1]]}
      selectedTabId="tab-1"
      onSelect={vi.fn()}
      onClose={vi.fn()}
      onDoubleClick={onDoubleClick}
      onReorder={onReorder}
    />,
  )
  expect(screen.getByRole('tablist')).not.toHaveAttribute('data-compact')
  expect(screen.getByRole('textbox', { name: 'Rename Tab 1' })).toBeInTheDocument()

  rerender(
    <HolonTabStrip
      tabs={allTabs}
      selectedTabId="tab-1"
      onSelect={vi.fn()}
      onClose={vi.fn()}
      onDoubleClick={onDoubleClick}
      onReorder={onReorder}
    />,
  )
  expect(screen.getByRole('tablist')).toHaveAttribute('data-compact', 'true')
  expect(screen.queryByRole('textbox', { name: 'Rename Tab 1' })).not.toBeInTheDocument()
})

it('maps drag insertion positions in an overflow window to full-list indexes', () => {
  const resize = resizeHarness()
  const onReorder = vi.fn()
  render(
    <HolonTabStrip
      tabs={tabs(5)}
      selectedTabId="tab-4"
      addOptions={addOptions()}
      onSelect={vi.fn()}
      onClose={vi.fn()}
      onReorder={onReorder}
    />,
    { wrapper: DismissiblePopoverGroup },
  )
  resize.setWidth(160)

  const second = screen.getByRole('tab', { name: 'Tab 2' })
  const third = screen.getByRole('tab', { name: 'Tab 3' })
  const fourth = screen.getByRole('tab', { name: 'Tab 4' })
  const tablist = screen.getByRole('tablist')
  vi.spyOn(second, 'getBoundingClientRect').mockReturnValue(rect(0, 29))
  vi.spyOn(third, 'getBoundingClientRect').mockReturnValue(rect(29, 58))
  vi.spyOn(fourth, 'getBoundingClientRect').mockReturnValue(rect(58, 87))
  vi.spyOn(tablist, 'getBoundingClientRect').mockReturnValue(rect(0, 87))

  fireEvent.pointerDown(second, { button: 0, pointerId: 1, clientX: 10, clientY: 10 })
  fireEvent.pointerMove(second, { pointerId: 1, clientX: 86, clientY: 10 })
  fireEvent.pointerUp(second, { pointerId: 1, clientX: 86, clientY: 10 })

  expect(onReorder).toHaveBeenCalledWith('layout-2', 4)
})
