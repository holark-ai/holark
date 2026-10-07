import { LoaderCircle, Plus } from 'lucide-react'
import { useCallback, useEffect, useLayoutEffect, useRef, useState, type PointerEvent as ReactPointerEvent, type ReactNode } from 'react'
import { useDismissiblePopover } from '../../components/DismissiblePopover'
import { TabSelectionIndicator } from '../../components/TabSelectionIndicator'
import { useCreationShortcut } from '../navigation/useCreationShortcut'
import styles from './HolonTerminal.module.css'

export type SessionTabTone = 'running' | 'idle' | 'attention' | 'neutral' | 'completed' | 'failed'

export type HolonTabStripItem = {
  id: string
  layoutKey: string
  title: string
  icon: ReactNode
  iconOnlyWhenCompact?: boolean
  iconLabel: string
  tone: SessionTabTone
  label?: ReactNode
  renaming?: boolean
  action?: {
    label: string
    icon: ReactNode
    disabled?: boolean
    title?: string
  }
}

type HolonTabStripProps = {
  tabs: HolonTabStripItem[]
  selectedTabId: string
  tabListLabel?: string
  viewportTestId?: string
  addOptions?: ReactNode
  addingAgent?: boolean
  onSelect: (tab: HolonTabStripItem) => void
  onAction?: (tab: HolonTabStripItem) => void
  onClose: (tab: HolonTabStripItem) => void
  onDoubleClick?: (tab: HolonTabStripItem) => void
  onReorder?: (sourceKey: string, insertIndex: number) => void | Promise<void>
}

type TabDragState = {
  sourceKey: string
  pointerId: number
  originX: number
  originY: number
  active: boolean
}

type TabDragIndicator = {
  index: number
  left: number
  top: number
  height: number
}

const preferredTabWidth = 140
const compactTabWidth = 40
const compactSelectedWidth = 76
const compactSelectedWidthWithAction = 104
const minimumTabWidth = 24
const addTabWidth = 34
const reservedOverflowWidth = 32

type TabLayout = {
  natural: boolean
  selectedWidth: number
  compact: boolean
  tabWidth: number
  visibleTabCount: number
}

const toneClass: Record<SessionTabTone, string> = {
  completed: styles.tabIconCompleted,
  failed: styles.tabIconFailed,
  running: styles.tabIconRunning,
  idle: styles.tabIconIdle,
  attention: styles.tabIconAttention,
  neutral: styles.tabIconNeutral,
}

