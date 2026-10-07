import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { api, type ApiError } from '../../data/api'
import type { RepositoryCommit, RepositoryCommitsResponse } from '../../data/types'
import { useRetainedPolling } from '../../data/pageCache'
import { useProject } from '../project/ProjectContext'
import { useNavigationArrows } from '../navigation/useNavigationArrows'
import styles from './RepositoryCommitsView.module.css'

export function RepositoryCommitsView({ refName, selectedCommit, onSelectCommit, onReselectCommit, onViewCommitChanges }: {
  refName: string
  selectedCommit?: string
  onSelectCommit: (sha?: string) => void
  onReselectCommit?: (sha: string) => void
  onViewCommitChanges?: (sha: string) => void
}) {
  const project = useProject()
  const historyRef = useRef<HTMLDivElement>(null)
  const selectedRowRef = useRef<HTMLDivElement>(null)
  const keyboardSelection = useRef<string | undefined>(undefined)
  const pendingOlderSelection = useRef<string | undefined>(undefined)
  const [marker, setMarker] = useState<{ top: number; height: number } | null>(null)
  const [hasMoreBelow, setHasMoreBelow] = useState(false)
  const load = useCallback((signal: AbortSignal) => api.repositoryCommits(project.id, refName, 50, undefined, signal), [project.id, refName])
  const { data: latest, error, loading, refresh } = useRetainedPolling(JSON.stringify(['commits', project.id, refName]), load, 7000)
  const [data, setData] = useState<RepositoryCommitsResponse>()
  if (!data && latest) setData(latest)
  const [loadingMore, setLoadingMore] = useState(false)
  const [pageError, setPageError] = useState(false)
  const pageRequest = useRef<AbortController | undefined>(undefined)
  const loadMore = useCallback(async () => {
    if (!data?.next_cursor || pageRequest.current) return
    const cursor = data.next_cursor
    const controller = new AbortController()
    pageRequest.current = controller
    setLoadingMore(true)
    setPageError(false)
    try {
      const page = await api.repositoryCommits(project.id, refName, 50, cursor, controller.signal)
      if (controller.signal.aborted) return
      setData(current => current?.next_cursor === cursor
        ? { ...page, commits: [...current.commits, ...page.commits] } : current)
    } catch {
      if (!controller.signal.aborted) {
        pendingOlderSelection.current = undefined
        setPageError(true)
      }
    } finally {
      if (pageRequest.current === controller) {
        pageRequest.current = undefined
        setLoadingMore(false)
      }
    }
  }, [data, project.id, refName])
  useEffect(() => () => { pageRequest.current?.abort(); pageRequest.current = undefined }, [])
  const showLatest = () => {
    pageRequest.current?.abort()
    pageRequest.current = undefined
    setLoadingMore(false)
    setPageError(false)
    setData(latest)
    if (historyRef.current) historyRef.current.scrollTop = 0
    onSelectCommit(latest?.commits[0]?.sha)
  }
  const groups = useMemo(() => {
    const days = new Map<string, RepositoryCommit[]>()
    for (const commit of data?.commits ?? []) {
      const date = new Date(commit.authored_at).toLocaleDateString()
      days.set(date, [...(days.get(date) ?? []), commit])
    }
    return [...days].map(([day, commits]) => ({ day, commits }))
  }, [data])
  const selected = data?.commits.find((commit) => commit.sha === selectedCommit) ?? (!selectedCommit ? data?.commits[0] : undefined)

  const selectWithKeyboard = (sha: string) => {
    keyboardSelection.current = sha
    onSelectCommit(sha)
  }

  useNavigationArrows('vertical', (direction) => {
    if (!data?.commits.length) return true
    pendingOlderSelection.current = undefined
    const index = data.commits.findIndex((commit) => commit.sha === selected?.sha)
    const next = data.commits[index < 0 ? 0 : index + direction]
    if (next) selectWithKeyboard(next.sha)
    else if (direction === 1 && data.next_cursor) {
      pendingOlderSelection.current = selected?.sha
      void loadMore()
    }
    return true
  }, 'page')

  useEffect(() => {
    const pending = pendingOlderSelection.current
    if (!pending) return
    if (pending !== selected?.sha) {
      pendingOlderSelection.current = undefined
      return
    }
    const index = data?.commits.findIndex((commit) => commit.sha === pending) ?? -1
    const next = data?.commits[index + 1]
    if (next) {
      pendingOlderSelection.current = undefined
      keyboardSelection.current = next.sha
      onSelectCommit(next.sha)
    }
  }, [data, selected?.sha, onSelectCommit])

  useLayoutEffect(() => {
    const row = selectedRowRef.current
    const history = historyRef.current
    if (!row || !history) {
      setMarker(null)
      return
    }
    if (keyboardSelection.current === selected?.sha) {
      keyboardSelection.current = undefined
      // Scroll only the history pane, leaving the repository content in place.
      const top = row.offsetTop - 32
      const bottom = row.offsetTop + row.offsetHeight + 88
      if (top < history.scrollTop) history.scrollTop = Math.max(0, top)
      else if (bottom > history.scrollTop + history.clientHeight) history.scrollTop = bottom - history.clientHeight
    }
    const updateMarker = () => {
      const top = row.offsetTop + 7
      const height = Math.max(0, row.offsetHeight - 14)
      setMarker(current => current?.top === top && current.height === height ? current : { top, height })
    }
    updateMarker()
    const observer = new ResizeObserver(updateMarker)
    observer.observe(history)
    for (const group of history.querySelectorAll(':scope > section')) observer.observe(group)
    return () => observer.disconnect()
  }, [selected?.sha, groups])

  useEffect(() => {
    const history = historyRef.current
    if (!history) return
    const updateOverflow = () => {
      const remaining = history.scrollHeight - history.clientHeight - history.scrollTop
      setHasMoreBelow(remaining > 1)
      if (history.clientHeight > 0 && remaining < 200 && !pageError) void loadMore()
    }
    updateOverflow()
    history.addEventListener('scroll', updateOverflow, { passive: true })
    const observer = new ResizeObserver(updateOverflow)
    observer.observe(history)
    for (const group of history.children) observer.observe(group)
    return () => {
      history.removeEventListener('scroll', updateOverflow)
      observer.disconnect()
    }
  }, [groups, loadMore, pageError])

  return <section className={styles.panel} aria-label="Repository commits" data-more-below={hasMoreBelow}>
    {data && latest && latest.commits[0]?.sha !== data.commits[0]?.sha && <button type="button" className={styles.newCommits} onClick={showLatest}>New commits · Refresh history</button>}
    {loading && !data && <StateMessage>Loading commits…</StateMessage>}
    {error && <StateMessage>{data ? 'Could not refresh commits. Showing the last loaded list.' : repositoryErrorMessage(error)} <button type="button" onClick={() => void refresh()}>Retry</button></StateMessage>}
    {data?.commits.length === 0 && <StateMessage>No commits found for this ref.</StateMessage>}
    <div ref={historyRef} className={styles.history} aria-label="Commit history" aria-keyshortcuts="Shift+Alt+ArrowUp Shift+Alt+ArrowDown" tabIndex={0}>
      {marker && <span className={styles.selectionMarker} aria-hidden="true" style={{ transform: `translateY(${marker.top}px)`, height: marker.height }} />}
      {groups.map(({ day, commits }) => <section key={day}>
        <h2 className={styles.day}>
          <span>{dayLabel(commits[0].authored_at)}</span><span className={styles.count}>{commits.length} {commits.length === 1 ? 'commit' : 'commits'}</span>
        </h2>
        <div>
          {commits.map((commit) => {
            const subject = commit.message.split('\n')[0] || commit.sha.slice(0, 7)
            const pullRequest = subject.match(/\(#(\d+)\)$/)?.[1] ?? subject.match(/^Merge pull request #(\d+)\b/)?.[1]
            return <div key={commit.sha} className={styles.commit}
              ref={selected?.sha === commit.sha ? selectedRowRef : undefined}
              data-selected={selected?.sha === commit.sha}>
              <time dateTime={commit.authored_at}>{new Date(commit.authored_at).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', hour12: false })}</time>
              <span className={styles.commitContent}>
                <button type="button" className={styles.commitSelect}
                  aria-pressed={selected?.sha === commit.sha}
                  title={commit.message + ' — ' + (commit.author_name || 'Unknown author') + ' · ' + commit.sha.slice(0, 7) + ' · ' + new Date(commit.authored_at).toLocaleString()}
                  onClick={() => selected?.sha === commit.sha && onReselectCommit ? onReselectCommit(commit.sha) : onSelectCommit(commit.sha)}>
                  <span className={styles.subject}>{subject}</span>
                </button>
                <span className={styles.commitMeta}>
                  {onViewCommitChanges ? <button type="button" className={styles.commitLink}
                    aria-label={`View changes for ${commit.sha.slice(0, 7)}`}
                    onClick={() => onViewCommitChanges(commit.sha)}
                  ><code>{commit.sha.slice(0, 7)}</code></button> : <code>{commit.sha.slice(0, 7)}</code>}
                  <span aria-hidden="true">·</span>
                  <span className={styles.author}>{commit.author_name || 'Unknown author'}</span>
                  {pullRequest && <><span aria-hidden="true">·</span><span className={styles.pullRequest}>#{pullRequest}</span></>}
                </span>
              </span>
            </div>
          })}
        </div>
      </section>)}
      {loadingMore && <StateMessage>Loading older commits…</StateMessage>}
      {pageError && <StateMessage>Could not load older commits. <button type="button" onClick={() => void loadMore()}>Retry</button></StateMessage>}
      {data && data.commits.length > 0 && !data.next_cursor && <StateMessage>End of history</StateMessage>}
    </div>
  </section>
}

function StateMessage({ children }: { children: ReactNode }) {
  return <div className={styles.state} role="status">{children}</div>
}

function dayLabel(value: string) {
  const date = new Date(value)
  const today = new Date()
  if (date.toDateString() === today.toDateString()) return 'Today'
  today.setDate(today.getDate() - 1)
  if (date.toDateString() === today.toDateString()) return 'Yesterday'
  return date.toLocaleDateString(undefined, { month: 'short', day: 'numeric', ...(date.getFullYear() !== today.getFullYear() ? { year: 'numeric' } as const : {}) })
}

function repositoryErrorMessage(error: Error) {
  const code = (error as ApiError).code
  if (code === 'repository_unavailable') return 'Repository unavailable. Check the local repository and Git configuration.'
  if (code === 'ref_not_found') return 'Reference not found.'
  return error.message
}
