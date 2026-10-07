import { RefreshCw } from 'lucide-react'
import { useCallback, useEffect, useRef, useState } from 'react'
import { Link } from 'react-router-dom'
import { MarkdownBody } from '../attachments/MarkdownBody'
import { MarkdownEditor } from '../attachments/MarkdownEditor'
import { HolonTile } from '../agents/HolonTile'
import { holonDisplayStatus } from '../agents/holonDisplay'
import { useOptionalHolonStore } from '../project/holonStoreContext'
import { Button } from '../../components/Button'
import { api } from '../../data/api'
import type { PullRequest, PullRequestMetadata } from '../../data/types'
import { isActivePullRequestStatus } from './lifecycle'
import styles from './PullRequests.module.css'

export function PullRequestMetadataCard({ pullRequest, onSaved, onChange }: {
  pullRequest: PullRequest
  onSaved: () => Promise<void>
  onChange?: (metadata: PullRequestMetadata | undefined) => void
}) {
  const holonStore = useOptionalHolonStore()
  const [metadata, setMetadata] = useState<PullRequestMetadata>()
  const metadataRequest = useRef(0)
  useEffect(() => { onChange?.(metadata) }, [metadata, onChange])
  const acceptMetadata = useCallback((next: PullRequestMetadata) => {
    setMetadata((current) => {
      if (current && next.view_revision < current.view_revision) return current
      const unknown = !next.freshness || next.freshness.status === 'unknown'
      if (unknown && current?.freshness) return { ...next, freshness: { ...current.freshness, status: 'unknown' } }
      return next
    })
  }, [])
  const markFreshnessUnknown = useCallback(() => {
    setMetadata((current) => current?.freshness ? { ...current, freshness: { ...current.freshness, status: 'unknown' } } : current)
  }, [])
  const [starting, setStarting] = useState(false)
  const [applying, setApplying] = useState(false)
  const [launchError, setLaunchError] = useState('')
  const [error, setError] = useState('')

  const load = useCallback(async () => {
    const request = ++metadataRequest.current
    const next = await api.pullRequestMetadata(pullRequest.id)
    if (request !== metadataRequest.current) return undefined
    const base = pullRequest.diff_base_commit || pullRequest.base_commit
    if (next.head_commit !== undefined && next.head_commit !== pullRequest.head_commit || next.diff_base_commit !== undefined && next.diff_base_commit !== base) {
      markFreshnessUnknown()
      return undefined
    }
    return next
  }, [pullRequest.id, pullRequest.head_commit, pullRequest.diff_base_commit, pullRequest.base_commit, markFreshnessUnknown])

  useEffect(() => {
    let cancelled = false
    void load()
      .then((next) => {
        if (cancelled || !next) return
        acceptMetadata(next)
        setError('')
        if (isActivePullRequestStatus(pullRequest.status) && !metadataBusy(next) && (next.title !== pullRequest.title || next.description !== pullRequest.summary || ((pullRequest.status === 'wip' || pullRequest.status === 'draft') && pullRequest.publication_blocked && !next.preparation_target && next.generation_complete))) {
          void onSaved()
        }
      })
      .catch((value) => {
        if (cancelled) return
        markFreshnessUnknown()
        if (pullRequest.sync_provider) {
          setMetadata((current) => current ?? { view_revision: pullRequest.view_revision, pull_request_id: pullRequest.id, title: pullRequest.title, description: pullRequest.summary, updated_at: pullRequest.updated_at })
        } else {
          setError(errorMessage(value))
        }
      })
    return () => { cancelled = true }
  }, [load, onSaved, pullRequest, acceptMetadata, markFreshnessUnknown])

  const busy = isActivePullRequestStatus(pullRequest.status) && metadataBusy(metadata)
  useEffect(() => {
    if (!busy) return undefined
    let cancelled = false
    const poll = async () => {
      try {
        const next = await load()
        if (cancelled || !next) return
        acceptMetadata(next)
        setError('')
        if (!metadataBusy(next)) await onSaved()
      } catch (value) {
        if (!cancelled) { markFreshnessUnknown(); setError(errorMessage(value)) }
      }
    }
    const timer = window.setInterval(() => { void poll() }, 1500)
    return () => {
      cancelled = true
      window.clearInterval(timer)
    }
  }, [busy, load, onSaved, acceptMetadata, markFreshnessUnknown])

  const regenerate = async () => {
    if (starting || busy) return
    metadataRequest.current++
    setStarting(true)
    setLaunchError('')
    setError('')
    try {
      setMetadata(await api.startPullRequestMetadataAgent(pullRequest.id, 'regenerate', ''))
      await onSaved()
    } catch (value) {
      setLaunchError(errorMessage(value))
    } finally {
      setStarting(false)
    }
  }

  const retryApplication = async () => {
    metadataRequest.current++
    setApplying(true)
    setError('')
    try {
      setMetadata(await api.retryPullRequestMetadata(pullRequest.id))
      await onSaved()
    } catch (value) {
      setError(errorMessage(value))
    } finally {
      setApplying(false)
    }
  }

  const active = isActivePullRequestStatus(pullRequest.status)
  const session = metadata?.agent_session
  const applicationError = metadata?.application_error
  const generationError = launchError || metadata?.generation_error || (!metadata?.generation_complete && !applicationError
    ? session?.error || (session && ['failed', 'lost', 'cancelled'].includes(session.status) ? 'Metadata generation failed.' : '')
      || (metadata?.preparation_target && !session ? 'Metadata generation did not start.' : '') : '')
  const description = metadata?.description ?? pullRequest.summary
  const freshness = metadata?.freshness
  const working = starting || busy || applying
  const holon = session?.id ? holonStore?.getHolon(session.id) : undefined
  const finalizing = holon && holonDisplayStatus(holon) === 'finalizing'
  const showingHolon = (working && busy || finalizing) && Boolean(session?.id)
  const warning = freshness?.outdated
  return (
    <>
      {(description.trim() || metadata) && <GeneratedMetadata
        key={pullRequest.id}
        description={description}
        onRegenerate={active && metadata && !generationError && (warning || !applicationError) ? () => void regenerate() : undefined}
        regenerateDisabled={working}
        editDisabled={working}
        onSave={active ? async (description) => {
          metadataRequest.current++
          setMetadata(await api.updatePullRequestMetadata(pullRequest.id, { description }))
          await onSaved()
        } : undefined}
      />}
      {warning && (
        <p className={styles.metadataFreshness}>
          Description may be outdated. Generated for <CommitHash hash={freshness.generated.diff_base_commit} /> → <CommitHash hash={freshness.generated.head_commit} />;
          {' '}current PR changes are <CommitHash hash={freshness.current.diff_base_commit} /> → <CommitHash hash={freshness.current.head_commit} />.
        </p>
      )}
      {active && (showingHolon || applying || (generationError && !working) || applicationError || error || (!metadata && !working && !description.trim())) && (
        <section aria-label="Pull request metadata generation" className={`${styles.overviewBlock} ${styles.metadataWorkflow}`}>
          {showingHolon && session?.id && <HolonTile holonId={session.id} label="Metadata holon" />}
          {applying && <p role="status" className={styles.metadataProgress}>Applying description…</p>}
          {!metadata && !working && !error && <p role="status">Loading description…</p>}
          {generationError && !working && <>
            {session?.id && <HolonTile holonId={session.id} label="Metadata holon" />}
            <p role="alert" className={styles.metadataError}>{generationError}</p>
            <Button icon={<RefreshCw />} disabled={working} onClick={() => void regenerate()}>Retry</Button>
          </>}
          {applicationError && <>
            <p role="alert" className={styles.metadataError}>{applicationError.message}</p>
            <Button disabled={working} onClick={() => void retryApplication()}>
              {applicationError.operation === 'publication' ? 'Retry publication' : 'Retry saving'}
            </Button>
          </>}
          {error && <>
            <p role="alert" className={styles.metadataError}>{error}</p>
            {!applicationError && !generationError && <Button disabled={working} onClick={() => void load().then((next) => { if (next) acceptMetadata(next) }).then(() => setError('')).catch((value) => setError(errorMessage(value)))}>Retry loading</Button>}
          </>}
        </section>
      )}
    </>
  )
}

