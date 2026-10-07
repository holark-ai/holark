import type { FormEvent } from 'react'
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Button } from '../../components/Button'
import { Dialog } from '../../components/Dialog'
import { api, type ApiError } from '../../data/api'
import type { Issue, IssueLabel } from '../../data/types'
import styles from './Issues.module.css'

export function IssueLabelManager({ issue, projectId, onIssueChange }: {
  issue: Issue
  projectId: string
  onIssueChange: (issue: Issue) => void
}) {
  const triggerRef = useRef<HTMLButtonElement>(null)
  const menuRef = useRef<HTMLDivElement>(null)
  const searchRef = useRef<HTMLInputElement>(null)
  const requestRef = useRef(0)
  const [open, setOpen] = useState(false)
  const [createOpen, setCreateOpen] = useState(false)
  const [catalog, setCatalog] = useState<IssueLabel[]>()
  const [loading, setLoading] = useState(false)
  const [query, setQuery] = useState('')
  const [error, setError] = useState('')
  const [isSavingLabelChange, setIsSavingLabelChange] = useState(false)

  const restoreTrigger = () => queueMicrotask(() => triggerRef.current?.focus())
  const closeMenu = useCallback((restoreFocus = true) => {
    requestRef.current += 1
    setOpen(false)
    setQuery('')
    if (restoreFocus) restoreTrigger()
  }, [])

  const loadCatalog = useCallback(async () => {
    const request = requestRef.current + 1
    requestRef.current = request
    setLoading(true)
    setError('')
    try {
      const labels = await api.issueLabels(projectId)
      if (requestRef.current === request) setCatalog(labels)
    } catch (value) {
      if (requestRef.current === request) {
        setCatalog(undefined)
        setError(errorMessage(value))
      }
    } finally {
      if (requestRef.current === request) setLoading(false)
    }
  }, [projectId])

  const openMenu = () => {
    setOpen(true)
    setQuery('')
    void loadCatalog()
  }

  useEffect(() => {
    if (open) searchRef.current?.focus()
  }, [open])

  useEffect(() => {
    if (!open) return undefined
    const dismiss = (event: MouseEvent) => {
      const target = event.target
      if (target instanceof Node && !menuRef.current?.contains(target) && !triggerRef.current?.contains(target)) closeMenu()
    }
    const escape = (event: KeyboardEvent) => {
      if (event.key !== 'Escape') return
      event.preventDefault()
      event.stopPropagation()
      closeMenu()
    }
    document.addEventListener('mousedown', dismiss)
    document.addEventListener('keydown', escape, true)
    return () => {
      document.removeEventListener('mousedown', dismiss)
      document.removeEventListener('keydown', escape, true)
    }
  }, [closeMenu, open])

  const assigned = useMemo(() => new Set(issue.labels.map((label) => label.name.toLocaleLowerCase())), [issue.labels])
  const normalizedQuery = query.trim().toLocaleLowerCase()
  const visibleLabels = (catalog ?? []).filter((label) => normalizedQuery === ''
    || label.name.toLocaleLowerCase().includes(normalizedQuery)
    || label.description.toLocaleLowerCase().includes(normalizedQuery))

  const toggle = async (label: IssueLabel) => {
    if (isSavingLabelChange) return
    const isAssigned = assigned.has(label.name.toLocaleLowerCase())
    setIsSavingLabelChange(true)
    setError('')
    try {
      const updated = isAssigned
        ? await api.removeIssueLabels(issue.id, [label.name])
        : await api.addIssueLabels(issue.id, [label.name])
      onIssueChange(updated)
    } catch (value) {
      setError(errorMessage(value))
    } finally {
      setIsSavingLabelChange(false)
    }
  }

  const rememberCreatedLabel = (label: IssueLabel) => {
    setCatalog((current) => current
      ? [...current.filter((candidate) => candidate.id !== label.id), label]
      : [label])
  }

  return (
    <div className={styles.labelManager} ref={menuRef}>
      <Button
        ref={triggerRef}
        className={styles.manageLabelsButton}
        size="small"
        aria-expanded={open}
        aria-haspopup="dialog"
        onClick={() => open ? closeMenu() : openMenu()}
      >
        Manage labels
      </Button>
      {open && (
        <div className={styles.labelDropdown} role="dialog" aria-label="Manage labels">
          <div className={styles.labelSearch}>
            <input
              ref={searchRef}
              type="search"
              aria-label="Search labels"
              placeholder="Search labels"
              value={query}
              onChange={(event) => setQuery(event.target.value)}
            />
          </div>
          <div className={styles.labelOptions}>
            {loading && <p className={styles.labelMenuState}>Loading labels...</p>}
            {!loading && error && <p className={styles.labelMenuError} role="alert">{error}</p>}
            {!loading && catalog?.length === 0 && <p className={styles.labelMenuState}>No repository labels yet.</p>}
            {!loading && catalog && catalog.length > 0 && visibleLabels.length === 0 && <p className={styles.labelMenuState}>No matching labels.</p>}
            {!loading && visibleLabels.map((label) => (
              <label className={styles.labelOption} key={label.id}>
                <input
                  type="checkbox"
                  checked={assigned.has(label.name.toLocaleLowerCase())}
                  disabled={isSavingLabelChange}
                  onChange={() => { void toggle(label) }}
                />
                <span className={styles.labelOptionColor} style={{ backgroundColor: /^[0-9a-f]{6}$/i.test(label.color) ? `#${label.color}` : undefined }} />
                <span className={styles.labelOptionText}>
                  <strong>{label.name}</strong>
                  {label.description && <small>{label.description}</small>}
                </span>
              </label>
            ))}
          </div>
          <button
            className={styles.createLabelButton}
            type="button"
            onClick={() => {
              closeMenu(false)
              setCreateOpen(true)
            }}
          >
            Create new label
          </button>
        </div>
      )}
      <CreateIssueLabelDialog
        open={createOpen}
        issueId={issue.id}
        projectId={projectId}
        onCreated={rememberCreatedLabel}
        onIssueChange={onIssueChange}
        onClose={() => {
          setCreateOpen(false)
          restoreTrigger()
        }}
      />
    </div>
  )
}

