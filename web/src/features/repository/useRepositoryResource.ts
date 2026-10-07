import { useEffect, useState } from 'react'

// Repository contents are immutable at a commit. Reload only when the selection changes.
export function useRepositoryResource<T>(load: (signal: AbortSignal) => Promise<T>) {
  const [result, setResult] = useState<{ load: typeof load; data?: T; error?: Error }>()
  useEffect(() => {
    const controller = new AbortController()
    void load(controller.signal).then(
      (data) => { if (!controller.signal.aborted) setResult({ load, data }) },
      (error: Error) => { if (!controller.signal.aborted) setResult({ load, error }) },
    )
    return () => controller.abort()
  }, [load])
  return result?.load === load ? result : undefined
}
