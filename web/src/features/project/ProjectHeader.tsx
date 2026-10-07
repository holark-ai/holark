import { Plus, Search } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { Button } from '../../components/Button'
import { CreationShortcut } from '../../components/CreationShortcut'
import { KeyboardShortcut } from '../../components/KeyboardShortcut'
import { NewAgentDialog } from '../agents/NewAgentDialog'
import { ProfileLink } from '../profile/ProfileLink'
import { useProject } from './ProjectContext'
import styles from './ProjectHeader.module.css'

type ProjectHeaderProps = {
  pageTitle?: string
  repositoryBranch?: string
  search?: string
  onSearchChange?: (value: string) => void
}

export function ProjectHeader({ pageTitle, repositoryBranch, search, onSearchChange }: ProjectHeaderProps) {
  const searchInput = useRef<HTMLInputElement>(null)
  const navigate = useNavigate()
  const [searchDraft, setSearchDraft] = useState('')
  const searchValue = onSearchChange ? search ?? '' : searchDraft
  const updateSearch = onSearchChange ?? setSearchDraft
  useEffect(() => {
    const focusSearch = (event: KeyboardEvent) => {
      if (event.defaultPrevented || event.isComposing || !event.altKey || !event.shiftKey || event.metaKey || event.ctrlKey) return
      // Option changes event.key on macOS, so use the physical key when available.
      if (event.code ? event.code !== 'KeyF' : event.key.toLowerCase() !== 'f') return
      if (document.querySelector('[role="dialog"][aria-modal="true"], dialog[open]')) return
      event.preventDefault()
      searchInput.current?.focus()
      searchInput.current?.select()
    }
    window.addEventListener('keydown', focusSearch)
    return () => window.removeEventListener('keydown', focusSearch)
  }, [])
  const [composerOpen, setComposerOpen] = useState(false)
  const project = useProject()
  const repositoryRoot = repositoryBranch === undefined
    ? `/`
    : `/?ref=${encodeURIComponent(repositoryBranch)}`

  return (
    <>
      <header className={`${styles.header} ${pageTitle || onSearchChange ? '' : styles.standalone}`}>
        <div className={styles.identity}>
          <div className={styles.copy}>
            <nav className={styles.breadcrumbs} aria-label="Project">
              <Link to={repositoryRoot} title={project.name}>{project.name}</Link>
              {pageTitle && <><span className={styles.separator} aria-hidden="true">/</span><h1>{pageTitle}</h1></>}
            </nav>
          </div>
        </div>
          <form className={styles.search} role="search" aria-label="Repository files and content" onSubmit={(event) => {
            event.preventDefault()
            if (!onSearchChange && searchDraft.trim()) navigate(`/?${new URLSearchParams({ q: searchDraft.trim() })}`)
          }}><Search /><input ref={searchInput} type="search" aria-label="Search files and content" aria-keyshortcuts="Shift+Alt+F" placeholder="Search files and content" value={searchValue} onChange={(event) => updateSearch(event.target.value)} onKeyDown={(event) => { if (event.key === 'Escape' && searchValue) { event.preventDefault(); updateSearch('') } }} /><KeyboardShortcut letter="F" /></form>
          <div className={styles.actions}>
          <Button variant="primary" icon={<Plus />} aria-keyshortcuts="Shift+Alt+N" onClick={() => setComposerOpen(true)}>New Agent <CreationShortcut letter="N" /></Button>
          <ProfileLink />
        </div>
      </header>
      <NewAgentDialog open={composerOpen} project={project} initialBranch={repositoryBranch} onClose={() => setComposerOpen(false)} />
    </>
  )
}
