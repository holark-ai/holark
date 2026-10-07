import { FileCode2, FileText, Folder } from 'lucide-react'
import { Link } from 'react-router-dom'
import type { RepositoryTreeEntry } from '../../data/types'
import styles from './RepositoryList.module.css'
import { repositoryRoute } from './repositoryRoutes'

type RepositoryListProps = {
  commit?: string
  projectId: string
  refName: string
  entries: RepositoryTreeEntry[]
  currentPath?: string
}

export function RepositoryList({ commit, projectId, refName, entries, currentPath = '' }: RepositoryListProps) {
  const parentPath = currentPath.split('/').slice(0, -1).join('/')

  return (
    <div className={styles.wrapper}>
      <div className={styles.columnHead}><span>Name</span><span>Size</span><span>Type</span></div>
      <div className={styles.table} aria-label="Repository files">
        {currentPath && (
          <Link className={styles.row} to={repositoryRoute(projectId, 'tree', parentPath, refName, commit)}>
            <span className={styles.name}><Folder className={styles.folder} /> ..</span>
            <span>Parent directory</span>
            <span />
          </Link>
        )}
        {entries.map((entry) => {
          const url = repositoryRoute(projectId, entry.type === 'directory' ? 'tree' : 'blob', entry.path, refName, commit)
          const FileIcon = entry.name.endsWith('.md') ? FileText : FileCode2
          return (
            <Link className={styles.row} to={url} key={entry.path}>
              <span className={styles.name}>
                {entry.type === 'directory' ? <Folder className={styles.folder} /> : <FileIcon className={styles.file} />}
                <span>{entry.name}</span>
              </span>
              <span className={styles.message}>{entry.type === 'directory' ? '—' : formatBytes(entry.size ?? 0)}</span>
              <span className={styles.updated}>{entry.type === 'directory' ? 'Directory' : entry.language || 'File'}</span>
            </Link>
          )
        })}
      </div>
    </div>
  )
}

function formatBytes(bytes: number) {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / 1024 / 1024).toFixed(1)} MB`
}
