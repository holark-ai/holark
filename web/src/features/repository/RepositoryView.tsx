import { FileText, Folder } from 'lucide-react'
import { lazy, Suspense, useCallback } from 'react'
import ReactMarkdown from 'react-markdown'
import rehypeSanitize from 'rehype-sanitize'
import remarkGfm from 'remark-gfm'
import { Link, useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { api, type ApiError } from '../../data/api'
import { ProjectHeader } from '../project/ProjectHeader'
import { useProject } from '../project/ProjectContext'
import { useNavigationArrows } from '../navigation/useNavigationArrows'
import { HolonComposer } from '../agents/HolonComposer'
import { BranchCombobox } from './BranchCombobox'
import { RepositoryChangesView } from './RepositoryChangesView'
import { RepositoryCommitsView } from './RepositoryCommitsView'
import { RepositoryList } from './RepositoryList'
import { RepositorySearchResults } from './RepositorySearchResults'
import { repositoryRoute } from './repositoryRoutes'
import { useRepositoryBranches } from './useRepositoryBranches'
import { useRepositoryResource } from './useRepositoryResource'
import styles from './RepositoryView.module.css'
import pageStyles from '../project/ProjectPage.module.css'

const FileViewer = lazy(() => import('./FileViewer'))

type Mode = 'tree' | 'blob'

export function RepositoryView({ mode }: { mode: Mode }) {
  const params = useParams()
  const [searchParams, setSearchParams] = useSearchParams()
  const requestedView = searchParams.get('view')
  const navigate = useNavigate()
  const project = useProject()
  // React Router has already decoded the path, including literal percent signs.
  const path = (params['*'] ?? '').replace(/^\/+|\/+$/g, '')
  const query = searchParams.get('q') ?? ''
  const setSearchQuery = (query: string) => {
    const next = new URLSearchParams(searchParams)
    if (query) next.set('q', query)
    else next.delete('q')
    setSearchParams(next, { replace: true })
  }
  const branchState = useRepositoryBranches(project.id, project.default_branch)
  const requestedRef = searchParams.get('ref')
  const ref = requestedRef || branchState.response?.default_ref || project.default_branch
  const selectedRef = branchState.response?.refs.find((item) => item.name === ref || item.short_name === ref)
  // Keep provider branch history and its cache stable when fully qualified refs arrive.
  // Leave revisions such as detached HEAD unchanged when no branch entry matches.
  const historyRef = (selectedRef?.name || ref).replace(/^refs\/(?:heads\/(?!holark\/)|holark\/browse\/origin\/)/, '')
  const branch = selectedRef?.short_name || ref
  const navigationRef = requestedRef || branch
  const requestedCommit = searchParams.get('commit') || undefined
  const latestCommit = selectedRef?.target
  const commit = requestedCommit === latestCommit ? undefined : requestedCommit
  const changes = mode === 'tree' && requestedView === 'changes'
  const viewSuffix = changes ? '&view=changes' : ''
  const revision = commit || selectedRef?.target || ref
  const changeBranch = (nextBranch: string) => {
    navigate(repositoryRoute(project.id, commit || changes ? 'tree' : mode, commit || changes ? '' : path, nextBranch) + viewSuffix)
  }
  const selectCommit = (sha?: string, view = changes ? 'changes' : 'files') => {
    navigate(repositoryRoute(project.id, 'tree', '', navigationRef, sha === latestCommit ? undefined : sha) + (view === 'changes' ? '&view=changes' : ''))
  }

  const changeView = (view: 'files' | 'changes') => {
    const next = new URLSearchParams(searchParams)
    next.delete('q')
    if (view === 'changes') next.set('view', 'changes')
    else next.delete('view')
    navigate({ search: next.toString() })
  }

  useNavigationArrows('horizontal', (direction) => {
    if (mode !== 'tree') return false
    const nextChanges = direction === 1
    if (nextChanges !== changes) changeView(nextChanges ? 'changes' : 'files')
    return true
  })

  return (
    <div className={`${pageStyles.page} ${styles.page}`}>
      <ProjectHeader pageTitle="Repository" repositoryBranch={branch} search={query} onSearchChange={setSearchQuery} />
      <div className={styles.toolbar}>
        <div className={styles.branchSelector}>
          <BranchCombobox
            variant="timeline"
            enableOpenShortcut
            popupClassName={styles.branchPopup}
            branches={branchState.response?.refs ?? branchState.branches}
            value={branch}
            defaultBranch={project.default_branch}
            loading={branchState.loading}
            refreshing={branchState.refreshing}
            error={branchState.error}
            onOpen={() => void branchState.refresh()}
            onCommitSelect={(branch) => navigate(repositoryRoute(project.id, 'tree', '', branch.name) + '&view=changes')}
            onValueChange={(name) => changeBranch(branchState.response?.refs.find((item) => item.short_name === name && item.kind !== 'branch')?.name || name)}
          />
        </div>
        {mode === 'tree' && <div className={styles.viewSwitch} data-view={changes ? 'changes' : 'files'} role="group" aria-label="Repository view">
          <button type="button" aria-pressed={!changes} aria-keyshortcuts="Shift+Alt+ArrowLeft" title="Files (Shift + Option/Alt + ←)" onClick={() => changeView('files')}>Files</button>
          <button type="button" aria-pressed={changes} aria-keyshortcuts="Shift+Alt+ArrowRight" title="Changes (Shift + Option/Alt + →)" onClick={() => changeView('changes')}>Changes</button>
        </div>}
      </div>
      <div className={`${styles.content} ${changes ? styles.contentWithHistory : ''}`}>
        {changes && <aside className={`${styles.history} ${styles.stickyHistory}`} aria-label="Repository history">
          <RepositoryCommitsView key={project.id + ':' + historyRef} refName={historyRef} selectedCommit={commit} onSelectCommit={selectCommit} onReselectCommit={(sha) => selectCommit(sha, 'files')} onViewCommitChanges={(sha) => selectCommit(sha, 'changes')} />
        </aside>}
        <section className={styles.files} aria-label={changes ? "Repository changes" : "Repository files"}>
          {!changes && <div className={styles.filesTop}>
            <Breadcrumbs path={path} mode={mode} refName={navigationRef} commit={commit} />
            <span className={styles.revision}>{commit ? <>
              <code>{commit.slice(0, 7)}</code>
              <span aria-hidden="true">·</span>
              <button type="button" onClick={() => selectCommit()}>Back to latest</button>
            </> : <>
              {latestCommit && <><code>{latestCommit.slice(0, 7)}</code><span aria-hidden="true">·</span></>}
              Latest
            </>}</span>
          </div>}

          {query.trim() ? (
            <RepositorySearchResults projectId={project.id} refName={navigationRef} revision={revision} query={query.trim()} commit={commit} />
          ) : (
          <>
            {!branchState.response ? <StateMessage>{branchState.error || 'Loading repository…'}</StateMessage> : changes ? (
              <RepositoryChangesView revision={revision} refName={navigationRef} />
            ) : mode === 'blob' ? (
              <FileContent commit={commit} path={path} revision={revision} refName={navigationRef} />
            ) : (
              <DirectoryContent commit={commit} path={path} revision={revision} refName={navigationRef} />
            )}
          </>
        )}
        </section>
      </div>
      {!query.trim() && <HolonComposer
        contextLabel={mode === 'blob' ? path : `${project.name}${path ? ` / ${path}` : ''}`}
        contextPrompt={`Repository: ${project.name}\nBranch: ${branch}\n${commit ? 'Commit: ' + commit + '\n' : ''}${mode === 'blob' ? 'File: ' + path : 'Folder: ' + (path || '.')}`}
        branch={branch}
      />}
    </div>
  )
}

function Breadcrumbs({ path, mode, refName, commit }: { path: string; mode: Mode; refName: string; commit?: string }) {
  const project = useProject()
  const segments = path.split('/').filter(Boolean)
  const Icon = mode === 'blob' ? FileText : Folder
  return <nav className={styles.breadcrumbs} aria-label="Repository breadcrumb">
    <Icon aria-hidden="true" />
    <Link to={repositoryRoute(project.id, 'tree', '', refName, commit)}>{project.name}</Link>
    {segments.map((segment, index) => <span className={styles.crumb} key={index}>
      <span>/</span>
      {index === segments.length - 1 ? <h1 aria-current="page">{segment}</h1> : <Link to={repositoryRoute(project.id, 'tree', segments.slice(0, index + 1).join('/'), refName, commit)}>{segment}</Link>}
    </span>)}
  </nav>
}

function DirectoryContent({ path, revision, refName, commit }: { commit?: string; path: string; revision: string; refName: string; }) {
  const project = useProject()
  const load = useCallback((signal: AbortSignal) => api.repositoryTree(project.id, path, revision, signal), [project.id, path, revision])
  const result = useRepositoryResource(load)
  if (!result) return <StateMessage>Loading repository…</StateMessage>
  if (result.error) return <RepositoryError error={result.error} refName={refName} commit={commit} />
  const tree = result.data!
  const readme = tree.entries.find((entry) => entry.type === 'file' && entry.name === 'README.md')
  return <>
    <section aria-label="Directory contents">
      <RepositoryList commit={commit} projectId={project.id} refName={refName} currentPath={path} entries={tree.entries} />
      {tree.entries.length === 0 && <StateMessage>This folder is empty.</StateMessage>}
    </section>
    {readme && <Readme path={readme.path} revision={tree.commit} />}
  </>
}

function Readme({ path, revision }: { path: string; revision: string }) {
  const project = useProject()
  const load = useCallback((signal: AbortSignal) => api.repositoryBlob(project.id, path, revision, signal), [project.id, path, revision])
  const result = useRepositoryResource(load)
  if (!result) return null
  if (result.error) return <StateMessage>{repositoryErrorMessage(result.error)}</StateMessage>
  return <section className={styles.readme} aria-labelledby="readme-title">
    <header><FileText aria-hidden="true" /><h2 id="readme-title">README.md</h2></header>
    <article className={styles.markdown}><ReactMarkdown remarkPlugins={[remarkGfm]} rehypePlugins={[rehypeSanitize]}>{result.data!.content}</ReactMarkdown></article>
  </section>
}

function FileContent({ path, revision, refName, commit }: { commit?: string; path: string; revision: string; refName: string }) {
  const project = useProject()
  const load = useCallback((signal: AbortSignal) => api.repositoryBlob(project.id, path, revision, signal), [project.id, path, revision])
  const result = useRepositoryResource(load)
  if (!result) return <StateMessage>Loading file…</StateMessage>
  if (result.error) return <RepositoryError error={result.error} refName={refName} commit={commit} />
  const blob = result.data!
  const lines = blob.content ? blob.content.replace(/\n$/, '').split('\n').length : 0
  return <section className={styles.file} aria-label="File contents">
    <div className={styles.fileMeta}>{lines} {lines === 1 ? 'line' : 'lines'} · {formatBytes(blob.size)}</div>
    <Suspense fallback={<StateMessage>Loading source viewer…</StateMessage>}>
      <FileViewer key={`${blob.commit}:${path}`} content={blob.content} language={blob.language} />
    </Suspense>
  </section>
}

function StateMessage({ children }: { children: React.ReactNode }) {
  return <div className={styles.state} role="status">{children}</div>
}

function RepositoryError({ error, refName, commit }: { commit?: string; error: Error; refName: string }) {
  const project = useProject()
  if ((error as ApiError).code === 'path_not_found') return <StateMessage>
    <p>This path does not exist on {refName}.</p>
    <Link to={repositoryRoute(project.id, 'tree', '', refName, commit)}>Go to repository root</Link>
  </StateMessage>
  if ((error as ApiError).code === 'ref_not_found') return <StateMessage>Branch {refName} does not exist.</StateMessage>
  return <StateMessage>{repositoryErrorMessage(error)}</StateMessage>
}

function repositoryErrorMessage(error: Error) {
  switch ((error as ApiError).code) {
    case 'binary_file': return 'Binary files cannot be displayed.'
    case 'file_too_large': return 'This file exceeds the 2 MB preview limit.'
    case 'repository_unavailable': return 'Repository unavailable. Check the local repository and Git configuration.'
    case 'path_not_directory': return 'This path is not a directory.'
    case 'path_not_file': return 'This path is not a file.'
    default: return error.message
  }
}

function formatBytes(bytes: number) {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / 1024 / 1024).toFixed(1)} MB`
}
