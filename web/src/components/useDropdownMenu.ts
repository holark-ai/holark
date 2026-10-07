import { useEffect, useRef, useState } from 'react'

export function useDropdownMenu(closedValue: string | boolean, dismissOnEscape = false) {
  const [openValue, setOpenValue] = useState<string | boolean>(closedValue)
  const rootRef = useRef<HTMLDivElement>(null)
  const open = openValue !== closedValue
  const close = () => setOpenValue(closedValue)
  const toggle = (value: string | boolean) => setOpenValue((current) => current === value ? closedValue : value)

  useEffect(() => {
    if (!open) return undefined
    const dismissOutside = (event: PointerEvent) => {
      if (!rootRef.current?.contains(event.target as Node)) setOpenValue(closedValue)
    }
    const dismissEscape = (event: KeyboardEvent) => {
      if (dismissOnEscape && event.key === 'Escape') setOpenValue(closedValue)
    }
    document.addEventListener('pointerdown', dismissOutside)
    if (dismissOnEscape) document.addEventListener('keydown', dismissEscape)
    return () => {
      document.removeEventListener('pointerdown', dismissOutside)
      if (dismissOnEscape) document.removeEventListener('keydown', dismissEscape)
    }
  }, [closedValue, dismissOnEscape, open])

  return { open, openValue, rootRef, close, toggle }
}
