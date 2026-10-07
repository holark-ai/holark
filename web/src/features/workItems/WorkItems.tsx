import { useCallback, useEffect, useState } from 'react'
import { Link, useLocation, useSearchParams } from 'react-router-dom'
import { CircleDot, GitPullRequest } from 'lucide-react'
import { api } from '../../data/api'
import type { WorkItemSummary, GitHubMember } from '../../data/types'
import { usePolling } from '../../data/usePolling'
import { useRetainedPolling } from '../../data/pageCache'
import { SyncActions } from '../../components/SyncActions'
import { StatusBadge } from '../../components/StatusBadge'
import { WorkItemPerson } from './WorkItemPerson'
import { ProjectHeader } from '../project/ProjectHeader'
import { useProject } from '../project/ProjectContext'
import { ListSearchToolbar, ListPagination } from './ListSearchToolbar'
import styles from './WorkItems.module.css'
import pageStyles from '../project/ProjectPage.module.css'

const defaultQuery = 'is:active sort:created-desc'
const views = [['all', 'All'], ['review_requested', 'Review requested'], ['assigned_issues', 'Assigned issues'], ['assigned_pull_requests', 'Assigned PRs']] as const

export function PullRequestList() { return <WorkItems personal={false} /> }
export function MyWork() { return <WorkItems personal /> }
function WorkItems({ personal }: { personal: boolean }) {
  const project = useProject()
  const loadMembers = useCallback(() => api.githubMembers(project.id), [project.id])
  const members = usePolling(loadMembers, 60_000).data ?? []
  return <WorkItemsPage personal={personal} members={members} />
}
function WorkItemsPage({ personal, members }: { personal: boolean; members: GitHubMember[] }) {
  const project = useProject()
  const location = useLocation()
  const [params, setParams] = useSearchParams()
  const query = params.get('q') ?? defaultQuery
  const view = params.get('view') ?? 'all'
  const page = Math.max(1, Number(params.get('page')) || 1)
  const title = params.get('title') ?? ''
  const [syncing, setSyncing] = useState(false)
  const [message, setMessage] = useState('')
  const [syncSucceeded, setSyncSucceeded] = useState(false)
  const load = useCallback((signal: AbortSignal) => personal ? api.myWork(view, page, signal) : api.searchPullRequests(query, page, signal, title), [personal, view, query, page, title])
  const cacheKey = JSON.stringify(personal ? ['my-work', project.id, view, page] : ['pull-search', project.id, query, title, page])
  const { data, error, loading, refresh } = useRetainedPolling(cacheKey, load)
  const usesMe = /(?:^|\s)(author|assignee|user-review-requested):@me(?:\s|$)/.test(query)
  useEffect(() => {
    if (data?.query && (params.get('q') !== data.query || !params.has('page'))) {
      const next = new URLSearchParams(params)
      next.set('q', data.query)
      next.set('page', String(page))
      setParams(next, { replace: true, state: { searchNormalization: true } })
    }
  }, [data?.query, params, page, setParams])
  const sync = async () => {
    setSyncing(true); setMessage(''); setSyncSucceeded(false)
    try {
      let outcome: string
      if (personal) { await api.syncMyWork(); outcome = 'My work synchronized.' }
      else { const result = await api.syncPullRequests(project.id, 'history'); outcome = `Synced ${result.imported} imported, ${result.updated} updated.` }
      await refresh()
      setMessage(outcome)
      setSyncSucceeded(true)
    } catch (value) { setMessage(value instanceof Error ? value.message : 'Sync failed. Cached results are retained.') }
    finally { setSyncing(false) }
  }
  const origin = location.pathname + location.search
  const syncActions = <SyncActions syncing={syncing} message={message} succeeded={syncSucceeded} onSync={() => void sync()} onDismiss={() => setMessage('')} />
  return <main className={`${pageStyles.page} ${personal ? '' : styles.cardList}`}>
    <ProjectHeader pageTitle={personal ? 'My work' : 'Pull requests'} />
    {personal ? <div className={styles.workControls}><nav className={styles.filters} aria-label="My work filters">{views.map(([id, label]) => <button key={id} type="button" aria-pressed={view === id} onClick={() => setParams({ view: id, page: '1' })}>{label} <span>{data?.counts?.[id] ?? '…'}</span></button>)}</nav>{syncActions}</div> : <ListSearchToolbar kind="pull_request" members={members} identity={data?.identity} actions={syncActions} />}
    {loading && <p role="status">Loading cached {personal ? 'work' : 'pull requests'}…</p>}
    {error && <p role="alert" className={styles.error}>{error.message}</p>}
    {data && <>
      {!personal && !data.identity && usesMe && <p className={styles.notice}>GitHub identity unavailable. @me filters need a successful identity sync.</p>}
      {personal && !data.identity ? <p className={styles.notice}>GitHub identity unavailable. Sync to identify your assigned work.</p> : <>
        {data.total === 0 && <p className={styles.notice}>{personal ? 'No matching work in the current cache.' : 'No pull requests match this search.'}</p>}
        <div className={styles.list}>{data.rows.map((row) => <WorkRow key={`${row.kind}:${row.id}`} row={row} origin={origin} fullCard={!personal} />)}</div>
        <ListPagination page={page} total={data.total} perPage={data.per_page} personal={personal} />
      </>}
    </>}
  </main>
}
function WorkRow({ row, origin, fullCard }: { row: WorkItemSummary; origin: string; fullCard: boolean }) {
  const destination = `/${row.kind === 'issue' ? 'issues' : 'pulls'}/${row.id}?returnTo=${encodeURIComponent(origin)}`
  const title = <strong>{row.title}</strong>
  const content = <>
    {row.kind === 'issue' ? <CircleDot aria-label="Issue" /> : <GitPullRequest aria-label="Pull request" />}
    <div className={styles.summary}>{fullCard ? title : <Link to={destination}>{title}</Link>}<div className={styles.metadata}><span>{row.number ? `#${row.number}` : 'Local'} · {row.kind === 'issue' ? 'Issue' : 'PR'}</span>{row.reasons.map((reason) => <span key={reason} className={styles.reason}>{reason}</span>)}<time dateTime={row.updated_at} title={new Date(row.updated_at).toLocaleString()}>Updated {new Date(row.updated_at).toLocaleDateString()}</time></div></div>
    <div className={styles.people}>
      {row.author_id && <WorkItemPerson memberId={row.author_id} role="Author" />}
      {row.assignee_ids.map((id) => <WorkItemPerson key={`a:${id}`} memberId={id} role="Assignee" />)}
      {row.reviewer_ids.map((id) => <WorkItemPerson key={`r:${id}`} memberId={id} role="Requested reviewer" />)}
    </div>
    {fullCard
      ? <span className={styles.state} data-state={row.status}>{row.status === 'wip' ? 'WIP' : row.status}</span>
      : <StatusBadge status={row.status} />}
  </>
  return fullCard
    ? <Link className={styles.row} to={destination} aria-label={row.title}>{content}</Link>
    : <article className={styles.row}>{content}</article>
}
