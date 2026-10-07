import { activeComposer, type ScopedComments } from './useCommentDrafts'
import { parseUnifiedDiff, selectDiffExcerpt } from './unifiedDiff'
import { ChevronDown, ChevronRight, Columns2, File, Folder, MessageSquare, Plus, Rows3, Search, X } from 'lucide-react'
import { type ReactNode, useCallback, useEffect, useId, useMemo, useRef, useState } from 'react'
import type { Token } from 'monaco-editor'
import { api } from '../../data/api'
import type { PullRequestComment, PullRequestCommentLocation, WorkspaceFileDiff, WorkspaceInspection } from '../../data/types'
import styles from './PullRequestChanges.module.css'

type TreeNode = { name: string, path: string, children: TreeNode[], file?: WorkspaceFileDiff }
type PatchLine = { text: string, number: number, kind: 'context' | 'added' | 'removed' }
type PatchRow = { before?: PatchLine, after?: PatchLine, note?: string }

export type DiffComments = {
  comments: PullRequestComment[]
  render: (comments: PullRequestComment[], showSavedContext?: boolean) => ReactNode
}

export function PullRequestChanges({ pullRequestId, inspection, scopedComments, initialFilePath, discussions }: { pullRequestId: string, inspection: WorkspaceInspection, scopedComments?: ScopedComments, initialFilePath?: string, discussions?: DiffComments }) {
  const [selectedPath, setSelectedPath] = useState(initialFilePath ?? inspection.files[0]?.path ?? '')
  const [query, setQuery] = useState('')
  const [searching, setSearching] = useState(false)
  const [preferredInline, setPreferredInline] = useState<boolean>()
  const [automaticInline, setAutomaticInline] = useState(false)
  const [navigation, setNavigation] = useState({ path: '', sequence: 0 })
  const [loadedFiles, setLoadedFiles] = useState<Map<string, WorkspaceFileDiff>>(new Map())
  const loaded = useRef(new Map<string, WorkspaceFileDiff>())
  const pending = useRef(new Map<string, Promise<WorkspaceFileDiff>>())
  const controller = useRef(new AbortController())
  const sections = useRef(new Map<string, HTMLElement>())
  const lastInitialPath = useRef('')
  const searchId = useId()
  const diffRef = useRef<HTMLElement>(null)
  const tree = useMemo(() => fileTree(inspection.files), [inspection.files])
  const roots = discussions?.comments.filter((comment) => !comment.parent_comment_id && (comment.scope === 'file' || comment.scope === 'line')) ?? []
  const missingFiles = roots.filter((comment) => !inspection.files.some((file) => file.path === comment.path))
  const inline = preferredInline ?? automaticInline

  useEffect(() => {
    if (controller.current.signal.aborted) controller.current = new AbortController()
    const abort = controller.current
    return () => abort.abort()
  }, [])

  useEffect(() => {
    const container = diffRef.current
    if (!container || !('ResizeObserver' in window)) return
    const observer = new ResizeObserver(() => setAutomaticInline(container.clientWidth < 660))
    observer.observe(container)
    return () => observer.disconnect()
  }, [])

  const loadFile = useCallback((path: string): Promise<WorkspaceFileDiff> => {
    const cached = loaded.current.get(path)
    if (cached) return Promise.resolve(cached)
    const original = inspection.files.find((file) => file.path === path)
    if (!original) return Promise.reject(new Error('File is no longer in this pull request.'))
    if (original.binary || original.diff && !original.diff_truncated) return Promise.resolve(original)
    const underway = pending.current.get(path)
    if (underway) return underway
    const request = api.pullRequestChanges(pullRequestId, { path, signal: controller.current.signal }).then((result) => {
      if (result.inputs.head_commit !== inspection.head_commit || result.inputs.diff_base_commit !== inspection.base_commit) throw new Error('The pull request changed. Refresh to see the latest patch.')
      const file = result.data.files.find((entry) => entry.path === path)
      if (!file) throw new Error('This file’s patch is unavailable.')
      loaded.current.set(path, file)
      setLoadedFiles(new Map(loaded.current))
      return file
    }).finally(() => pending.current.delete(path))
    pending.current.set(path, request)
    return request
  }, [inspection, pullRequestId])

  useEffect(() => {
    const term = query.trim()
    if (!term) { setSearching(false); return }
    let cancelled = false
    const paths = inspection.files.filter((file) => !file.binary && (!file.diff || file.diff_truncated) && !loaded.current.has(file.path)).map((file) => file.path)
    if (!paths.length) { setSearching(false); return }
    setSearching(true)
    let index = 0
    void Promise.all(Array.from({ length: Math.min(3, paths.length) }, async () => {
      while (!cancelled && index < paths.length) {
        const path = paths[index++]
        try { await loadFile(path) } catch { /* A file error remains visible in its section. */ }
      }
    })).finally(() => { if (!cancelled) setSearching(false) })
    return () => { cancelled = true }
  }, [query, inspection.files, loadFile])

  const matches = useMemo(() => {
    const term = query.trim().toLowerCase()
    return term ? inspection.files.filter((file) => file.path.toLowerCase().includes(term) || (loadedFiles.get(file.path)?.diff ?? file.diff ?? '').toLowerCase().includes(term)).map((file) => file.path) : []
  }, [query, inspection.files, loadedFiles])
  const matchingPaths = useMemo(() => new Set(matches), [matches])
  const jumpTo = (path: string) => {
    setSelectedPath(path)
    setNavigation((current) => ({ path, sequence: current.sequence + 1 }))
    sections.current.get(path)?.scrollIntoView?.({ block: 'start' })
    sections.current.get(path)?.querySelector<HTMLElement>('header')?.focus({ preventScroll: true })
  }
  const jumpMatch = (direction: number) => {
    if (!matches.length) return
    const index = matches.indexOf(selectedPath)
    jumpTo(matches[(index + direction + matches.length) % matches.length])
  }
  useEffect(() => {
    if (!initialFilePath || !inspection.files.some((file) => file.path === initialFilePath)) return
    if (lastInitialPath.current === initialFilePath) return
    lastInitialPath.current = initialFilePath
    const frame = requestAnimationFrame(() => jumpTo(initialFilePath))
    return () => cancelAnimationFrame(frame)
  }, [initialFilePath, inspection.files])

  if (!inspection.has_changes) return <><p className={styles.notice}>No changes.</p>{discussions?.render(roots, true)}</>

  return <div className={styles.layout}>
    <aside className={styles.files} aria-label="Changed files">
      <div className={styles.filesHeader}><strong>Changed files</strong><span>{inspection.files.length}</span></div>
      <div className={styles.fileSearch}>
        <Search aria-hidden="true" />
        <label htmlFor={searchId} className={styles.srOnly}>Search paths and changed lines</label>
        <input id={searchId} type="search" value={query} onChange={(event) => setQuery(event.target.value)} onKeyDown={(event) => { if (event.key === 'Enter') jumpMatch(1) }} placeholder="Search changed code…" />
        {query && <button type="button" aria-label="Clear search" onClick={() => setQuery('')}><X aria-hidden="true" /></button>}
      </div>
      {query && <div className={styles.searchStatus} role="status">{searching ? 'Searching all changed files…' : `${matches.length} ${matches.length === 1 ? 'matching file' : 'matching files'}`}{matches.length > 0 && <span><button type="button" aria-label="Previous match" onClick={() => jumpMatch(-1)}>↑</button><button type="button" aria-label="Next match" onClick={() => jumpMatch(1)}>↓</button></span>}</div>}
      <FileTree nodes={tree} selectedPath={selectedPath} onSelect={jumpTo} matchingPaths={matchingPaths} searching={Boolean(query)} />
    </aside>
    <section ref={diffRef} className={styles.diff} aria-label="Pull request file diffs">
      <div className={styles.fileSummary}><strong>{inspection.files.length} {inspection.files.length === 1 ? 'changed file' : 'changed files'}</strong><span><span className={styles.additions}>+{inspection.files.reduce((sum, file) => sum + file.additions, 0)}</span> <span className={styles.deletions}>−{inspection.files.reduce((sum, file) => sum + file.deletions, 0)}</span></span><button type="button" aria-label={inline ? 'Show side-by-side diff' : 'Show inline diff'} onClick={() => setPreferredInline(!inline)}>{inline ? <Columns2 /> : <Rows3 />}{inline ? 'Side by side' : 'Inline'}</button></div>
      {inspection.files.map((summary) => <FileSection key={summary.path} file={loadedFiles.get(summary.path) ?? summary} loaded={loadedFiles.has(summary.path)} inspection={inspection} inline={inline} query={query} selected={selectedPath === summary.path} navigationSequence={navigation.path === summary.path ? navigation.sequence : 0} loadFile={loadFile} scopedComments={scopedComments} discussions={discussions} roots={roots} register={(element) => { if (element) sections.current.set(summary.path, element); else sections.current.delete(summary.path) }} />)}
      {missingFiles.length > 0 && <section className={styles.unplacedThreads} aria-label="Comments on other files"><h3>Comments on other files</h3>{discussions?.render(missingFiles, true)}</section>}
    </section>
  </div>
}