function CreateIssueLabelDialog({ open, issueId, projectId, onCreated, onIssueChange, onClose }: {
  open: boolean
  issueId: string
  projectId: string
  onCreated: (label: IssueLabel) => void
  onIssueChange: (issue: Issue) => void
  onClose: () => void
}) {
  const nameRef = useRef<HTMLInputElement>(null)
  const wasOpenRef = useRef(false)
  const [name, setName] = useState('')
  const [color, setColor] = useState('0969da')
  const [description, setDescription] = useState('')
  const [createdLabel, setCreatedLabel] = useState<IssueLabel>()
  const [createdLabelName, setCreatedLabelName] = useState('')
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')

  useEffect(() => {
    if (open && !wasOpenRef.current) {
      setName('')
      setColor('0969da')
      setDescription('')
      setCreatedLabel(undefined)
      setCreatedLabelName('')
      setSaving(false)
      setError('')
    }
    wasOpenRef.current = open
  }, [open])

  const normalizedColor = color.replace(/^#/, '')
  const valid = name.trim() !== '' && /^[0-9a-f]{6}$/i.test(normalizedColor) && description.length <= 100
  const creationCompleted = createdLabelName !== ''
  const previewName = createdLabel?.name ?? (name.trim() || 'Label preview')
  const previewColor = createdLabel?.color ?? normalizedColor

  const submit = async (event: FormEvent) => {
    event.preventDefault()
    if (!valid || saving) return
    setSaving(true)
    setError('')
    let labelName = createdLabelName
    try {
      if (!labelName) {
        try {
          const label = await api.createIssueLabel(projectId, {
            name: name.trim(),
            color: normalizedColor.toLocaleLowerCase(),
            description: description.trim(),
          })
          setCreatedLabel(label)
          setCreatedLabelName(label.name)
          onCreated(label)
          labelName = label.name
        } catch (value) {
          if (!isLabelProjectionPending(value)) throw value
          labelName = name.trim()
          setCreatedLabelName(labelName)
          let reconciledLabel: IssueLabel | undefined
          try {
            const labels = await api.issueLabels(projectId)
            reconciledLabel = labels.find((label) => label.name.toLocaleLowerCase() === labelName.toLocaleLowerCase())
          } catch {
            // The assignment-only recovery state remains available if refreshing fails.
          }
          if (!reconciledLabel) {
            setError(`Label “${labelName}” was created on GitHub, but Holark has not stored it yet. Try adding again to reconcile and assign it.`)
            return
          }
          setCreatedLabel(reconciledLabel)
          setCreatedLabelName(reconciledLabel.name)
          onCreated(reconciledLabel)
          labelName = reconciledLabel.name
        }
      }
      try {
        const updated = await api.addIssueLabels(issueId, [labelName])
        onIssueChange(updated)
        onClose()
      } catch (value) {
        setError(`Label “${labelName}” was created, but it was not added. ${errorMessage(value)}`)
      }
    } catch (value) {
      setError(errorMessage(value))
    } finally {
      setSaving(false)
    }
  }

  return (
    <Dialog
      open={open}
      title="Create new label"
      onClose={onClose}
      initialFocusRef={nameRef}
      footer={<>
        <Button onClick={onClose}>Cancel</Button>
        <Button variant="primary" disabled={!valid || saving} form="create-issue-label-form" type="submit">
          {saving ? (creationCompleted ? 'Adding...' : 'Creating...') : creationCompleted ? 'Try adding again' : 'Create and add'}
        </Button>
      </>}
    >
      <form id="create-issue-label-form" className={styles.form} onSubmit={submit}>
        <label>Label name<input ref={nameRef} value={name} disabled={creationCompleted} onChange={(event) => setName(event.target.value)} /></label>
        <div className={styles.colorFields}>
          <label>Label color<input type="color" value={/^[0-9a-f]{6}$/i.test(normalizedColor) ? `#${normalizedColor}` : '#000000'} disabled={creationCompleted} onChange={(event) => setColor(event.target.value.slice(1))} /></label>
          <label>Hex color<input value={color} disabled={creationCompleted} inputMode="text" maxLength={7} onChange={(event) => setColor(event.target.value.replace(/^#/, ''))} /></label>
        </div>
        <label>Description (optional)<textarea value={description} disabled={creationCompleted} maxLength={100} rows={3} onChange={(event) => setDescription(event.target.value)} /></label>
        <div className={styles.labelPreview}>
          <span>Preview</span>
          <span aria-label="Label preview" className={styles.labelChip} style={labelColors(previewColor)}>{previewName}</span>
        </div>
        {error && <p className={styles.formError} role="alert">{error}</p>}
      </form>
    </Dialog>
  )
}


export function IssueLabels({ labels, limit, showDescriptions = false }: { labels: IssueLabel[]; limit?: number; showDescriptions?: boolean }) {
  if (labels.length === 0) return null
  const visibleLabels = limit === undefined ? labels : labels.slice(0, limit)
  const overflow = labels.length - visibleLabels.length

  return (
    <ul className={showDescriptions ? `${styles.labelList} ${styles.detailLabelList}` : styles.labelList} aria-label="Labels">
      {visibleLabels.map((label) => {
        const description = label.description.trim()
        const hasHiddenDescription = !showDescriptions && description !== ''
        const tooltip = showDescriptions ? undefined : description ? `${label.name}: ${description}` : label.name
        return (
          <li className={showDescriptions ? styles.detailLabel : undefined} key={label.id} title={tooltip}>
            <span aria-hidden={hasHiddenDescription || undefined} className={styles.labelChip} style={labelColors(label.color)}>
              {label.name}
            </span>
            {hasHiddenDescription && <span className={styles.srOnly}>{label.name}: {description}</span>}
            {showDescriptions && description && <span className={styles.labelDescription}>{description}</span>}
          </li>
        )
      })}
      {overflow > 0 && <li aria-label={`${overflow} more ${overflow === 1 ? 'label' : 'labels'}`} className={styles.labelOverflow}>+{overflow}</li>}
    </ul>
  )
}

function labelColors(color: string) {
  if (!/^[0-9a-f]{6}$/i.test(color)) {
    return { backgroundColor: 'var(--color-surface-hover)', color: 'var(--color-text)' }
  }
  const channels = [0, 2, 4].map((offset) => Number.parseInt(color.slice(offset, offset + 2), 16) / 255)
  const linear = channels.map((channel) => channel <= 0.04045 ? channel / 12.92 : ((channel + 0.055) / 1.055) ** 2.4)
  const luminance = (0.2126 * linear[0]) + (0.7152 * linear[1]) + (0.0722 * linear[2])
  const foreground = ((luminance + 0.05) / 0.05) >= (1.05 / (luminance + 0.05)) ? '#000000' : '#ffffff'
  return { backgroundColor: `#${color}`, color: foreground }
}


function isLabelProjectionPending(value: unknown) {
  const error = value as ApiError
  return error?.code === 'label_projection_pending'
}

function errorMessage(value: unknown) {
  const error = value as ApiError
  return error?.message || 'Request failed.'
}
