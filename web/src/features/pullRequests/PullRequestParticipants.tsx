import { useCallback, useEffect, useId, useRef, useState } from 'react'
import { api } from '../../data/api'
import type { PullRequest, PullRequestParticipantSnapshot } from '../../data/types'
import { MemberSelectionPicker, MemberSelectionSection } from '../members/MemberSelectionSection'

type ParticipantKind = 'assignees' | 'reviewers'
type ParticipantFailure = { kind: ParticipantKind, message: string }

const noParticipantIds: string[] = []
const failureDurationMs = 5_000

export function PullRequestParticipants({ pullRequest, onSnapshot }: {
  pullRequest: PullRequest
  onSnapshot: (snapshot: PullRequestParticipantSnapshot) => void
}) {
  const [editing, setEditing] = useState<ParticipantKind>()
  const [draftIds, setDraftIds] = useState<string[]>([])
  const [saving, setSaving] = useState(false)
  const [failure, setFailure] = useState<ParticipantFailure>()
  const savingRef = useRef(false)
  const openingIdsRef = useRef<string[]>([])
  const searchInputRef = useRef<HTMLInputElement>(null)
  const pickerPanelRef = useRef<HTMLDivElement>(null)
  const assigneeEditRef = useRef<HTMLButtonElement>(null)
  const reviewerEditRef = useRef<HTMLButtonElement>(null)
  const pickerId = useId()

  const assigneeIds = pullRequest.assignee_holark_ids ?? noParticipantIds
  const reviewerIds = pullRequest.requested_reviewer_holark_ids ?? noParticipantIds
  const editable = pullRequest.sync_provider === 'github' && (pullRequest.status === 'draft' || pullRequest.status === 'open')

  const focusEditButton = useCallback((kind: ParticipantKind) => {
    const button = kind === 'assignees' ? assigneeEditRef.current : reviewerEditRef.current
    button?.focus()
  }, [])

  const commit = useCallback(async (restoreFocus: boolean) => {
    if (!editing || savingRef.current) return

    const kind = editing
    const nextIds = [...draftIds]
    const previousSnapshot: PullRequestParticipantSnapshot = {
      pull_request_id: pullRequest.id,
      assignee_holark_ids: [...assigneeIds],
      requested_reviewer_holark_ids: [...reviewerIds],
    }
    const currentIds = kind === 'assignees' ? assigneeIds : reviewerIds

    setEditing(undefined)
    if (restoreFocus) focusEditButton(kind)
    if (sameIds(currentIds, nextIds)) return

    const optimisticSnapshot: PullRequestParticipantSnapshot = {
      ...previousSnapshot,
      ...(kind === 'assignees'
        ? { assignee_holark_ids: nextIds }
        : { requested_reviewer_holark_ids: nextIds }),
    }

    savingRef.current = true
    setSaving(true)
    setFailure(undefined)
    onSnapshot(optimisticSnapshot)

    try {
      const snapshot = kind === 'assignees'
        ? await api.replacePullRequestAssignees(pullRequest.id, nextIds)
        : await api.replacePullRequestRequestedReviewers(pullRequest.id, nextIds)
      onSnapshot(snapshot)
      setFailure(undefined)
    } catch {
      try {
        const snapshot = await api.syncPullRequestParticipants(pullRequest.id)
        onSnapshot(snapshot)
        setFailure({
          kind,
          message: `Couldn’t update ${participantLabel(kind)}. GitHub’s selection was restored.`,
        })
      } catch {
        onSnapshot(previousSnapshot)
        setFailure({
          kind,
          message: `Couldn’t update ${participantLabel(kind)}. Refresh to check GitHub.`,
        })
      }
    } finally {
      savingRef.current = false
      setSaving(false)
    }
  }, [assigneeIds, draftIds, editing, focusEditButton, onSnapshot, pullRequest.id, reviewerIds])

  const discard = useCallback((restoreFocus: boolean) => {
    if (!editing || savingRef.current) return
    const kind = editing
    setDraftIds([...openingIdsRef.current])
    setEditing(undefined)
    if (restoreFocus) focusEditButton(kind)
  }, [editing, focusEditButton])

  useEffect(() => {
    if (editing) searchInputRef.current?.focus()
  }, [editing])

  useEffect(() => {
    if (!failure) return
    const timeout = window.setTimeout(() => setFailure(undefined), failureDurationMs)
    return () => window.clearTimeout(timeout)
  }, [failure])

  useEffect(() => {
    if (!editing) return

    const closeOnOutsidePointer = (event: PointerEvent) => {
      const target = event.target
      if (!(target instanceof Node) || pickerPanelRef.current?.contains(target)) return
      const activeEditButton = editing === 'assignees' ? assigneeEditRef.current : reviewerEditRef.current
      if (activeEditButton?.contains(target)) return
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

  if (pullRequest.status !== 'draft' && pullRequest.status !== 'open' && pullRequest.status !== 'merged') return null

  const open = async (kind: ParticipantKind, memberIds: string[]) => {
    if (savingRef.current) return
    setFailure(undefined)
    if (editing) {
      const switchingKind = editing !== kind
      await commit(false)
      if (!switchingKind) return
    }
    openingIdsRef.current = [...memberIds]
    setDraftIds([...memberIds])
    setEditing(kind)
  }

  return (
    <>
      <MemberSelectionSection
        title="Assignees"
        editLabel="Edit"
        emptyMessage="None assigned."
        memberIds={assigneeIds}
        editable={editable}
        editing={editing === 'assignees'}
        disabled={saving}
        pickerId={`${pickerId}-assignees`}
        editButtonRef={assigneeEditRef}
        failure={failure?.kind === 'assignees' ? failure.message : ''}
        onEdit={() => { void open('assignees', assigneeIds) }}
        editor={editing === 'assignees' && (
          <MemberSelectionPicker
            id={`${pickerId}-assignees`}
            panelRef={pickerPanelRef}
            selectedIds={draftIds}
            searchInputRef={searchInputRef}
            onChange={setDraftIds}
            onDone={() => { void commit(true) }}
          />
        )}
      />
      <MemberSelectionSection
        title="Requested reviewers"
        editLabel="Edit"
        emptyMessage="None requested."
        memberIds={reviewerIds}
        editable={editable}
        editing={editing === 'reviewers'}
        disabled={saving}
        pickerId={`${pickerId}-reviewers`}
        editButtonRef={reviewerEditRef}
        failure={failure?.kind === 'reviewers' ? failure.message : ''}
        onEdit={() => { void open('reviewers', reviewerIds) }}
        editor={editing === 'reviewers' && (
          <MemberSelectionPicker
            id={`${pickerId}-reviewers`}
            panelRef={pickerPanelRef}
            selectedIds={draftIds}
            searchInputRef={searchInputRef}
            onChange={setDraftIds}
            onDone={() => { void commit(true) }}
          />
        )}
      />
    </>
  )
}


function sameIds(left: string[], right: string[]) {
  if (left.length !== right.length) return false
  const rightIds = new Set(right)
  return left.every((id) => rightIds.has(id))
}

function participantLabel(kind: ParticipantKind) {
  return kind === 'assignees' ? 'assignees' : 'reviewers'
}
