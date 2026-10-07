import { useState } from 'react'
import { Button } from '../../components/Button'
import { api } from '../../data/api'
import type { Issue } from '../../data/types'
import { MarkdownEditor } from '../attachments/MarkdownEditor'
import { IssueMarkdown } from './IssueMarkdown'
import styles from './IssueDiscussion.module.css'

export function IssueBody({ issue, onSaved }: { issue: Issue, onSaved: (issue: Issue) => void }) {
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState('')
  const [saving, setSaving] = useState(false)
  const [uploading, setUploading] = useState(false)
  const [error, setError] = useState('')
  const save = async () => {
    if (saving || uploading) return
    setSaving(true)
    setError('')
    try {
      onSaved(await api.updateIssueBody(issue.id, draft))
      setEditing(false)
    } catch (error) { setError(error instanceof Error ? error.message : 'The issue body could not be saved.') }
    finally { setSaving(false) }
  }
  return <div>
    <div className={styles.heading}>
      <h2>Body</h2>
      {!editing && <Button size="small" variant="ghost" onClick={() => { setDraft(issue.body); setError(''); setEditing(true) }}>Edit body</Button>}
    </div>
    {editing ? <form onSubmit={(event) => { event.preventDefault(); void save() }}>
      <MarkdownEditor aria-label="Issue body" autoFocus rows={8} value={draft} maxLength={4000} disabled={saving} onChange={setDraft} onUploadingChange={setUploading} />
      {error && <p role="alert">{error}</p>}
      <div className={styles.actions}>
        <Button disabled={saving || uploading} onClick={() => setEditing(false)}>Cancel</Button>
        <Button type="submit" disabled={saving || uploading}>{saving ? 'Saving…' : 'Save body'}</Button>
      </div>
    </form> : <IssueMarkdown body={issue.body || 'No body provided.'} />}
  </div>
}
