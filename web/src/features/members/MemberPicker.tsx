import { Check, Search, X } from 'lucide-react'
import { useEffect, useMemo, useRef, useState, type RefObject } from 'react'
import type { ProjectMember } from '../../data/types'
import { MemberIdentity } from './MemberIdentity'
import { useProjectMemberStore } from './projectMemberContext'
import styles from './MemberPicker.module.css'

export function MemberPicker({ selectedIds, multiple = false, disabled = false, searchInputRef, onChange }: {
  selectedIds: string[]
  multiple?: boolean
  disabled?: boolean
  searchInputRef?: RefObject<HTMLInputElement | null>
  onChange: (memberIds: string[]) => void
}) {
  const { searchMembers } = useProjectMemberStore()
  const [query, setQuery] = useState('')
  const [results, setResults] = useState<ProjectMember[]>([])
  const [loading, setLoading] = useState(true)
  const [failed, setFailed] = useState(false)
  const sequence = useRef(0)
  const selected = useMemo(() => new Set(selectedIds), [selectedIds])

  useEffect(() => {
    const current = ++sequence.current
    void searchMembers(query, 20).then((members) => {
      if (current !== sequence.current) return
      setResults(members)
      setLoading(false)
    }).catch(() => {
      if (current !== sequence.current) return
      setResults([])
      setFailed(true)
      setLoading(false)
    })
  }, [query, searchMembers])

  const toggle = (id: string) => {
    if (disabled) return
    if (!multiple) {
      onChange(selected.has(id) ? [] : [id])
      return
    }
    onChange(selected.has(id) ? selectedIds.filter((candidate) => candidate !== id) : [...selectedIds, id])
  }

  return (
    <div className={styles.picker}>
      {selectedIds.length > 0 && (
        <div className={styles.selected} aria-label="Selected members">
          {selectedIds.map((id) => (
            <button
              className={styles.selectedMember}
              type="button"
              disabled={disabled}
              key={id}
              onClick={() => toggle(id)}
            >
              <span className={styles.srOnly}>Remove</span>{' '}
              <MemberIdentity memberId={id} />
              <X aria-hidden="true" />
            </button>
          ))}
        </div>
      )}
      <label className={styles.search}>
        <Search aria-hidden="true" />
        <span className={styles.srOnly}>Search project members</span>
        <input
          ref={searchInputRef}
          type="search"
          aria-label="Search project members"
          disabled={disabled}
          placeholder="Search members"
          value={query}
          onChange={(event) => {
            setLoading(true)
            setFailed(false)
            setQuery(event.target.value)
          }}
        />
      </label>
      <div className={styles.results} role="listbox" aria-label="Project members" aria-multiselectable={multiple || undefined}>
        {results.map((member) => (
          <button
            className={styles.option}
            type="button"
            role="option"
            aria-selected={selected.has(member.id)}
            disabled={disabled}
            key={member.id}
            onClick={() => toggle(member.id)}
          >
            {member.avatar_url
              ? <img src={member.avatar_url} alt="" />
              : <span className={styles.optionFallback} aria-hidden="true">{member.login.slice(0, 1).toUpperCase()}</span>}
            <span><strong>{member.login}</strong><small>{member.permission}</small></span>
            {selected.has(member.id) && <Check aria-hidden="true" />}
          </button>
        ))}
        {loading && <p className={styles.message}>Searching members…</p>}
        {!loading && failed && <p className={styles.message}>Members unavailable.</p>}
        {!loading && !failed && results.length === 0 && <p className={styles.message}>No members found.</p>}
      </div>
    </div>
  )
}
