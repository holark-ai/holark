import { useRef, useState } from 'react'
import { Button } from '../../components/Button'
import { Dialog } from '../../components/Dialog'
import { MemberPicker } from './MemberPicker'
import styles from './ProjectMemberPickerDialog.module.css'

export function ProjectMemberPickerDialog({ open, title, selectedIds, onChange, onClose }: {
  open: boolean
  title: string
  selectedIds: string[]
  onChange: (memberIds: string[]) => Promise<void>
  onClose: () => void
}) {
  const searchInputRef = useRef<HTMLInputElement>(null)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')

  const close = () => {
    if (saving) return
    setError('')
    onClose()
  }

  const change = async (memberIds: string[]) => {
    if (saving) return
    setSaving(true)
    setError('')
    try {
      await onChange(memberIds)
    } catch (value) {
      const message = value instanceof Error ? value.message : 'Member selection could not be updated.'
      setError(message)
    } finally {
      setSaving(false)
    }
  }

  return (
    <Dialog
      open={open}
      title={title}
      initialFocusRef={searchInputRef}
      onClose={close}
      footer={<Button variant="primary" disabled={saving} onClick={close}>Done</Button>}
    >
      <div className={styles.content}>
        {error && <p className={styles.error} role="alert">{error}</p>}
        <MemberPicker
          selectedIds={selectedIds}
          multiple
          disabled={saving}
          searchInputRef={searchInputRef}
          onChange={(memberIds) => { void change(memberIds) }}
        />
      </div>
    </Dialog>
  )
}