function FileSection({ file, loaded, inspection, inline, query, selected, navigationSequence, loadFile, scopedComments, discussions, roots, register }: { file: WorkspaceFileDiff, loaded: boolean, inspection: WorkspaceInspection, inline: boolean, query: string, selected: boolean, navigationSequence: number, loadFile: (path: string) => Promise<WorkspaceFileDiff>, scopedComments?: ScopedComments, discussions?: DiffComments, roots: PullRequestComment[], register: (element: HTMLElement | null) => void }) {
  const section = useRef<HTMLElement>(null)
  const [near, setNear] = useState(false)
  const [collapsed, setCollapsed] = useState(false)
  const [error, setError] = useState('')
  const [retry, setRetry] = useState(0)
  const needsPatch = !file.binary && !loaded && (!file.diff || file.diff_truncated)
  const location: PullRequestCommentLocation = { scope: 'file', path: file.path, ...(file.old_path ? { old_path: file.old_path } : {}) }
  const fileComments = roots.filter((comment) => comment.path === file.path)
  const lineAnchors = useMemo(() => anchorComments(file, inspection, discussions?.comments ?? []), [file, inspection, discussions?.comments])
  const anchoredIds = new Set([...lineAnchors.values()].flat().map((comment) => comment.id))
  const unplaced = fileComments.filter((comment) => comment.scope === 'line' && !anchoredIds.has(comment.id))

  useEffect(() => {
    const element = section.current
    if (!element) return
    if (!('IntersectionObserver' in window)) { setNear(true); return }
    const observer = new IntersectionObserver(([entry]) => setNear(entry.isIntersecting), { rootMargin: '700px 0px' })
    observer.observe(element)
    return () => observer.disconnect()
  }, [])
  useEffect(() => {
    if (!near || collapsed || !needsPatch) return
    let cancelled = false
    void loadFile(file.path).then(() => { if (!cancelled) setError('') }).catch((value: unknown) => { if (!cancelled) setError(value instanceof Error ? value.message : 'Could not load this patch.') })
    return () => { cancelled = true }
  }, [near, collapsed, needsPatch, file.path, loadFile, retry])
  useEffect(() => {
    if (!navigationSequence || !query.trim() || !selected || needsPatch) return
    const frame = requestAnimationFrame(() => section.current?.querySelector<HTMLElement>('[data-search-hit="true"]')?.scrollIntoView?.({ block: 'center' }))
    return () => cancelAnimationFrame(frame)
  }, [navigationSequence, query, selected, needsPatch, file.diff])

  return <article ref={(element) => { section.current = element; register(element) }} className={styles.fileSection} data-diff-path={file.path} tabIndex={-1} aria-label={`Review ${file.path}`}>
    <header className={styles.fileHeading} tabIndex={-1}>
      <button type="button" aria-label={collapsed ? 'Expand diff' : 'Collapse diff'} aria-expanded={!collapsed} onClick={() => setCollapsed(!collapsed)}>{collapsed ? <ChevronRight /> : <ChevronDown />}</button>
      <File /><h2>{file.old_path && file.old_path !== file.path ? `${file.old_path} → ${file.path}` : file.path}</h2>
      <span className={styles.stats}><span className={styles.additions}>+{file.additions}</span> <span className={styles.deletions}>−{file.deletions}</span></span>
      {selected && scopedComments && <button type="button" aria-label={`Comment on file ${file.path}`} onClick={() => scopedComments.select(location)}><MessageSquare aria-hidden="true" />Comment on file</button>}
    </header>
    {!collapsed && <div className={styles.fileBody}>
      {fileComments.some((comment) => comment.scope === 'file') && <section className={styles.fileThreads} aria-label={`File comments for ${file.path}`}>{discussions?.render(fileComments.filter((comment) => comment.scope === 'file'))}</section>}
      {selected && activeComposer(location, scopedComments)}
      {error && <p className={styles.notice} role="alert">{error} <button type="button" onClick={() => { setError(''); setRetry((current) => current + 1) }}>Retry</button></p>}
      {file.binary ? <p className={styles.notice}>Binary file.</p> : !near && !selected ? <div className={styles.patchPlaceholder} /> : needsPatch && !error ? <p className={styles.notice}>Loading full patch…</p> : file.diff ? <Patch file={file} inline={inline} searchTerm={query.trim().toLowerCase()} truncated={file.diff_truncated} scopedComments={selected ? scopedComments : undefined} lineAnchors={lineAnchors} discussions={discussions} /> : !error && <p className={styles.notice}>No text diff available.</p>}
      {unplaced.length > 0 && <section className={styles.unplacedThreads} aria-label={`Comments outside the diff for ${file.path}`}><h3>Comments outside the current diff</h3>{discussions?.render(unplaced, true)}</section>}
      {file.diff_truncated && loaded && <p className={styles.truncationNotice}>This file’s patch exceeds the display limit.</p>}
    </div>}
  </article>
}

