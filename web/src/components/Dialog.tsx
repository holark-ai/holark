import { X } from 'lucide-react'
import { useId, useLayoutEffect, useRef, type RefObject } from 'react'
import { createPortal } from 'react-dom'
import { useLocation } from 'react-router-dom'
import { Button } from './Button'
import styles from './Dialog.module.css'

type DialogProps = {
  open: boolean
  title: string
  titleControl?: React.ReactNode
  headerAction?: React.ReactNode
  children: React.ReactNode
  onClose: () => void
  onKeyDownCapture?: React.KeyboardEventHandler<HTMLElement>
  footer?: React.ReactNode
  initialFocusRef?: RefObject<HTMLElement | null>
}

type ModalEntry = {
  portal: HTMLElement
  dialog: HTMLElement
  opener: HTMLElement | null
  route: string
  currentRoute: () => string
  initialFocus: () => HTMLElement | null
  close: () => void
  active: boolean
  lifecycle: number
  currentLifecycle: () => number
}

type IsolationState = {
  inert: boolean
  hadInertAttribute: boolean
  ariaHidden: string | null
}

const modalStack: ModalEntry[] = []
const isolatedElements = new Map<HTMLElement, IsolationState>()
let listening = false
let redirectingFocus = false

export function Dialog({ open, title, titleControl, headerAction, children, onClose, onKeyDownCapture, footer, initialFocusRef }: DialogProps) {
  const closeRef = useRef<HTMLButtonElement>(null)
  const portalRef = useRef<HTMLDivElement>(null)
  const dialogRef = useRef<HTMLElement>(null)
  const entryRef = useRef<ModalEntry | undefined>(undefined)
  const onCloseRef = useRef(onClose)
  const initialFocusRefRef = useRef(initialFocusRef)
  const openerRef = useRef<HTMLElement | null | undefined>(undefined)
  const didApplyInitialFocus = useRef(false)
  const lifecycle = useRef(0)
  const location = useLocation()
  const route = routeIdentity(location)
  const currentRoute = useRef(route)
  const titleId = useId()

  useLayoutEffect(() => {
    onCloseRef.current = onClose
    initialFocusRefRef.current = initialFocusRef
    currentRoute.current = route
  }, [initialFocusRef, onClose, route])

  useLayoutEffect(() => {
    if (!open) {
      didApplyInitialFocus.current = false
      openerRef.current = undefined
      return undefined
    }
    const portal = portalRef.current
    const dialog = dialogRef.current
    if (!portal || !dialog) return undefined

    lifecycle.current += 1
    if (openerRef.current === undefined) {
      openerRef.current = meaningfulActiveElement()
    }

    const entry: ModalEntry = {
      portal,
      dialog,
      opener: openerRef.current,
      route: currentRoute.current,
      currentRoute: () => currentRoute.current,
      initialFocus: () => initialFocusRefRef.current?.current ?? closeRef.current,
      close: () => onCloseRef.current(),
      active: true,
      lifecycle: lifecycle.current,
      currentLifecycle: () => lifecycle.current,
    }
    entryRef.current = entry
    registerModal(entry)
    if (!didApplyInitialFocus.current) {
      didApplyInitialFocus.current = true
      focusModal(entry)
    }
    syncBackgroundIsolation()

    return () => {
      unregisterModal(entry)
      if (entryRef.current === entry) entryRef.current = undefined
    }
  }, [open])

  if (!open) return null

  return createPortal(
    <div
      className={styles.backdrop}
      data-holark-dialog-portal=""
      ref={portalRef}
      onMouseDown={(event) => {
        if (event.target === event.currentTarget && isTopModal(entryRef.current)) onCloseRef.current()
      }}
    >
      <section ref={dialogRef} className={styles.dialog} role="dialog" aria-modal="true" aria-labelledby={titleId} onKeyDownCapture={onKeyDownCapture}>
        <div className={styles.header}>
          <div className={styles.titleRow}>
            <h2 id={titleId} className={titleControl ? 'sr-only' : undefined}>{title}</h2>
            {titleControl}
            {headerAction}
          </div>
          <Button ref={closeRef} variant="ghost" className={styles.close} aria-label="Close dialog" onClick={() => onCloseRef.current()} icon={<X />} />
        </div>
        <div className={styles.content}>{children}</div>
        <div className={styles.footer}>
          {footer ?? <Button variant="primary" onClick={() => onCloseRef.current()}>Got it</Button>}
        </div>
      </section>
    </div>,
    document.body,
  )
}

function routeIdentity(location: { key: string, pathname: string, search: string, hash: string }) {
  return `${location.key}:${location.pathname}${location.search}${location.hash}`
}

function registerModal(entry: ModalEntry) {
  modalStack.push(entry)
  if (!listening) {
    document.addEventListener('keydown', handleDocumentKeyDown, true)
    document.addEventListener('focusin', handleDocumentFocus, true)
    listening = true
  }
}

