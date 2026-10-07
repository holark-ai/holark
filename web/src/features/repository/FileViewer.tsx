import { useEffect, useRef } from 'react'
import { monaco } from '../../components/monaco'
import { colorizeSource } from '../../components/sourceHighlighting'
import { useColorTheme } from '../../app/colorTheme'
import styles from './FileViewer.module.css'

type FileViewerProps = { content: string; language: string }

export default function FileViewer({ content, language }: FileViewerProps) {
  const { theme } = useColorTheme()
  const source = useRef<HTMLElement>(null)
  const syntaxTheme = theme === 'dark' ? 'vs-dark' : 'holark-light'

  useEffect(() => {
    const element = source.current
    if (!element) return
    let cancelled = false
    element.textContent = content
    monaco.editor.setTheme(syntaxTheme)
    // Use Monaco only for highlighting; native text selection needs no editor.
    void colorizeSource(content, language).then((html) => {
      if (!cancelled) element.innerHTML = html
    }).catch(() => { /* Keep plain text readable if highlighting fails. */ })
    return () => { cancelled = true }
  }, [content, language, syntaxTheme])

  return (
    <div className={styles.viewer} aria-label="Source code" data-language={language} data-theme={syntaxTheme} tabIndex={0}>
      <pre className={styles.lineNumbers} aria-hidden="true">{content.split(/\r\n|\r|\n/).map((_, index) => index + 1).join('\n')}</pre>
      <pre className={styles.content}><code ref={source}>{content}</code></pre>
    </div>
  )
}