function FileTree({ nodes, selectedPath, onSelect, matchingPaths, searching }: { nodes: TreeNode[], selectedPath?: string, onSelect: (path: string) => void, matchingPaths: Set<string>, searching: boolean }) {
  return <div className={styles.tree}>
    {nodes.map((node) => node.file ? (
      <button className={styles.treeFile} data-match={searching && matchingPaths.has(node.path) ? 'true' : undefined} type="button" key={node.path} aria-current={node.path === selectedPath ? 'true' : undefined} title={node.path} onClick={() => onSelect(node.path)}>
        <File /><span>{node.name}</span><small className={node.file.status === 'added' || node.file.status === 'A' ? styles.additions : undefined}>{fileStatus(node.file.status)}</small>
      </button>
    ) : (
      <details open key={node.path} className={styles.directory}>
        <summary><ChevronRight /><Folder /><span title={node.name}>{node.name}</span></summary>
        <FileTree nodes={node.children} selectedPath={selectedPath} onSelect={onSelect} matchingPaths={matchingPaths} searching={searching} />
      </details>
    ))}
  </div>
}

function fileTree(files: WorkspaceFileDiff[]) {
  const root: TreeNode = { name: '', path: '', children: [] }
  for (const file of files) {
    const parts = file.path.split('/')
    let current = root
    parts.forEach((name, index) => {
      const path = parts.slice(0, index + 1).join('/')
      let child = current.children.find((node) => node.path === path)
      if (!child) {
        child = { name, path, children: [] }
        current.children.push(child)
      }
      if (index === parts.length - 1) child.file = file
      current = child
    })
  }
  const compact = (nodes: TreeNode[]): TreeNode[] => nodes.map((node) => {
    while (!node.file && node.children.length === 1 && !node.children[0].file) {
      const child = node.children[0]
      node = { ...child, name: `${node.name}/${child.name}` }
    }
    return { ...node, children: compact(node.children) }
  }).sort((a, b) => Number(Boolean(a.file)) - Number(Boolean(b.file)) || a.name.localeCompare(b.name))
  return compact(root.children)
}

