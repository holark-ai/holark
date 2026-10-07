import { diffLines, diffWordsWithSpace } from 'diff'
import { Fragment, useEffect, useMemo, useRef, useState, type RefObject } from 'react'
import type { Token } from 'monaco-editor'
import { monaco } from '../../components/monaco'
import { colorizeSource } from '../../components/sourceHighlighting'
import type { DiffLayout } from './HolonDiffReview'
import styles from './TextComparison.module.css'

export type ComparisonViewState = { expanded: Set<string>; scrollLeft: number }
type Props = {
  path: string
  original: string
  modified: string
  layout: DiffLayout
  onHeight: (height: number) => void
  viewState: RefObject<ComparisonViewState>
}
type Line = { text: string; number: number; changed: boolean; noNewline: boolean; marks: [number, number][] }
type Row = { before?: Line; after?: Line }
type Group = { key: string; context: boolean; rows: Row[] }

export default function TextComparison({ path, original, modified, layout, onHeight, viewState }: Props) {
  const host = useRef<HTMLDivElement>(null)
  const [narrow, setNarrow] = useState(false)
  const [expanded, setExpanded] = useState(() => new Set(viewState.current.expanded))
  const [highlighting, setHighlighting] = useState<{ original: string; modified: string; path: string; before: Token[][]; after: Token[][] }>()
  const groups = useMemo(() => compare(original, modified), [original, modified])
  const inline = layout === 'inline' || layout === 'automatic' && narrow
  const tokens = highlighting?.original === original && highlighting.modified === modified && highlighting.path === path ? highlighting : undefined

  useEffect(() => {
    const element = host.current
    if (!element) return
    element.scrollLeft = viewState.current.scrollLeft
    const observer = new ResizeObserver(() => {
      setNarrow(element.clientWidth < 600)
      onHeight(element.getBoundingClientRect().height)
    })
    observer.observe(element)
    return () => observer.disconnect()
  }, [onHeight, viewState])

  useEffect(() => {
    let cancelled = false
    const language = monaco.languages.getLanguages().find((item) => item.filenames?.includes(path.split('/').at(-1)!) || item.extensions?.some((extension) => path.endsWith(extension)))?.id ?? 'plaintext'
    // Load the tokenizer, including JSON's asynchronous registration, without an editor.
    void colorizeSource('', language).then(() => {
      if (!cancelled) setHighlighting({ original, modified, path, before: monaco.editor.tokenize(original, language), after: monaco.editor.tokenize(modified, language) })
    }).catch(() => { /* The diff remains readable without syntax colors. */ })
    return () => { cancelled = true }
  }, [path, original, modified])

  function reveal(key: string) {
    const next = new Set(expanded).add(key)
    viewState.current.expanded = next
    setExpanded(next)
  }
  function cell(line: Line | undefined, side: 'before' | 'after') {
    return <div className={`${styles.cell} ${line?.changed ? side === 'before' ? styles.removed : styles.added : ''} ${!line ? styles.blank : ''}`} data-code-side={side}>
      <span className={styles.lineNumber} aria-hidden="true">{line?.number}</span>
      <span className={styles.marker} aria-hidden="true">{line?.changed ? side === 'before' ? '−' : '+' : ' '}</span>
      <code>{line && <SourceLine line={line} tokens={tokens?.[side][line.number - 1]} />}</code>
      {line?.noNewline && <span className={styles.noNewline} title="No newline at end of file" aria-label="No newline at end of file">↵</span>}
    </div>
  }
  function rows(items: Row[], context: boolean, key: string) {
    if (inline) {
      // Keep each deletion block before its additions, as in a unified patch.
      const lines = context ? items.map((row) => ({ line: row.after, side: 'after' as const })) : [
        ...items.filter((row) => row.before).map((row) => ({ line: row.before, side: 'before' as const })),
        ...items.filter((row) => row.after).map((row) => ({ line: row.after, side: 'after' as const })),
      ]
      return lines.map(({ line, side }) => <div className={styles.row} key={`${key}:${side}:${line?.number}`}>{cell(line, side)}</div>)
    }
    return items.map((row) => <div className={styles.row} key={`${key}:${row.before?.number}:${row.after?.number}`}>{cell(row.before, 'before')}{cell(row.after, 'after')}</div>)
  }
  return <div ref={host} className={`${styles.comparison} ${inline ? styles.inline : ''}`} aria-label={`Comparison ${path}`} tabIndex={0} onScroll={(event) => { viewState.current.scrollLeft = event.currentTarget.scrollLeft }}>
    <div className={styles.code}>
      {groups.map((group, index) => {
        const leading = index === 0 ? 0 : 3
        const trailing = index === groups.length - 1 ? 0 : 3
        const hidden = group.rows.length - leading - trailing
        if (!group.context || hidden < 8 || expanded.has(group.key)) return <Fragment key={group.key}>{rows(group.rows, group.context, group.key)}</Fragment>
        return <Fragment key={group.key}>
          {rows(group.rows.slice(0, leading), true, group.key)}
          <div className={styles.context}><button type="button" onClick={() => reveal(group.key)}>Show {hidden} unchanged lines</button></div>
          {trailing > 0 && rows(group.rows.slice(-trailing), true, group.key)}
        </Fragment>
      })}
      {groups.length === 0 && <p className={styles.context}>Both files are empty.</p>}
    </div>
  </div>
}

