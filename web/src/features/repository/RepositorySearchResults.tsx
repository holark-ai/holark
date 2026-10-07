import { FileText } from 'lucide-react'
import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { api } from '../../data/api'
import type { RepositorySearchResponse } from '../../data/types'
import { repositoryRoute } from './repositoryRoutes'
import styles from './RepositorySearchResults.module.css'

export function RepositorySearchResults({ projectId, refName, revision, query, commit }: { projectId: string; refName: string; revision: string; query: string; commit?: string }) {
  // A new search starts empty; late responses cannot replace results for a newer query.
  return <SearchResults key={JSON.stringify([projectId, refName, revision, query])} projectId={projectId} refName={refName} revision={revision} query={query} commit={commit} />
}

function SearchResults({ projectId, refName, revision, query, commit }: { projectId: string; refName: string; revision: string; query: string; commit?: string }) {
  const [result, setResult] = useState<RepositorySearchResponse>()
  const [error, setError] = useState('')
  useEffect(() => {
    const controller = new AbortController()
    const timer = window.setTimeout(() => {
      void api.repositorySearch(projectId, revision, query, controller.signal).then((data) => {
        if (!controller.signal.aborted) setResult(data)
      }).catch((error: Error) => {
        if (!controller.signal.aborted) setError(error.message)
      })
    }, 200)
    return () => { window.clearTimeout(timer); controller.abort() }
  }, [projectId, refName, revision, query])

  return <section className={styles.results} aria-label="Repository search results">
    <h1>Search results</h1>
    <p role="status">{error || (result ? `${result.matches.length}${result.truncated ? '+' : ''} ${result.matches.length === 1 ? 'file' : 'files'} matching “${query}”` : 'Searching files and content…')}</p>
    {result?.truncated && <p>Showing the first 200 files. Refine your search to narrow the results.</p>}
    <ul>{result?.matches.map((match) => <li key={match.path}>
      <Link to={repositoryRoute(projectId, 'blob', match.path, refName, commit)}><FileText aria-hidden="true" /><span>{match.path}</span><small>{match.name_match && match.content_match ? 'Name and content' : match.name_match ? 'Name' : 'Content'}</small></Link>
    </li>)}</ul>
  </section>
}
