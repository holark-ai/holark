import { ChevronDown, ChevronRight } from 'lucide-react'
import { Component, lazy, Suspense, useEffect, useRef, useState, type ReactNode } from 'react'
import type { ComparisonViewState } from './TextComparison'
import type { ContentLoader } from './workspaceContents'
import type { WorkspaceFileDiff, WorkspaceInspection } from '../../data/types'
import styles from './HolonDiffPanel.module.css'

const TextComparison = lazy(() => import('./TextComparison'))
export type ReviewRange = { base: string; target: string; label: string; inspection: WorkspaceInspection }
export type DiffLayout = 'automatic' | 'side-by-side' | 'inline'

type Props = {
  holonId: string
  load: ContentLoader
  provided?: boolean
  inspection: Pick<WorkspaceInspection, 'files'>
  base: string
  target: string
  layout: DiffLayout
  scrollMode?: 'panel' | 'page'
  fileAction?: (file: WorkspaceFileDiff) => ReactNode
  ranges?: ReviewRange[]
  navigation: { path: string; sequence: number }
  collapsedFiles: Set<string>
  onFileCollapsedChange: (path: string, collapsed: boolean) => void
}

export function HolonDiffReview({ holonId, provided, inspection, base, target, layout, navigation, load, ranges, fileAction, collapsedFiles, onFileCollapsedChange, scrollMode = 'panel' }: Props) {
  const scroll = useRef<HTMLDivElement>(null)
  const content = useRef<HTMLDivElement>(null)
  const anchor = useRef<{ path: string; offset: number } | undefined>(undefined)
  const navigating = useRef(false)
  const adjusting = useRef(false)
  const rememberPosition = () => {
    const root = scroll.current
    if (scrollMode === 'page' || !root || navigating.current || adjusting.current) return
    const top = root.getBoundingClientRect().top
    const sections = root.querySelectorAll<HTMLElement>('[data-diff-path]')
    const visible = Array.from(sections).find((section) => section.getBoundingClientRect().bottom > top)
    if (visible) anchor.current = { path: visible.dataset.diffPath!, offset: visible.getBoundingClientRect().top - top }
  }
  useEffect(() => {
    if (navigation.sequence) {
      anchor.current = { path: navigation.path, offset: 0 }
      navigating.current = true
    }
  }, [navigation])
  useEffect(() => {
    const root = scroll.current, element = content.current
    if (scrollMode === 'page' || !root || !element) return
    let frame = 0
    const observer = new ResizeObserver(() => {
      const position = anchor.current
      if (!position) return
      const section = Array.from(element.querySelectorAll<HTMLElement>('[data-diff-path]')).find((item) => item.dataset.diffPath === position.path)
      if (!section) return
      const delta = section.getBoundingClientRect().top - root.getBoundingClientRect().top - position.offset
      if (Math.abs(delta) < 1) return
      adjusting.current = true
      root.scrollTop += delta
      cancelAnimationFrame(frame)
      frame = requestAnimationFrame(() => { adjusting.current = false })
    })
    observer.observe(element)
    return () => { observer.disconnect(); cancelAnimationFrame(frame) }
  }, [scrollMode])
  // Resolved commits can change on a background refresh without resetting
  // section collapse/scroll state. Requests are cancelled by their section.
  return <div ref={scroll} className={`${styles.reviewBody} ${scrollMode === 'page' ? styles.pageReviewBody : ''}`} onScroll={rememberPosition}
    onWheel={() => { navigating.current = false }}
    onTouchMove={() => { navigating.current = false }}
    onPointerDown={() => { navigating.current = false; rememberPosition() }}
    onKeyDown={() => { navigating.current = false; rememberPosition() }}>
    <div ref={content}>
    {inspection.files.map((file) => {
      const parts = ranges?.filter((range) => range.inspection.files.some((entry) => entry.path === file.path))
      if (ranges && ranges.length > 1 && parts?.length) return <RangeComparisons key={file.path} scrollMode={scrollMode} fileAction={fileAction} file={file} ranges={parts} holonId={holonId} provided={provided} base={base} target={target} layout={layout} navigation={navigation} scroll={scroll} load={load} collapsed={collapsedFiles.has(file.path)} onFileCollapsedChange={onFileCollapsedChange} />
      const range = parts?.[0]
      return <FileComparison key={file.path} scrollMode={scrollMode} fileAction={fileAction} file={file} provided={provided} holonId={holonId} base={range?.base ?? base} target={range?.target ?? target} refresh={(range?.target ?? target) === 'worktree' ? inspection : undefined} layout={layout} navigation={navigation} scroll={scroll} load={load} collapsed={collapsedFiles.has(file.path)} onFileCollapsedChange={onFileCollapsedChange} />
    })}
    </div>
  </div>
}

