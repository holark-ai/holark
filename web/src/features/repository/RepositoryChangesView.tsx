import { useCallback, useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import { CopyButton } from '../../components/CopyButton'
import { api, type ApiError } from '../../data/api'
import type { RepositoryCommitChanges, WorkspaceFileDiff } from '../../data/types'
import { MarkdownBody } from '../attachments/MarkdownBody'
import { parseUnifiedDiff } from '../pullRequests/unifiedDiff'
import { useProject } from '../project/ProjectContext'
import { DiffReviewHeader } from '../terminal/DiffReviewHeader'
import { HolonDiffReview, type DiffLayout } from '../terminal/HolonDiffReview'
import type { ContentLoader } from '../terminal/workspaceContents'
import { repositoryRoute } from './repositoryRoutes'
import { useRepositoryResource } from './useRepositoryResource'
import styles from './RepositoryChangesView.module.css'

export function RepositoryChangesView({ revision, refName }: { revision: string; refName: string }) {
  const [layout, setLayout] = useState<DiffLayout>('inline')
  const load = useCallback((signal: AbortSignal) => api.repositoryCommitChanges(revision, signal), [revision])
  const result = useRepositoryResource(load)
  if (!result) return <p className={styles.state} role="status">Loading changes…</p>
  if (result.error) return <p className={styles.state} role="alert">Could not load commit changes. {result.error.message}</p>
  return <CommitReview key={result.data!.commit} changes={result.data!} refName={refName} layout={layout} onLayoutChange={setLayout} />
}

function CommitReview({ changes, refName, layout, onLayoutChange }: { changes: RepositoryCommitChanges; refName: string; layout: DiffLayout; onLayoutChange: (layout: DiffLayout) => void }) {
  const project = useProject()
  const [collapsedFiles, setCollapsedFiles] = useState<Set<string>>(() => new Set())
  const files = useMemo<WorkspaceFileDiff[]>(() => changes.files.map((file) => {
    const rows = parseUnifiedDiff(file.patch, file.truncated)
    return {
      path: file.path,
      status: ({ A: 'added', D: 'removed', M: 'modified', T: 'type changed' } as Record<string, string>)[file.status] || file.status,
      additions: rows.filter((row) => row.kind === 'added').length,
      deletions: rows.filter((row) => row.kind === 'removed').length,
      binary: file.binary, diff: file.patch, diff_truncated: file.truncated,
    }
  }), [changes])
  const inspection = useMemo(() => ({ files }), [files])
  const stats = files.reduce((total, file) => ({ files: total.files + 1, additions: total.additions + file.additions, deletions: total.deletions + file.deletions }), { files: 0, additions: 0, deletions: 0 })
  const allCollapsed = files.length > 0 && files.every((file) => collapsedFiles.has(file.path))
  const setFileCollapsed = useCallback((path: string, collapsed: boolean) => {
    setCollapsedFiles((current) => {
      if (current.has(path) === collapsed) return current
      const next = new Set(current)
      if (collapsed) next.add(path)
      else next.delete(path)
      return next
    })
  }, [])
  const loadContents = useCallback<ContentLoader>(async (_id, base, target, path, signal) => {
    const file = files.find((file) => file.path === path)!
    if (file.binary) return { ...file, content_status: 'binary' }
    try {
      const read = async (revision: string, absent: boolean) => {
        if (absent || !revision) return ''
        const blob = await api.repositoryBlob(project.id, path, revision, signal)
        if (blob.size > 256 * 1024) throw Object.assign(new Error('File too large'), { code: 'file_too_large' })
        return blob.content
      }
      const [original, modified] = await Promise.all([read(base, file.status === 'added'), read(target, file.status === 'removed')])
      return { ...file, content_status: 'ready', contents: { original, modified } }
    } catch (error) {
      const status = ({ binary_file: 'binary', file_too_large: 'too_large', path_not_file: 'unsupported' } as const)[(error as ApiError).code as 'binary_file' | 'file_too_large' | 'path_not_file']
      if (!status) throw error
      return { ...file, content_status: status }
    }
  }, [files, project.id])

  return <>
    <CommitDetails commit={changes.commit} />
    <section className={styles.review} aria-label="Commit changes">
    <DiffReviewHeader stats={stats} layout={layout} onLayoutChange={onLayoutChange} allFilesCollapsed={allCollapsed}
      onToggleCollapsed={() => setCollapsedFiles(allCollapsed ? new Set() : new Set(files.map((file) => file.path)))} />
    {files.some((file) => file.diff_truncated) && <p className={styles.state}>Large patches are truncated; line totals reflect the available preview.</p>}
    {files.length === 0 ? <p className={styles.state}>This commit has no file changes.</p> : <HolonDiffReview
      holonId={project.id} inspection={inspection} load={loadContents} scrollMode="page" base={changes.parent} target={changes.commit}
      layout={layout} navigation={{ path: '', sequence: 0 }} collapsedFiles={collapsedFiles} onFileCollapsedChange={setFileCollapsed}
      fileAction={(file) => file.status !== 'removed' && <Link className={styles.openFile} to={repositoryRoute(project.id, 'blob', file.path, refName, changes.commit)}>Open file</Link>} />}
    </section>
  </>
}

function CommitDetails({ commit }: { commit: string }) {
  const project = useProject()
  const load = useCallback(() => api.repositoryCommits(project.id, commit, 1), [project.id, commit])
  const result = useRepositoryResource(load)
  const details = result?.data?.commits.find((item) => item.sha === commit)
  const message = details?.message ?? ''
  const firstNewline = message.indexOf('\n')
  const title = firstNewline < 0 ? message : message.slice(0, firstNewline)
  const body = firstNewline < 0 ? '' : message.slice(firstNewline + 1).replace(/^\r?\n/, '')

  return <section className={styles.commitDetails} aria-label="Commit details">
    <dl className={styles.commitMeta}>
      <dt>Commit</dt>
      <dd className={styles.commitHash}><code>{commit}</code><CopyButton text={commit} label="Copy commit SHA" /></dd>
      {details && <>
        <dt>Author</dt>
        <dd title={details.author_email}>{details.author_name || details.author_email || 'Unknown author'}</dd>
        <dt>Date</dt>
        <dd><time dateTime={details.authored_at}>{new Date(details.authored_at).toLocaleString()}</time></dd>
      </>}
    </dl>
    {!result ? <p className={styles.state} role="status">Loading commit details…</p>
      : result.error ? <p className={styles.state} role="alert">Could not load commit details. {result.error.message}</p>
      : <>
        {title && <h2 className={styles.commitTitle}>{title}</h2>}
        {body && <MarkdownBody className={styles.commitBody} value={body} />}
      </>}
  </section>
}
