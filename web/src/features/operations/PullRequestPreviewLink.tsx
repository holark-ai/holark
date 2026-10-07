import { useEffect, useId, useRef, useState, type FocusEvent, type PointerEvent } from 'react'
import { Link, type LinkProps } from 'react-router-dom'
import type { PullRequest } from '../../data/types'
import styles from './OperationsPanel.module.css'

const hoverIntentMs = 300
// Crossing the chevron on the way to the preview must not dismiss it.
const chevronCrossingMs = 200

type PullRequestPreviewLinkProps = Omit<LinkProps, 'to'> & { pullRequest: PullRequest, label: string }

// Links to a pull request and previews its summary beside the Holons panel.
export function PullRequestPreviewLink({ pullRequest, label, onClick, children, ...linkProps }: PullRequestPreviewLinkProps) {
  const id = useId()
  const popover = useRef<HTMLDivElement>(null)
  const trigger = useRef<HTMLAnchorElement>(null)
  const hoverTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)
  const closeTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)
  const [open, setOpen] = useState(false)
  const cancelHover = () => {
    if (hoverTimer.current !== undefined) clearTimeout(hoverTimer.current)
    hoverTimer.current = undefined
  }
  const cancelClose = () => {
    if (closeTimer.current !== undefined) clearTimeout(closeTimer.current)
    closeTimer.current = undefined
  }
  // The group's chevron sits between the preview and the link.
  const chevron = () => trigger.current?.previousElementSibling ?? null
  const close = () => {
    cancelHover()
    cancelClose()
    popover.current?.hidePopover?.()
  }
  const panelExpanded = () => trigger.current?.closest('aside')?.dataset.expanded === 'true'
  const show = () => {
    // The panel widens on hover; positioning against the narrow rail would cover it.
    if (!panelExpanded()) return
    position()
    popover.current?.showPopover?.()
  }
  const showAfterIntent = (event: PointerEvent<HTMLElement>) => {
    if (event.pointerType === 'touch' || open || hoverTimer.current !== undefined) return
    hoverTimer.current = setTimeout(() => {
      hoverTimer.current = undefined
      show()
    }, hoverIntentMs)
  }
  const leave = (event: PointerEvent<HTMLElement> | FocusEvent<HTMLElement>) => {
    const next = event.relatedTarget
    if (next instanceof Node && (trigger.current?.contains(next) || popover.current?.contains(next))) return
    if (next instanceof Node && chevron()?.contains(next)) {
      cancelClose()
      closeTimer.current = setTimeout(close, chevronCrossingMs)
      return
    }
    close()
  }
  useEffect(() => () => { cancelHover(); cancelClose() }, [])
  useEffect(() => {
    if (!open) return
    const dismiss = () => popover.current?.hidePopover?.()
    window.addEventListener('resize', dismiss)
    window.addEventListener('scroll', dismiss, true)
    return () => { window.removeEventListener('resize', dismiss); window.removeEventListener('scroll', dismiss, true) }
  }, [open])
  const position = () => {
    const anchor = trigger.current?.getBoundingClientRect()
    const panel = trigger.current?.closest('aside')?.getBoundingClientRect()
    if (!popover.current || !anchor || !panel) return
    const left = Math.max(12, panel.left - 348)
    // Bridge the gap up to the chevron, leaving it hoverable and clickable.
    const bridgeEnd = chevron()?.getBoundingClientRect().left ?? anchor.left
    const top = Math.max(12, Math.min(anchor.top - 8, window.innerHeight - 280))
    const width = Math.min(340, window.innerWidth - 24)
    popover.current.style.left = `${left}px`
    popover.current.style.top = `${top}px`
    popover.current.style.setProperty('--bridge-width', `${Math.max(0, bridgeEnd - left - width)}px`)
  }
  return <>
    <Link
      {...linkProps}
      ref={trigger}
      to={`/pulls/${pullRequest.id}`}
      aria-haspopup="dialog"
      aria-expanded={open}
      aria-controls={id}
      onClick={(event) => { close(); onClick?.(event) }}
      onFocus={show}
      onPointerEnter={(event) => { cancelClose(); showAfterIntent(event) }}
      onPointerMove={showAfterIntent}
      onPointerLeave={leave}
      onBlur={leave}
    >
      {children}
    </Link>
    <div ref={popover} id={id} popover="auto" role="dialog" aria-label={`${label}: ${pullRequest.title}`} className={styles.prPopover} onPointerEnter={cancelClose} onPointerLeave={leave} onBlur={leave} onToggle={(event) => setOpen(event.newState === 'open')}>
      <div className={styles.prPreview}>
        <small><Link className={styles.prPreviewTitleLink} to={`/pulls/${pullRequest.id}`} onClick={close}>{label}</Link></small>
        <h3><Link className={styles.prPreviewTitleLink} to={`/pulls/${pullRequest.id}`} onClick={close}>{pullRequest.title}</Link></h3>
        <p>{pullRequest.status === 'wip' ? 'In preparation' : pullRequest.status} · {pullRequest.head_branch} → {pullRequest.base_branch}</p>
        <div>{pullRequest.summary}</div>
      </div>
    </div>
  </>
}
