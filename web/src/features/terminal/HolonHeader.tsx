import { ChevronDown, ChevronUp, Ellipsis } from 'lucide-react'
import { useCallback, useId, useLayoutEffect, useRef, useState, type ReactNode } from 'react'
import { DismissiblePopoverGroup, useDismissiblePopover } from '../../components/DismissiblePopover'
import { CopyButton } from '../../components/CopyButton'
import styles from './HolonHeader.module.css'

type HolonHeaderProps = {
  notice?: ReactNode
  tabs: ReactNode
  prompt?: string
  actions?: ReactNode
  overflowActions?: ReactNode
}

export function HolonHeader({
  notice,
  tabs,
  prompt,
  actions,
  overflowActions,
}: HolonHeaderProps) {
  return (
    <DismissiblePopoverGroup>
      <div className={styles.frame}>
        <div inert={Boolean(notice)}>
          <HolonHeaderContent
            tabs={tabs}
            prompt={prompt}
            actions={actions}
            overflowActions={overflowActions}
          />
        </div>
        {notice && <div className={styles.noticeOverlay}>{notice}</div>}
      </div>
    </DismissiblePopoverGroup>
  )
}

function HolonHeaderContent({
  tabs,
  prompt,
  actions,
  overflowActions,
}: HolonHeaderProps) {
  const {
    open: overflowOpen,
    rootRef: overflowRootRef,
    triggerRef: overflowTriggerRef,
    toggle: toggleOverflow,
    close: closeOverflow,
  } = useDismissiblePopover('holon-actions')
  const headerRef = useRef<HTMLElement>(null)
  const overflowId = useId()

  const alignPopovers = useCallback(() => {
    const header = headerRef.current
    if (!header) return
    header.querySelectorAll<HTMLElement>('[data-holon-header-popover]').forEach(alignPopoverToViewport)
  }, [])

  useLayoutEffect(() => {
    const header = headerRef.current
    if (!header) return undefined
    alignPopovers()
    const observer = new ResizeObserver(alignPopovers)
    observer.observe(header)
    header.querySelectorAll<HTMLElement>('[data-holon-header-popover]').forEach((popover) => observer.observe(popover))
    window.addEventListener('resize', alignPopovers)
    return () => {
      observer.disconnect()
      window.removeEventListener('resize', alignPopovers)
    }
  })

  return (
    <header className={styles.header} ref={headerRef}>
      <div className={styles.tabs} data-testid="holon-tabs">{tabs}</div>
      <div className={styles.actionGroup}>
        {actions && <div className={styles.actions}>{actions}</div>}
        <div className={styles.overflow} ref={overflowRootRef} data-open={overflowOpen ? 'true' : undefined}>
          <button ref={overflowTriggerRef} type="button" aria-label="More holon actions" title="More holon actions" aria-controls={overflowOpen ? overflowId : undefined} aria-expanded={overflowOpen} onClick={toggleOverflow}><Ellipsis /></button>
          {overflowOpen && <div
            id={overflowId}
            role="region"
            tabIndex={-1}
            aria-label="Holon details and actions"
            className={styles.overflowMenu}
            data-holon-header-popover
          >
            <HolonTask key={prompt} prompt={prompt} />
            {overflowActions && <div className={styles.overflowActions} onClick={(event) => {
              if ((event.target as Element).closest('button:not(:disabled), a')) closeOverflow()
            }}>{overflowActions}</div>}
          </div>}
        </div>
      </div>
    </header>
  )
}

function HolonTask({ prompt }: { prompt?: string }) {
  const contentId = useId()
  const headingId = useId()
  const contentRef = useRef<HTMLParagraphElement>(null)
  const [expanded, setExpanded] = useState(false)
  const [truncated, setTruncated] = useState(false)

  useLayoutEffect(() => {
    const content = contentRef.current
    if (!content || expanded) return
    const measure = () => setTruncated(content.scrollHeight > content.clientHeight)
    const observer = new ResizeObserver(measure)
    observer.observe(content)
    const frame = window.requestAnimationFrame(measure)
    return () => {
      observer.disconnect()
      window.cancelAnimationFrame(frame)
    }
  }, [expanded])

  return <section className={styles.task} aria-labelledby={headingId}>
    <div className={styles.taskHeading}>
      <h2 id={headingId}>Task</h2>
      {prompt && <CopyButton text={prompt} label="Copy task" />}
    </div>
    <p ref={contentRef} id={contentId} className={styles.taskContent} data-expanded={expanded || undefined}>
      {prompt || 'This holon was created without a task.'}
    </p>
    {(truncated || expanded) && <button type="button" className={styles.taskToggle} aria-expanded={expanded} aria-controls={contentId} onClick={() => setExpanded(!expanded)}>
      {expanded ? 'See less' : 'See more'}{expanded ? <ChevronUp /> : <ChevronDown />}
    </button>}
  </section>
}

function alignPopoverToViewport(popover: HTMLElement) {
  popover.style.transform = ''
  const viewportPadding = 8
  const anchor = popover.parentElement?.getBoundingClientRect()
  // Fit on one side of the trigger so a tall menu cannot cover neighboring
  // header controls when the viewport is short.
  const availableAbove = (anchor?.top ?? 0) - viewportPadding - 4
  const availableBelow = window.innerHeight - (anchor?.bottom ?? 0) - viewportPadding - 4
  popover.style.maxHeight = `${Math.max(0, availableAbove, availableBelow)}px`
  const bounds = popover.getBoundingClientRect()
  const headerBounds = popover.closest('header')?.getBoundingClientRect()
  const left = Math.max(viewportPadding, headerBounds?.left ?? 0)
  const right = Math.min(window.innerWidth - viewportPadding, headerBounds?.right ?? window.innerWidth)
  let translateX = 0
  let translateY = 0
  if (bounds.left < left) translateX = left - bounds.left
  else if (bounds.right > right) translateX = right - bounds.right
  if (bounds.top < viewportPadding) translateY = viewportPadding - bounds.top
  else if (bounds.bottom > window.innerHeight - viewportPadding) {
    const flippedTop = anchor ? anchor.top - 4 - bounds.height : -1
    translateY = flippedTop >= viewportPadding
      ? flippedTop - bounds.top
      : window.innerHeight - viewportPadding - bounds.bottom
  }
  popover.style.transform = `translate(${translateX}px, ${translateY}px)`
}