export function HolonTabStrip({
  tabs,
  selectedTabId,
  tabListLabel = 'Holon terminals',
  viewportTestId = 'tab-viewport',
  addOptions,
  addingAgent = false,
  onSelect,
  onAction,
  onClose,
  onDoubleClick,
  onReorder,
}: HolonTabStripProps) {
  const [stripWidth, setStripWidth] = useState(0)
  const [naturalTabMeasurement, setNaturalTabMeasurement] = useState({ tabs, selectedTabId, width: preferredTabWidth })
  const [preferredWindowStart, setPreferredWindowStart] = useState(0)
  const [draggingTab, setDraggingTab] = useState<string | null>(null)
  const [dragIndicator, setDragIndicator] = useState<TabDragIndicator | null>(null)
  const tabElements = useRef<Record<string, HTMLElement | null>>({})
  const tabStripElement = useRef<HTMLDivElement>(null)
  const tabListElement = useRef<HTMLDivElement>(null)
  const {
    open: tabOverflowOpen,
    rootRef: tabOverflowRootRef,
    triggerRef: tabOverflowTriggerRef,
    toggle: toggleTabOverflow,
    close: closeTabOverflow,
  } = useDismissiblePopover('hidden-tabs')
  const returnFocusRef = useRef<HTMLElement | null>(null)
  const hoverTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)
  const pinnedOpen = useRef(false)
  const focusOnOpen = useRef(false)
  const {
    open: addTabOpen,
    rootRef: addTabRootRef,
    triggerRef: addTabTriggerRef,
    show: showAddTab,
    close: closeAddTab,
  } = useDismissiblePopover('add-tab', returnFocusRef)
  const clearHoverTimer = () => clearTimeout(hoverTimer.current)
  useEffect(() => () => clearTimeout(hoverTimer.current), [])

  const focusDefaultOption = useCallback(() => {
    const root = addTabRootRef.current
    const option = root?.querySelector<HTMLButtonElement>('[data-default-tab-option]:not(:disabled)')
      ?? root?.querySelector<HTMLButtonElement>('[data-holon-header-popover] button:not(:disabled)')
    const target = option ?? addTabTriggerRef.current
    target?.focus()
  }, [addTabRootRef, addTabTriggerRef])
  const openAddTab = (keyboard: boolean) => {
    clearHoverTimer()
    if (!addTabOpen) returnFocusRef.current = document.activeElement instanceof HTMLElement ? document.activeElement : null
    pinnedOpen.current = keyboard
    focusOnOpen.current = keyboard
    showAddTab()
    if (addTabOpen && keyboard) focusDefaultOption()
  }
  useCreationShortcut('KeyT', () => {
    if (addOptions) openAddTab(true)
  })
  useLayoutEffect(() => {
    if (addTabOpen && focusOnOpen.current) {
      focusOnOpen.current = false
      focusDefaultOption()
    }
    if (!addTabOpen) {
      clearHoverTimer()
      pinnedOpen.current = false
    }
  }, [addTabOpen, focusDefaultOption])
  const tabDrag = useRef<TabDragState | null>(null)
  const isMac = /Mac|iPhone|iPad|iPod/i.test(navigator.platform)
  // The add button only stays while every tab still fits with its icon (and, for the
  // selected one, its action and close buttons); otherwise its room goes to the tabs.
  const selectedTab = tabs.find((tab) => tab.id === selectedTabId)
  // Keep room for the 112px rename input, padding, and any visible icon/actions.
  const selectedWidth = selectedTab?.renaming
    ? 180 + (selectedTab.iconOnlyWhenCompact ? 0 : 24) + (selectedTab.action ? 24 : 0)
    : !selectedTab ? compactTabWidth : selectedTab.action ? compactSelectedWidthWithAction : compactSelectedWidth
  // Changed tabs must render in full once so even a collapsed/overflowing strip
  // can measure their current contents. The layout effect settles before paint.
  const needsTabMeasurement = naturalTabMeasurement.selectedTabId !== selectedTabId
    || naturalTabMeasurement.tabs.length !== tabs.length
    || tabs.some((tab, index) => {
      const measured = naturalTabMeasurement.tabs[index]
      return tab.layoutKey !== measured.layoutKey || tab.title !== measured.title
        || tab.label !== measured.label || tab.renaming !== measured.renaming
        || tab.iconOnlyWhenCompact !== measured.iconOnlyWhenCompact || Boolean(tab.action) !== Boolean(measured.action)
    })
  const naturalTabWidth = needsTabMeasurement ? 0 : naturalTabMeasurement.width
  const layoutWithAdd = calculateTabLayout(stripWidth, tabs.length, addOptions ? addTabWidth : 0, naturalTabWidth, selectedWidth)
  const showAddControl = Boolean(addOptions) && layoutWithAdd.visibleTabCount === tabs.length && layoutWithAdd.selectedWidth === selectedWidth
  const layout = showAddControl || !addOptions ? layoutWithAdd : calculateTabLayout(stripWidth, tabs.length, 0, naturalTabWidth, selectedWidth)
  const naturalWidth = layout.natural
  const compactPresentation = layout.compact && !tabs.some((tab) => tab.renaming)
  const selectedTabIndex = tabs.findIndex((tab) => tab.id === selectedTabId)
  const visibleTabCount = layout.visibleTabCount
  const windowStart = tabWindowStart(preferredWindowStart, tabs.length, visibleTabCount, selectedTabIndex)
  const visibleTabs = tabs.slice(windowStart, windowStart + visibleTabCount)
  const selectedVisible = visibleTabs.some((tab) => tab.id === selectedTabId)
  const hiddenTabs = tabs.filter((_, index) => index < windowStart || index >= windowStart + visibleTabCount)

  const measureTabLayout = useCallback(() => {
    const strip = tabStripElement.current
    if (!strip) return
    const width = strip.getBoundingClientRect().width || strip.clientWidth
    setStripWidth((current) => current === width ? current : width)
  }, [])

  useLayoutEffect(() => {
    const strip = tabStripElement.current
    if (!strip) return undefined
    measureTabLayout()
    const observer = new ResizeObserver(measureTabLayout)
    observer.observe(strip)
    return () => observer.disconnect()
  }, [measureTabLayout])

  // Remember how wide tabs really are at their natural size so the strip only
  // switches to fixed widths when the tabs genuinely do not fit.
  useLayoutEffect(() => {
    const track = tabListElement.current
    if (!naturalWidth || !track || tabs.length === 0 || visibleTabs.length !== tabs.length) return
    // Measure unconstrained tabs, excluding the animated selection indicator:
    // its previous width can temporarily inflate scrollWidth after a rename.
    const previousMaxWidth = track.style.maxWidth
    track.style.maxWidth = 'none'
    const fullWidth = Array.from(track.querySelectorAll<HTMLElement>('[data-tab-key]'))
      .reduce((width, tab) => width + tab.offsetWidth, 0)
    track.style.maxWidth = previousMaxWidth
    const perTab = fullWidth > 0 ? Math.ceil(fullWidth / tabs.length) + 4 : preferredTabWidth
    setNaturalTabMeasurement((current) => !needsTabMeasurement && current.width === perTab
      ? current : { tabs, selectedTabId, width: perTab })
  })

  const selectOverflowTab = (tab: HolonTabStripItem) => {
    const index = tabs.findIndex((candidate) => candidate.layoutKey === tab.layoutKey)
    setPreferredWindowStart(tabWindowStart(windowStart, tabs.length, visibleTabCount, index))
    onSelect(tab)
    closeTabOverflow()
  }

  const tabInsertionMarker = (clientX: number): TabDragIndicator | null => {
    const tabList = tabListElement.current
    const tabRects = visibleTabs
      .map((tab) => tabElements.current[tab.layoutKey]?.getBoundingClientRect())
      .filter((rect): rect is DOMRect => Boolean(rect))
    if (!tabList || !tabRects.length) return null
    const listRect = tabList.getBoundingClientRect()
    const firstRect = tabRects[0]
    const lastRect = tabRects[tabRects.length - 1]
    const x = Math.min(Math.max(clientX, firstRect.left), lastRect.right)
    let index = tabRects.findIndex((rect) => x < rect.left + rect.width / 2)
    if (index < 0) index = tabRects.length
    const previous = index > 0 ? tabRects[index - 1] : undefined
    const next = index < tabRects.length ? tabRects[index] : undefined
    const left = previous && next
      ? ((previous.right + next.left) / 2) - listRect.left
      : next
        ? next.left - listRect.left - 3
        : (previous?.right ?? lastRect.right) - listRect.left + 3
    const top = firstRect.top - listRect.top + 5
    const height = Math.max(20, firstRect.height - 10)
    return { index: windowStart + index, left, top, height }
  }

  const startTabPointerDrag = (event: ReactPointerEvent<HTMLElement>, tab: HolonTabStripItem) => {
    if (!onReorder || event.button !== 0 || tab.renaming || (compactPresentation && tab.id === selectedTabId)) return
    tabDrag.current = { sourceKey: tab.layoutKey, pointerId: event.pointerId, originX: event.clientX, originY: event.clientY, active: false }
    event.currentTarget.setPointerCapture?.(event.pointerId)
  }

  const moveTabPointerDrag = (event: ReactPointerEvent<HTMLElement>) => {
    const drag = tabDrag.current
    if (!drag || drag.pointerId !== event.pointerId) return
    const moved = Math.abs(event.clientX - drag.originX) + Math.abs(event.clientY - drag.originY)
    if (!drag.active && moved < 4) return
    if (!drag.active) {
      drag.active = true
      setDraggingTab(drag.sourceKey)
    }
    event.preventDefault()
    setDragIndicator(tabInsertionMarker(event.clientX))
  }

  const clearTabDrag = () => {
    setDraggingTab(null)
    setDragIndicator(null)
  }

  const finishTabPointerDrag = (event: ReactPointerEvent<HTMLElement>) => {
    const drag = tabDrag.current
    if (!drag || drag.pointerId !== event.pointerId) return
    event.currentTarget.releasePointerCapture?.(event.pointerId)
    tabDrag.current = null
    const indicator = tabInsertionMarker(event.clientX) ?? dragIndicator
    clearTabDrag()
    if (!drag.active || !indicator || !onReorder) return
    event.preventDefault()
    void onReorder(drag.sourceKey, indicator.index)
  }

  const cancelTabPointerDrag = (event: ReactPointerEvent<HTMLElement>) => {
    if (tabDrag.current?.pointerId !== event.pointerId) return
    event.currentTarget.releasePointerCapture?.(event.pointerId)
    tabDrag.current = null
    clearTabDrag()
  }

  return (
    <div className={styles.tabs} ref={tabStripElement}>
      <div
        className={styles.tabViewport}
        data-natural-width={naturalWidth || undefined}
        data-testid={viewportTestId}
        style={{ width: naturalWidth ? undefined : `${visibleTabs.length * layout.tabWidth + (selectedVisible ? layout.selectedWidth - layout.tabWidth : 0)}px` }}
      >
        <div className={styles.tabTrack} role="tablist" aria-label={tabListLabel} data-compact={compactPresentation ? 'true' : undefined} data-constrained={layout.selectedWidth < selectedWidth || undefined} ref={tabListElement}>
          <TabSelectionIndicator stripRef={tabListElement} className={styles.tabSelectionIndicator} />
          {dragIndicator && (
            <span
              aria-hidden="true"
              className={styles.tabInsertMarker}
              data-testid="tab-insert-marker"
              style={{ left: dragIndicator.left, top: dragIndicator.top, height: dragIndicator.height }}
            />
          )}
          {visibleTabs.map((tab) => (
            <span
              className={[styles.tab, draggingTab === tab.layoutKey ? styles.draggingTab : ''].filter(Boolean).join(' ')}
              key={tab.layoutKey}
              ref={(element) => {
                if (element) tabElements.current[tab.layoutKey] = element
                else delete tabElements.current[tab.layoutKey]
              }}
              data-tab-key={tab.layoutKey}
              role="tab"
              tabIndex={0}
              aria-label={tab.title}
              aria-selected={selectedTabId === tab.id}
              title={tab.title}
              style={naturalWidth ? undefined : tabWidthStyle(tab.id === selectedTabId ? layout.selectedWidth : layout.tabWidth)}
              onClick={() => onSelect(tab)}
              onDoubleClick={compactPresentation ? undefined : () => onDoubleClick?.(tab)}
              onPointerDown={(event) => startTabPointerDrag(event, tab)}
              onPointerMove={moveTabPointerDrag}
              onPointerUp={finishTabPointerDrag}
              onPointerCancel={cancelTabPointerDrag}
              onKeyDown={(event) => {
                if (event.key === 'Enter' || event.key === ' ') onSelect(tab)
              }}
            >
              {(compactPresentation || !tab.iconOnlyWhenCompact) && <span className={[styles.tabIcon, toneClass[tab.tone]].join(' ')} title={compactPresentation ? tab.title : tab.iconLabel}>{tab.icon}</span>}
              {!compactPresentation && (tab.label === undefined ? <span className={styles.tabLabel}>{tab.title}</span> : tab.label)}
              {(!compactPresentation || selectedTabId === tab.id) && (
                <span className={styles.tabActions}>
                  {tab.action && <TabAction tab={tab} onAction={onAction} />}
                  <button
                    className={styles.closeTab}
                    type="button"
                    aria-label={`Close ${tab.title}`}
                    onPointerDown={(event) => event.stopPropagation()}
                    onClick={(event) => { event.stopPropagation(); onClose(tab) }}
                    onDoubleClick={(event) => event.stopPropagation()}
                  >
                    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeLinecap="round" aria-hidden="true"><path d="m7 7 10 10M17 7 7 17" /></svg>
                  </button>
                </span>
              )}
            </span>
          ))}
        </div>
      </div>
      {hiddenTabs.length > 0 && (
        <div className={styles.tabOverflowMenu} ref={tabOverflowRootRef} data-open={tabOverflowOpen ? 'true' : undefined}>
          <button ref={tabOverflowTriggerRef} type="button" className={styles.tabOverflow} aria-label={`${hiddenTabs.length} hidden ${hiddenTabs.length === 1 ? 'tab' : 'tabs'}`} aria-expanded={tabOverflowOpen} onClick={toggleTabOverflow}>+{hiddenTabs.length}</button>
          {tabOverflowOpen && <div className={styles.tabOverflowOptions} data-holon-header-popover>
            {hiddenTabs.map((tab) => (
              <button
                key={tab.layoutKey}
                type="button"
                aria-current={selectedTabId === tab.id ? 'page' : undefined}
                onClick={(event) => { event.preventDefault(); selectOverflowTab(tab) }}
              >
                {tab.title}
              </button>
            ))}
          </div>}
        </div>
      )}
      {addOptions && (showAddControl || addTabOpen) && (
        <div className={styles.addTabMenu} ref={addTabRootRef} data-open={addTabOpen ? 'true' : undefined}
          style={{ marginLeft: showAddControl ? undefined : 0 }}
          onPointerEnter={(event) => {
            if (event.pointerType !== 'mouse') return
            clearHoverTimer()
            if (!addTabOpen) hoverTimer.current = setTimeout(() => openAddTab(false), 200)
          }}
          onPointerLeave={() => {
            clearHoverTimer()
            if (!pinnedOpen.current && !addTabRootRef.current?.contains(document.activeElement)) hoverTimer.current = setTimeout(closeAddTab, 250)
          }}
          onKeyDown={(event) => {
            if (!['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) return
            event.preventDefault()
            if (!addTabOpen) { openAddTab(true); return }
            const options = Array.from(addTabRootRef.current?.querySelectorAll<HTMLButtonElement>('[data-holon-header-popover] button:not(:disabled)') ?? [])
            if (!options.length) return
            const index = options.indexOf(document.activeElement as HTMLButtonElement)
            const next = event.key === 'Home' ? 0 : event.key === 'End' ? options.length - 1 : (index + (event.key === 'ArrowDown' ? 1 : -1) + options.length) % options.length
            options[next]?.focus()
          }}
        >
          {showAddControl && <button ref={addTabTriggerRef} type="button" className={styles.addTab} aria-label={addingAgent ? 'Adding agent' : 'New tab'} title={`New tab (Shift + ${isMac ? 'Option' : 'Alt'} + T)`} aria-keyshortcuts="Shift+Alt+T" aria-busy={addingAgent} aria-expanded={addTabOpen} onClick={(event) => {
            if (addTabOpen && pinnedOpen.current) closeAddTab()
            else {
              openAddTab(event.detail === 0)
              pinnedOpen.current = true
            }
          }}>
            {addingAgent ? <LoaderCircle className={styles.spinner} aria-hidden="true" /> : <Plus aria-hidden="true" />}
          </button>}
          {addTabOpen && <div
            className={styles.addTabOptions}
            data-holon-header-popover
            onClick={(event) => {
              if ((event.target as Element).closest('button:not(:disabled)')) closeAddTab()
            }}
          >
            {addOptions}
            <div className={styles.addTabFooter}>
              <span>New tab</span>
              <kbd aria-label={`Shift ${isMac ? 'Option' : 'Alt'} T`}>⇧ {isMac ? '⌥' : 'Alt'} T</kbd>
            </div>
          </div>}
        </div>
      )}
    </div>
  )
}

function TabAction({ tab, onAction }: { tab: HolonTabStripItem, onAction?: (tab: HolonTabStripItem) => void }) {
  if (!tab.action) return null
  return (
    <button
      className={styles.tabAction}
      type="button"
      aria-label={tab.action.label}
      title={tab.action.title}
      disabled={tab.action.disabled}
      onPointerDown={(event) => event.stopPropagation()}
      onClick={(event) => { event.stopPropagation(); onAction?.(tab) }}
      onDoubleClick={(event) => event.stopPropagation()}
    >
      {tab.action.icon}
    </button>
  )
}

function tabWidthStyle(width: number) {
  return { width: `${width}px`, maxWidth: `${width}px`, flex: `0 0 ${width}px` }
}

// When full tabs do not fit, unselected tabs shrink to their icon and the selected
// tab keeps its icon plus action and close buttons.
function calculateTabLayout(stripWidth: number, tabCount: number, addControlWidth: number, naturalTabWidth: number, selectedWidth: number): TabLayout {
  const natural = (visibleTabCount: number): TabLayout => ({ natural: true, selectedWidth, compact: false, tabWidth: preferredTabWidth, visibleTabCount })
  if (tabCount === 0) return natural(0)
  if (stripWidth <= 0) return natural(tabCount)

  const availableWithoutOverflow = Math.max(0, stripWidth - addControlWidth)
  if (availableWithoutOverflow >= tabCount * naturalTabWidth) return natural(tabCount)

  const unselectedWidth = (available: number, count: number) => (
    count > 1 ? Math.min(compactTabWidth, Math.max(minimumTabWidth, (available - selectedWidth) / (count - 1))) : compactTabWidth
  )
  if (availableWithoutOverflow >= selectedWidth + (tabCount - 1) * minimumTabWidth) {
    return { natural: false, selectedWidth, compact: true, tabWidth: unselectedWidth(availableWithoutOverflow, tabCount), visibleTabCount: tabCount }
  }

  const availableWithOverflow = Math.max(0, availableWithoutOverflow - (tabCount > 1 ? reservedOverflowWidth : 0))
  const visibleTabCount = Math.min(tabCount, Math.max(1, 1 + Math.floor((availableWithOverflow - selectedWidth) / minimumTabWidth)))
  // A lone selected tab must also fit beside overflow at the 120px strip minimum.
  return { natural: false, selectedWidth: Math.min(selectedWidth, availableWithOverflow), compact: true, tabWidth: unselectedWidth(availableWithOverflow, visibleTabCount), visibleTabCount }
}

function tabWindowStart(preferredStart: number, tabCount: number, capacity: number, selectedIndex: number) {
  const maxStart = Math.max(0, tabCount - capacity)
  let start = Math.min(Math.max(preferredStart, 0), maxStart)
  if (selectedIndex < 0) return start
  if (selectedIndex < start) start = selectedIndex
  if (selectedIndex >= start + capacity) start = selectedIndex - capacity + 1
  return Math.min(Math.max(start, 0), maxStart)
}