function fileStatus(status: string) {
  if (status === 'removed' || status === 'deleted') return 'D'
  return status.slice(0, 1).toUpperCase()
}

// Keep comments from earlier revisions separate. For legacy comments without
// a revision, require matching saved code as well as an unambiguous line number.
function anchorComments(file: WorkspaceFileDiff, inspection: WorkspaceInspection, comments: PullRequestComment[]) {
  const rows = parseUnifiedDiff(file.diff ?? '', file.diff_truncated)
  const result = new Map<string, PullRequestComment[]>()
  for (const comment of comments) {
    if (comment.parent_comment_id || comment.scope !== 'line' || comment.path !== file.path || file.binary) continue
    if ((comment.side !== 'LEFT' && comment.side !== 'RIGHT') || !comment.line) continue
    if (comment.original_head_commit && comment.original_head_commit !== inspection.head_commit) continue
    const matches = rows.filter((row) => row.hunk !== undefined && (comment.side === 'LEFT' ? row.oldLine : row.newLine) === comment.line)
    if (matches.length !== 1) continue
    const saved = selectDiffExcerpt(comment.diff_hunk, comment.side, comment.line).find((row) => row.commented)
    if (saved ? saved.text !== matches[0].text : comment.original_head_commit !== inspection.head_commit) continue
    const key = `${comment.side}:${comment.line}`
    result.set(key, [...(result.get(key) ?? []), comment])
  }
  return result
}

