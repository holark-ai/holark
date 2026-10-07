import { MemberIdentity } from './MemberIdentity'
import styles from './MemberAvatarStack.module.css'

export function MemberAvatarStack({ memberIds }: { memberIds: string[] }) {
  if (memberIds.length === 0) return null

  return (
    <span className={styles.stack} role="list" aria-label="Assignees">
      {memberIds.map((memberId, index) => (
        <span className={styles.item} role="listitem" key={`${memberId}:${index}`}>
          <MemberIdentity memberId={memberId} compact />
        </span>
      ))}
    </span>
  )
}