function compare(original: string, modified: string): Group[] {
  // Bound costly comparisons; a full deletion/addition still shows every byte.
  const changes = diffLines(original, modified, { timeout: 200 }) ?? [
    { value: original, removed: true, added: false },
    { value: modified, removed: false, added: true },
  ]
  const groups: Group[] = []
  let beforeNumber = 1, afterNumber = 1
  let before: Line[] = [], after: Line[] = []
  const lines = (value: string, changed: boolean, side: 'before' | 'after') => {
    const parts = value.match(/[^\n]*\n|[^\n]+$/g) ?? []
    return parts.map((text) => ({ text: text.replace(/\r?\n$/, ''), number: side === 'before' ? beforeNumber++ : afterNumber++, changed, noNewline: !text.endsWith('\n'), marks: [] as [number, number][] }))
  }
  const flush = () => {
    if (!before.length && !after.length) return
    const rows = Array.from({ length: Math.max(before.length, after.length) }, (_, index) => ({ before: before[index], after: after[index] }))
    for (const row of rows) {
      if (!row.before || !row.after) continue
      const words = diffWordsWithSpace(row.before.text, row.after.text, { timeout: 5, maxEditLength: 200 })
      let left = 0, right = 0
      for (const word of words ?? []) {
        if (word.removed) row.before.marks.push([left, left + word.value.length])
        if (word.added) row.after.marks.push([right, right + word.value.length])
        if (!word.added) left += word.value.length
        if (!word.removed) right += word.value.length
      }
    }
    groups.push({ key: `change:${rows[0].before?.number}:${rows[0].after?.number}`, context: false, rows })
    before = []; after = []
  }
  for (const change of changes) {
    if (change.removed) before.push(...lines(change.value, true, 'before'))
    else if (change.added) after.push(...lines(change.value, true, 'after'))
    else {
      flush()
      const left = lines(change.value, false, 'before'), right = lines(change.value, false, 'after')
      if (left.length) groups.push({ key: `context:${left[0].number}:${right[0].number}`, context: true, rows: left.map((line, index) => ({ before: line, after: right[index] })) })
    }
  }
  flush()
  return groups
}

function SourceLine({ line, tokens }: { line: Line; tokens?: Token[] }) {
  const boundaries = [...new Set([0, line.text.length, ...(tokens ?? []).map((token) => token.offset), ...line.marks.flat()])].sort((a, b) => a - b)
  let tokenIndex = 0, markIndex = 0
  return <>{boundaries.slice(0, -1).map((start, index) => {
    while (tokens && tokenIndex + 1 < tokens.length && tokens[tokenIndex + 1].offset <= start) tokenIndex++
    while (markIndex < line.marks.length && line.marks[markIndex][1] <= start) markIndex++
    const marked = line.marks[markIndex]?.[0] <= start
    const type = tokens?.[tokenIndex]?.type ?? ''
    const syntax = /comment/.test(type) ? styles.comment : /string|regexp/.test(type) ? styles.string : /keyword|storage/.test(type) ? styles.keyword : /number|constant/.test(type) ? styles.number : /type|tag|class/.test(type) ? styles.type : ''
    return <span className={`${syntax} ${marked ? styles.wordChange : ''}`} key={start}>{line.text.slice(start, boundaries[index + 1])}</span>
  })}</>
}
