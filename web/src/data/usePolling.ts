import { useCallback, useEffect, useRef, useState } from 'react'

export type PollingResult<T> = {
  data: T | undefined
  error: Error | undefined
  loading: boolean
  refresh: () => Promise<void>
}

export type PollingCache<T> = {
  read: () => T | undefined
  write: (data: T) => void
}

function initialState<T>(cache: PollingCache<T> | undefined, loader: (signal: AbortSignal) => Promise<T>) {
  const data = cache?.read()
  return { cache, loader, data, error: undefined as Error | undefined, loading: data === undefined }
}

export function usePolling<T>(loader: (signal: AbortSignal) => Promise<T>, interval = 3000, cache?: PollingCache<T>, resetOnChange = false): PollingResult<T> {
  const [state, setState] = useState(() => initialState(cache, loader))
  // A different branch or repository must never display the previous one's data.
  if (state.cache !== cache || (resetOnChange && state.loader !== loader)) setState(initialState(cache, loader))
  const refreshSequence = useRef(0)
  const activeRequest = useRef<AbortController | undefined>(undefined)
  const mounted = useRef(false)
  const activeLoader = useRef(loader)

  const refresh = useCallback(async () => {
    if (!mounted.current || (resetOnChange && activeLoader.current !== loader)) return
    const sequence = ++refreshSequence.current
    activeRequest.current?.abort()
    const controller = new AbortController()
    activeRequest.current = controller
    try {
      const next = await loader(controller.signal)
      if (!mounted.current || sequence !== refreshSequence.current || controller.signal.aborted) return
      cache?.write(next)
      setState({ cache, loader, data: next, error: undefined, loading: false })
    } catch (value) {
      if (!mounted.current || sequence !== refreshSequence.current || controller.signal.aborted) return
      setState((current) => current.cache === cache ? { ...current, error: value instanceof Error ? value : new Error('Request failed') } : current)
    } finally {
      if (mounted.current && sequence === refreshSequence.current) setState((current) => current.cache === cache ? { ...current, loading: false } : current)
    }
  }, [loader, cache, resetOnChange])

  useEffect(() => {
    mounted.current = true
    activeLoader.current = loader
    let active = true
    let timer: number | undefined
    const poll = async () => {
      await refresh()
      if (active) timer = window.setTimeout(poll, interval)
    }
    timer = window.setTimeout(poll)
    return () => {
      mounted.current = false
      active = false
      activeRequest.current?.abort()
      refreshSequence.current += 1
      if (timer !== undefined) window.clearTimeout(timer)
    }
  }, [interval, loader, refresh])

  return { data: state.data, error: state.error, loading: state.loading, refresh }
}