function unregisterModal(entry: ModalEntry) {
  entry.active = false
  const index = modalStack.lastIndexOf(entry)
  if (index >= 0) modalStack.splice(index, 1)
  syncBackgroundIsolation()
  if (modalStack.length === 0 && listening) {
    document.removeEventListener('keydown', handleDocumentKeyDown, true)
    document.removeEventListener('focusin', handleDocumentFocus, true)
    listening = false
  }
  scheduleOpenerRestore(entry)
}

function topModal() {
  return modalStack[modalStack.length - 1]
}

function isTopModal(entry: ModalEntry | undefined) {
  return Boolean(entry && topModal() === entry)
}

function handleDocumentKeyDown(event: KeyboardEvent) {
  const entry = topModal()
  if (!entry) return
  if (event.key === 'Escape') {
    if (event.target instanceof Element && event.target.closest('[data-holark-dialog-escape-handler]')) return
    if (event.target instanceof Element && event.target.closest('[data-holark-dialog-nested-layer]')) return
    event.preventDefault()
    event.stopPropagation()
    entry.close()
    return
  }
  if (event.key !== 'Tab') return
  const focusable = focusableElements(entry.dialog)
  if (focusable.length === 0) {
    event.preventDefault()
    focusModal(entry)
    return
  }
  const activeIndex = focusable.indexOf(document.activeElement as HTMLElement)
  if (event.shiftKey && activeIndex <= 0) {
    event.preventDefault()
    focusable[focusable.length - 1].focus()
  } else if (!event.shiftKey && (activeIndex < 0 || activeIndex === focusable.length - 1)) {
    event.preventDefault()
    focusable[0].focus()
  }
}

function handleDocumentFocus(event: FocusEvent) {
  if (redirectingFocus) return
  const entry = topModal()
  const target = event.target
  if (!entry || (target instanceof Node && entry.dialog.contains(target)) || (target instanceof Element && target.closest('[data-holark-dialog-nested-layer]'))) return
  redirectingFocus = true
  focusModal(entry)
  redirectingFocus = false
}

function focusModal(entry: ModalEntry) {
  const requested = entry.initialFocus()
  const target = requested?.isConnected && entry.dialog.contains(requested)
    ? requested
    : focusableElements(entry.dialog)[0] ?? entry.dialog
  if (target === entry.dialog && !entry.dialog.hasAttribute('tabindex')) entry.dialog.tabIndex = -1
  target.focus()
}

function focusableElements(dialog: HTMLElement) {
  const selector = 'button:not([disabled]), [href], input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])'
  return Array.from(dialog.querySelectorAll<HTMLElement>(selector)).filter((element) => element.getAttribute('aria-hidden') !== 'true' && !element.closest('[inert]'))
}

function meaningfulActiveElement() {
  const active = document.activeElement
  return active instanceof HTMLElement
    && active !== document.body
    && active !== document.documentElement
    && active.isConnected
    ? active
    : null
}

function scheduleOpenerRestore(entry: ModalEntry) {
  queueMicrotask(() => {
    if (entry.active || entry.currentLifecycle() !== entry.lifecycle) return
    const opener = entry.opener
    if (!opener?.isConnected || entry.currentRoute() !== entry.route) return
    const active = meaningfulActiveElement()
    if (active && entry.portal.contains(active)) return
    if (active && active !== opener) return
    const currentTop = topModal()
    if (currentTop && !currentTop.dialog.contains(opener)) {
      focusModal(currentTop)
      return
    }
    opener.focus()
  })
}

function syncBackgroundIsolation() {
  const activePortal = topModal()?.portal
  const targets = new Set<HTMLElement>(activePortal
    ? Array.from(document.body.children).filter((element): element is HTMLElement => element instanceof HTMLElement && element !== activePortal)
    : [])

  isolatedElements.forEach((state, element) => {
    if (targets.has(element)) return
    restoreIsolation(element, state)
    isolatedElements.delete(element)
  })

  targets.forEach((element) => {
    if (!isolatedElements.has(element)) {
      isolatedElements.set(element, {
        inert: element.inert,
        hadInertAttribute: element.hasAttribute('inert'),
        ariaHidden: element.getAttribute('aria-hidden'),
      })
    }
    element.inert = true
    element.setAttribute('inert', '')
    element.setAttribute('aria-hidden', 'true')
  })
}

function restoreIsolation(element: HTMLElement, state: IsolationState) {
  element.inert = state.inert
  if (state.hadInertAttribute) element.setAttribute('inert', '')
  else element.removeAttribute('inert')
  if (state.ariaHidden === null) element.removeAttribute('aria-hidden')
  else element.setAttribute('aria-hidden', state.ariaHidden)
}