function GeneratedMetadata({ description, onSave, onRegenerate, editDisabled = false, regenerateDisabled = false }: {
  description: string
  onSave?: (description: string) => Promise<void>
  onRegenerate?: () => void
  editDisabled?: boolean
  regenerateDisabled?: boolean
}) {
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState('')
  const [saving, setSaving] = useState(false)
  const [uploading, setUploading] = useState(false)
  const [error, setError] = useState('')

  const save = async () => {
    if (!onSave || saving || uploading) return
    setSaving(true)
    setError('')
    try {
      await onSave(draft)
      setEditing(false)
    } catch (value) {
      setError(value instanceof Error ? value.message : 'The description could not be saved.')
    } finally {
      setSaving(false)
    }
  }

  return (
    <section aria-label="Pull request metadata" className={`${styles.overviewBlock} ${styles.metadataDescription}`}>
      <h2>Description</h2>
      {editing && onSave ? (
        <form className={styles.metadataEditor} onSubmit={(event) => { event.preventDefault(); void save() }}>
          <MarkdownEditor aria-label="Description" autoFocus rows={10} maxLength={4000} value={draft} disabled={saving} onChange={setDraft} onUploadingChange={setUploading} />
          {error && <p role="alert" className={styles.metadataError}>{error}</p>}
          <div className={styles.metadataEditorActions}>
            <Button disabled={saving || uploading} onClick={() => setEditing(false)}>Cancel</Button>
            <Button type="submit" variant="primary" disabled={saving || uploading}>{saving ? 'Saving...' : 'Save'}</Button>
          </div>
        </form>
      ) : (
        <>
          {(onSave || onRegenerate) && (
            <div className={styles.metadataHeaderActions}>
              {onSave && <Button className={styles.metadataEditButton} size="small" variant="ghost" aria-label="Edit description" title="Edit description" disabled={editDisabled} onClick={() => {
                setDraft(description)
                setError('')
                setEditing(true)
              }}>Edit</Button>}
              {onRegenerate && <Button className={styles.metadataRegenerateButton} size="small" variant="ghost" disabled={regenerateDisabled} onClick={onRegenerate}>{description.trim() ? 'Regenerate' : 'Generate'}</Button>}
            </div>
          )}
          {(description.trim() || !editDisabled) && <PullRequestMarkdown value={description} />}
        </>
      )}
    </section>
  )
}

function agentBusy(holon?: PullRequestMetadata['agent_session']) {
  return Boolean(holon && (holon.status === 'queued' || holon.status === 'preparing' || holon.status === 'running'))
}

function metadataBusy(metadata?: PullRequestMetadata) {
  if (metadata?.application_error || metadata?.generation_complete) return false
  if (agentBusy(metadata?.agent_session)) return true
  if (metadata?.generation_complete === false && metadata.agent_session?.status === 'completed' && !metadata.agent_session.error) return true
  return metadata?.agent_session?.status === 'completed' && !metadata.agent_session.error
    && metadata.preparation_target === 'open'
}

function errorMessage(value: unknown) {
  return value instanceof Error ? value.message : 'Pull request metadata could not be loaded.'
}

export function PullRequestMarkdown({ value }: { value: string }) {
  if (!value.trim()) return <p>No description provided.</p>
  return (
    <MarkdownBody className={styles.markdownBody} value={value} />
  )
}

function CommitHash({ hash }: { hash: string }) {
  return <Link to={`/?view=commits&ref=${encodeURIComponent(hash)}`} title={hash}><code>{hash.slice(0, 7)}</code></Link>
}
