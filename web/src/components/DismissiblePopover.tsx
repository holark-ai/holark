import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
  type RefObject,
} from 'react'

type DismissiblePopoverGroupValue = {
  openPopoverId: string | null
  setOpenPopoverId: React.Dispatch<React.SetStateAction<string | null>>
}

const DismissiblePopoverContext = createContext<DismissiblePopoverGroupValue | null>(null)

export function DismissiblePopoverGroup({ children }: { children: ReactNode }) {
  const [openPopoverId, setOpenPopoverId] = useState<string | null>(null)
  const value = useMemo(() => ({ openPopoverId, setOpenPopoverId }), [openPopoverId])

  return <DismissiblePopoverContext.Provider value={value}>{children}</DismissiblePopoverContext.Provider>
}

// eslint-disable-next-line react-refresh/only-export-components
export function useDismissiblePopover(id: string, returnFocusRef?: RefObject<HTMLElement | null>) {
  const popover = useOptionalDismissiblePopover(id, returnFocusRef)
  if (!popover) throw new Error('useDismissiblePopover must be used within DismissiblePopoverGroup')
  return popover
}

// eslint-disable-next-line react-refresh/only-export-components
export function useOptionalDismissiblePopover(id: string, returnFocusRef?: RefObject<HTMLElement | null>) {
  const group = useContext(DismissiblePopoverContext)
  const openPopoverId = group?.openPopoverId
  const setOpenPopoverId = group?.setOpenPopoverId
  const rootRef = useRef<HTMLDivElement>(null)
  const triggerRef = useRef<HTMLButtonElement>(null)
  const open = Boolean(group && openPopoverId === id)

  const close = useCallback(() => {
    setOpenPopoverId?.((current) => current === id ? null : current)
  }, [id, setOpenPopoverId])

  const toggle = useCallback(() => {
    setOpenPopoverId?.((current) => current === id ? null : id)
  }, [id, setOpenPopoverId])

  const show = useCallback(() => setOpenPopoverId?.(id), [id, setOpenPopoverId])

  useEffect(() => {
    if (!open) return undefined

    const onPointerDown = (event: PointerEvent) => {
      if (event.target instanceof Node && !rootRef.current?.contains(event.target)) close()
    }
    const onFocusIn = (event: FocusEvent) => {
      if (event.target instanceof Node && !rootRef.current?.contains(event.target)) close()
    }
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key !== 'Escape') return
      event.preventDefault()
      event.stopPropagation()
      close()
      const target = returnFocusRef?.current ?? triggerRef.current
      target?.focus()
    }

    document.addEventListener('pointerdown', onPointerDown, true)
    document.addEventListener('focusin', onFocusIn, true)
    document.addEventListener('keydown', onKeyDown, true)
    return () => {
      document.removeEventListener('pointerdown', onPointerDown, true)
      document.removeEventListener('focusin', onFocusIn, true)
      document.removeEventListener('keydown', onKeyDown, true)
    }
  }, [close, open, returnFocusRef])

  return group ? { open, rootRef, triggerRef, toggle, close, show } : null
}
