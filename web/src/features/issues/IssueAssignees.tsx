import { useCallback, useEffect, useId, useRef, useState } from 'react'
import { api, type ApiError } from '../../data/api'
import type { Issue } from '../../data/types'
import { MemberSelectionPicker, MemberSelectionSection } from '../members/MemberSelectionSection'

const noAssigneeIds: string[] = []
const failureDurationMs = 5_000

export function IssueAssignees({ issue, projectId, onIssueChange }: {
  issue: Issue
  projectId: string
  onIssueChange: (issue: Issue) => void
}) {
  const [editing, setEditing] = useState(false)
  const [draftIds, setDraftIds] = useState<string[]>([])
  const [saving, setSaving] = useState(false)
  const [failure, setFailure] = useState('')
  const savingRef = useRef(false)
  const openingIdsRef = useRef<string[]>([])
  const searchInputRef = useRef<HTMLInputElement>(null)
  const pickerPanelRef = useRef<HTMLDivElement>(null)
  const editButtonRef = useRef<HTMLButtonElement>(null)
  const pickerId = `${useId()}-issue-assignees`

  const assigneeIds = issue.assignee_holark_ids ?? noAssigneeIds
  const editable = issue.status === 'open' && issue.sync_provider === 'github'

  const commit = useCallback(async (restoreFocus: boolean) => {
    if (!editing || savingRef.current) return

    const nextIds = [...draftIds]
    const previousIssue = issue
    setEditing(false)
    if (restoreFocus) editButtonRef.current?.focus()
    if (sameIds(assigneeIds, nextIds)) return

    savingRef.current = true
    setSaving(true)
    setFailure('')
    onIssueChange({ ...previousIssue, assignee_holark_ids: nextIds })

    try {
      onIssueChange(await api.replaceIssueAssignees(issue.id, nextIds))
    } catch (value) {
      if (isIssueProjectionPending(value)) {
        try {
          const result = await api.syncIssues(projectId)
          const reconciledIssue = result.issues.find((candidate) => candidate.id === issue.id)
          if (reconciledIssue) {
            onIssueChange(reconciledIssue)
          } else {
            onIssueChange(previousIssue)
            setFailure('GitHub accepted the assignee change, but Holark must be refreshed.')
          }
        } catch {
          onIssueChange(previousIssue)
          setFailure('GitHub accepted the assignee change, but Holark must be refreshed.')
        }
      } else {
        onIssueChange(previousIssue)
        setFailure('Couldn’t update assignees. GitHub’s selection was restored.')
      }
    } finally {
      savingRef.current = false
      setSaving(false)
    }
  }, [assigneeIds, draftIds, editing, issue, onIssueChange, projectId])

  const discard = useCallback((restoreFocus: boolean) => {
    if (!editing || savingRef.current) return
    setDraftIds([...openingIdsRef.current])
    setEditing(false)
    if (restoreFocus) editButtonRef.current?.focus()
  }, [editing])

  useEffect(() => {
    if (editing) searchInputRef.current?.focus()
  }, [editing])

  useEffect(() => {
    if (!failure) return
    const timeout = window.setTimeout(() => setFailure(''), failureDurationMs)
    return () => window.clearTimeout(timeout)
  }, [failure])

  useEffect(() => {
    if (!editing) return

    const closeOnOutsidePointer = (event: PointerEvent) => {
      const target = event.target
      if (!(target instanceof Node) || pickerPanelRef.current?.contains(target) || editButtonRef.current?.contains(target)) return
      void commit(false)
    }

    document.addEventListener('pointerdown', closeOnOutsidePointer)
    return () => document.removeEventListener('pointerdown', closeOnOutsidePointer)
  }, [commit, editing])

  useEffect(() => {
    if (!editing) return

    const cancelOnEscape = (event: KeyboardEvent) => {
      if (event.key !== 'Escape') return
      event.preventDefault()
      event.stopPropagation()
      discard(true)
    }

    document.addEventListener('keydown', cancelOnEscape)
    return () => document.removeEventListener('keydown', cancelOnEscape)
  }, [discard, editing])

  const open = () => {
    if (savingRef.current || !editable) return
    setFailure('')
    if (editing) {
      void commit(false)
      return
    }
    openingIdsRef.current = [...assigneeIds]
    setDraftIds([...assigneeIds])
    setEditing(true)
  }

  return (
    <MemberSelectionSection
      title="Assignees"
      emptyMessage="None assigned."
      memberIds={assigneeIds}
      editable={editable}
      editing={editing}
      disabled={saving}
      pickerId={pickerId}
      editButtonRef={editButtonRef}
      failure={failure}
      onEdit={open}
      editor={editing && (
        <MemberSelectionPicker
          id={pickerId}
          panelRef={pickerPanelRef}
          selectedIds={draftIds}
          searchInputRef={searchInputRef}
          onChange={setDraftIds}
          onDone={() => { void commit(true) }}
        />
      )}
    />
  )
}

function sameIds(left: string[], right: string[]) {
  if (left.length !== right.length) return false
  const rightIds = new Set(right)
  return left.every((id) => rightIds.has(id))
}

function isIssueProjectionPending(value: unknown) {
  return (value as ApiError)?.code === 'issue_projection_pending'
}
