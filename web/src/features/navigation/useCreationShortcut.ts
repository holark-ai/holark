import { useEffect, useEffectEvent } from 'react'

export function useCreationShortcut(code: 'KeyN' | 'KeyT', create: () => void) {
  const handleKeyDown = useEffectEvent((event: KeyboardEvent) => {
    if (event.defaultPrevented || event.isComposing || event.code !== code || !event.shiftKey || !event.altKey || event.ctrlKey || event.metaKey) return
    if (document.querySelector('[role="dialog"][aria-modal="true"], dialog[open]')) return
    // Capture before xterm so the shortcut is never sent to the process.
    event.preventDefault()
    event.stopImmediatePropagation()
    if (!event.repeat) create()
  })

  useEffect(() => {
    document.addEventListener('keydown', handleKeyDown, true)
    return () => document.removeEventListener('keydown', handleKeyDown, true)
  }, [])
}
