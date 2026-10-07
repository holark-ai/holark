import { Check, Copy } from 'lucide-react'
import { useCallback, useEffect, useRef, useState } from 'react'
import styles from './CopyButton.module.css'

let resetActiveCopy: (() => void) | undefined

export function CopyButton({ text, label }: { text: string; label: string }) {
  const [result, setResult] = useState('')
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)
  const attempt = useRef(0)
  const reset = useCallback(() => {
    clearTimeout(timer.current)
    attempt.current += 1
    setResult('')
    if (resetActiveCopy === reset) resetActiveCopy = undefined
  }, [])

  useEffect(() => () => {
    clearTimeout(timer.current)
    attempt.current += 1
    if (resetActiveCopy === reset) resetActiveCopy = undefined
  }, [reset])

  return <button type="button" className={styles.copy} aria-label={result || label} title={result || label} onBlur={reset} onClick={async () => {
    resetActiveCopy?.()
    resetActiveCopy = reset
    const currentAttempt = ++attempt.current
    let nextResult = 'Copied'
    try { await navigator.clipboard.writeText(text) }
    catch { nextResult = 'Copy unavailable; select the text to copy' }
    if (attempt.current !== currentAttempt) return
    setResult(nextResult)
    timer.current = setTimeout(reset, 2000)
  }}>{result === 'Copied' ? <Check /> : <Copy />}</button>
}
