import type { ITheme } from '@xterm/xterm'
import { createContext, useCallback, useContext, useEffect, useLayoutEffect, useMemo, useRef, useState, useSyncExternalStore, type ReactNode } from 'react'
import { useColorTheme } from '../../app/colorTheme'
import styles from './HolonTerminal.module.css'
import { initialHandleState, TerminalHandle, type HandleOptions } from './TerminalHandle'
import { terminalThemeFromTokens } from './terminalTheme'
import type { TerminalAvailabilityProbe, TerminalSocketFactory } from './terminalProtocol'

export type { TerminalSocketFactory } from './terminalProtocol'

type WorkspaceContextValue = {
  get: (id: string, url: string, socketFactory: TerminalSocketFactory, availabilityProbe: TerminalAvailabilityProbe | undefined, options: HandleOptions) => TerminalHandle
  evict: (id: string) => void
  theme: ITheme
}

const TerminalWorkspaceContext = createContext<WorkspaceContextValue | null>(null)

// The provider and hook intentionally share one private handle context.
// eslint-disable-next-line react-refresh/only-export-components
export function useTerminalWorkspace() {
  const workspace = useContext(TerminalWorkspaceContext)
  if (!workspace) throw new Error('useTerminalWorkspace requires TerminalWorkspaceProvider')
  return workspace
}

// TerminalWorkspaceProvider is the renderer-side terminal service. Handles
// outlive individual surfaces and are evicted only with their terminal tab.
export function TerminalWorkspaceProvider({ children }: { children: ReactNode }) {
  const parkingRef = useRef<HTMLDivElement>(null)
  const handles = useRef(new Map<string, TerminalHandle>())
  const { theme: colorTheme } = useColorTheme()
  const theme = useMemo(() => terminalThemeFromTokens(colorTheme), [colorTheme])
  const themeRef = useRef(theme)
  const get = useCallback((id: string, url: string, socketFactory: TerminalSocketFactory, availabilityProbe: TerminalAvailabilityProbe | undefined, options: HandleOptions) => {
    const parking = parkingRef.current
    if (!parking) throw new Error('terminal workspace is not mounted')
    let handle = handles.current.get(id)
    if (!handle) {
      handle = new TerminalHandle(id, url, parking, socketFactory, availabilityProbe, options, themeRef.current)
      handles.current.set(id, handle)
    }
    handle.update(options, themeRef.current)
    return handle
  }, [])
  const evict = useCallback((id: string) => {
    handles.current.get(id)?.dispose()
    handles.current.delete(id)
  }, [])
  useLayoutEffect(() => {
    themeRef.current = theme
    handles.current.forEach((handle) => handle.updateTheme(theme))
  }, [theme])
  useEffect(() => () => {
    handles.current.forEach((handle) => handle.dispose())
    handles.current.clear()
  }, [])
  const value = useMemo(() => ({ get, evict, theme }), [evict, get, theme])
  return (
    <TerminalWorkspaceContext.Provider value={value}>
      <div ref={parkingRef} aria-hidden="true" data-terminal-parking style={{ position: 'fixed', width: 1, height: 1, overflow: 'hidden', visibility: 'hidden', pointerEvents: 'none' }} />
      {children}
    </TerminalWorkspaceContext.Provider>
  )
}

type TerminalSurfaceProps = {
  terminalId: string
  label: string
  url: string
  active: boolean
  activated: boolean
  socketFactory: TerminalSocketFactory
  availabilityProbe?: TerminalAvailabilityProbe
  inputEnabled: boolean
  resizeEnabled: boolean
  focusSuppressed: boolean
  focusRequest: number
  onFocusRequestHandled?: (sequence: number) => void
  onTerminalFocus?: () => void
}

// TerminalSurface presents a persistent handle; mounting and unmounting this
// component never recreates the xterm model or its transport.
export function TerminalSurface(props: TerminalSurfaceProps) {
  const workspace = useTerminalWorkspace()
  const frame = useRef<HTMLDivElement>(null)
  const [handle, setHandle] = useState<TerminalHandle>()
  const onTerminalFocusRef = useRef(props.onTerminalFocus)
  useLayoutEffect(() => {
    onTerminalFocusRef.current = props.onTerminalFocus
  }, [props.onTerminalFocus])
  const onTerminalFocus = useCallback(() => onTerminalFocusRef.current?.(), [])
  const options = useMemo<HandleOptions>(() => ({
    label: props.label,
    inputEnabled: props.inputEnabled,
    resizeEnabled: props.resizeEnabled,
    focusSuppressed: props.focusSuppressed,
    active: props.active,
    onTerminalFocus,
  }), [props.active, props.focusSuppressed, props.inputEnabled, props.label, onTerminalFocus, props.resizeEnabled])
  const optionsRef = useRef(options)
  useLayoutEffect(() => {
    optionsRef.current = options
  }, [options])
  const getHandle = workspace.get
  useLayoutEffect(() => {
    const target = frame.current
    if (!props.activated || !target) return
    const current = getHandle(props.terminalId, props.url, props.socketFactory, props.availabilityProbe, optionsRef.current)
    setHandle(current)
    current.mount(target)
    return () => current.park(target)
  }, [getHandle, props.activated, props.availabilityProbe, props.socketFactory, props.terminalId, props.url])
  useLayoutEffect(() => {
    if (!handle) return
    handle.update(options, workspace.theme)
  }, [handle, options, workspace.theme])
  const state = useHandleState(handle)
  const handledFocusRequest = useRef(0)
  const active = props.active
  const focusRequest = props.focusRequest
  const onFocusRequestHandled = props.onFocusRequestHandled
  useEffect(() => {
    if (!handle || !active || state.restoring || focusRequest <= handledFocusRequest.current) return
    handle.focus()
    handledFocusRequest.current = focusRequest
    onFocusRequestHandled?.(focusRequest)
  }, [active, focusRequest, handle, onFocusRequestHandled, state.restoring])
  return (
    <div
      className={styles.terminalFrame}
      data-active={props.active}
      data-terminal-id={props.terminalId}
      data-restoring={state.restoring}
      aria-hidden={!props.active}
      role="tabpanel"
      data-terminal-pane
      onMouseDown={(event) => {
        if (event.button !== 0 || (event.target instanceof Element && event.target.closest('.xterm'))) return
        event.preventDefault()
        handle?.focus()
      }}
    >
      <div className={styles.terminalNotices}>
        {state.message && <div className={styles.terminalHistoryNotice} role="alert">{state.message}</div>}
        {state.restorationQuality === 'degraded' && !state.degradedWarningDismissed && (
          <button type="button" className={`${styles.terminalHistoryNotice} ${styles.terminalHistoryAction}`} onClick={() => handle?.dismissDegradedWarning()}>
            Restored after a capture failure. Some earlier screen state may be missing. Click to dismiss.
          </button>
        )}
        {state.restoring && <div className={styles.terminalRestoreNotice} role="status">{state.status}</div>}
      </div>
      <div className={styles.terminalRuntime} data-presented="true" ref={frame} />
    </div>
  )
}

function useHandleState(handle?: TerminalHandle) {
  const subscribe = useCallback((listener: () => void) => handle?.subscribe(listener) ?? (() => {}), [handle])
  const snapshot = useCallback(() => handle?.snapshot() ?? initialHandleState, [handle])
  return useSyncExternalStore(subscribe, snapshot, snapshot)
}
