import { Bot, CircleDot, ExternalLink, RotateCcw, XCircle } from 'lucide-react'
import type { FormEvent } from 'react'
import { useCallback, useEffect, useRef, useState } from 'react'
import { Link, useLocation, useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { Button } from '../../components/Button'
import { Dialog } from '../../components/Dialog'
import { SyncActions } from '../../components/SyncActions'
import { StatusBadge } from '../../components/StatusBadge'
import { api, type ApiError } from '../../data/api'
import type { Issue, IssueListItem } from '../../data/types'
import { usePolling } from '../../data/usePolling'
import { ListSearchToolbar, ListPagination } from '../workItems/ListSearchToolbar'
import { WorkItemPerson } from '../workItems/WorkItemPerson'
import listStyles from '../workItems/WorkItems.module.css'
import { useRetainedPolling } from '../../data/pageCache'
import { MemberIdentity } from '../members/MemberIdentity'
import { ProjectHeader } from '../project/ProjectHeader'
import { ProfileLink } from '../profile/ProfileLink'
import { useProject } from '../project/ProjectContext'
import { useOptionalHolonStore } from '../project/holonStoreContext'
import { IssueAssignees } from './IssueAssignees'
import { IssueLabelManager, IssueLabels } from './IssueLabels'
import styles from './Issues.module.css'
import pageStyles from '../project/ProjectPage.module.css'
import { IssueBody } from './IssueBody'
import { MarkdownEditor } from '../attachments/MarkdownEditor'
import { HolonComposer } from '../agents/HolonComposer'
import { IssueDiscussion } from './IssueDiscussion'
import { listReturnTo } from '../workItems/returnTo'

export function IssueList() {
  const project = useProject()
  const holonStore = useOptionalHolonStore()
  const navigate = useNavigate()
  const [dialogOpen, setDialogOpen] = useState(false)
  const [startingIssueId, setStartingIssueId] = useState('')
  const [agentError, setAgentError] = useState('')
  const [syncing, setSyncing] = useState(false)
  const [syncMessage, setSyncMessage] = useState('')
  const [syncSucceeded, setSyncSucceeded] = useState(false)
  const location = useLocation()
  const [params, setParams] = useSearchParams()
  const query = params.get('q') ?? 'is:open sort:created-desc'
  const title = params.get('title') ?? ''
  const page = Math.max(1, Number(params.get('page')) || 1)
  const origin = location.pathname + location.search
  const loadMembers = useCallback(() => api.githubMembers(project.id), [project.id])
  const members = usePolling(loadMembers, 60_000).data ?? []
  const loadIssues = useCallback((signal: AbortSignal) => api.searchIssues(query, page, signal, title), [query, page, title])
  const issues = useRetainedPolling(JSON.stringify(['issue-search', project.id, query, title, page]), loadIssues)
  const { data, loading, error, refresh } = issues
  useEffect(() => {
    if (data?.query && (params.get('q') !== data.query || !params.has('page'))) {
      const next = new URLSearchParams(params)
      next.set('q', data.query)
      next.set('page', String(page))
      setParams(next, { replace: true, state: { searchNormalization: true } })
    }
  }, [data?.query, params, page, setParams])

  const create = async (title: string, body: string) => {
    const issue = await api.createIssue(project.id, title, body)
    navigate(`/issues/${issue.id}`)
  }
  const startIssueAgent = async (issue: IssueListItem) => {
    setAgentError('')
    setStartingIssueId(issue.id)
    try {
      const holon = await api.startIssueAgent(issue.id)
      holonStore?.updateHolon(holon.id, holon)
      void holonStore?.refresh()
      navigate('/holons/' + holon.id)
    } catch (value) {
      setAgentError(errorMessage(value))
      setStartingIssueId('')
    }
  }

  const syncIssues = useCallback(async () => {
    setSyncing(true)
    setSyncMessage('')
    setSyncSucceeded(false)
    try {
      const result = await api.syncIssues(project.id)
      await refresh()
      setSyncMessage(`Synced ${result.imported} imported, ${result.updated} updated, ${result.exported} exported.`)
      setSyncSucceeded(true)
    } catch (value) {
      setSyncMessage(errorMessage(value))
    } finally {
      setSyncing(false)
    }
  }, [project.id, refresh])

  const renderIssue = (issue: IssueListItem) => {
    const destination = `/issues/${issue.id}?returnTo=${encodeURIComponent(origin)}`
    return <article className={`${listStyles.row} ${styles.issueListCard}`} key={issue.id}>
      <CircleDot aria-label="Issue" />
      <div className={listStyles.summary}>
        <Link className={styles.cardTitleLink} to={destination} aria-label={issue.title}><strong>{issue.title}</strong></Link>
        <div className={listStyles.metadata}>
          <span>{issue.number ? `#${issue.number}` : 'Local'} · Issue</span>
          <time dateTime={issue.updated_at} title={formatDateTime(issue.updated_at)}>Updated {new Date(issue.updated_at).toLocaleDateString()}</time>
          <span>{(issue.linked_pull_request_ids ?? []).length} PRs</span>
        </div>
        <IssueLabels labels={issue.labels ?? []} limit={3} />
      </div>
      <div className={`${listStyles.people} ${styles.cardControl}`}>
        {issue.author_id && <WorkItemPerson memberId={issue.author_id} role="Author" />}
        {issue.assignee_ids.map(id => <WorkItemPerson key={id} memberId={id} role="Assignee" />)}
      </div>
      {issue.url && <a className={`${styles.externalLink} ${styles.cardControl}`} href={issue.url} rel="noreferrer" target="_blank"><ExternalLink /> GitHub{issue.number ? ` #${issue.number}` : ''}</a>}
      <span className={listStyles.state} data-state={issue.status}>{issue.status}</span>
      <span className={styles.cardControl}><Button variant="primary" size="small" icon={<Bot />} disabled={startingIssueId === issue.id} onClick={() => void startIssueAgent(issue)}>
        {startingIssueId === issue.id ? 'Starting...' : 'Start agent'}
      </Button></span>
    </article>
  }

  return <main className={`${pageStyles.page} ${listStyles.cardList}`}>
    <ProjectHeader pageTitle="Issues" />
    <ListSearchToolbar kind="issue" members={members} identity={data?.identity} actions={
      <SyncActions syncing={syncing} message={syncMessage} succeeded={syncSucceeded} onSync={() => void syncIssues()} onDismiss={() => setSyncMessage('')}>
        <Button icon={<CircleDot />} onClick={() => setDialogOpen(true)}>New issue</Button>
      </SyncActions>
    } />
    {loading && <p role="status">Loading issues...</p>}
    {error && <p className={listStyles.error} role="alert">{error.message}</p>}
    {agentError && <p className={listStyles.error} role="alert">{agentError}</p>}
    {data && <>
      {!data.identity && /(?:^|\s)(author|assignee):@me(?:\s|$)/.test(query) && <p className={listStyles.notice}>GitHub identity unavailable. @me filters need a successful identity sync.</p>}
      {data.total === 0 && <p className={listStyles.notice}>No issues match this search.</p>}
      <div className={listStyles.list}>{data.rows.map(renderIssue)}</div>
      <ListPagination page={page} total={data.total} perPage={data.per_page} />
    </>}
    <NewIssueDialog open={dialogOpen} onClose={() => setDialogOpen(false)} onCreate={create} />
  </main>
}

export function IssueDetail() {
  const location = useLocation()
  const returnTo = listReturnTo(location.search, '/issues')
  const { issueId = '' } = useParams()
  const project = useProject()
  const holonStore = useOptionalHolonStore()
  const [issue, setIssue] = useState<Issue>()
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [startingAgent, setStartingAgent] = useState(false)
  const navigate = useNavigate()
  const github = issue ? githubDetails(issue) : {}

  const refresh = useCallback(async () => {
    setLoading(true)
    setError('')
    try {
      setIssue(await api.issue(issueId))
    } catch (value) {
      setError(errorMessage(value))
    } finally {
      setLoading(false)
    }
  }, [issueId, setIssue, setError, setLoading])

  useEffect(() => {
    const timer = window.setTimeout(() => { void refresh() })
    return () => window.clearTimeout(timer)
  }, [refresh])

  const updateStatus = async () => {
    if (!issue) return
    setSaving(true)
    setError('')
    try {
      setIssue(issue.status === 'open' ? await api.closeIssue(issue.id) : await api.reopenIssue(issue.id))
    } catch (value) {
      setError(errorMessage(value))
    } finally {
      setSaving(false)
    }
  }

  const startIssueHolon = async () => {
	if (!issue) throw new Error('Issue unavailable.')
	const holon = await api.startIssueAgent(issue.id)
    holonStore?.updateHolon(holon.id, holon)
    void holonStore?.refresh()
    return holon
  }

  const startIssueAgent = async () => {
    if (!issue || startingAgent) return
	setError('')
    setStartingAgent(true)
    try {
	  const holon = await startIssueHolon()
      navigate('/holons/' + holon.id)
    } catch (value) {
      setError(errorMessage(value))
      setStartingAgent(false)
    }
  }

  return (
    <>
    <main className={`${styles.page} ${styles.detailPage}`}>
      <header className={styles.detailHeader}>
        <div>
          <Link className={styles.backLink} to={returnTo}>{returnTo.startsWith('/my-work') ? 'Back to My work' : 'Back to issues'}</Link>
          <h1>{issue?.title || 'Issue'}</h1>
          {issue && (
            <div className={styles.meta}>
              <StatusBadge status={issue.status} />
              <span>Created {formatDateTime(issue.created_at)}</span>
              <span>Updated {formatDateTime(issue.updated_at)}</span>
            </div>
          )}
        </div>
        <div className={styles.headerActions}>
          {github.url && <a className={styles.actionLink} href={github.url} rel="noreferrer" target="_blank"><ExternalLink /> GitHub{github.number ? ` #${github.number}` : ''}</a>}
          {issue && (
            <Button variant="primary" icon={<Bot />} disabled={startingAgent} onClick={() => void startIssueAgent()}>
              {startingAgent ? 'Starting...' : 'Start agent'}
            </Button>
          )}
          {issue && <Button icon={issue.status === 'open' ? <XCircle /> : <RotateCcw />} disabled={saving} onClick={() => void updateStatus()}>{issue.status === 'open' ? 'Close issue' : 'Reopen issue'}</Button>}
          <ProfileLink />
        </div>
      </header>
      {loading && <p className={styles.message}>Loading issue...</p>}
      {error && <p className={styles.message}>{error}</p>}
      {!loading && !error && !issue && <p className={styles.message}>Issue unavailable.</p>}
      {issue && (
        <div className={styles.detailGrid}>
          <section className={styles.bodyPanel}>
            <IssueBody key={issue.id} issue={issue} onSaved={(saved) => setIssue((current) => current?.id === saved.id ? saved : current)} />
            {issue.id === issueId && <IssueDiscussion issueId={issue.id} githubURL={github.url} />}
          </section>
          <aside className={styles.sidebar}>
            <section>
              <h2>Status</h2>
              <StatusBadge status={issue.status} />
            </section>
            <section>
              <h2>Labels</h2>
              {issue.labels.length > 0
                ? <IssueLabels labels={issue.labels} showDescriptions />
                : <p>No labels assigned.</p>}
              {issue.sync_provider === 'github' && (
                <IssueLabelManager issue={issue} projectId={project.id} onIssueChange={setIssue} />
              )}
            </section>
            {issue.issuer_holark_id && (
              <section>
                <h2>Created by</h2>
                <MemberIdentity memberId={issue.issuer_holark_id} linkProfile />
              </section>
            )}
            <IssueAssignees issue={issue} projectId={project.id} onIssueChange={setIssue} />
            <section>
              <h2>Pull requests</h2>
              {issue.linked_pull_request_ids.length === 0 ? <p>None linked.</p> : <ul className={styles.linkList}>{issue.linked_pull_request_ids.map((id) => <li key={id}><Link to={`/pulls/${id}`}>{id}</Link></li>)}</ul>}
            </section>
            {issue.closed_at && (
              <section>
                <h2>Closed</h2>
                <p>{formatDateTime(issue.closed_at)}</p>
              </section>
            )}
          </aside>
        </div>
      )}
    </main>
    {issue && <HolonComposer contextLabel={issue.title} contextPrompt={`Issue: ${issue.id} — ${issue.title}\n${github.url || ''}\n\n${issue.body || ''}`} branch={project.default_branch} icon={<CircleDot />} issueId={issue.id} />}
    </>
  )
}

export function NewIssueDialog({ open, onClose, onCreate }: {
  open: boolean
  onClose: () => void
  onCreate: (title: string, body: string) => Promise<void>
}) {
  const titleRef = useRef<HTMLInputElement>(null)
  const wasOpenRef = useRef(false)
  const [title, setTitle] = useState('')
  const [body, setBody] = useState('')
  const [error, setError] = useState('')
  const [saving, setSaving] = useState(false)

  const [uploading, setUploading] = useState(false)

  useEffect(() => {
    if (open && !wasOpenRef.current) {
      setTitle('')
      setBody('')
      setError('')
      setSaving(false)
    }
    wasOpenRef.current = open
  }, [open])

  const submit = async (event: FormEvent) => {
    event.preventDefault()
    if (saving || uploading) return
    setSaving(true)
    setError('')
    try {
      await onCreate(title, body)
    } catch (value) {
      setError(errorMessage(value))
      setSaving(false)
    }
  }

  return (
    <Dialog
      open={open}
      title="New issue"
      onClose={() => { if (!uploading) onClose() }}
      initialFocusRef={titleRef}
      footer={<><Button disabled={uploading} onClick={onClose}>Cancel</Button><Button variant="primary" disabled={saving || uploading || title.trim() === ''} form="new-issue-form" type="submit">{saving ? 'Creating...' : 'Create'}</Button></>}
    >
      <form id="new-issue-form" className={styles.form} onSubmit={submit}>
        <label>Title<input ref={titleRef} value={title} onChange={(event) => setTitle(event.target.value)} maxLength={160} /></label>
        <div><label htmlFor="new-issue-body">Body</label><MarkdownEditor key={String(open)} id="new-issue-body" value={body} onChange={setBody} onUploadingChange={setUploading} disabled={saving} maxLength={4000} rows={6} /></div>
        {error && <p className={styles.formError}>{error}</p>}
      </form>
    </Dialog>
  )
}

function githubDetails(issue: Issue) {
  return issue.sync_data?.github ?? {}
}

function formatDateTime(value: string) {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' }).format(date)
}

function errorMessage(value: unknown) {
  const error = value as ApiError
  return error?.message || 'Request failed.'
}