type FileProps = Omit<Props, 'inspection' | 'collapsedFiles'> & {
  file: WorkspaceFileDiff
  collapsed: boolean
  rangeLabel?: string
  refresh?: Pick<WorkspaceInspection, 'files'>
  scroll: React.RefObject<HTMLDivElement | null>
}

function RangeComparisons({ file, ranges, navigation, scroll, collapsed, onFileCollapsedChange, ...props }: FileProps & { ranges: ReviewRange[] }) {
  const section = useRef<HTMLElement>(null)
  useEffect(() => {
    if (!navigation.sequence || navigation.path !== file.path) return
    let frame = requestAnimationFrame(() => {
      onFileCollapsedChange(file.path, false)
      frame = requestAnimationFrame(() => {
      if (section.current && scroll.current) {
        if (props.scrollMode === 'page') section.current.scrollIntoView({ block: 'start' })
        else scroll.current.scrollTop += section.current.getBoundingClientRect().top - scroll.current.getBoundingClientRect().top
        section.current.focus({ preventScroll: true })
      }
      })
    })
    return () => cancelAnimationFrame(frame)
  }, [navigation, file.path, scroll, onFileCollapsedChange, props.scrollMode])
  return <section ref={section} data-diff-path={file.path} className={styles.fileSection} role="region" aria-label={`Review ${file.path}`} tabIndex={-1}>
    <header className={styles.fileHeader}>
      <button className={styles.fileDisclosure} aria-label={`${collapsed ? 'Expand' : 'Collapse'} diff`} aria-expanded={!collapsed} onClick={() => onFileCollapsedChange(file.path, !collapsed)}>{collapsed ? <ChevronRight /> : <ChevronDown />}</button>
      <span className={styles.status}>{file.status.slice(0, 1).toUpperCase()}</span><h3>{file.path}</h3>
      <span className={styles.additions}>+{file.additions}</span><span className={styles.deletions}>−{file.deletions}</span>
    </header>
    {!collapsed && ranges.map((range) => <FileComparison {...props} key={`${range.base}:${range.target}`} rangeLabel={range.label} file={range.inspection.files.find((entry) => entry.path === file.path)!} base={range.base} target={range.target} refresh={range.target === 'worktree' ? range.inspection : undefined} navigation={{ path: '', sequence: 0 }} scroll={scroll} collapsed={false} onFileCollapsedChange={onFileCollapsedChange} />)}
  </section>
}

