import { useEffect, useEffectEvent } from 'react'

// Capture before xterm handles the key so navigation never reaches the process.
export function useNavigationArrows(axis: 'horizontal' | 'vertical', move: (direction: -1 | 1) => boolean, priority: 'default' | 'page' = 'default') {
  const handleKeyDown = useEffectEvent((event: KeyboardEvent) => {
    if (event.defaultPrevented || event.isComposing || !event.altKey || !event.shiftKey || event.ctrlKey || event.metaKey) return
    const previous = axis === 'horizontal' ? 'ArrowLeft' : 'ArrowUp'
    const next = axis === 'horizontal' ? 'ArrowRight' : 'ArrowDown'
    if (event.key !== previous && event.key !== next) return
    if (document.querySelector('[role="dialog"][aria-modal="true"], dialog[open]')) return
    const target = event.target
    if (target instanceof HTMLElement && !target.closest('[data-terminal-handle]')) {
      if (target.isContentEditable || target.closest('input, textarea, select, [role="textbox"], [contenteditable="true"], [contenteditable=""], [contenteditable="plaintext-only"]')) return
    }
    if (!move(event.key === previous ? -1 : 1)) return
    event.preventDefault()
    event.stopImmediatePropagation()
  })

  useEffect(() => {
    // Page navigation runs before the sidebar's document capture listener.
    const target = priority === 'page' ? window : document
    target.addEventListener('keydown', handleKeyDown as EventListener, true)
    return () => target.removeEventListener('keydown', handleKeyDown as EventListener, true)
  }, [priority])
}
