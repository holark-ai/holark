import { useCallback, useEffect, useId, useRef, useState, type ReactNode } from 'react'
import { useLocation, useNavigationType, useSearchParams } from 'react-router-dom'
import { ChevronLeft, ChevronRight, Search, X } from 'lucide-react'
import type { GitHubMember } from '../../data/types'
import { Button } from '../../components/Button'
import { MemberIdentityView } from '../members/MemberIdentity'
import { SearchFilter, type SearchFilterOption } from './SearchFilter'
import { StateFilter } from './StateFilter'
import { LabelFilter } from './LabelFilter'
import { searchTokens } from './searchQuery'
import styles from './WorkItems.module.css'

const issuePeople = [['author', 'Author'], ['assignee', 'Assignee']] as const
const prPeople = [...issuePeople, ['user-review-requested', 'Reviewer']] as const
export function ListSearchToolbar({ kind, members, identity, actions }: {
  kind: 'issue' | 'pull_request'; members: GitHubMember[]; identity?: GitHubMember | null; actions: ReactNode
}) {
  const [params, setParams] = useSearchParams()
  const location = useLocation()
  const navigationType = useNavigationType()
  const defaultQuery = kind === 'issue' ? 'is:open sort:created-desc' : 'is:active sort:created-desc'
  const query = params.get('q') ?? defaultQuery
  const title = params.get('title') ?? ''
  const inputId = useId()
  const currentMember = members.find(member => member.is_me) ?? identity
  const [draft, setDraft] = useState({ key: location.key, title, text: title })
  const searchInput = useRef<HTMLInputElement>(null)
  const advanced = useRef<HTMLDetailsElement>(null)
  const advancedInput = useRef<HTMLTextAreaElement>(null)
  const [advancedDraft, setAdvancedDraft] = useState({ key: location.key, text: query })
  if (draft.key !== location.key) setDraft({ key: location.key, title,
    text: navigationType === 'REPLACE' && location.state?.searchNormalization && draft.title === title ? draft.text : title,
  })
  if (advancedDraft.key !== location.key) setAdvancedDraft({ key: location.key, text: query })
  const editAdvanced = () => {
    if (advanced.current) advanced.current.open = true
    window.requestAnimationFrame(() => advancedInput.current?.focus())
  }
  const searchParams = useCallback((filters: string, text: string) => {
    const next = new URLSearchParams({ q: filters.trim() || defaultQuery, page: '1' })
    if (text.trim()) next.set('title', text.trim())
    return next
  }, [defaultQuery])
  const navigateQuery = (next: string) => setParams(searchParams(next, draft.text))
  useEffect(() => {
    if (draft.key !== location.key || draft.text.trim() === title) return
    const timer = window.setTimeout(() => setParams(searchParams(query, draft.text), { replace: true }), 250)
    return () => window.clearTimeout(timer)
  }, [draft, title, query, location.key, searchParams, setParams])
  const tokens = searchTokens(query)
  const qualifier = (name: string) => tokens.find((token) => token.startsWith(`${name}:`))?.slice(name.length + 1) ?? ''
  const setQualifier = (name: string, value: string) => navigateQuery([...tokens.filter((token) => !token.startsWith(`${name}:`)), ...(value ? [`${name}:${value}`] : [])].join(' '))
  return <section className={styles.toolbar} aria-label={`${kind === 'issue' ? 'Issue' : 'Pull request'} search and filters`}>
      <div className={styles.searchRow}>
        <form className={styles.search} onSubmit={(event) => { event.preventDefault(); setParams(searchParams(query, draft.text)) }}>
          <label htmlFor={inputId} className="sr-only">Search {kind === 'issue' ? 'issues' : 'pull requests'}</label>
          <input ref={searchInput} id={inputId} value={draft.text} onChange={(event) => { const text = event.target.value; setDraft((current) => ({ ...current, text })) }} placeholder={kind === 'issue' ? 'Search issue titles…' : 'Search PR titles…'} />
          {draft.text && <Button size="small" variant="ghost" icon={<X aria-hidden="true" />} aria-label="Clear search" title="Clear search" onClick={() => {
            setDraft({ key: location.key, title: '', text: '' })
            setParams(searchParams(query, ''))
            searchInput.current?.focus()
          }} />}
          <Button type="submit" size="small" variant="ghost" icon={<Search aria-hidden="true" />} aria-label="Search" title="Search" />
        </form>
      </div>
      <div className={styles.controls}>
        <StateFilter kind={kind} tokens={tokens} onChange={navigateQuery} />
        {(kind === 'issue' ? issuePeople : prPeople).map(([name, label]) => {
          const value = qualifier(name)
          const selectedValue = currentMember && value.toLowerCase() === currentMember.login.toLowerCase() ? '@me' : value
          const options: SearchFilterOption[] = [
            ['', 'Anyone'],
            currentMember
              ? ['@me', `${currentMember.login} (Me)`, <span className={styles.filterMember}><MemberIdentityView member={currentMember} />{' '}<span>(Me)</span></span>]
              : ['@me', 'Me'],
          ]
          if (selectedValue && selectedValue !== '@me' && !members.some((member) => member.login.toLowerCase() === selectedValue.toLowerCase())) options.push([selectedValue, selectedValue])
          options.push(...members
            .filter((member) => member.id !== currentMember?.id && member.login.toLowerCase() !== currentMember?.login.toLowerCase())
            .map((member): SearchFilterOption => [member.login.toLowerCase(), member.login, <MemberIdentityView member={member} />]))
          return <SearchFilter key={name} label={label} value={selectedValue} active={Boolean(value)} options={options} onValueChange={(next) => setQualifier(name, next)} />
        })}
        {kind === 'issue' && <LabelFilter tokens={tokens} onChange={navigateQuery} onEditAdvanced={editAdvanced} />}
        <SearchFilter
          label="Sort"
          className={styles.sort}
          value={qualifier('sort') || 'created-desc'}
          options={[
            ['created-desc', 'Newest created'],
            ['created-asc', 'Oldest created'],
            ['updated-desc', 'Recently updated'],
            ['updated-asc', 'Least recently updated'],
          ]}
          onValueChange={(value) => setQualifier('sort', value)}
        />
        {actions}
      </div>
      <details ref={advanced} className={styles.advanced}>
        <summary>Advanced filters</summary>
        <form onSubmit={(event) => { event.preventDefault(); navigateQuery(advancedDraft.text) }}>
          <label htmlFor={`${inputId}-advanced`}>Query expression</label>
          <textarea ref={advancedInput} id={`${inputId}-advanced`} value={advancedDraft.text} onChange={event => setAdvancedDraft({ key: location.key, text: event.target.value })} rows={2} />
          <p>Title search and this expression both apply. Edits apply when you select Apply.</p>
          <p>Use words, “quoted phrases”, <code>#123</code>, <code>author:login</code>, <code>assignee:@me</code>, <code>is:open</code> or <code>is:all</code>, and <code>sort:updated-desc</code>. Selected states match any checked state; other filters combine with AND.</p>
          {kind === 'issue'
            ? <p>Labels support nested AND/OR: <code>label:(bug OR (frontend AND backend))</code>. Any group may match; every label in a group must match.</p>
            : <p>PRs also support <code>user-review-requested:@me</code>, <code>draft:true/false</code>, and <code>is:active/merged/unmerged/wip</code>.</p>}
          <Button type="submit" size="small">Apply</Button>
        </form>
      </details>
    </section>
}

export function ListPagination({ page, total, perPage, personal = false }: { page: number; total: number; perPage: number; personal?: boolean }) {
  const [params, setParams] = useSearchParams()
  const move = (page: number) => { const next = new URLSearchParams(params); next.set('page', String(page)); setParams(next) }
  return <nav className={styles.pagination} aria-label="Pagination">
  <Button variant={personal ? 'secondary' : 'ghost'} aria-label="Previous" title="Previous page" disabled={page <= 1} onClick={() => move(page - 1)}>{personal ? 'Previous' : <ChevronLeft aria-hidden="true" />}</Button>
  <span>Page {page} of {Math.max(1, Math.ceil(total / perPage))}</span>
  <Button variant={personal ? 'secondary' : 'ghost'} aria-label="Next" title="Next page" disabled={page * perPage >= total} onClick={() => move(page + 1)}>{personal ? 'Next' : <ChevronRight aria-hidden="true" />}</Button>
  </nav>
}