function Patch({ file, inline, searchTerm, truncated, scopedComments, lineAnchors, discussions }: { file: WorkspaceFileDiff, inline: boolean, searchTerm: string, truncated?: boolean, scopedComments?: ScopedComments, lineAnchors: Map<string, PullRequestComment[]>, discussions?: DiffComments }) {
  const rows = useMemo(() => parsePatch(file.diff ?? ''), [file.diff])
  const [highlighting, setHighlighting] = useState<{ rows: PatchRow[], tokens: Map<PatchLine, Token[]> }>()
  useEffect(() => {
    let cancelled = false
    // Load the existing syntax engine only when a text diff is opened.
    void import('monaco-editor').then(async (monaco) => {
      const name = file.path.split('/').pop() ?? ''
      const extension = name.includes('.') ? `.${name.split('.').pop()}` : ''
      const language = monaco.languages.getLanguages().find((item) => item.filenames?.includes(name) || item.extensions?.includes(extension))?.id ?? 'plaintext'
      const tokens = new Map<PatchLine, Token[]>()
      // Each hunk is partial source. Tokenize its before/after independently so
      // removed text cannot affect highlighting of added text.
      let before: PatchLine[] = []
      let after: PatchLine[] = []
      const groups: PatchLine[][] = []
      for (const row of rows) {
        if (row.note) {
          groups.push(before, after)
          before = []
          after = []
        }
        if (row.before) before.push(row.before)
        if (row.after) after.push(row.after)
      }
      groups.push(before, after)
      await Promise.all(groups.filter((lines) => lines.length > 0).map(async (lines) => {
        const source = lines.map((line) => line.text).join('\n')
        await monaco.editor.colorize(source, language, {})
        const highlighted = monaco.editor.tokenize(source, language)
        lines.forEach((line, index) => tokens.set(line, highlighted[index] ?? []))
      }))
      if (!cancelled) setHighlighting({ rows, tokens })
    }).catch(() => { /* Plain code remains readable if highlighting is unavailable. */ })
    return () => { cancelled = true }
  }, [file.path, rows])

  const locations = useMemo(() => {
    const result = new Map<string, PullRequestCommentLocation>()
    for (const row of parseUnifiedDiff(file.diff ?? '', truncated)) {
      const side = row.kind === 'removed' ? 'LEFT' : 'RIGHT'
      const line = side === 'LEFT' ? row.oldLine : row.newLine
      if (line && row.hunk !== undefined) result.set(`${side}:${line}`, {
        scope: 'line', path: file.path, ...(file.old_path ? { old_path: file.old_path } : {}), side, line, diff_hunk: row.hunk,
      })
    }
    return result
  }, [file.diff, file.path, file.old_path, truncated])
  const locationFor = (line: PatchLine | undefined, side: 'before' | 'after') => line ? locations.get(`${side === 'before' ? 'LEFT' : 'RIGHT'}:${line.number}`) : undefined
  const action = (line: PatchLine | undefined, side: 'before' | 'after') => {
    const location = locationFor(line, side)
    return scopedComments && <span className={styles.lineAction}>{location && <button type="button" aria-label={`Comment on ${file.path} ${location.side} line ${location.line}`} onClick={() => scopedComments.select(location)}><Plus aria-hidden="true" /></button>}</span>
  }
  const composer = (line: PatchLine | undefined, side: 'before' | 'after') => {
    const location = locationFor(line, side)
    return location && activeComposer(location, scopedComments)
  }
  const thread = (line: PatchLine | undefined, side: 'before' | 'after') => {
    const comments = line && lineAnchors.get(`${side === 'before' ? 'LEFT' : 'RIGHT'}:${line.number}`)
    const draft = composer(line, side)
    if (!comments?.length && !draft) return null
    return <div className={styles.lineThreads} data-diff-side={side === 'before' ? 'LEFT' : 'RIGHT'} data-diff-line={line?.number}>
      {comments && discussions?.render(comments)}{draft}
    </div>
  }
  const tokens = highlighting?.rows === rows ? highlighting.tokens : undefined
  const cell = (line: PatchLine | undefined, side: 'before' | 'after', contextOldLine?: number) => <div data-code-side={side} data-search-hit={searchTerm && line?.text.toLowerCase().includes(searchTerm) ? 'true' : undefined} className={`${styles.cell} ${line ? styles[line.kind] : styles.blank}`}>
    {inline ? <><span className={styles.lineNumber}>{side === 'before' ? line?.number : line?.kind === 'context' ? contextOldLine : ''}</span><span className={styles.lineNumber}>{side === 'after' ? line?.number : ''}</span></> : <span className={styles.lineNumber}>{line?.number}</span>}
    {action(line, side)}
    <span className={styles.marker}>{line?.kind === 'added' ? '+' : line?.kind === 'removed' ? '−' : ' '}</span>
    <code>{line ? <HighlightedLine line={line} tokens={tokens?.get(line)} /> : ' '}</code>
  </div>

  if (rows.length === 0) return <pre className={styles.rawPatch}>{file.diff}</pre>
  return <div className={inline ? styles.inline : styles.split}>
    <div className={styles.labels}>{!inline && <span>Before</span>}<span>{inline ? 'Before / After' : 'After'}</span></div>
    <div className={styles.codeViewport}><div className={styles.code}>
      {rows.map((row, index) => row.note ? <div className={styles.hunk} key={index}>{row.note}</div> : (
        <div className={styles.row} data-context={row.after?.kind === 'context' || undefined} key={index}>
          {cell(row.before, 'before')}
          <div className={styles.beforeThreads}>{thread(row.before, 'before')}</div>
          {cell(row.after, 'after', row.before?.number)}
          <div className={styles.afterThreads}>{thread(row.after, 'after')}</div>
        </div>
      ))}
    </div></div>
  </div>
}