function FileComparison({ file, provided, holonId, base, target, refresh, layout, navigation, scroll, load, rangeLabel, collapsed, onFileCollapsedChange, fileAction, scrollMode }: FileProps) {
  const section = useRef<HTMLElement>(null)
  const body = useRef<HTMLDivElement>(null)
  const viewState = useRef<ComparisonViewState>({ expanded: new Set(), scrollLeft: 0 })
  const refreshContents = useRef<(() => void) | undefined>(undefined)
  const [near, setNear] = useState(false)
  const [height, setHeight] = useState(180)
  const [result, setResult] = useState<{ base: string; target: string; file: WorkspaceFileDiff }>()
  const [error, setError] = useState('')
  const [retry, setRetry] = useState(0)
  const providedFile = provided ? file : undefined
  const data = result?.base === base && result?.target === target ? result.file : undefined

  useEffect(() => {
    const element = section.current, root = scroll.current
    if (!element || !root) return
    if (!('IntersectionObserver' in window)) { setNear(true); return }
    let observer: IntersectionObserver
    const observe = () => {
      observer?.disconnect()
      observer = new IntersectionObserver(([entry]) => setNear(entry.isIntersecting), { root: scrollMode === 'page' ? null : root, rootMargin: `${scrollMode === 'page' ? window.innerHeight : root.clientHeight}px 0px` })
      observer.observe(element)
    }
    const resize = new ResizeObserver(observe)
    resize.observe(scrollMode === 'page' ? document.documentElement : root)
    observe()
    return () => { observer.disconnect(); resize.disconnect() }
  }, [scroll, scrollMode])

  useEffect(() => {
    if (!near || collapsed || !body.current) return
    const observer = new ResizeObserver(() => {
      if (body.current) setHeight(Math.ceil(body.current.getBoundingClientRect().height))
    })
    observer.observe(body.current)
    return () => observer.disconnect()
  }, [near, collapsed])

  useEffect(() => {
    if (!navigation.sequence || navigation.path !== file.path) return
    onFileCollapsedChange(file.path, false)
    setNear(true)
    const frame = requestAnimationFrame(() => {
      const element = section.current, root = scroll.current
      if (!element || !root) return
      if (scrollMode === 'page') element.scrollIntoView({ block: 'start' })
      else root.scrollTop += element.getBoundingClientRect().top - root.getBoundingClientRect().top
      element.focus({ preventScroll: true })
    })
    return () => cancelAnimationFrame(frame)
  }, [navigation, file.path, scroll, onFileCollapsedChange, scrollMode])

  useEffect(() => {
    if (!near || collapsed) return
    const controller = new AbortController()
    let loading = false
    let pending = false
    const request = () => {
      // Preserve this file's place in the shared queue and coalesce polls into
      // one follow-up load, enqueued after the current request finishes.
      if (loading) { pending = true; return }
      loading = true
      const result = providedFile ? Promise.resolve(providedFile) : load(holonId, base, target, file.path, controller.signal)
      result.then((next) => {
        if (!controller.signal.aborted) { setResult({ base, target, file: next }); setError('') }
      }).catch((value: unknown) => {
        if (!controller.signal.aborted) setError(value instanceof Error ? value.message : 'Could not load this comparison.')
      }).finally(() => {
        loading = false
        if (pending && !controller.signal.aborted) { pending = false; request() }
      })
    }
    refreshContents.current = request
    return () => { refreshContents.current = undefined; controller.abort() }
  }, [base, target, holonId, file.path, providedFile, near, collapsed, load])

  useEffect(() => {
    refreshContents.current?.()
  }, [base, target, holonId, file.path, providedFile, near, collapsed, load, refresh, retry])

  const status = data?.content_status ?? (data?.binary ? 'binary' : 'unsupported')
  return <section ref={section} data-diff-path={rangeLabel ? undefined : file.path} className={styles.fileSection} role="region" aria-label={rangeLabel ? `${rangeLabel}: ${file.path}` : `Review ${file.path}`} tabIndex={-1}>
    {rangeLabel ? <div className={styles.rangeLabel}>{rangeLabel}</div> : <header className={styles.fileHeader}>
      <button className={styles.fileDisclosure} aria-label={`${collapsed ? 'Expand' : 'Collapse'} diff`} aria-expanded={!collapsed} onClick={() => onFileCollapsedChange(file.path, !collapsed)}>{collapsed ? <ChevronRight /> : <ChevronDown />}</button>
      <span className={styles.status} title={file.status}>{file.status === 'removed' ? 'D' : file.status.slice(0, 1).toUpperCase()}</span>
      <h3 title={file.old_path ? `${file.old_path} → ${file.path}` : file.path}>{file.old_path ? `${file.old_path} → ${file.path}` : file.path}</h3>
      <span className={styles.additions}>+{file.additions}</span><span className={styles.deletions}>-{file.deletions}</span>
      {fileAction?.(file)}
    </header>}
    {!collapsed && <div ref={body} style={{ minHeight: near ? undefined : height }}>
      {near && <>
        {error && <p className={styles.message} role="alert">{error} <button onClick={() => setRetry((current) => current + 1)}>Retry</button></p>}
        {!data && !error && <p className={styles.message}>Loading comparison…</p>}
        {data?.contents && status === 'ready' ? <ComparisonBoundary><Suspense fallback={<div style={{ height }}>Loading comparison…</div>}><TextComparison path={file.path} original={data.contents.original} modified={data.contents.modified} layout={layout} onHeight={setHeight} viewState={viewState} /></Suspense></ComparisonBoundary> : data && <div className={styles.fallback}>
          <p>{status === 'binary' ? 'Binary file. Text comparison is unavailable.' : status === 'too_large' ? 'This file exceeds the 256 KiB per-side comparison limit.' : 'Complete text comparison is unavailable for this file type.'}</p>
          {status !== 'binary' && data.diff && <details><summary>Bounded patch preview{data.diff_truncated ? ' (truncated)' : ''}</summary><pre>{data.diff}</pre></details>}
        </div>}
      </>}
    </div>}
  </section>
}

class ComparisonBoundary extends Component<{ children: ReactNode }, { failed: boolean }> {
  state = { failed: false }
  static getDerivedStateFromError() { return { failed: true } }
  render() {
    return this.state.failed
      ? <p className={styles.message} role="alert">Could not open this comparison. <button onClick={() => window.location.reload()}>Reload page</button></p>
      : this.props.children
  }
}
