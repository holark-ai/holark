import { Pencil } from 'lucide-react'
import type { ReactNode, RefObject } from 'react'
import { Button } from '../../components/Button'
import { MemberIdentity } from './MemberIdentity'
import { MemberPicker } from './MemberPicker'
import styles from './MemberSelectionSection.module.css'

export function MemberSelectionSection({ title, emptyMessage, memberIds, editable, editing, disabled, pickerId, editButtonRef, failure, editor, editLabel, onEdit }: {
  title: string
  emptyMessage: string
  memberIds: string[]
  editable: boolean
  editing: boolean
  disabled: boolean
  pickerId: string
  editButtonRef: RefObject<HTMLButtonElement | null>
  failure: string
  editor?: ReactNode
  editLabel?: string
  onEdit: () => void
}) {
  return (
    <section>
      <div className={styles.heading}>
        <h2>{title}</h2>
        {editable && (
          <button
            ref={editButtonRef}
            className={`${styles.edit} ${editLabel ? styles.editText : ''}`}
            type="button"
            aria-label={`Edit ${title.toLowerCase()}`}
            aria-expanded={editing}
            aria-controls={pickerId}
            aria-disabled={disabled || undefined}
            onClick={onEdit}
          >
            {editLabel || <Pencil aria-hidden="true" />}
          </button>
        )}
      </div>
      {memberIds.length > 0
        ? <div className={styles.identityList}>
            {memberIds.map((memberId, index) => (
              <MemberIdentity memberId={memberId} linkProfile key={`${memberId}:${index}`} />
            ))}
          </div>
        : <p>{emptyMessage}</p>}
      {editor}
      {failure && <p className={styles.error} role="alert">{failure}</p>}
    </section>
  )
}

export function MemberSelectionPicker({ id, panelRef, selectedIds, searchInputRef, onChange, onDone }: {
  id: string
  panelRef: RefObject<HTMLDivElement | null>
  selectedIds: string[]
  searchInputRef: RefObject<HTMLInputElement | null>
  onChange: (memberIds: string[]) => void
  onDone: () => void
}) {
  return (
    <div ref={panelRef} className={styles.pickerPanel} id={id} aria-label="Participant picker">
      <MemberPicker selectedIds={selectedIds} multiple searchInputRef={searchInputRef} onChange={onChange} />
      <div className={styles.actions}>
        <Button size="small" variant="primary" onClick={onDone}>Done</Button>
      </div>
    </div>
  )
}