function HighlightedLine({ line, tokens }: { line: PatchLine, tokens?: Token[] }) {
  if (!tokens?.length) return line.text || ' '
  return <>{tokens.map((token, index) => <span className={tokenClass(token.type)} key={index}>{line.text.slice(token.offset, tokens[index + 1]?.offset)}</span>)}</>
}

function tokenClass(type: string) {
  if (/comment/.test(type)) return styles.syntaxComment
  if (/string|regexp/.test(type)) return styles.syntaxString
  if (/keyword|storage/.test(type)) return styles.syntaxKeyword
  if (/number|constant/.test(type)) return styles.syntaxNumber
  if (/type|tag|class/.test(type)) return styles.syntaxType
  return undefined
}

function parsePatch(diff: string): PatchRow[] {
  const rows: PatchRow[] = []
  let oldLine = 0
  let newLine = 0
  let inHunk = false
  let removed: PatchLine[] = []
  let added: PatchLine[] = []
  const flush = () => {
    for (let index = 0; index < Math.max(removed.length, added.length); index++) rows.push({ before: removed[index], after: added[index] })
    removed = []
    added = []
  }
  for (const line of diff.split('\n')) {
    const hunk = /^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@/.exec(line)
    if (hunk) {
      flush()
      oldLine = Number(hunk[1])
      newLine = Number(hunk[2])
      inHunk = true
      rows.push({ note: line })
    } else if (inHunk && line.startsWith('-')) removed.push({ text: line.slice(1), number: oldLine++, kind: 'removed' })
    else if (inHunk && line.startsWith('+')) added.push({ text: line.slice(1), number: newLine++, kind: 'added' })
    else if (inHunk && line.startsWith(' ')) {
      flush()
      rows.push({ before: { text: line.slice(1), number: oldLine++, kind: 'context' }, after: { text: line.slice(1), number: newLine++, kind: 'context' } })
    } else if (inHunk && line.startsWith('\\')) {
      flush()
      rows.push({ note: line })
    }
  }
  flush()
  return rows
}
